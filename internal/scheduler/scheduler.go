// Package scheduler drives the poll loop for poll-mode adapters. It is
// intentionally adapter-agnostic: the SULO adapter (and any future poll
// adapters) are registered via adapters.Registry at startup.
//
// The scheduler owns discovery (which Things/Datastreams exist) and the
// poll cursor; the per-observation write path lives in internal/ingest,
// which push-mode adapters share (ADR-011). Within a single Datastream the
// fetch→ingest→advance sequence is serial so cursor advancement is
// deterministic; Datastreams run concurrently.
//
// With zero registered poll adapters the Run loop is a no-op tick.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/ingest"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/metrics"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/oms"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/state"
)

// Scheduler orchestrates poll cycles across registered poll adapters.
type Scheduler struct {
	Registry  *adapters.Registry
	Store     state.Store
	Processor *ingest.Processor
	Logger    *slog.Logger

	PollInterval       time.Duration
	CursorInitLookback time.Duration

	now func() time.Time // injectable clock for tests
}

// New returns a Scheduler.
func New(
	reg *adapters.Registry,
	store state.Store,
	processor *ingest.Processor,
	logger *slog.Logger,
	pollInterval, cursorInitLookback time.Duration,
) *Scheduler {
	return &Scheduler{
		Registry:           reg,
		Store:              store,
		Processor:          processor,
		Logger:             logger,
		PollInterval:       pollInterval,
		CursorInitLookback: cursorInitLookback,
		now:                time.Now,
	}
}

// Run drives the poll loop until ctx is cancelled. Returns ctx.Err on
// shutdown. With zero registered poll adapters the loop ticks but does no
// work — safe for startup before adapters land.
func (s *Scheduler) Run(ctx context.Context) error {
	t := time.NewTicker(s.PollInterval)
	defer t.Stop()

	// Run one cycle immediately at startup so we don't wait a full
	// interval before the first work happens.
	s.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	poll := s.Registry.PollAdapters()
	if len(poll) == 0 {
		s.Logger.Debug("scheduler tick skipped: no poll adapters registered")
		return
	}
	for _, a := range poll {
		s.runAdapterCycle(ctx, a)
	}
}

// runAdapterCycle discovers/refreshes Things and Datastreams for one
// adapter, then fetches observations for each Datastream concurrently.
func (s *Scheduler) runAdapterCycle(ctx context.Context, a adapters.PollAdapter) {
	vendor := a.VendorID()
	log := s.Logger.With(slog.String("vendor", vendor))

	things, err := a.ListThings(ctx)
	if err != nil {
		s.classifyAndMeter(log, vendor, "list_things", err)
		return
	}

	var wg sync.WaitGroup
	for _, t := range things {
		thingID, err := s.Store.UpsertThing(ctx, t.VendorID, t.VendorNativeID, oms.ThingName(t.VendorID, t.VendorNativeID))
		if err != nil {
			log.Error("upsert thing", slog.Any("err", err), slog.String("vendor_native_id", t.VendorNativeID))
			continue
		}

		datastreams, err := a.ListDatastreamsForThing(ctx, t.VendorNativeID)
		if err != nil {
			s.classifyAndMeter(log, vendor, "list_datastreams", err)
			continue
		}

		for _, d := range datastreams {
			if d.ObservedProperty != canonical.FillLevel {
				// v1 ingests fill-level only (ADR-003 defers vehicles/RFID).
				continue
			}
			dsID, err := s.Store.UpsertDatastream(ctx, thingID, string(d.ObservedProperty), d.ExpectedCadenceSeconds)
			if err != nil {
				log.Error("upsert datastream", slog.Any("err", err))
				continue
			}

			wg.Add(1)
			tt := t
			dd := d
			go func() {
				defer wg.Done()
				s.runDatastreamCycle(ctx, a, vendor, tt, dd, thingID, dsID)
			}()
		}
	}
	wg.Wait()
}

// runDatastreamCycle is the per-Datastream poll → ingest → advance path.
// It is the only place the cursor advances.
//
//  1. seed cursor if absent (now - lookback)
//  2. fetch observations
//  3. hand to the ingest core (validate → upsert chain → write → record)
//  4. advance cursor to the max covered phenomenonTime (F1 step 7)
func (s *Scheduler) runDatastreamCycle(
	ctx context.Context,
	a adapters.PollAdapter,
	vendor string,
	t canonical.Thing,
	d canonical.Datastream,
	thingID, dsID int64,
) {
	// Record poll attempt regardless of outcome.
	defer func() {
		if err := s.Store.SetLastPolledAt(ctx, dsID, s.now()); err != nil {
			s.Logger.Warn("set last polled at", slog.Any("err", err))
		}
	}()

	cursor, err := s.Store.GetPollCursor(ctx, dsID)
	if err != nil {
		s.Logger.Error("get poll cursor", slog.Any("err", err))
		return
	}
	if cursor.IsZero() {
		cursor = s.now().Add(-s.CursorInitLookback)
	}

	obs, err := a.FetchObservations(ctx, t.VendorNativeID, d.ObservedProperty, cursor, 1000)
	if err != nil {
		s.classifyAndMeter(s.Logger, vendor, "fetch_observations", err)
		return
	}
	metrics.ObservationsPolledTotal.WithLabelValues(vendor).Add(float64(len(obs)))

	res, err := s.Processor.ProcessStream(ctx, vendor, thingID, dsID, t, d, cursor, obs)
	if err != nil {
		// Transient write-path failure: leave the cursor put so the next
		// tick re-polls and retries. The freshness watchdog covers a
		// stream that stays stuck.
		s.classifyAndMeter(s.Logger, vendor, "ingest", err)
		return
	}

	if res.MaxPhenomenonTime.After(cursor) {
		if err := s.Store.AdvancePollCursor(ctx, dsID, res.MaxPhenomenonTime); err != nil {
			s.Logger.Warn("advance poll cursor", slog.Any("err", err))
		}
	}
}

func (s *Scheduler) classifyAndMeter(log *slog.Logger, vendor, op string, err error) {
	if err == nil {
		return
	}
	switch {
	case adapters.IsPermanent(err):
		metrics.VendorPermanentErrorsTotal.WithLabelValues(vendor).Inc()
		log.Warn("vendor permanent error", slog.String("op", op), slog.Any("err", err))
	case adapters.IsTransient(err):
		metrics.VendorTransientErrorsTotal.WithLabelValues(vendor).Inc()
		log.Info("vendor transient error", slog.String("op", op), slog.Any("err", err))
	default:
		// Unclassified errors default to transient per F2 table.
		metrics.VendorTransientErrorsTotal.WithLabelValues(vendor).Inc()
		log.Warn("vendor error (unclassified, treated transient)", slog.String("op", op), slog.Any("err", err))
	}
}
