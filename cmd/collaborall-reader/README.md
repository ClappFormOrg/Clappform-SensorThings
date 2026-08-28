# collaborall-reader

Standalone reader that replicates a **Collaborall FROST-Server**
(`sta-server.collaborall.net`) into the translation-layer's destination FROST,
**via the service's push endpoint** — it does not write to FROST directly.

```
Collaborall FROST  ──GET──▶  collaborall-reader  ──POST /ingest/collaborall──▶  translation-layer  ──▶  destination FROST(s)
   (source, read)              (this binary)          (one endpoint, Bearer)         (validate → CF_-prefix map → dual-write)
```

Faithful passthrough: source ObservedProperty / unit / Sensor / Datastream names
and `observationType` are preserved; every created FROST entity is prefixed with
`ENTITY_NAME_PREFIX` (e.g. `CF_`) on the service side. Non-numeric results
(booleans from `OM_TruthObservation`, integers from `OM_CountObservation`, …) are
replicated verbatim.

## Configuration (environment)

Source (read):
- `COLLABORALL_FROST_BASE_URL` (required) — e.g. `https://sta-server.collaborall.net/v1.1`
- `COLLABORALL_BASIC_AUTH_USER`, `COLLABORALL_BASIC_AUTH_PASSWORD` — HTTP Basic
- `COLLABORALL_TOKEN` — Bearer alternative to Basic
- `COLLABORALL_TLS_INSECURE_SKIP_VERIFY` — `true` only for a self-signed/mismatched cert
- `COLLABORALL_HTTP_TIMEOUT_SECONDS` — default 30
- `COLLABORALL_WATCH_SENSORS` — comma-separated Sensor **names or @iot.ids**;
  only their Datastreams are replicated. **Empty = replicate all** (460 datastreams).
- `COLLABORALL_OBSERVATION_PAGE_LIMIT` — max observations per stream per cycle (default 1000)

Sink (write, into the service):
- `API_INGEST_URL` (required) — e.g. `https://<service>/ingest/collaborall`
- `COLLABORALL_INGEST_SECRET` (required) — must equal the service's value

Loop / state:
- `READER_POLL_INTERVAL_SECONDS` — default 900
- `READER_CURSOR_LOOKBACK_SECONDS` — first-run backfill window, default 3600
- `READER_CURSOR_FILE` — per-stream cursor state file (default `collaborall-cursors.json`)
- `READER_ONCE` — `true` runs a single cycle and exits (cron/testing)
- `LOG_LEVEL` — DEBUG/INFO/WARN/ERROR

## Run

```bash
export COLLABORALL_FROST_BASE_URL="https://sta-server.collaborall.net/v1.1"
export COLLABORALL_BASIC_AUTH_USER="..."
export COLLABORALL_BASIC_AUTH_PASSWORD="..."
export COLLABORALL_WATCH_SENSORS="24E124707E427318,Drukopnemer pomp-1"   # pick from GET /Sensors
export API_INGEST_URL="http://localhost:8081/ingest/collaborall"
export COLLABORALL_INGEST_SECRET="shared-secret"
export READER_ONCE=true
go run ./cmd/collaborall-reader
```

The reader owns its cursor; a re-run is safe (the service's write-log
deduplicates on `(datastream, phenomenon_time)`).
