// Package validator enforces the data-quality rules from F1 step 5
// before observations reach the FROST writer. Rejected observations
// are reported with a stable reason code; the scheduler decides
// whether to advance the cursor past them (see F1 step 7 / cursor
// advance rule).
package validator

import (
	"errors"
	"math"
	"time"

	"github.com/clappformorg/geonovum-sta-translation/internal/canonical"
)

// Reason is a stable, machine-readable rejection code. New codes are
// additive; existing codes never change meaning.
type Reason string

const (
	ReasonMissingResult         Reason = "missing_result"
	ReasonOutOfRange            Reason = "out_of_range"
	ReasonInFuture              Reason = "in_future"
	ReasonBeforeCursor          Reason = "before_cursor"
	ReasonOutOfBoundsCoordinate Reason = "out_of_bounds_coordinates"
)

// Rejection identifies a single rejected observation with its reason.
// Returned alongside the kept slice from Filter so the caller can log
// and meter without re-walking the input.
type Rejection struct {
	Index       int // position in the original input
	Observation canonical.Observation
	Reason      Reason
}

// Config captures the validation knobs from the Implementation
// Contract: clock-skew tolerance and the per-property numeric range.
type Config struct {
	// ClockSkewTolerance bounds how far in the future a phenomenonTime
	// may be from now() before it is rejected. Default 5 min.
	ClockSkewTolerance time.Duration

	// Now is the clock used for in_future comparisons. Defaults to
	// time.Now if nil. Injectable for tests.
	Now func() time.Time
}

// DefaultConfig returns the Implementation-Contract defaults.
func DefaultConfig() Config {
	return Config{
		ClockSkewTolerance: 5 * time.Minute,
		Now:                time.Now,
	}
}

// Filter validates every observation against its Datastream's cursor
// and returns the kept and rejected slices. The input is not mutated.
//
// Range rules are property-specific:
//   - FillLevel: result in [0, 100] inclusive
//   - Temperature, Battery: not ingested in v1 — accepted as-is when
//     they appear, no range check
func Filter(
	cfg Config,
	cursor time.Time,
	observations []canonical.Observation,
) (kept []canonical.Observation, rejected []Rejection) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	now := cfg.Now()

	kept = make([]canonical.Observation, 0, len(observations))
	for i, o := range observations {
		if reason, ok := validate(o, cursor, now, cfg.ClockSkewTolerance); !ok {
			rejected = append(rejected, Rejection{Index: i, Observation: o, Reason: reason})
			continue
		}
		kept = append(kept, o)
	}
	return kept, rejected
}

// ErrAdapter wraps a validator-internal failure. Currently unused but
// reserved so callers can errors.Is/As against a stable sentinel.
var ErrAdapter = errors.New("validator: adapter contract violation")

func validate(o canonical.Observation, cursor, now time.Time, skew time.Duration) (Reason, bool) {
	// Missing or NaN result.
	if math.IsNaN(o.Result) {
		return ReasonMissingResult, false
	}

	// Range check (fill-level only in v1).
	if o.ObservedProperty == canonical.FillLevel {
		if o.Result < 0 || o.Result > 100 {
			return ReasonOutOfRange, false
		}
	}

	// Future-clock skew.
	if o.PhenomenonTime.After(now.Add(skew)) {
		return ReasonInFuture, false
	}

	// Late / duplicate. cursor is the last phenomenon_time already
	// covered; equal-or-earlier is rejected.
	if !cursor.IsZero() && !o.PhenomenonTime.After(cursor) {
		return ReasonBeforeCursor, false
	}

	return "", true
}

// IsDefinitivelyRejected reports whether a reason permits advancing
// the cursor past the rejected observation (per F1 step 7).
// Network/transient failures never come through this path, so every
// validator reason qualifies as definitive.
func IsDefinitivelyRejected(r Reason) bool {
	switch r {
	case ReasonMissingResult,
		ReasonOutOfRange,
		ReasonInFuture,
		ReasonBeforeCursor,
		ReasonOutOfBoundsCoordinate:
		return true
	default:
		return false
	}
}
