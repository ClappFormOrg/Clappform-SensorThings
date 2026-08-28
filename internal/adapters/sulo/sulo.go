package sulo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
)

// Adapter defaults.
const (
	// DefaultObservationPageLimit bounds how many fill-level rows a single
	// FetchObservations call pulls for one slot per cycle.
	DefaultObservationPageLimit = 1000

	// DefaultMinConfidence is the lowest REEN "confidence" an estimate may
	// carry and still be ingested. REEN uses confidence 0 for a
	// measurement it determined to be erroneous (typically a distance
	// reading over 5 metres), so the default of 1 drops exactly those and
	// keeps everything else — including confidence 60 ("no measurement",
	// i.e. analytics interpolated the value) and 80 (reading inside the
	// sensor's minimum measurable distance).
	DefaultMinConfidence = 1
)

// Config configures the SULO adapter.
type Config struct {
	// BaseURL is the REEN API root, with or without the "/api/3" suffix
	// (SULO_API_BASE_URL). Required.
	BaseURL string

	// Username and Password authenticate against POST /session
	// (SULO_API_USERNAME / SULO_API_PASSWORD). Required — REEN has no
	// static API-key scheme.
	Username string
	Password string

	// CustomerID sets the X-Customer scope header (SULO_CUSTOMER_ID).
	// Only needed when the account has rights over several REEN customer
	// accounts and the target is not its own; empty uses the default.
	CustomerID string

	// HTTPTimeout bounds one REEN request. Zero uses DefaultHTTPTimeout.
	HTTPTimeout time.Duration

	// PageSize is the "limit" used per paged request. Zero uses
	// DefaultPageSize.
	PageSize int

	// ObservationPageLimit caps fill-level rows fetched per slot per
	// cycle. Non-positive uses DefaultObservationPageLimit.
	ObservationPageLimit int

	// MinConfidence is the REEN confidence floor for ingestion. Values
	// <= 0 use DefaultMinConfidence.
	MinConfidence int

	// ExpectedCadenceSeconds is the per-Datastream freshness expectation
	// published to FROST and used by the watchdog (ADR-008). Zero falls
	// back to the global FRESHNESS_THRESHOLD_HOURS. REEN fill levels are
	// analytics estimates whose rate varies per customer, so there is no
	// safe universal default — set it per deployment.
	ExpectedCadenceSeconds int
}

// Adapter is the SULO/REEN poll adapter.
//
// The scheduler calls ListThings, then ListDatastreamsForThing per Thing,
// then FetchObservations per Datastream — the last of those concurrently.
// ListThings resolves the related entities a slot needs (site, device,
// content type) in one pass and caches the result in `slots`, so the
// per-Thing calls that follow are served from memory instead of issuing
// N more requests. The cache is replaced wholesale on each discovery pass
// and read under RLock by the concurrent fetchers.
type Adapter struct {
	client    *client
	pageLimit int
	minConf   int
	cadence   int
	logger    *slog.Logger
	now       func() time.Time // injectable clock for tests

	mu    sync.RWMutex
	slots map[string]slotView
}

var _ adapters.PollAdapter = (*Adapter)(nil)

// New returns a SULO adapter. It validates that the credentials needed to
// open a REEN session are present; the session itself is established lazily
// on first use so a misconfigured vendor cannot block service startup.
func New(cfg Config, logger *slog.Logger) (*Adapter, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("sulo: SULO_API_BASE_URL is required")
	}
	if cfg.Username == "" || cfg.Password == "" {
		return nil, errors.New("sulo: SULO_API_USERNAME and SULO_API_PASSWORD are required (REEN issues session tokens, not static API keys)")
	}
	if logger == nil {
		logger = slog.Default()
	}

	pageLimit := cfg.ObservationPageLimit
	if pageLimit <= 0 {
		pageLimit = DefaultObservationPageLimit
	}
	minConf := cfg.MinConfidence
	if minConf <= 0 {
		minConf = DefaultMinConfidence
	}

	return &Adapter{
		client:    newClient(cfg.BaseURL, cfg.Username, cfg.Password, cfg.CustomerID, cfg.HTTPTimeout, cfg.PageSize, logger),
		pageLimit: pageLimit,
		minConf:   minConf,
		cadence:   cfg.ExpectedCadenceSeconds,
		logger:    logger,
		now:       time.Now,
		slots:     map[string]slotView{},
	}, nil
}

// VendorID returns "sulo".
func (a *Adapter) VendorID() string { return VendorID }

// ListThings discovers every container slot for the configured customer and
// projects it into a canonical Thing.
//
// Container slots and sites are both required: a slot supplies the identity
// and its site supplies the coordinates, and the write path always emits a
// Location and FeatureOfInterest Point for a Thing. Devices and content
// types are enrichment only — a failure there degrades the Sensor metadata
// and descriptions but is not allowed to stall ingestion, so those errors
// are logged and swallowed.
//
// Slots whose coordinates cannot be resolved are skipped rather than
// published at Point(0,0): this feeds a geospatial API, where a container
// pinned to the Gulf of Guinea is worse than a container that is absent and
// warned about every cycle.
func (a *Adapter) ListThings(ctx context.Context) ([]canonical.Thing, error) {
	slots, err := a.fetchContainerSlots(ctx)
	if err != nil {
		return nil, err
	}
	sites, err := a.fetchSites(ctx)
	if err != nil {
		return nil, err
	}

	// Best-effort enrichment.
	devicesByContainer := map[int64]*deviceDTO{}
	if devices, err := a.fetchLinkedDevices(ctx); err != nil {
		a.logger.Warn("sulo: device enrichment unavailable, sensor metadata will be generic", slog.Any("err", err))
	} else {
		devicesByContainer = devices
	}
	contentTypes := map[int64]*contentTypeDTO{}
	if cts, err := a.fetchContentTypes(ctx); err != nil {
		a.logger.Warn("sulo: content-type enrichment unavailable, descriptions will be generic", slog.Any("err", err))
	} else {
		contentTypes = cts
	}

	snapshot := make(map[string]slotView, len(slots))
	things := make([]canonical.Thing, 0, len(slots))
	var skipped []int64

	for _, s := range slots {
		v := slotView{
			slot:        s,
			site:        sites[int64(s.Site)],
			device:      devicesByContainer[s.Container],
			contentType: contentTypes[s.ContentType],
		}
		if _, ok := v.coord(); !ok {
			skipped = append(skipped, s.ID)
			continue
		}
		snapshot[v.nativeID()] = v
		things = append(things, v.toCanonicalThing())
	}

	if len(skipped) > 0 {
		a.logger.Warn("sulo: container slots skipped — no site coordinates in REEN",
			slog.Int("skipped", len(skipped)),
			slog.Int("published", len(things)),
			slog.Any("container_slot_ids", firstN(skipped, 20)),
		)
	}

	a.mu.Lock()
	a.slots = snapshot
	a.mu.Unlock()

	a.logger.Info("sulo: discovery complete",
		slog.Int("container_slots", len(things)),
		slog.Int("sites", len(sites)),
		slog.Int("linked_devices", len(devicesByContainer)),
	)
	return things, nil
}

// ListDatastreamsForThing returns the single fill-level stream for a slot.
//
// The stream exists for every slot REEN reports, so it is returned even
// when the discovery cache has no entry (a Thing polled without a preceding
// ListThings in this process); the result is then simply less descriptive.
func (a *Adapter) ListDatastreamsForThing(_ context.Context, vendorNativeID string) ([]canonical.Datastream, error) {
	a.mu.RLock()
	v, ok := a.slots[vendorNativeID]
	a.mu.RUnlock()

	if !ok {
		slotID, err := strconv.ParseInt(vendorNativeID, 10, 64)
		if err != nil {
			return nil, &adapters.PermanentError{Err: fmt.Errorf("sulo: %q is not a REEN container slot id: %w", vendorNativeID, err)}
		}
		v = slotView{slot: containerSlotDTO{ID: slotID}}
	}
	return []canonical.Datastream{v.toCanonicalDatastream(a.cadence)}, nil
}

// FetchObservations pulls fill-level estimates for one slot recorded after
// the cursor, oldest first.
//
// Three REEN behaviours are absorbed here rather than leaked downstream:
//
//   - Predicted values. /fillLevels mixes matured ("frozen") estimates with
//     forecasts, and the guide states forecasts always carry future
//     timestamps. They must not reach the ingest core: the validator would
//     reject a future reading as in_future, but in_future counts as a
//     definitive rejection, so the cursor would advance to the forecast's
//     timestamp and every real measurement until that date would then be
//     dropped as before_cursor. Anything newer than now is discarded.
//   - Erroneous values. confidence 0 marks a measurement REEN itself judged
//     bad; see DefaultMinConfidence.
//   - Descending order. REEN returns newest-first; the ingest core and
//     cursor arithmetic want oldest-first, and offset paging over a live
//     data set can repeat a row, so results are de-duplicated by timestamp
//     and sorted ascending.
func (a *Adapter) FetchObservations(
	ctx context.Context,
	vendorNativeID string,
	op canonical.ObservedProperty,
	since time.Time,
	limit int,
) ([]canonical.Observation, error) {
	if op != canonical.FillLevel {
		// v1 publishes fill level only; the scheduler filters this too.
		return nil, nil
	}
	slotID, err := strconv.ParseInt(vendorNativeID, 10, 64)
	if err != nil {
		return nil, &adapters.PermanentError{Err: fmt.Errorf("sulo: %q is not a REEN container slot id: %w", vendorNativeID, err)}
	}
	if limit <= 0 {
		limit = a.pageLimit
	}

	q := url.Values{}
	if !since.IsZero() {
		// REEN's "after" is exclusive, matching the cursor's semantics.
		q.Set("after", since.UTC().Format(reenTimeLayout))
	}

	path := fmt.Sprintf("/fillLevels/containerSlot/%d", slotID)
	var rows []fillLevelDTO
	err = a.client.paged(ctx, path, q, limit, func(page []byte) (int, error) {
		var resp fillLevelsResponse
		if err := json.Unmarshal(page, &resp); err != nil {
			return 0, &adapters.TransientError{Err: fmt.Errorf("sulo: decode fill levels for slot %d: %w", slotID, err)}
		}
		rows = append(rows, resp.FillLevels...)
		return len(resp.FillLevels), nil
	})
	if err != nil {
		return nil, err
	}

	return a.toObservations(slotID, since, rows), nil
}

// toObservations applies the filtering described on FetchObservations and
// returns canonical observations sorted oldest-first.
func (a *Adapter) toObservations(slotID int64, since time.Time, rows []fillLevelDTO) []canonical.Observation {
	now := a.now().UTC()
	seen := make(map[int64]struct{}, len(rows))
	out := make([]canonical.Observation, 0, len(rows))

	var dropPredicted, dropConfidence, dropNoValue, dropUnparsed int

	for _, r := range rows {
		t, ok := parseREENTime(r.Time)
		if !ok {
			dropUnparsed++
			continue
		}
		if t.After(now) {
			dropPredicted++
			continue
		}
		if !r.FillLevel.Set {
			dropNoValue++
			continue
		}
		if r.Confidence != nil && *r.Confidence < a.minConf {
			dropConfidence++
			continue
		}
		// Defensive: honour the cursor even if REEN widens "after".
		if !since.IsZero() && !t.After(since) {
			continue
		}
		if _, dup := seen[t.UnixNano()]; dup {
			continue
		}
		seen[t.UnixNano()] = struct{}{}

		out = append(out, canonical.Observation{
			ThingVendorNativeID: strconv.FormatInt(slotID, 10),
			ObservedProperty:    canonical.FillLevel,
			PhenomenonTime:      t,
			// REEN timestamps an estimate once; there is no separate
			// result time, so the two coincide.
			ResultTime:       t,
			Result:           r.FillLevel.Value,
			RawObservationID: observationID(slotID, t),
		})
	}

	slices.SortFunc(out, func(x, y canonical.Observation) int {
		return x.PhenomenonTime.Compare(y.PhenomenonTime)
	})

	if dropPredicted+dropConfidence+dropNoValue+dropUnparsed > 0 {
		a.logger.Debug("sulo: fill-level rows filtered",
			slog.Int64("container_slot_id", slotID),
			slog.Int("kept", len(out)),
			slog.Int("predicted", dropPredicted),
			slog.Int("low_confidence", dropConfidence),
			slog.Int("no_value", dropNoValue),
			slog.Int("unparseable_time", dropUnparsed),
		)
	}
	return out
}

// fetchContainerSlots lists every container slot for the customer.
func (a *Adapter) fetchContainerSlots(ctx context.Context) ([]containerSlotDTO, error) {
	var out []containerSlotDTO
	err := a.client.paged(ctx, "/containerSlots", nil, 0, func(page []byte) (int, error) {
		var resp containerSlotsResponse
		if err := json.Unmarshal(page, &resp); err != nil {
			return 0, &adapters.TransientError{Err: fmt.Errorf("sulo: decode container slots: %w", err)}
		}
		out = append(out, resp.ContainerSlots...)
		return len(resp.ContainerSlots), nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// fetchSites lists every site, indexed by id, for slot coordinates.
func (a *Adapter) fetchSites(ctx context.Context) (map[int64]*siteDTO, error) {
	out := map[int64]*siteDTO{}
	err := a.client.paged(ctx, "/sites", nil, 0, func(page []byte) (int, error) {
		var resp sitesResponse
		if err := json.Unmarshal(page, &resp); err != nil {
			return 0, &adapters.TransientError{Err: fmt.Errorf("sulo: decode sites: %w", err)}
		}
		for i := range resp.Sites {
			s := resp.Sites[i]
			out[s.ID] = &s
		}
		return len(resp.Sites), nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// fetchLinkedDevices lists devices installed in a container, indexed by the
// container they occupy — which is how a slot reaches its sensor
// (slot.container → device.container).
func (a *Adapter) fetchLinkedDevices(ctx context.Context) (map[int64]*deviceDTO, error) {
	out := map[int64]*deviceDTO{}
	err := a.client.paged(ctx, "/devices/linked", nil, 0, func(page []byte) (int, error) {
		var resp devicesResponse
		if err := json.Unmarshal(page, &resp); err != nil {
			return 0, &adapters.TransientError{Err: fmt.Errorf("sulo: decode devices: %w", err)}
		}
		for i := range resp.Devices {
			d := resp.Devices[i]
			if d.Container != 0 {
				out[d.Container] = &d
			}
		}
		return len(resp.Devices), nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// fetchContentTypes lists the customer's waste fractions, indexed by id.
// /contentTypes is a global method that disregards the default limit, so it
// is fetched in one unpaged request.
func (a *Adapter) fetchContentTypes(ctx context.Context) (map[int64]*contentTypeDTO, error) {
	var resp contentTypesResponse
	if err := a.client.get(ctx, "/contentTypes", nil, &resp); err != nil {
		return nil, err
	}
	out := make(map[int64]*contentTypeDTO, len(resp.ContentTypes))
	for i := range resp.ContentTypes {
		c := resp.ContentTypes[i]
		out[c.ID] = &c
	}
	return out, nil
}

// firstN bounds a slice used in a log field so a large misconfiguration
// cannot produce an unbounded log line.
func firstN(in []int64, n int) []int64 {
	if len(in) <= n {
		return in
	}
	return in[:n]
}
