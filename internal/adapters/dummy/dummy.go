// Package dummy is a synthetic poll adapter for end-to-end validation
// (ADR-011 / connector-phase smoke testing). It generates a fixed set of
// fake waste containers around 's-Hertogenbosch and emits deterministic
// fill-level observations, so the full pipeline — adapter → ingest core →
// FROST writer → FROST-Server → STA queries — can be exercised before the
// real SULO adapter exists.
//
// It is opt-in (DUMMY_ADAPTER_ENABLED) and must never be registered in a
// production deployment. Generation is deterministic (no randomness): each
// container follows a sawtooth fill curve with a stable per-container phase
// offset, so a given timestamp always yields the same value — which makes
// parity checks and restart-safety reproducible.
package dummy

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
)

// VendorID is the stable identifier this adapter registers under.
const VendorID = "dummy"

// Defaults used when New is given zero values.
const (
	DefaultThingsCount = 5
	DefaultCadence     = 5 * time.Minute
	// fillCycle is how long a container takes to go from empty to full
	// before being "collected" and resetting to ~0.
	fillCycle = 12 * time.Hour
)

// Adapter is a synthetic PollAdapter.
type Adapter struct {
	things  []canonical.Thing
	cadence time.Duration
	now     func() time.Time
}

var _ adapters.PollAdapter = (*Adapter)(nil)

// New returns a dummy adapter with count containers at the given cadence.
// Non-positive count/cadence fall back to the package defaults. now
// defaults to time.Now; inject a fixed clock in tests.
func New(count int, cadence time.Duration, now func() time.Time) *Adapter {
	if count <= 0 {
		count = DefaultThingsCount
	}
	if cadence <= 0 {
		cadence = DefaultCadence
	}
	if now == nil {
		now = time.Now
	}
	return &Adapter{
		things:  buildThings(count),
		cadence: cadence,
		now:     now,
	}
}

func (a *Adapter) VendorID() string { return VendorID }

// ListThings returns the fixed synthetic container set.
func (a *Adapter) ListThings(context.Context) ([]canonical.Thing, error) {
	out := make([]canonical.Thing, len(a.things))
	copy(out, a.things)
	return out, nil
}

// ListDatastreamsForThing returns the single fill-level stream per container.
func (a *Adapter) ListDatastreamsForThing(_ context.Context, vendorNativeID string) ([]canonical.Datastream, error) {
	return []canonical.Datastream{{
		ThingVendorNativeID: vendorNativeID,
		ObservedProperty:    canonical.FillLevel,
		Unit:                canonical.Percent,
		SensorMetadata: map[string]string{
			"model":            "DUMMY-1",
			"firmware_version": "1.0.0",
		},
		ExpectedCadenceSeconds: int(a.cadence.Seconds()),
	}}, nil
}

// FetchObservations generates deterministic fill-level readings at the
// configured cadence for every tick in (since, now], capped at limit.
func (a *Adapter) FetchObservations(
	_ context.Context,
	vendorNativeID string,
	op canonical.ObservedProperty,
	since time.Time,
	limit int,
) ([]canonical.Observation, error) {
	if op != canonical.FillLevel {
		return nil, nil
	}
	now := a.now().UTC()
	phase := phaseOffset(vendorNativeID)

	// Start at the first cadence boundary strictly after `since`.
	step := a.cadence
	start := since.UTC().Truncate(step).Add(step)
	if !start.After(since.UTC()) {
		start = start.Add(step)
	}

	var out []canonical.Observation
	for t := start; !t.After(now); t = t.Add(step) {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, canonical.Observation{
			ThingVendorNativeID: vendorNativeID,
			ObservedProperty:    canonical.FillLevel,
			PhenomenonTime:      t,
			ResultTime:          t,
			Result:              fillLevelAt(t, phase),
			RawObservationID:    fmt.Sprintf("%s@%d", vendorNativeID, t.Unix()),
		})
	}
	return out, nil
}

// buildThings lays out count containers on a small grid around the
// Afvalstoffendienst 's-Hertogenbosch area (≈ 51.69 N, 5.30 E).
func buildThings(count int) []canonical.Thing {
	const (
		baseLon = 5.2913
		baseLat = 51.6978
		delta   = 0.0025 // ~150–280 m spacing
	)
	things := make([]canonical.Thing, 0, count)
	for i := 0; i < count; i++ {
		nativeID := fmt.Sprintf("DUMMY-%04d", i+1)
		row := i / 5
		col := i % 5
		things = append(things, canonical.Thing{
			VendorID:       VendorID,
			VendorNativeID: nativeID,
			Name:           fmt.Sprintf("Dummy Container %s", nativeID),
			Description:    "Synthetic waste container for end-to-end validation (not a real sensor)",
			Location: canonical.Coord{
				Lon: baseLon + float64(col)*delta,
				Lat: baseLat + float64(row)*delta,
			},
			Properties: map[string]string{
				"synthetic": "true",
				"area":      "s-hertogenbosch-dummy",
			},
		})
	}
	return things
}

// fillLevelAt returns a deterministic fill percentage in [0, 100] for time
// t given a per-container phase offset: a sawtooth that rises over fillCycle
// then resets (a container filling up and being emptied on collection).
func fillLevelAt(t time.Time, phase time.Duration) float64 {
	elapsed := (time.Duration(t.UnixNano()) + phase) % fillCycle
	if elapsed < 0 {
		elapsed += fillCycle
	}
	pct := float64(elapsed) / float64(fillCycle) * 100.0
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

// phaseOffset derives a stable per-container offset so containers don't all
// fill in lockstep. Deterministic from the native id.
func phaseOffset(vendorNativeID string) time.Duration {
	h := fnv.New64a()
	_, _ = h.Write([]byte(vendorNativeID))
	return time.Duration(h.Sum64() % uint64(fillCycle))
}
