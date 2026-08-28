// Package ingest is the transport-agnostic ingestion core (ADR-011).
// It takes canonical observations for a single (Thing, Datastream) and
// runs the full write path: validate → upsert the STA entity chain →
// write each observation to every FROST target → record the write log →
// meter.
//
// Both source modes funnel through here:
//   - poll (internal/scheduler) passes the Datastream's poll cursor so the
//     validator applies the before_cursor rule, and advances the cursor
//     afterwards using Result.MaxPhenomenonTime.
//   - push (internal/api push listener) passes a zero cursor so the
//     validator skips before_cursor — push may backfill or arrive out of
//     order, and de-duplication is delegated to the write-log idempotency
//     key (UNIQUE (datastream_id, phenomenon_time)).
//
// This is the only place the per-observation FROST write happens.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/frost"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/metrics"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/oms"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/state"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/validator"
)

// Result summarises one ProcessStream call. Counts are across the whole
// input batch; WrittenPerTarget is keyed by FROST target label.
type Result struct {
	Received          int
	Accepted          int
	WrittenPerTarget  map[string]int
	SkippedIdempotent int
	Rejected          []validator.Rejection
	// MaxPhenomenonTime is the newest phenomenonTime that the caller may
	// safely treat as "covered" — the max over written, idempotently
	// skipped, and definitively-rejected observations. Poll uses this to
	// advance its cursor; push ignores it.
	MaxPhenomenonTime time.Time
}

// Processor owns the mapper, the FROST writers (one per target, dual-write
// per F6), the state store, and the validator config. It is safe for
// concurrent use across Datastreams: the mapper is stateless, each Writer
// has its own cache, and the store is concurrency-safe.
type Processor struct {
	Mapper    *oms.Mapper
	Writers   []*frost.Writer
	Store     state.Store
	Validator validator.Config
	Logger    *slog.Logger

	now func() time.Time
}

// New returns a Processor. now defaults to time.Now.
func New(mapper *oms.Mapper, writers []*frost.Writer, store state.Store, vcfg validator.Config, logger *slog.Logger) *Processor {
	now := vcfg.Now
	if now == nil {
		now = time.Now
	}
	return &Processor{
		Mapper:    mapper,
		Writers:   writers,
		Store:     store,
		Validator: vcfg,
		Logger:    logger,
		now:       now,
	}
}

// ProcessStream validates and writes obs for one (Thing, Datastream).
//
// thingID and dsID are the translation_state row ids (already upserted by
// the caller). cursor is the Datastream's poll cursor in poll mode, or the
// zero time in push mode (which disables the before_cursor rejection).
//
// The entity-upsert chain is resolved once per FROST target before the
// observation loop; subsequent calls hit each Writer's cache.
func (p *Processor) ProcessStream(
	ctx context.Context,
	vendor string,
	thingID, dsID int64,
	t canonical.Thing,
	d canonical.Datastream,
	cursor time.Time,
	obs []canonical.Observation,
) (Result, error) {
	res := Result{Received: len(obs), WrittenPerTarget: map[string]int{}, MaxPhenomenonTime: cursor}

	kept, rejected := validator.Filter(p.Validator, cursor, obs)
	res.Rejected = rejected
	res.Accepted = len(kept)
	for _, r := range rejected {
		metrics.ObservationsDroppedTotal.WithLabelValues(string(r.Reason), vendor).Inc()
		// Definitively-rejected observations are "covered" — advance past
		// them so poll does not replay them forever (F1 step 7).
		if validator.IsDefinitivelyRejected(r.Reason) && r.Observation.PhenomenonTime.After(res.MaxPhenomenonTime) {
			res.MaxPhenomenonTime = r.Observation.PhenomenonTime
		}
	}
	metrics.ObservationsAcceptedTotal.WithLabelValues(vendor).Add(float64(len(kept)))

	if len(kept) == 0 {
		return res, nil
	}

	// Resolve the STA Datastream @iot.id (and FoI) per target up front.
	targets, err := p.resolveTargets(ctx, vendor, t, d)
	if err != nil {
		return res, err
	}
	if len(targets) > 0 {
		p.recordSTAIDs(ctx, thingID, dsID, targets[0])
	}

	var maxResultTime time.Time
	for _, o := range kept {
		wrote, skipped, dropped, err := p.writeObservation(ctx, vendor, dsID, o, targets)
		if err != nil {
			// Transient FROST/store error. Stop the stream so poll leaves
			// the cursor put and retries next cycle; push surfaces it to
			// the caller. Observations already written in this batch are
			// durable and idempotent on retry.
			return res, err
		}
		// err == nil means the observation is "covered": it was written,
		// skipped as already-present, or permanently dropped (FROST 4xx).
		// Advance past it in all three cases so poll does not replay it.
		if skipped {
			res.SkippedIdempotent++
		}
		for label := range wrote {
			res.WrittenPerTarget[label]++
		}
		if o.PhenomenonTime.After(res.MaxPhenomenonTime) {
			res.MaxPhenomenonTime = o.PhenomenonTime
		}
		if !dropped && o.ResultTime.After(maxResultTime) {
			maxResultTime = o.ResultTime
		}
	}

	if !maxResultTime.IsZero() {
		metrics.ObservationFreshnessSeconds.
			WithLabelValues(vendor, string(d.ObservedProperty)).
			Set(p.now().Sub(maxResultTime).Seconds())
	}
	return res, nil
}

// resolvedTarget binds a Writer to the STA ids it needs for observation writes.
type resolvedTarget struct {
	writer  *frost.Writer
	thingID int64 // STA Thing @iot.id
	dsID    int64 // STA Datastream @iot.id
	foiID   int64 // STA FeatureOfInterest @iot.id (0 → omit, FROST uses default)
}

// resolveTargets runs the Thing→Location→Sensor→ObservedProperty→Datastream
// →FoI upsert chain for every FROST target. Each entity is resolved via the
// Writer's GetOrCreate (cache → name-filter → POST), so this is cheap after
// the first stream on a given target.
func (p *Processor) resolveTargets(ctx context.Context, vendor string, t canonical.Thing, d canonical.Datastream) ([]resolvedTarget, error) {
	out := make([]resolvedTarget, 0, len(p.Writers))
	for _, w := range p.Writers {
		rt, err := p.resolveChain(ctx, w, t, d)
		if err != nil {
			return nil, fmt.Errorf("resolve entity chain on target %q: %w", w.Target.Label, err)
		}
		out = append(out, rt)
	}
	return out, nil
}

func (p *Processor) resolveChain(ctx context.Context, w *frost.Writer, t canonical.Thing, d canonical.Datastream) (resolvedTarget, error) {
	when := p.now().UTC()

	thingName := p.Mapper.ThingEntityName(t)
	staThingID, err := w.GetOrCreate(ctx, frost.EntityThings, thingName, "/Things",
		func() any { return p.Mapper.ThingPayload(t) })
	if err != nil {
		return resolvedTarget{}, err
	}

	// Location is created under the Thing; its dated name keeps it
	// idempotent on "same day, same coords".
	locName := p.Mapper.LocationEntityName(t, when)
	if _, err := w.GetOrCreate(ctx, frost.EntityLocations, locName,
		fmt.Sprintf("/Things(%d)/Locations", staThingID),
		func() any { return p.Mapper.LocationPayload(t, when) }); err != nil {
		return resolvedTarget{}, err
	}

	sensorPayload := p.Mapper.SensorPayload(t, d)
	staSensorID, err := w.GetOrCreate(ctx, frost.EntitySensors, sensorPayload.Name, "/Sensors",
		func() any { return sensorPayload })
	if err != nil {
		return resolvedTarget{}, err
	}

	opPayload := p.Mapper.ObservedPropertyPayload(d)
	staOPID, err := w.GetOrCreate(ctx, frost.EntityObservedProperties, opPayload.Name, "/ObservedProperties",
		func() any { return opPayload })
	if err != nil {
		return resolvedTarget{}, err
	}

	dsName := p.Mapper.DatastreamEntityName(t, d)
	staDSID, err := w.GetOrCreate(ctx, frost.EntityDatastreams, dsName, "/Datastreams",
		func() any { return p.Mapper.DatastreamPayload(t, d, staThingID, staSensorID, staOPID) })
	if err != nil {
		return resolvedTarget{}, err
	}

	foiName := p.Mapper.FoIEntityName(t)
	staFoIID, err := w.GetOrCreate(ctx, frost.EntityFeaturesOfInterest, foiName, "/FeaturesOfInterest",
		func() any { return p.Mapper.FoIPayload(t) })
	if err != nil {
		return resolvedTarget{}, err
	}

	return resolvedTarget{writer: w, thingID: staThingID, dsID: staDSID, foiID: staFoIID}, nil
}

// recordSTAIDs stores the server-assigned Thing and Datastream @iot.ids
// against our own rows, so the state store can be joined to FROST without
// re-resolving entities by name.
//
// With several targets the first one wins and a single id is kept, which is
// the same convention writeObservation already uses for sta_observation_id
// (ADR-004 defers per-target bookkeeping until a second target is live).
//
// This is bookkeeping, not part of the write path: a failure here is logged
// and swallowed, because losing the cross-reference must never abort an
// ingest that is otherwise succeeding.
func (p *Processor) recordSTAIDs(ctx context.Context, thingID, dsID int64, rt resolvedTarget) {
	if rt.thingID != 0 {
		if err := p.Store.SetSTAThingID(ctx, thingID, rt.thingID); err != nil {
			p.Logger.Warn("record sta thing id", slog.Any("err", err),
				slog.Int64("thing_id", thingID), slog.Int64("sta_thing_id", rt.thingID))
		}
	}
	if rt.dsID != 0 {
		if err := p.Store.SetSTADatastreamID(ctx, dsID, rt.dsID); err != nil {
			p.Logger.Warn("record sta datastream id", slog.Any("err", err),
				slog.Int64("datastream_id", dsID), slog.Int64("sta_datastream_id", rt.dsID))
		}
	}
}

// writeObservation writes one observation to every target and records the
// write log once on success. The state-store write log is the authoritative
// idempotency gate (target-agnostic; per-target logging is deferred per
// ADR-004 until a second target is live).
//
// Returns:
//   - wrote: target labels that accepted a fresh write
//   - skipped: the observation was already present (idempotency hit)
//   - dropped: a permanent FROST 4xx rejected it on every target — it is
//     not retryable, so the caller advances past it without recording
//     (data flow side effect: FROST 4xx → log, meter, advance cursor)
//   - err: a transient FROST/store error — the caller halts the stream
func (p *Processor) writeObservation(
	ctx context.Context,
	vendor string,
	dsID int64,
	o canonical.Observation,
	targets []resolvedTarget,
) (wrote map[string]bool, skipped, dropped bool, err error) {
	exists, err := p.Store.ObservationExists(ctx, dsID, o.PhenomenonTime)
	if err != nil {
		return nil, false, false, err
	}
	if exists {
		for _, rt := range targets {
			metrics.ObservationsSkippedIdempotentTotal.WithLabelValues(vendor, rt.writer.Target.Label).Inc()
		}
		return nil, true, false, nil
	}

	wrote = map[string]bool{}
	var staObservationID int64
	var permanent bool
	for _, rt := range targets {
		label := rt.writer.Target.Label
		payload := p.Mapper.ObservationPayload(o, rt.foiID)
		id, created, perr := rt.writer.PostObservation(ctx, rt.dsID, o.PhenomenonTime, payload)
		if perr != nil {
			if frost.IsPermanent(perr) {
				// Non-retryable on this target — drop and move on.
				permanent = true
				metrics.ObservationsDroppedTotal.WithLabelValues("frost_4xx", vendor).Inc()
				p.Logger.Warn("frost permanent error; dropping observation",
					slog.String("target", label),
					slog.String("phenomenon_time", o.PhenomenonTime.Format(time.RFC3339Nano)),
					slog.Any("err", perr),
				)
				continue
			}
			return wrote, false, false, fmt.Errorf("post observation to target %q: %w", label, perr)
		}
		if created {
			wrote[label] = true
			metrics.ObservationsWrittenTotal.WithLabelValues(vendor, label).Inc()
			if staObservationID == 0 {
				staObservationID = id
			}
		} else {
			metrics.ObservationsSkippedIdempotentTotal.WithLabelValues(vendor, label).Inc()
		}
	}

	// Nothing landed anywhere and at least one target permanently rejected
	// it → dropped. Don't record a write log row for data not in FROST.
	if len(wrote) == 0 && permanent {
		return wrote, false, true, nil
	}

	if err := p.Store.RecordObservationWrite(ctx, dsID, o.PhenomenonTime, o.Result, o.RawObservationID, staObservationID); err != nil {
		if errors.Is(err, state.ErrAlreadyExists) {
			// Raced with another write of the same key — idempotent no-op.
			return wrote, true, false, nil
		}
		return wrote, false, false, err
	}
	return wrote, false, false, nil
}
