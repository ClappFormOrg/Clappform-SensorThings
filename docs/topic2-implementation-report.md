# Topic 2 — Connecting Sensors to a SensorThings API

## Initial Implementation Report

**Testbed:** Geonovum 2026 · Topic #2 (connecting sensors to an OGC SensorThings API server)
**Implementing party:** Clappform B.V.
**Target server:** Waterschap Brabantse Delta (WBD) FROST-Server — `https://sta.wbd-rd.nl/FROST-Server/v1.1`
**Status:** initial report — synthetic end-to-end validation complete; real SULO sensor
connection is the next milestone
**Date:** 2026-07-22

---

## Summary

We implemented an **intermediary translation layer** that connects sensor sources to the
Brabantse Delta OGC SensorThings API server, and validated the complete write path
end-to-end against the live endpoint using a synthetic sensor set — five containers, their
fill-level datastreams, and 1,400+ observations written and read back over both HTTP and
MQTT. The solution demonstrated the reliability properties that matter for production
ingestion: idempotent writes with no duplicates, restart-safe recovery of a multi-day data
gap, and automatic reconnection after a transport drop. The dominant implementation effort
was **not** the STA mapping — which was straightforward — but the operational envelope around
it: client-side network filtering, authentication edge cases, a customised FROST-Server
profile, and getting concurrency and idempotency correct under real network latency.
Connecting the real SULO sensors is the next milestone and reuses this now-proven pipeline
unchanged.

## 1. Objective

The second implementation topic focuses on connecting sensors to a SensorThings API (STA)
server: demonstrating how existing sensors can be adapted or connected so their observations
reach a central STA endpoint. It emphasises interoperability in practice — participants are
free to choose their own technical approach (direct sensor connections or intermediary
translation layers) — and asks each participant to connect one or more sensors, validate the
reliability of the solution, document a reproducible approach, demonstrate it publicly, and
capture lessons learned.

This report covers our implementation against those tasks. It is an *initial* report: the
full write path has been validated end-to-end against the live WBD server using a synthetic
sensor set, and the connection of the real SULO waste-container sensors is the next step,
reusing the same, now-proven pipeline.

## 2. Technical approach and choices

**We chose an intermediary translation layer rather than a direct sensor-to-STA connection.**
The rationale reflects the practical reality of the data source: the SULO waste-container
fleet is exposed through a vendor API (and, in the wider programme, an ItsPerfect-style ERP),
not as devices that can speak STA natively. An intermediary lets us:

- keep vendor-specific concerns (authentication, pagination, rate limits, wire formats)
  isolated behind a stable adapter contract;
- apply a single, testable mapping from canonical types to the OGC STA entity model;
- reuse the exact same write path for any future sensor source by swapping only the adapter.

Key technical decisions:

- **Language: Go** (≥ 1.25) for a small, statically-linked, concurrency-friendly service.
- **Hand-rolled STA client** over `net/http` rather than a third-party STA library, to keep
  the dependency surface minimal and the upsert/idempotency behaviour explicit.
- **Two source modes** behind one ingestion core: *poll* (the layer pulls on a schedule) and
  *push* (the vendor posts to the layer). SULO is poll-mode in v1.
- **A synthetic "dummy" adapter** to exercise the entire pipeline before the real sensor
  connector exists — deterministic, clearly-labelled fake data so the full chain
  (adapter → ingest → FROST-Server) could be validated against the live WBD endpoint without
  a dependency on vendor availability and without polluting real records.

## 3. What was built

A translation layer with a clean separation of concerns:

```
Sensor source        Translation layer                                 STA server
────────────    ─────────────────────────────────────────────    ─────────────────
Adapter    →    Ingest core  →  Validator  →  FROST writer     →   WBD FROST-Server
(poll/push)     (cursor,         (clock skew,   (entity upsert       (OGC STA v1.1;
                 dedup)           ranges)         + idempotency)       REST + MQTT)
```

- **Adapter contract** — vendor implementations expose Things, their Datastreams, and
  Observations as canonical types; the rest of the layer depends only on this contract.
- **Ingest core** — validates, resolves the STA entity chain
  (Thing → Location → Sensor → ObservedProperty → Datastream → FeatureOfInterest), writes each
  observation, records a write-log, and advances the poll cursor.
- **FROST writer** — name-keyed entity upsert (`GET ?$filter=name eq …` then create), a
  pre-write idempotency probe for observations, and a **dual transport**: HTTP POST or, when
  enabled, **MQTT publish over WebSockets** (`wss://…/mqtt`) for observation creates.
- **State store** — Postgres holding per-datastream cursors and the observation write-log
  (idempotency key: `datastream_id, phenomenon_time`), giving restart-safety.

## 4. Sensors connected and demonstration

For this cycle we connected a **synthetic sensor set** as the validation vehicle: five waste
containers around 's-Hertogenbosch, each modelled as a Thing with one fill-level Datastream
(unit: percent) and a sensor, emitting deterministic fill-level observations on a 5-minute
cadence. All five were registered on WBD end-to-end and read back through the public STA API.

| `@iot.id` | Thing | Native ID |
|---:|---|---|
| 96 | Dummy Container DUMMY-0001 | DUMMY-0001 |
| 97 | Dummy Container DUMMY-0003 | DUMMY-0003 |
| 98 | Dummy Container DUMMY-0004 | DUMMY-0004 |
| 99 | Dummy Container DUMMY-0002 | DUMMY-0002 |
| 100 | Dummy Container DUMMY-0005 | DUMMY-0005 |

Each Thing carries one fill-level Datastream (STA ids in the 661–665 range). As a worked
example, container 96 → Datastream **663** accumulated a contiguous series of fill-level
observations (1423 and still climbing while a multi-day back-fill drained). Common properties
on every Thing: `vendor=dummy`, `clappform_source_system=smartsulo`,
`area=s-hertogenbosch-dummy`, `synthetic=true`. Each stored observation carries validation
provenance (`resultQuality.validated_by = clappform-translation-layer`) and the vendor
idempotency key (`parameters.raw_observation_id`).

**Public demonstration** is planned on the shared WBD server (the synthetic entities are
already publicly queryable there via anonymous/authenticated STA reads). A status slide deck
and this report accompany the demonstration; the reproducible run instructions below let any
reviewer stand the pipeline up themselves.

The connection of the **real SULO sensors** is the immediate next milestone: it swaps the
dummy adapter for the SULO connector behind the same canonical contract, so the validated
ingest → validate → FROST path is unchanged.

## 5. Reliability validation

Reliability was validated against the live WBD server, not a local mock:

- **End-to-end persistence.** The full Thing → Datastream → Observation chain was created and
  read back through the public API.
- **Idempotency / no duplicates.** Observations formed a *contiguous* 5-minute series; the
  stored count equalled the number of distinct cadence ticks in the window (16 → 455 → 1016 →
  1423 across the run), i.e. no double-writes, despite MQTT publish offering no server-side
  uniqueness guarantee.
- **Restart-safe gap recovery.** Because the poll cursor persists, a multi-day gap between
  runs was **idempotently back-filled** with no duplicates — evidence that ingestion is
  restart-safe and gap-filling.
- **Transport resilience.** The MQTT/WebSocket session dropped (an abnormal-closure event)
  and **auto-reconnected within ~300 ms**; publishes during the gap retried.
- **Two write transports validated.** Observation writes were confirmed over both HTTP POST
  and MQTT publish (`wss`), with entity upserts remaining on HTTP in both modes.

Detailed evidence, queries, and raw responses are in
[`testbed-wbd-e2e-findings.md`](testbed-wbd-e2e-findings.md).

## 6. Reproducible approach

The pipeline runs from a single `docker compose` command (state-store Postgres + the
translation layer), with all behaviour driven by environment variables. A reviewer can
reproduce the synthetic end-to-end run without any vendor credentials:

```
# from deploy/, with the WBD Basic-Auth password in the shell
docker compose up --build postgres translation-layer
```

Verification is by plain STA reads, e.g.:

```
GET /FROST-Server/v1.1/Things?$filter=startswith(name,'Dummy')&$count=true
GET /FROST-Server/v1.1/Things(96)/Datastreams?$count=true
GET /FROST-Server/v1.1/Datastreams(663)/Observations?$count=true
```

Configuration, deployment manifests, and the full verification query set are documented in
the design document and the findings report. Synthetic entities are clearly labelled and a
cleanup script is provided for a pristine reset.

## 7. Findings and lessons learned

The exercise gave concrete insight into the practical effort of exposing sensor data through
a common standard. The most useful lessons were rarely about STA itself — they were about the
network, the authentication, and the specific server build:

1. **Transport failures can masquerade as server misconfiguration.** The first connection
   attempts failed with a TLS certificate-name mismatch and intermittent connection drops.
   The root cause was **client-side network filtering** (a TLS-intercepting proxy on our
   network), not a WBD defect. Lesson: distinguish client-side interception from a genuine
   server certificate problem before escalating; the fix is to allowlist the endpoint.
2. **Authentication edge cases matter.** WBD authenticates *all* requests (reads included), so
   credentials must be attached to the upsert probes, not just writes. And a missing/empty
   password returned an HTTP **500**, not a clean 401 — so a 500 there is best read as a
   credential problem.
3. **"FROST-Server" is not one thing.** The WBD deployment is a customised build with
   non-standard entities (`Projects`, `DeviceSecrets`, a `restricted` flag) — a project-scoped
   authorization extension. Writes were accepted without project association, but integration
   contracts should not assume a stock server.
4. **Concurrency is a correctness concern for shared entities.** Resolving datastreams in
   parallel raced when several shared one Sensor/ObservedProperty, creating duplicates
   (FROST does not reject duplicate names). We fixed this with a concurrency-safe cache and a
   per-key single-flight around the create. Lesson: name-keyed upserts need coordination when
   entities are shared across concurrently-processed streams.
5. **Idempotency has a throughput cost.** Each observation is gated by a synchronous HTTP
   idempotency probe. During a large back-fill through a latency-adding proxy, that probe —
   not the write — became the bottleneck. Mitigation: a configurable request timeout; future
   optimisation: rely on the persisted cursor to skip the probe when freshness is already
   guaranteed.
6. **Interoperability is per-server, not just per-spec.** Dual-writing to a *second*, independent
   STA server (`sta-server.collaborall.net`) surfaced a payload our WBD run accepts happily: our
   `Sensor.metadata` is sent as an inline JSON object, but the second server enforces a stricter
   reading and returns `422 — "The metadata field must be a string."` The OGC spec leaves
   `Sensor.metadata`'s shape to `encodingType`, so both servers are defensible; the *same payload*
   is simply portable to one and not the other. Lesson: "STA-conformant" does not guarantee
   cross-server portability — serialize `metadata` as a JSON-encoded string (and treat `422`
   schema rejections as *permanent*, not transient-retryable).

Practical-effort takeaway: mapping to the STA entity model was the *straightforward* part.
The real effort went into the operational envelope — network access, authentication quirks,
server-specific behaviour, and getting concurrency and idempotency right under real latency.

> The full treatment of these lessons — every challenge with its root cause and resolution, a
> point-by-point assessment of what the standard does well, what we would change and why, and
> the adoption case for third parties — is in
> [`topic2-lessons-and-standard-evaluation.md`](topic2-lessons-and-standard-evaluation.md).
> It also covers the later Collaborall work (reading a *second*, unfamiliar STA server as a
> data source), which post-dates this report.

### 7.1 Where the effort went

A qualitative breakdown of implementation effort (relative, not measured hours), to answer
the topic's question about the *practical effort* of exposing sensor data through a common
standard:

| Area | Relative effort | Why |
|---|---|---|
| STA entity mapping + write path | **Low** | The OGC STA model (Thing/Location/Sensor/ObservedProperty/Datastream/Observation) is well-specified; the mapping and name-keyed upsert were mechanical. |
| Network / transport | **High** | Diagnosing the client-side TLS-intercepting proxy (cert-name mismatch + dropped connections) and working around it consumed the most time — and it was the hardest to attribute correctly. |
| Authentication | **Medium** | Realising reads are authenticated too, and that an empty password yields a 500 rather than a 401, took investigation. |
| Server-specific behaviour | **Medium** | The customised WBD FROST profile (`Projects`, `restricted`, extra entities) required verifying that plain writes are accepted. |
| Concurrency + idempotency correctness | **Medium–High** | The shared-entity upsert race and the observation idempotency-under-load behaviour needed a real fix (concurrency-safe cache + per-key single-flight) and careful reasoning. |
| Reproducibility / tooling | **Low–Medium** | The synthetic dummy adapter and one-command `docker compose` run paid for themselves by making the whole path testable before the real sensor source existed. |

The distribution is the headline lesson: **standard-conformance was cheap; production
robustness against a real network, a real auth scheme, and a real (non-stock) server was
where the engineering actually lived.**

## 8. Status and next steps

- ✅ Synthetic sensor set connected and validated end-to-end on WBD (HTTP and MQTT).
- ✅ Reliability properties demonstrated: idempotency, restart-safe gap recovery, resilience.
- ✅ Reproducible run + verification documented; findings captured.
- ⏭️ **Connect the real SULO sensors** (swap the adapter; pipeline unchanged).
- ⏭️ **Network hardening** — allowlist `sta.wbd-rd.nl` and disable the TLS skip-verify
  workaround so production traffic verifies the certificate genuinely.
- ⏭️ **Public demonstration** on the shared WBD endpoint, with this report and the status deck.
- ⏭️ Confirm the WBD project/authorization model for production entities; exercise dual-write
  and the freshness watchdog before go-live.
- ⏭️ **Cross-server portability** — serialize `Sensor.metadata` as a JSON string (per-target if
  needed) and reclassify `STA-422` as permanent, so writes succeed against stricter STA servers
  such as `sta-server.collaborall.net` (see findings §3.8).

---

### References

- Lessons learned + standard evaluation: [`topic2-lessons-and-standard-evaluation.md`](topic2-lessons-and-standard-evaluation.md)
- Architecture and implementation contract: [`sulo-sta-translation-layer-design.md`](sulo-sta-translation-layer-design.md)
- End-to-end validation evidence and findings: [`testbed-wbd-e2e-findings.md`](testbed-wbd-e2e-findings.md)
- Status presentation: [`testbed-wbd-status-deck.md`](testbed-wbd-status-deck.md)
