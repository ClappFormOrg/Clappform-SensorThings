package state

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations.sql
var migrationsSQL string

// PGStore is the Postgres implementation of Store. Use NewPGStore to
// construct; the zero value is not usable.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore connects to dsn, applies migrations, and returns a ready
// PGStore. Caller owns Close.
func NewPGStore(ctx context.Context, dsn string) (*PGStore, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	// Small pool — single-replica deployment, low concurrency.
	cfg.MaxConns = 8
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	s := &PGStore{pool: pool}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *PGStore) migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, migrationsSQL)
	return err
}

func (s *PGStore) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

func (s *PGStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *PGStore) UpsertThing(ctx context.Context, vendorID, vendorNativeID, name string) (int64, error) {
	const q = `
INSERT INTO translation_state.thing (vendor_id, vendor_native_id, name)
VALUES ($1, $2, $3)
ON CONFLICT (vendor_id, vendor_native_id) DO UPDATE
    SET last_seen_at = NOW(),
        name = EXCLUDED.name
RETURNING id`
	var id int64
	err := s.pool.QueryRow(ctx, q, vendorID, vendorNativeID, name).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert thing: %w", err)
	}
	return id, nil
}

func (s *PGStore) SetSTAThingID(ctx context.Context, thingID int64, staID int64) error {
	const q = `UPDATE translation_state.thing SET sta_thing_id = $2 WHERE id = $1`
	tag, err := s.pool.Exec(ctx, q, thingID, staID)
	if err != nil {
		return fmt.Errorf("set sta thing id: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) GetThing(ctx context.Context, thingID int64) (ThingRow, error) {
	const q = `
SELECT id, vendor_id, vendor_native_id, sta_thing_id, name, first_seen_at, last_seen_at
FROM translation_state.thing
WHERE id = $1`
	var r ThingRow
	err := s.pool.QueryRow(ctx, q, thingID).Scan(
		&r.ID, &r.VendorID, &r.VendorNativeID, &r.STAThingID,
		&r.Name, &r.FirstSeenAt, &r.LastSeenAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ThingRow{}, ErrNotFound
	}
	if err != nil {
		return ThingRow{}, fmt.Errorf("get thing: %w", err)
	}
	return r, nil
}

func (s *PGStore) UpsertDatastream(
	ctx context.Context,
	thingID int64,
	observedProperty string,
	expectedCadenceSeconds int,
) (int64, error) {
	const q = `
INSERT INTO translation_state.datastream (thing_id, observed_property, expected_cadence_seconds)
VALUES ($1, $2, $3)
ON CONFLICT (thing_id, observed_property) DO UPDATE
    SET expected_cadence_seconds = EXCLUDED.expected_cadence_seconds
RETURNING id`
	var id int64
	err := s.pool.QueryRow(ctx, q, thingID, observedProperty, expectedCadenceSeconds).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert datastream: %w", err)
	}
	return id, nil
}

func (s *PGStore) SetSTADatastreamID(ctx context.Context, datastreamID int64, staID int64) error {
	const q = `UPDATE translation_state.datastream SET sta_datastream_id = $2 WHERE id = $1`
	tag, err := s.pool.Exec(ctx, q, datastreamID, staID)
	if err != nil {
		return fmt.Errorf("set sta datastream id: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) GetDatastream(ctx context.Context, datastreamID int64) (DatastreamRow, error) {
	const q = `
SELECT id, thing_id, observed_property, sta_datastream_id,
       poll_cursor, last_polled_at, last_written_at, expected_cadence_seconds
FROM translation_state.datastream
WHERE id = $1`
	var r DatastreamRow
	err := s.pool.QueryRow(ctx, q, datastreamID).Scan(
		&r.ID, &r.ThingID, &r.ObservedProperty, &r.STADatastreamID,
		&r.PollCursor, &r.LastPolledAt, &r.LastWrittenAt, &r.ExpectedCadenceSeconds,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DatastreamRow{}, ErrNotFound
	}
	if err != nil {
		return DatastreamRow{}, fmt.Errorf("get datastream: %w", err)
	}
	return r, nil
}

func (s *PGStore) ListDatastreams(ctx context.Context) ([]DatastreamRow, error) {
	const q = `
SELECT id, thing_id, observed_property, sta_datastream_id,
       poll_cursor, last_polled_at, last_written_at, expected_cadence_seconds
FROM translation_state.datastream
ORDER BY id`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list datastreams: %w", err)
	}
	defer rows.Close()

	var out []DatastreamRow
	for rows.Next() {
		var r DatastreamRow
		if err := rows.Scan(
			&r.ID, &r.ThingID, &r.ObservedProperty, &r.STADatastreamID,
			&r.PollCursor, &r.LastPolledAt, &r.LastWrittenAt, &r.ExpectedCadenceSeconds,
		); err != nil {
			return nil, fmt.Errorf("scan datastream row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PGStore) GetPollCursor(ctx context.Context, datastreamID int64) (time.Time, error) {
	const q = `SELECT poll_cursor FROM translation_state.datastream WHERE id = $1`
	var t *time.Time
	err := s.pool.QueryRow(ctx, q, datastreamID).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("get poll cursor: %w", err)
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

func (s *PGStore) AdvancePollCursor(ctx context.Context, datastreamID int64, newCursor time.Time) error {
	// Monotonic: only advance forward.
	const q = `
UPDATE translation_state.datastream
SET poll_cursor = $2
WHERE id = $1
  AND (poll_cursor IS NULL OR poll_cursor < $2)`
	_, err := s.pool.Exec(ctx, q, datastreamID, newCursor)
	if err != nil {
		return fmt.Errorf("advance poll cursor: %w", err)
	}
	// If the cursor was already >= newCursor we silently no-op — that's fine.
	return nil
}

func (s *PGStore) SetLastPolledAt(ctx context.Context, datastreamID int64, ts time.Time) error {
	const q = `UPDATE translation_state.datastream SET last_polled_at = $2 WHERE id = $1`
	_, err := s.pool.Exec(ctx, q, datastreamID, ts)
	if err != nil {
		return fmt.Errorf("set last polled at: %w", err)
	}
	return nil
}

func (s *PGStore) ObservationExists(ctx context.Context, datastreamID int64, phenomenonTime time.Time) (bool, error) {
	const q = `
SELECT 1 FROM translation_state.observation_write_log
WHERE datastream_id = $1 AND phenomenon_time = $2
LIMIT 1`
	var x int
	err := s.pool.QueryRow(ctx, q, datastreamID, phenomenonTime).Scan(&x)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("observation exists: %w", err)
	}
	return true, nil
}

func (s *PGStore) RecordObservationWrite(
	ctx context.Context,
	datastreamID int64,
	phenomenonTime time.Time,
	result float64,
	rawObservationID string,
	staObservationID int64,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	const insert = `
INSERT INTO translation_state.observation_write_log
    (datastream_id, phenomenon_time, result, raw_observation_id, sta_observation_id)
VALUES ($1, $2, $3, $4, $5)`
	if _, err := tx.Exec(ctx, insert, datastreamID, phenomenonTime, result, rawObservationID, staObservationID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return ErrAlreadyExists
		}
		return fmt.Errorf("insert observation write log: %w", err)
	}

	const updateDS = `
UPDATE translation_state.datastream
SET last_written_at = $2
WHERE id = $1
  AND (last_written_at IS NULL OR last_written_at < $2)`
	if _, err := tx.Exec(ctx, updateDS, datastreamID, phenomenonTime); err != nil {
		return fmt.Errorf("update datastream last_written_at: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s *PGStore) GetStalenessSnapshot(ctx context.Context, now time.Time) (StalenessSnapshot, error) {
	// Threshold per Datastream = max(3 * expected_cadence_seconds, 3600).
	// Stale iff last_written_at IS NULL AND last_polled_at IS NOT NULL
	//   (we polled but nothing came back for a long time), OR
	//   now - last_written_at > threshold.
	// Newly-discovered Datastreams that have never been polled don't count.
	const query = `
WITH evaluated AS (
    SELECT
        d.id,
        t.name AS thing_name,
        d.observed_property,
        d.last_written_at,
        d.last_polled_at,
        GREATEST(3 * d.expected_cadence_seconds, 3600) AS threshold_seconds
    FROM translation_state.datastream d
    JOIN translation_state.thing t ON t.id = d.thing_id
    WHERE d.last_polled_at IS NOT NULL
)
SELECT
    (SELECT COUNT(*) FROM evaluated) AS total,
    COALESCE(SUM(CASE WHEN last_written_at IS NULL
                       OR EXTRACT(EPOCH FROM ($1::timestamptz - last_written_at)) > threshold_seconds
                       THEN 1 ELSE 0 END), 0) AS stale,
    COALESCE((
        SELECT array_agg(thing_name || ' / ' || observed_property)
        FROM (
            SELECT thing_name, observed_property
            FROM evaluated
            WHERE last_written_at IS NULL
               OR EXTRACT(EPOCH FROM ($1::timestamptz - last_written_at)) > threshold_seconds
            ORDER BY COALESCE(last_written_at, '-infinity'::timestamptz) ASC
            LIMIT 5
        ) AS sample
    ), ARRAY[]::TEXT[]) AS examples
FROM evaluated`

	var snap StalenessSnapshot
	snap.ComputedAt = now
	err := s.pool.QueryRow(ctx, query, now).Scan(&snap.Total, &snap.Stale, &snap.ExampleNames)
	if err != nil {
		return StalenessSnapshot{}, fmt.Errorf("staleness snapshot: %w", err)
	}
	return snap, nil
}

func (s *PGStore) GetWatchdogState(ctx context.Context) (WatchdogState, error) {
	const q = `SELECT current_status, since_ts, last_fired_ts FROM translation_state.watchdog_state WHERE id = 1`
	var w WatchdogState
	var status string
	err := s.pool.QueryRow(ctx, q).Scan(&status, &w.SinceTS, &w.LastFiredTS)
	if errors.Is(err, pgx.ErrNoRows) {
		// Singleton wasn't seeded — extremely unusual but recoverable.
		return WatchdogState{CurrentStatus: StatusOK, SinceTS: time.Now()}, nil
	}
	if err != nil {
		return WatchdogState{}, fmt.Errorf("get watchdog state: %w", err)
	}
	w.CurrentStatus = Status(status)
	return w, nil
}

func (s *PGStore) SetWatchdogState(ctx context.Context, w WatchdogState) error {
	const q = `
INSERT INTO translation_state.watchdog_state (id, current_status, since_ts, last_fired_ts)
VALUES (1, $1, $2, $3)
ON CONFLICT (id) DO UPDATE
    SET current_status = EXCLUDED.current_status,
        since_ts       = EXCLUDED.since_ts,
        last_fired_ts  = EXCLUDED.last_fired_ts`
	_, err := s.pool.Exec(ctx, q, string(w.CurrentStatus), w.SinceTS, w.LastFiredTS)
	if err != nil {
		return fmt.Errorf("set watchdog state: %w", err)
	}
	return nil
}

func (s *PGStore) PurgeOldWriteLog(ctx context.Context, retentionDays int) (int64, error) {
	const q = `
DELETE FROM translation_state.observation_write_log
WHERE written_at < NOW() - ($1 || ' days')::interval`
	tag, err := s.pool.Exec(ctx, q, fmt.Sprintf("%d", retentionDays))
	if err != nil {
		return 0, fmt.Errorf("purge old write log: %w", err)
	}
	return tag.RowsAffected(), nil
}
