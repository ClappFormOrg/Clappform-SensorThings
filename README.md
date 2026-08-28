# Geonovum 2026 Testbed — sensor data → OGC SensorThings API translation layer

Implementation for Topic #2 of the Geonovum Testbed Sensor Data 2026.
Clappform B.V. (lead) + SULO Group (research partner).

This service takes sensor data from sources that do not speak OGC SensorThings
API (STA) and republishes it as STA entities on a FROST-Server, modelled per the
OGC Observations & Measurements Standard (OMS). It was built to answer the
testbed's question about what integrating a common standard actually costs.

**Start with the report.** The findings, the problems we hit, and what we would
change about the standard are in
[docs/topic2-lessons-findings-and-results.md](docs/topic2-lessons-findings-and-results.md).
The code here is the evidence behind it.

## Status

**Three integrations running against live servers.** Not a scaffold and not a
mock: every finding in the report came from real traffic.

| Integration | Direction | What it proves |
| ------------- | ----------- | ---------------- |
| **SULO**, via the REEN CMS REST API | We poll a proprietary vendor API | The adapter contract against a real vendor platform: 51 container slots, hourly fill-level estimates |
| **Brabantse Delta (WBD)** FROST-Server | We write | The write path end to end against a live shared server, over HTTP and MQTT |
| **Collaborall** | We read, then write into WBD | STA as a *source*, and portability between two independent STA implementations |

## Architecture

A scheduled poll loop walks every registered poll adapter, fetches observations
since each Datastream's persisted cursor, and hands them to a transport-agnostic
ingest core: validate, map to STA entities, then write to one or more
FROST-Server targets. Push-mode vendors post to `/ingest/{vendorID}` and join the
same ingest core, so there is one write path to trust regardless of how data
arrives. A watchdog evaluates per-Datastream staleness and posts a freshness
alert on transition.

```
  SOURCES                                                     TARGETS

  REEN REST API (SULO)   --poll-->  \                     /-->  FROST-Server (WBD)
                                     >--  ingest core  --<        over HTTP and MQTT
  Collaborall STA        --read-->  /     validate          \-->  further targets
    via cmd/collaborall-reader            map to STA (OMS)         (dual-write)
        --push--> /ingest/{vendorID}      write + record

                                    Postgres state store:
                                    poll cursors, write log, watchdog state
```

### Layout

```
cmd/
  translation-layer     the service: scheduler, ingest core, writers, admin HTTP
  collaborall-reader    standalone reader; posts canonical batches to /ingest
  sulo-probe            read-only SULO/REEN diagnostic (writes nowhere)

internal/
  adapters              PollAdapter + PushAdapter contracts, Registry
    sulo                SULO via the REEN CMS REST API (poll)
    collaborall         push decoder + wire format shared with the reader
    dummy               synthetic fill-level generator for validation
  canonical             vendor-agnostic types; vendor shapes never escape an adapter
  ingest                validate → upsert chain → write → record (shared by both modes)
  validator             range, clock-skew and cursor rules
  oms                   STA entity payload builder and entity naming
  frost                 hand-rolled STA client, Writer, MQTT publisher
  state                 Postgres state store: cursors, write log, watchdog state
  scheduler             per-Datastream poll orchestration
  watchdog              freshness alert state machine
  api                   /healthz, /healthz/freshness, /metrics, push listener
  config, logging, metrics
```

## Running it

### Local stack, no credentials needed

Brings up Postgres, a local FROST-Server and the service, and writes to the
local FROST rather than anything shared:

```bash
docker compose -f deploy/docker-compose.yml up --build

curl http://localhost:8081/FROST-Server/v1.1/Things      # STA endpoint
curl http://localhost:8080/healthz                       # service admin
curl http://localhost:8080/metrics
```

With no vendor credentials configured, no adapter registers and the scheduler
ticks without doing work. To exercise the whole chain anyway, enable the
synthetic adapter:

```bash
DUMMY_ADAPTER_ENABLED=true docker compose -f deploy/docker-compose.yml up --build
```

It registers five fake containers around 's-Hertogenbosch and emits
deterministic fill levels every five minutes, so re-runs and restarts reproduce
the same values. Every entity it creates is named `Dummy …` and tagged
`properties.synthetic = "true"`. Never enable it in production.

```bash
# latest fill level per container: the canonical demo query
curl "http://localhost:8081/FROST-Server/v1.1/Things?\$expand=Datastreams(\$expand=Observations(\$orderby=phenomenonTime%20desc;\$top=1))"
```

### SULO, against the real vendor API

Check the credentials and see what the vendor exposes before writing anything
anywhere. `sulo-probe` makes the same calls the scheduler does and writes
nowhere:

```bash
go run ./cmd/sulo-probe                      # reads .env, samples 3 slots
go run ./cmd/sulo-probe -slots 10 -lookback 720h -debug
```

It prints the discovered slots, their coordinates, and the observed cadence per
stream. Use it after any credential rotation.

To run the service against SULO and write to the WBD testbed server, name both
compose files explicitly (which also suppresses the auto-loaded
`docker-compose.override.yml`, the Collaborall path):

```bash
export FROST_PASSWORD='...'        # PowerShell: $env:FROST_PASSWORD='...'
cd deploy
docker compose -f docker-compose.yml -f docker-compose.sulo.yml up --build postgres translation-layer
```

Then verify what landed:

```powershell
./deploy/check-wbd-sulo.ps1 -Detail      # read-only; counts entities and observations
```

### Collaborall replication

`deploy/docker-compose.override.yml` wires the Collaborall source through the
reader into WBD. It is Compose's default override file, so a bare
`docker compose up` in `deploy/` selects this path. It needs
`FROST_PASSWORD`, `COLLABORALL_USER`, `COLLABORALL_PASSWORD` and
`COLLABORALL_INGEST_SECRET` in your shell.

## Building

```bash
go mod download
go build ./...
go test ./...
```

Requires Go ≥ 1.25. No third-party STA library: the client is hand-rolled on
`net/http` per ADR-002, which turned out cheaper than the alternative and is a
finding in its own right (report §1.6).

## Deployment

Kubernetes manifests are in [deploy/k8s/](deploy/k8s/):

```bash
kubectl apply -f deploy/k8s/namespace.yaml
kubectl apply -f deploy/k8s/configmap.yaml
# populate secret.template.yaml -> secret.yaml (gitignored) first
kubectl apply -f deploy/k8s/secret.yaml
kubectl apply -f deploy/k8s/deployment.yaml
kubectl apply -f deploy/k8s/service.yaml
kubectl apply -f deploy/k8s/networkpolicy.yaml   # optional hardening
```

Single replica, `Recreate` strategy, per ADR-009: poll cursors assume a single
writer. Do not scale out without adding leader election.

## Configuration

[.env.example](.env.example) is the full catalogue with the reasoning inline.
The ones that decide behaviour:

| Variable | Purpose |
| ---------- | --------- |
| `FROST_TARGETS` | Comma-separated STA endpoints to write to; more than one dual-writes |
| `FROST_BASIC_AUTH_USER` / `_PASSWORD` | FROST credential (Basic wins over Bearer when set) |
| `SULO_API_BASE_URL` / `_USERNAME` / `_PASSWORD` | REEN session credentials; set all three or none |
| `SULO_EXPECTED_CADENCE_SECONDS` | Freshness expectation, **not** the sensor cadence — see report §4.13 |
| `POLL_INTERVAL_SECONDS` | Poll cycle, default 900 |
| `CURSOR_INIT_LOOKBACK_SECONDS` | How far back a newly discovered stream reaches on first sight |
| `ENTITY_NAME_PREFIX` | Marks our entities on a shared FROST-Server and avoids name collisions |
| `DUMMY_ADAPTER_ENABLED` | Synthetic data; never in production |

## Documents

| Document | Content |
| ---------- | --------- |
| [topic2-lessons-findings-and-results.md](docs/topic2-lessons-findings-and-results.md) | **The report.** Findings, problems, and what we would change about the standard |
| [sulo-sta-translation-layer-design.md](docs/sulo-sta-translation-layer-design.md) | Architecture, ADRs, Implementation Contract, operational runbook |
| [testbed-wbd-e2e-findings.md](docs/testbed-wbd-e2e-findings.md) | Raw end-to-end evidence against the WBD server |
| [collaborall-source-findings.md](docs/collaborall-source-findings.md) | Inspection of the Collaborall source server |
| [sulo-reen-source-findings.md](docs/sulo-reen-source-findings.md) | Inspection of the SULO/REEN vendor API, redacted where the vendor's documentation is confidential |

## License

Apache 2.0 — see [LICENSE](LICENSE). Documentation deliverables are published
separately under CC-BY 4.0.

## Performers (per the tender)

- Diego Tolen — Solution Architect (Clappform)
- Tom Griffioen — Project Management (Clappform)
- Bowen Harkema — OGC API / Software Engineer (Clappform)
- Yashvir Jhingur — OGC API / Software Engineer (Clappform)
- Wybren Terpstra — GIS Specialist (Clappform)
- Vincent Esajas — Domain Expert (SULO)
