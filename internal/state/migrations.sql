-- Embedded schema for the translation layer's state store.
-- Applied on startup via the pgstore migrator; idempotent.
-- Mirrors the schema documented in docs/sulo-sta-translation-layer-design.md
-- under "Internal state store".

CREATE SCHEMA IF NOT EXISTS translation_state;

CREATE TABLE IF NOT EXISTS translation_state.thing (
    id               BIGSERIAL PRIMARY KEY,
    vendor_id        TEXT        NOT NULL,
    vendor_native_id TEXT        NOT NULL,
    sta_thing_id     BIGINT,
    name             TEXT        NOT NULL,
    first_seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (vendor_id, vendor_native_id)
);

CREATE INDEX IF NOT EXISTS thing_name_idx ON translation_state.thing (name);

CREATE TABLE IF NOT EXISTS translation_state.datastream (
    id                       BIGSERIAL PRIMARY KEY,
    thing_id                 BIGINT      NOT NULL REFERENCES translation_state.thing(id) ON DELETE CASCADE,
    observed_property        TEXT        NOT NULL,
    sta_datastream_id        BIGINT,
    poll_cursor              TIMESTAMPTZ,
    last_polled_at           TIMESTAMPTZ,
    last_written_at          TIMESTAMPTZ,
    expected_cadence_seconds INTEGER     NOT NULL DEFAULT 3600,
    UNIQUE (thing_id, observed_property)
);

CREATE INDEX IF NOT EXISTS datastream_last_written_at_idx
    ON translation_state.datastream (last_written_at);

CREATE TABLE IF NOT EXISTS translation_state.observation_write_log (
    id                  BIGSERIAL PRIMARY KEY,
    datastream_id       BIGINT           NOT NULL REFERENCES translation_state.datastream(id) ON DELETE CASCADE,
    phenomenon_time     TIMESTAMPTZ      NOT NULL,
    result              DOUBLE PRECISION NOT NULL,
    raw_observation_id  TEXT             NOT NULL,
    sta_observation_id  BIGINT,
    written_at          TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    UNIQUE (datastream_id, phenomenon_time)
);

CREATE INDEX IF NOT EXISTS observation_write_log_written_at_idx
    ON translation_state.observation_write_log (written_at);

CREATE TABLE IF NOT EXISTS translation_state.watchdog_state (
    id              INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    current_status  TEXT        NOT NULL CHECK (current_status IN ('ok', 'stale_pending', 'stale')),
    since_ts        TIMESTAMPTZ NOT NULL,
    last_fired_ts   TIMESTAMPTZ
);

-- Ensure the singleton row exists.
INSERT INTO translation_state.watchdog_state (id, current_status, since_ts)
VALUES (1, 'ok', NOW())
ON CONFLICT (id) DO NOTHING;
