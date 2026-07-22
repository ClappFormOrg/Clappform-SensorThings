// Package state defines the persistence interface for the translation
// layer's internal state. The Postgres implementation lives alongside
// in pgstore.go; tests may substitute an in-memory fake.
//
// Schema is documented in the design doc under "Internal state store"
// and embedded as migrations.sql.
package state

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a row that should exist does not.
var ErrNotFound = errors.New("state: not found")

// Status enumerates the watchdog's three states (ADR-008).
type Status string

const (
	StatusOK           Status = "ok"
	StatusStalePending Status = "stale_pending"
	StatusStale        Status = "stale"
)

// ThingRow is the projected shape of translation_state.thing.
type ThingRow struct {
	ID             int64
	VendorID       string
	VendorNativeID string
	STAThingID     *int64 // nil until first FROST upsert
	Name           string
	FirstSeenAt    time.Time
	LastSeenAt     time.Time
}

// DatastreamRow is the projected shape of translation_state.datastream.
type DatastreamRow struct {
	ID                     int64
	ThingID                int64
	ObservedProperty       string
	STADatastreamID        *int64
	PollCursor             *time.Time // nil until first successful cycle
	LastPolledAt           *time.Time
	LastWrittenAt          *time.Time
	ExpectedCadenceSeconds int
}

// WatchdogState is the single-row freshness-alert state machine.
type WatchdogState struct {
	CurrentStatus Status
	SinceTS       time.Time
	LastFiredTS   *time.Time
}

// StalenessSnapshot captures the counts the watchdog needs to evaluate
// the count-based alert trigger (ADR-008).
type StalenessSnapshot struct {
	Total        int
	Stale        int
	ExampleNames []string // up to 5 stale Datastream names
	ComputedAt   time.Time
}

// Store is the persistence contract. All methods are safe to call
// concurrently. Implementations use transactions where atomicity
// matters; callers should treat individual calls as atomic units.
type Store interface {
	// Thing upserts a vendor's container/vehicle by (VendorID,
	// VendorNativeID). Returns the row id, creating the row on first
	// sight.
	UpsertThing(ctx context.Context, vendorID, vendorNativeID, name string) (int64, error)

	// SetSTAThingID records the @iot.id assigned by FROST. Caller
	// should call this after a successful create or after the upsert
	// algorithm's GET-by-filter resolution.
	SetSTAThingID(ctx context.Context, thingID int64, staID int64) error

	// GetThing returns a single row by id.
	GetThing(ctx context.Context, thingID int64) (ThingRow, error)

	// UpsertDatastream upserts a (Thing, ObservedProperty) row,
	// initializing ExpectedCadenceSeconds on first sight.
	UpsertDatastream(
		ctx context.Context,
		thingID int64,
		observedProperty string,
		expectedCadenceSeconds int,
	) (int64, error)

	// SetSTADatastreamID records the @iot.id assigned by FROST.
	SetSTADatastreamID(ctx context.Context, datastreamID int64, staID int64) error

	// GetDatastream returns a single row by id.
	GetDatastream(ctx context.Context, datastreamID int64) (DatastreamRow, error)

	// ListDatastreams returns every Datastream row in stable id order.
	ListDatastreams(ctx context.Context) ([]DatastreamRow, error)

	// GetPollCursor returns the current cursor or the zero time if the
	// Datastream has never been polled. The scheduler interprets zero
	// as "seed from CURSOR_INIT_LOOKBACK_SECONDS".
	GetPollCursor(ctx context.Context, datastreamID int64) (time.Time, error)

	// AdvancePollCursor moves the cursor forward to newCursor. Implementations
	// must reject backwards moves (the cursor is monotonic).
	AdvancePollCursor(ctx context.Context, datastreamID int64, newCursor time.Time) error

	// SetLastPolledAt records that a poll was attempted, regardless of
	// outcome. Used to detect adapters that fail every cycle.
	SetLastPolledAt(ctx context.Context, datastreamID int64, ts time.Time) error

	// ObservationExists reports whether (datastream, phenomenon_time) is
	// already in the write log. Used as the pre-write idempotency probe.
	ObservationExists(ctx context.Context, datastreamID int64, phenomenonTime time.Time) (bool, error)

	// RecordObservationWrite appends a row to translation_state.observation_write_log.
	// Returns ErrAlreadyExists if (datastream, phenomenon_time) collides.
	// Updates the parent Datastream's LastWrittenAt.
	RecordObservationWrite(
		ctx context.Context,
		datastreamID int64,
		phenomenonTime time.Time,
		result float64,
		rawObservationID string,
		staObservationID int64,
	) error

	// GetStalenessSnapshot evaluates the per-Datastream staleness rule
	// from F5/ADR-008 and returns the snapshot for the watchdog.
	// Threshold for Datastream D = max(3 * expected_cadence_seconds, 1h).
	GetStalenessSnapshot(ctx context.Context, now time.Time) (StalenessSnapshot, error)

	// GetWatchdogState returns the single-row freshness state machine.
	GetWatchdogState(ctx context.Context) (WatchdogState, error)

	// SetWatchdogState writes the single-row freshness state machine.
	SetWatchdogState(ctx context.Context, w WatchdogState) error

	// PurgeOldWriteLog deletes write-log rows older than retentionDays.
	// Returns the count deleted. Idempotent. (R-RUN-2.)
	PurgeOldWriteLog(ctx context.Context, retentionDays int) (int64, error)

	// Ping verifies connectivity for /healthz. Cheap and bounded.
	Ping(ctx context.Context) error

	// Close releases the underlying connection pool. Idempotent.
	Close()
}

// ErrAlreadyExists is returned by RecordObservationWrite when the
// (datastream, phenomenon_time) idempotency key collides. The caller
// should treat this as a successful no-op.
var ErrAlreadyExists = errors.New("state: observation already recorded")
