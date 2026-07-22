# Geonovum 2026 Testbed — SULO → OGC SensorThings API translation layer

Implementation Topic #2 of the Geonovum Testbed Sensor Data 2026.
Clappform B.V. (lead) + SULO Group (research partner).

This service polls SULO's smart waste-container sensors (and, in
Phase 2, collection-vehicle GPS / RFID) and republishes their
observations as OGC SensorThings API (STA) entities on a
FROST-Server endpoint, modelled per the OGC Observations &
Measurements Standard (OMS).

The full design — including ADRs, the Implementation Contract,
Operational Runbook, and adversarial-review outcomes — lives in
[docs/sulo-sta-translation-layer-design.md](docs/sulo-sta-translation-layer-design.md).

## Status

**Base scaffolding is in place. Vendor connectors land next.** The
service compiles and runs; with zero adapters registered, the
scheduler ticks but does no work.

## Architecture (one-paragraph version)

A scheduled poll loop walks every registered vendor adapter, fetches
observations since each Datastream's persisted poll cursor, validates
them, maps them to STA entities through the OMS mapper, and writes
them to one or more FROST-Server targets (dual-write supports the
fallback-→-central FROST cutover). A separate watchdog evaluates
per-Datastream staleness every 30 minutes and POSTs a freshness
alert on transition. Cluster-internal admin endpoints expose
`/healthz`, `/healthz/freshness`, and `/metrics`.

```
┌──────────────────────────────────────────────────────────────┐
│  cmd/translation-layer                                       │
│  ├─ internal/config       env-driven Config                  │
│  ├─ internal/logging      slog JSON to stdout                │
│  ├─ internal/metrics      Prometheus registry                │
│  ├─ internal/canonical    vendor-agnostic types              │
│  ├─ internal/adapters     VendorAdapter interface + Registry │
│  ├─ internal/validator    F1 step 5 validation               │
│  ├─ internal/oms          STA entity payload builder         │
│  ├─ internal/frost        thin STA HTTP client + Writer      │
│  ├─ internal/state        Postgres state store (pgx)         │
│  ├─ internal/scheduler    per-Datastream poll orchestrator   │
│  ├─ internal/watchdog     freshness alert state machine      │
│  └─ internal/api          /healthz, /healthz/freshness, ...  │
└──────────────────────────────────────────────────────────────┘
```

## Local development

```bash
# 1. Bring up Postgres + FROST + the service.
docker compose -f deploy/docker-compose.yml up --build

# 2. STA endpoint:
curl http://localhost:8081/FROST-Server/v1.1/Things

# 3. Translation-layer admin:
curl http://localhost:8080/healthz
curl http://localhost:8080/healthz/freshness
curl http://localhost:8080/metrics
```

Without a registered adapter the FROST endpoint will be empty —
that's expected. Connectors are the next milestone.

## Validating end-to-end with the dummy adapter

To exercise the full pipeline (adapter → ingest core → FROST writer →
FROST-Server → STA queries) before the SULO adapter exists, enable the
synthetic `dummy` adapter. The provided `docker-compose.yml` already sets
`DUMMY_ADAPTER_ENABLED=true`, so a plain `up` produces queryable data:

```bash
docker compose -f deploy/docker-compose.yml up --build
```

It registers 5 synthetic waste containers around 's-Hertogenbosch and
emits deterministic fill-level observations every 5 minutes (seeded from a
1h lookback, so data appears on the first poll). After ~a minute:

```bash
# Synthetic containers show up as STA Things:
curl "http://localhost:8081/FROST-Server/v1.1/Things?\$count=true"

# Latest fill level per container (the canonical demo query):
curl "http://localhost:8081/FROST-Server/v1.1/Things?\$expand=Datastreams(\$expand=Observations(\$orderby=phenomenonTime%20desc;\$top=1))"

# Filter observations by time window:
curl "http://localhost:8081/FROST-Server/v1.1/Observations?\$filter=phenomenonTime%20gt%202026-01-01T00:00:00Z&\$top=5"

# Translation-layer freshness + metrics:
curl http://localhost:8080/healthz/freshness
curl http://localhost:8080/metrics | grep observations_
```

The dummy adapter is opt-in and must never be enabled in production
(`DUMMY_ADAPTER_ENABLED=false`, the default outside compose). Values are
deterministic per `(container, timestamp)`, so re-runs and restarts are
reproducible.

## Building from source

```bash
go mod download
go build ./...
go test ./...
```

Requires Go ≥ 1.25.

## Deployment

Kubernetes manifests are in [deploy/k8s/](deploy/k8s/). Apply in
order:

```bash
kubectl apply -f deploy/k8s/namespace.yaml
kubectl apply -f deploy/k8s/configmap.yaml
# populate deploy/k8s/secret.template.yaml -> secret.yaml (gitignored) first
kubectl apply -f deploy/k8s/secret.yaml
kubectl apply -f deploy/k8s/deployment.yaml
kubectl apply -f deploy/k8s/service.yaml
kubectl apply -f deploy/k8s/networkpolicy.yaml  # optional hardening
```

The Deployment runs as a single replica with `Recreate` strategy
per ADR-009 (single-writer invariant on poll cursors). Do not
scale to multiple replicas without introducing leader election.

## Configuration

See [.env.example](.env.example) for the full env-var catalogue;
the Implementation Contract in the design doc is the authoritative
reference.

## License

Apache 2.0 — see [LICENSE](LICENSE). Documentation deliverables
(reproducibility guide, runbook) are published separately under
CC-BY 4.0.

## Performers (per the tender)

- Diego Tolen — Solution Architect (Clappform)
- Tom Griffioen — Project Management (Clappform)
- Bowen Harkema — OGC API / Software Engineer (Clappform)
- Yashvir Jhingur — OGC API / Software Engineer (Clappform)
- Wybren Terpstra — GIS Specialist (Clappform)
- Vincent Esajas — Domain Expert (SULO)
