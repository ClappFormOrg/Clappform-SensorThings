# Design Document: SULO → OGC SensorThings API Translation Layer

**Version**: 1.0
**Date**: 2026-05-22
**Author**: Sarah (Product Owner & Solutions Architect)
**Quality Score**: To be re-scored after Phase 5 audit (interim: 76/100)
**Status**: Draft
**Programme**: Geonovum Testbed Sensor Data 2026 — Implementation Topic #2
**Bid**: Clappform B.V. (lead) + SULO Group (research partner)
**Phasing**: Setup May 2026 · Build June 2026 · Document & Demo July 2026 · Public availability through ≥ January 2027

---

# Part 1: Product Requirements

---

## Executive Summary

Dutch municipalities run thousands of smart waste containers and collection vehicles that emit fill-level, RFID, and GPS observations into proprietary vendor platforms. Today these streams cannot be queried by digital-twin platforms, cross-domain analytics, or municipal spatial data infrastructures without bespoke point-to-point connectors per vendor.

This project delivers a **vendor-agnostic translation layer** that ingests SULO's operational sensor streams from real municipal containers (starting with the Afvalstoffendienst 's-Hertogenbosch deployment) and republishes them as OGC SensorThings API (STA) observations on a FROST-Server instance, modelled per the OGC Observations & Measurements Standard (OMS) and georeferenced in EPSG:4326. The translation layer is built behind a SULO adapter so a second vendor adapter can be added in ≤ 1 working day per the reproducibility guide.

The deliverable matters for three audiences. Geonovum gets evidence that the OGC STA standard scales to mobile and high-cadence municipal sensors beyond the wastewater reference use case. Municipalities (s-Hertogenbosch first, then other municipalities) get a single read endpoint they can wire into their digital twins. Other vendors get a worked example showing how to put their proprietary streams behind OGC STA without changing their own platform.

---

## Project Constraints Applied

*Constraints extracted from the Geonovum tender (Topic #2) and from `internal workstation conventions`. A developer implementing this feature must comply with all items listed here.*

**From the Geonovum tender:**
- OGC SensorThings API conformance — the published endpoint must pass STA query verification (filter by `phenomenonTime`, by location, by `ObservedProperty`).
- OMS-aligned data modelling. Where Topic #1 (Brabantse Delta wastewater) publishes their OMS choices, we re-align ours to match.
- All spatial entities (`Thing.Location.location`, `FeatureOfInterest.feature`) encoded as GeoJSON in EPSG:4326.
- Translation layer must be **vendor-agnostic**. Vendor-specific code lives inside an adapter; the rest of the layer must work without modification when a second adapter is added.
- Documentation deliverables published under Creative Commons Attribution 4.0 International (CC-BY 4.0).
- Software deliverables published under an OSI-approved open-source license — **Apache 2.0** (chosen over MIT to give downstream users explicit patent protection, which matters in a multi-vendor reproducibility setting).
- Translation layer config + documentation added to the Geonovum GitHub repository.
- Integration pipeline, connected sensors, and STA endpoint remain operational and publicly accessible for **at least six months after the testbed concludes** (i.e., through at least 2027-01-31).
- Bi-weekly Geonovum coordination meetings (Tom Greiffioen as single point of contact).
- Active coordination with the Topic #1 contractor; where they publish a central FROST server, our data writes there.

**From `internal workstation conventions` (workstation conventions):**
- File-search uses `Glob`, content-search uses `Grep`, file edits use `Edit`/`Write`, system commands use `Bash`. Implementers should follow these tool conventions.
- Today's date for all relative dates in this document: **2026-05-22**.

---

## Problem Statement

**Current situation**: Operational waste-container fill-level data, RFID pickup events, and collection-vehicle GPS streams are emitted by SULO's SmartSULO platform via proprietary REST APIs. To consume these streams, every downstream system (municipal digital twin, BI dashboard, sustainability reporting tool) writes its own SmartSULO connector — duplicated effort, no standard schema, no interoperability with other waste-sensor vendors, and no path into Geonovum-aligned DTaaS digital twin platforms in cities such as 's-Hertogenbosch, Alkmaar, and Almere.

**Proposed solution**: A scheduled-polling translation layer that pulls observations from SmartSULO via a REST adapter, transforms them into OGC OMS-compliant SensorThings entities, and writes them to a FROST-Server STA endpoint. The translation layer maintains a per-Datastream poll cursor so it is restart-safe and exactly-once on writes. A second vendor adapter can be added without changes to the rest of the layer.

**Business impact**: After delivery, any digital-twin platform or municipal analytics tool that speaks OGC STA can consume SULO's operational sensors without writing SULO-specific code. The translation pattern, published as a reproducibility guide under CC-BY 4.0 with Apache-2.0 reference code, lowers the integration cost for the next vendor and the next municipality.

**Cost of not building**: Geonovum's DTaaS direction proceeds without a worked example for the waste-and-mobility sensor domain. Municipalities such as Alkmaar and Almere continue to build vendor-specific connectors per sensor type, locking themselves further into vendor-by-vendor integration debt and undercutting the cross-domain analytics that justify the digital-twin investment in the first place.

---

## Success Metrics

*Metrics proposed by Sarah; review and revise during Phase 5.*

| # | Metric | Target | Measurement method |
|---|--------|--------|--------------------|
| M1 | Containers connected end-to-end with valid Observations on FROST | ≥ 1,000 unique `Thing` entities, each with at least one `Observation` in the last 24h | `GET Things?$count=true` and `GET Things?$expand=Datastreams($expand=Observations($orderby=phenomenonTime desc;$top=1))` on the STA endpoint |
| M2 | Observation freshness (steady state) | p95 freshness ≤ (poll-interval + SULO API p95 round-trip + 30s); default poll interval = 15 min. **Target calibrated after first 7 days of real SULO traffic** — initial provisional target = 16 min | Compare `result_time` to ingestion timestamp; emit as `observation_freshness_seconds`; calibrate target during Phase 2 week 1 |
| M3 | STA query verification | All four canonical queries succeed and return non-empty result sets: filter by `phenomenonTime`, filter by location bbox, filter by `ObservedProperty`, expand `Datastreams/Observations` | Automated CI check; documented in reproducibility guide |
| M4 | Parity against SmartSULO native | For a sample of 50 randomly selected (container, day) pairs, ≥ 99% of observations match SmartSULO native readings on value (±0.5 %) and timestamp (±60 s) | Daily parity job; results logged; mismatches written to a parity-report table |
| M5 | Reproducibility time-to-second-adapter | A new contributor following the guide can wire a second non-SULO vendor adapter and produce valid Observations on FROST in ≤ 1 working day (8h) | Measured during testbed demo; ideally an external reviewer attempts it during a public session |
| M6 | Operational uptime through the 6-month tail | Data freshness alert (M2) does not fire for > 24h cumulative across any rolling 30-day window between 2026-07-31 and 2027-01-31 | Alert log retained in observability stack |

**Validation timeline**:
- M1, M3, M4 — validated continuously during Phase 2 (June 2026) build; re-validated at the July 2026 demo.
- M2, M6 — measured continuously from go-live (target 2026-06-30) through 2027-01-31.
- M5 — measured at the July 2026 public demonstration.

---

## User Personas

### Primary: Geonovum reviewer / OGC standards evaluator
- **Role**: Reviewer in the Geonovum testbed evaluation team, OGC standards practitioner.
- **Goals**: Verify that the testbed's STA endpoint genuinely conforms to OGC STA and OMS; verify that the translation pattern is reproducible by a third party.
- **Pain points**: Bid documents often claim STA compliance without query verification. Demo endpoints often go dark days after the testbed concludes.
- **Technical level**: Advanced. Will read the OpenAPI spec, run STA queries by hand, read the reproducibility guide critically.
- **Frequency**: Reads the doc once; queries the endpoint during the testbed window plus spot checks during the 6-month tail.

### Secondary: Municipal digital-twin developer (e.g., Gemeente Alkmaar, Gemeente Almere)
- **Role**: Backend or GIS engineer at a Dutch municipality integrating sensor streams into a digital-twin platform.
- **Goals**: Query waste-sensor data using the same OGC STA patterns they already use for wastewater, weather, traffic.
- **Pain points**: One connector per vendor; vendor data shapes change without warning; no shared semantics.
- **Technical level**: Intermediate to advanced. Comfortable with REST/GeoJSON; may not know STA spec depth.
- **Frequency**: Recurring — wires the endpoint into their twin once, then queries on an automated cadence.

### Tertiary: Vendor engineer adopting the reproducibility guide
- **Role**: Engineer at a different waste-container or mobility-sensor vendor.
- **Goals**: Add their own adapter to the same translation pattern to expose their proprietary stream via STA.
- **Pain points**: STA spec is large; OMS modelling decisions for non-canonical phenomena (RFID events, moving Things) are not obvious.
- **Technical level**: Intermediate. Knows their own API; new to OGC.
- **Frequency**: Reads the guide once; expects to be productive within 1 working day (M5).

### Operational: Clappform translation-layer maintainer (Bowen Harkema during testbed; on-call rotation after)
- **Role**: Engineer responsible for keeping the layer running through the 6-month tail.
- **Goals**: Know within hours when data has stopped flowing; restart the layer cleanly without data loss or duplication.
- **Pain points**: A six-month unattended tail with logs-only would normally hide silent failures.
- **Technical level**: Advanced.
- **Frequency**: Receives the data-freshness alert when it fires; otherwise reads weekly check-in.

---

## User Stories & Acceptance Criteria

### Story 1: Query containers and their latest fill levels via STA

**As a** municipal digital-twin developer
**I want to** issue an OGC STA query that returns every connected container and its most recent fill-level observation
**So that** I can render container fill state on a map in my digital twin without writing SULO-specific code

**Acceptance criteria:**
- [ ] `GET /v1.1/Things?$expand=Locations,Datastreams($expand=Observations($orderby=phenomenonTime desc;$top=1))` returns ≥ 1,000 Things, each with at least one Datastream and one Observation.
- [ ] Every returned `Thing.Locations[0].location` is a valid GeoJSON `Point` in EPSG:4326.
- [ ] Every returned fill-level Observation has `unitOfMeasurement.symbol == "%"` and a `result` in `[0, 100]`.
- [ ] Query response time ≤ 5 s at p95 for the canonical query above against the realistic-city scale (1,000 Things).

### Story 2: Filter observations by time window and bounding box

**As a** Geonovum reviewer
**I want to** filter Observations by `phenomenonTime` interval and by spatial bounding box in a single query
**So that** I can confirm STA filter conformance against a non-trivial query

**Acceptance criteria:**
- [ ] `GET /v1.1/Observations?$filter=phenomenonTime ge 2026-06-15T00:00:00Z and phenomenonTime lt 2026-06-16T00:00:00Z and st_within(FeatureOfInterest/feature, geography'POLYGON(...)')` returns a non-empty result.
- [ ] Spatial filter uses PostGIS (FROST-Server default) and works for both `Locations/location` and `FeatureOfInterest/feature`.
- [ ] Time filter works at minute granularity.

### Story 3: Replay observations after a translation-layer outage

**As a** translation-layer maintainer
**I want** the translation layer to resume from the last successfully written observation after any restart or outage
**So that** no observations are dropped and none are duplicated

**Acceptance criteria:**
- [ ] After a forced restart of the translation-layer pod, the next poll cycle resumes from the persisted poll cursor; the resulting STA Observation count is unchanged at steady state.
- [ ] Observations are idempotent on write: a re-pushed observation for the same `(Datastream.@iot.id, phenomenonTime)` does not create a duplicate.
- [ ] After a simulated 1-hour SULO API outage, all observations published by SULO during the outage window are written to FROST within one poll cycle of recovery.

### Story 4: Receive a freshness alert when data stops flowing

**As a** translation-layer maintainer
**I want** a single alert that fires when no new Observations have been written to FROST in the last 6 hours
**So that** the 6-month operational commitment is not broken by silent failure

**Acceptance criteria:**
- [ ] Alert fires (email or chat webhook to a configured destination) when `max(Observation.resultTime) < now() - 6h` AND the layer is supposed to be running.
- [ ] Alert does not fire during scheduled maintenance windows (configurable suppression).
- [ ] Alert state is observable in the freshness endpoint (`GET /healthz/freshness` on the translation layer).

### Story 5: Add a second vendor adapter from the reproducibility guide

**As a** vendor engineer
**I want** a step-by-step guide that walks me from "I have a proprietary sensor REST API" to "I have OMS-compliant Observations on a FROST endpoint"
**So that** I can replicate the integration for my own platform without consulting the original team

**Acceptance criteria:**
- [ ] Guide includes a complete worked example for SULO (the reference adapter).
- [ ] Guide includes a partially worked example for a fictional second vendor with a different API shape (REST + JSON, different pagination, different timestamp convention) to demonstrate the adapter contract.
- [ ] Guide includes a checklist of OMS modelling decisions for new phenomena (units, observationType URI, observed-property definition URI).
- [ ] An external engineer following only the guide produces valid Observations on FROST in ≤ 1 working day (M5).

### Story 6: Migrate observations from the fallback FROST to the central FROST

**As a** translation-layer maintainer
**I want** a documented migration procedure to switch from the testbed fallback FROST instance to the Topic #1 central FROST instance
**So that** if and when central FROST becomes available, we can move without data loss

**Acceptance criteria:**
- [ ] Migration procedure is documented in the reproducibility guide.
- [ ] Procedure is dry-runnable: the translation layer accepts a second `STA_TARGETS` configuration and writes to both targets in parallel during a cutover window.
- [ ] After cutover, the OMS entity identifiers are stable (same `name` and `properties` payload) so external consumers do not see entity churn.

---

## Functional Requirements

### F1. Scheduled polling of SmartSULO

**Description**: The translation layer polls SmartSULO REST endpoints on a configurable schedule (default 15 minutes), retrieves observations since the last poll cursor for each tracked Datastream, and queues them for transformation.

**User flow** (system-driven, not user-driven):
1. **Trigger**: Cron tick fires inside the translation layer (default every 15 minutes per Datastream, configurable per environment).
2. **Cursor read**: Layer reads the persisted poll cursor `(datastream_id, last_phenomenon_time)` from its internal Postgres state store.
3. **SULO call**: Adapter calls the SmartSULO REST endpoint for that Datastream's container with `?since=<last_phenomenon_time>`. Adapter handles paging until exhausted.
4. **Transform**: Each raw SULO observation is transformed by the SULO adapter into the internal canonical observation envelope (see Data Model).
5. **Validation**: Validator drops observations that fail any of: `result` missing or NaN; `result` outside `[0, 100]` inclusive for fill-level Datastreams; `phenomenonTime > now() + 5 min` (clock-skew tolerance); `phenomenonTime ≤ cursor` (late/duplicate). Dropped observations are logged with a reason code (`missing_result`, `out_of_range`, `in_future`, `before_cursor`) and counted.
6. **Write to FROST**: Each accepted observation is written to FROST as a POST to `/Datastreams(<id>)/Observations`. Idempotency is enforced by checking for an existing `(datastream_id, phenomenonTime)` before write.
7. **Cursor advance**: The poll cursor advances to `max(phenomenonTime)` across (a) successfully written observations and (b) definitively-rejected observations (reason in `{out_of_range, in_future, before_cursor, missing_result, frost_4xx}`). It does **not** advance past any observation that failed for a retryable reason (network, FROST 5xx, vendor transient). If any retryable failure remains after F4 backoff exhaustion, the cycle is failed and the cursor stays at its previous value; the next cron tick re-polls from there.
8. **Completion**: Metrics emitted (observations polled, accepted, written, dropped). On error, retry per F4.

**Decision branches**:
- If SULO returns 0 new observations: log, no-op, do not advance cursor.
- If SULO returns observations whose `phenomenonTime <= cursor`: drop as late/duplicate (already covered).
- If FROST POST fails with 4xx: log and skip (do not retry — the observation is malformed).
- If FROST POST fails with 5xx or network error: enter retry per F4.

**Error states**:
- SULO unreachable: retry per F4. Poll cursor unchanged. Eventual freshness alert fires (F5).
- FROST unreachable: same as above.
- Validation rejects > 5% of observations in a poll cycle: emit a `data_quality_degraded` log event (no alert in v1 — manual check-in catches this).

### F2. Vendor adapter contract

**Description**: All vendor-specific logic lives inside an adapter that implements a single interface. The rest of the translation layer is vendor-agnostic.

**Adapter contract** (Python-style for clarity; actual language confirmed in ADR-001):

```python
class VendorAdapter(Protocol):
    vendor_id: str  # e.g. "sulo"

    def list_things(self) -> Iterable[CanonicalThing]:
        """Discover every container/vehicle this vendor exposes."""

    def list_datastreams_for_thing(self, thing_id: str) -> Iterable[CanonicalDatastream]:
        """Discover every datastream (fill-level, temperature, battery, ...) for a Thing."""

    def fetch_observations(
        self,
        datastream_id: str,
        since: datetime,
        limit: int,
    ) -> Iterable[CanonicalObservation]:
        """Pull observations since the last poll cursor. Adapter handles pagination."""
```

**Error classification** (mandatory convention every adapter follows):

| Vendor situation | Exception type | Layer behavior |
|------------------|----------------|----------------|
| HTTP 5xx | `VendorTransientError` | Retry per F4 |
| Network error, DNS, connection refused | `VendorTransientError` | Retry per F4 |
| JSON parse error on response | `VendorTransientError` | Retry per F4 |
| HTTP 429 rate limit | `VendorTransientError` with `retry_after` honoring `Retry-After` header (clamped to ≤ 5 min) | Retry per F4, respecting hint |
| HTTP 401 / 403 (auth issue) | `VendorPermanentError` | Skip Datastream this cycle, log, do not advance cursor |
| HTTP 404 on a known Datastream | `VendorPermanentError` | Skip Datastream this cycle, log; layer may eventually deregister the Datastream after N consecutive cycles (out of scope for v1) |
| HTTP 400 with vendor-confirmed unrecoverable shape | `VendorPermanentError` | Skip Datastream this cycle, log |
| Any other unclassified exception | `VendorTransientError` (default) | Retry per F4 |

**Decision branches**:
- If the adapter raises `VendorTransientError`: layer retries per F4.
- If the adapter raises `VendorPermanentError`: layer skips this Datastream for this cycle, logs, increments error counter; does not advance cursor.

### F3. OMS mapping and FROST write

**Description**: Canonical observations are mapped to STA entities and written to FROST. Things, Sensors, ObservedProperties, Datastreams, and FeaturesOfInterest are upserted by stable `name` + `properties` keys.

**User flow**:
1. On first observation for a Datastream, the layer upserts the chain: `Thing` (by `name`) → `Location` (by `name` + GeoJSON) → `Sensor` (by `name`) → `ObservedProperty` (by `definition` URI) → `Datastream` (by `name`).
2. `FeatureOfInterest` is upserted by `name` (containers use container name; vehicle observations are deferred to ADR-003).
3. The Observation is POSTed to `/Datastreams(<id>)/Observations`.

**Decision branches**:
- Container moves location (relocated by Afvalstoffendienst): a new `Location` entity is created and linked to the existing `Thing`; previous Locations are preserved via `HistoricalLocations`.
- Sensor metadata changes (firmware version, model): new metadata is patched on the `Sensor` entity; no new Sensor created.

### F4. Retry and backoff

**Description**: Transient failures on either side (SULO or FROST) are retried with exponential backoff inside the translation layer's poll cycle.

**Rules**:
- Initial retry delay: 5 s.
- Multiplier: 2x per attempt.
- Max attempts: 5 (total elapsed ≈ 75 s).
- After max attempts in a cycle: the cycle fails, cursor unchanged, error logged. Next cron tick will try again.
- All retries are safe: SULO calls are GETs (idempotent); FROST POSTs are de-duplicated by `(datastream_id, phenomenonTime)` lookup before insert.

### F5. Data freshness alert

**Description**: An alert fires when the count of stale Datastreams exceeds a small threshold. Staleness is computed **per Datastream** using its declared `expected_cadence_seconds`.

**Per-Datastream staleness rule**:
- For Datastream `D` with `expected_cadence_seconds = c`, the staleness threshold is `max(3·c, 60·60)` — i.e., three expected cadences or 1 hour, whichever is larger.
- `D` is **stale** iff `now() - D.last_written_at > threshold(D)`.

**Implementation**:
- A separate watchdog cron tick runs every 30 minutes.
- Per tick: query each Datastream's `last_written_at` and `expected_cadence_seconds`, compute staleness, count the stale set.
- If `count(stale_datastreams) > max(1, 1% of total_datastreams)` AND no scheduled maintenance window is active, the watchdog enters a `stale_pending` state. The alert **only fires after two consecutive 30-min ticks** of stale_pending (i.e., ≥ 30 minutes of sustained staleness) — this hysteresis prevents flapping.
- Recovery fires immediately on the first tick where the count returns to zero.
- Alert state persisted in `translation_state.watchdog_state`.
- The alert is *stateful*: fires once on entry to `stale`, once on `recovered`. Does not spam every 30 minutes.
- The alert state is exposed at `GET /healthz/freshness` — returns JSON `{ status, stale_count, total_count, since_ts, examples: [<datastream_name>, ...] }` (up to 5 example stale Datastreams for triage).
- Alert payload is POSTed to `FRESHNESS_ALERT_WEBHOOK_URL` (configurable; supports SMTP-via-webhook or any chat webhook). Payload includes the stale count and up to 5 example Datastream names.

**Rationale for per-Datastream**: A single global "no observations in 6h" alert would either miss slow-cadence Datastreams (which legitimately go quiet for hours) or fire spuriously on every quiet container. The per-Datastream model auto-calibrates from each Datastream's declared cadence; the count-based trigger avoids paging on a single failed Datastream while catching systemic outages.

### F6. Configurable dual-write for FROST cutover

**Description**: The translation layer accepts a list of FROST targets, not a single one. During the cutover from fallback FROST to central FROST, it writes to both. Once central is verified, the fallback is removed from the list.

**Behavior**:
- Each configured target has its own credentials and retry state.
- A write is considered successful if all configured targets accept it.
- On asymmetric failure (target A accepts, target B fails), the cursor is not advanced; the cycle is retried; the eventually-consistent guarantee holds.

### Out of Scope

- **Vehicles and RFID collection events**: deferred to a Phase 2 stretch deliverable, with modelling decision due **2026-06-15** (ADR-003). MVP through July demo ships containers + fill-level only.
- **Real-time streaming *ingestion* (MQTT push, webhooks)**: explicitly out of scope; polling is the only ingestion mode in v1. (Note: MQTT is supported on the *outbound* side as an optional FROST write transport — publishing Observation creates to the FROST broker instead of HTTP POST; see `MQTT_ENABLED` in the Implementation Contract. This concerns how the layer writes, not how it ingests.)
- **Write API exposed to third parties**: the only writer to FROST is the translation layer itself. Third parties consume the STA endpoint read-only.
- **Multi-tenancy on the STA endpoint**: a single STA endpoint serves all consumers; no per-tenant data scoping in v1.
- **Per-municipality access control**: all connected data is publicly readable (anonymous GET).
- **Container-level historical bulk backfill before the testbed start**: only observations from 2026-05-22 onwards are ingested in v1; bulk backfill is documented as a follow-on activity.
- **Vendor-specific dashboards**: BI / visualization is downstream of STA; the testbed ships STA and the reproducibility guide only.

---

## Technical Constraints

### Performance

- **Steady-state ingestion**: support 1,000 Things × 1 fill-level Datastream × 1 observation/hour = ~24,000 observations/day, with poll cycles every 15 minutes.
- **STA query latency**: p95 ≤ 5 s for the canonical "all Things + latest Observation" query (Story 1).
- **Cold-start ingest**: full initial discovery of 1,000 Things and seeding of their Datastreams completes in ≤ 30 minutes.
- **Backfill mode**: documented separately from steady state. Out of scope for v1.

### Security & Access Control

- **SmartSULO API**: API key in environment variable, sourced from the Kubernetes Secret `clappform-sulo-credentials`. Rotated by Vincent (SULO) on a quarterly basis; rotation procedure documented in the runbook.
- **FROST write**: HTTP Basic or Bearer Token (FROST-Server supports both). Translation layer holds the only credential. Stored in Kubernetes Secret `clappform-frost-write-credentials`.
- **FROST read**: anonymous. Per OGC STA convention and per the tender's "publicly accessible" commitment. No GET endpoint requires authentication.
- **Internal state store** (Postgres holding poll cursors): network-isolated within the testbed cluster; no external exposure.
- **Translation-layer admin endpoints** (`/healthz`, `/healthz/freshness`, `/metrics`): exposed inside the cluster only. Enforcement: K8s `Service.type: ClusterIP` only — no Ingress rule maps to them. Recommended hardening: a `NetworkPolicy` restricting ingress to the namespace.
- **Trust boundary**: all SULO observations are treated as untrusted input — validated, range-checked, and timestamp-checked before being written to FROST.
- **Single-writer guarantee**: the translation layer runs as **exactly one pod**. K8s Deployment specifies `replicas: 1` and `strategy.type: Recreate` (not `RollingUpdate`) to prevent two pods running concurrently across a rollout. The `UNIQUE (datastream_id, phenomenon_time)` constraint on `observation_write_log` provides idempotency-at-the-DB-layer as a second line of defense.

### Privacy (GDPR)

- **Fill-level observations** are not personal data: a percentage reading from a container is not attributable to an individual.
- **Container location** in EPSG:4326 may correlate with household addresses in low-density placements. Mitigation: only publish containers whose location is on public street furniture; do not publish private-bin containers. SULO confirms which set falls into which category before ingestion.
- **RFID transponder IDs** on collection events are flagged as **potential personal data** (a transponder ID can identify a specific household). RFID is out of scope for v1 MVP; before any RFID data is published, Diego must run a legal review with Clappform's DPO and Vincent (SULO). Likely mitigation: publish a salted hash of the transponder ID, not the raw ID.
- **Data subject rights**: documented in the reproducibility guide. Erasure requests forwarded to SULO; deletion from FROST handled by entity ID lookup.

### Integrations

| System | Direction | Protocol | Data exchanged |
|--------|-----------|----------|----------------|
| SmartSULO API | Translation reads from | REST/HTTPS, polled | Containers, sensors, observations |
| FROST-Server (fallback) | Translation writes to; world reads from | OGC STA over HTTPS | OMS-modelled STA entities |
| FROST-Server (central, Topic #1) | Translation writes to (if/when available) | OGC STA over HTTPS | Same as fallback |
| Internal state store (Postgres) | Translation reads/writes | Postgres wire protocol over cluster network | Poll cursors, write-idempotency keys, datastream metadata, parity report |
| Alert webhook | Translation writes to | HTTPS POST or SMTP | Freshness alert payload |

### Tech stack constraints

- **Language**: Go ≥ 1.25. Confirmed in ADR-001 (revised 2026-05-22).
- **STA library**: hand-rolled thin wrapper over `net/http`. Confirmed in ADR-002.
- **FROST-Server**: latest stable release at testbed start. Postgres + PostGIS backing per FROST-Server default.
- **Container orchestration**: Kubernetes (Clappform internal). Manifests published as part of the reproducibility deliverable.
- **Internal state store**: Postgres (separate database from FROST-Server's backing store).
- **OS support for reproducibility guide**: Linux + Docker Desktop on macOS. Windows not officially supported (test environment for Bowen runs Windows but the guide targets Linux containers).

---

## Data Model

This section documents both the OGC STA / OMS entities written to FROST and the internal canonical / state-store entities owned by the translation layer.

### A. STA entities written to FROST

All entities conform to the OGC SensorThings API v1.1 specification. Field names below use STA's CamelCase / `@iot.*` conventions.

#### Thing (waste container; vehicles deferred — see ADR-003)

```json
{
  "@iot.id": "integer — auto-assigned by FROST",
  "name": "string — stable, format: '<Vendor> Container <vendor_native_id>' (e.g. 'SULO Container 7e3f2a')",
  "description": "string — '<container_type> waste container operated by <municipality> at <street_address>'",
  "properties": {
    "vendor": "string — vendor_id (e.g. 'sulo')",
    "vendor_native_id": "string — vendor's native identifier; immutable",
    "municipality": "string — e.g. 's-Hertogenbosch'",
    "waste_type": "string — open enum, lowercased and stripped; guidance values: restwaste | gft | papier | pmd | glas | textiel",
    "clappform_source_system": "string — e.g. 'smartsulo'",
    "first_seen_at": "ISO 8601 UTC — when the translation layer first discovered this Thing"
  }
}
```

**Notes:**
- `name` is the **identity key** used for upsert and **must encode the vendor** to prevent cross-vendor collisions on `vendor_native_id`. Format is stable; never changes after creation. `<Vendor>` segment is the title-case of `vendor_id`.
- `properties.vendor_native_id` is the cross-reference back to the vendor platform. Never published in spatial visualizations directly.
- `properties.waste_type` is an **open enum**: stored as-is (lowercased, stripped) from the vendor's payload; the translation layer does not validate against the guidance list.

#### Location

```json
{
  "@iot.id": "integer — auto",
  "name": "string — 'Location of SULO Container <sulo_container_id> at <YYYY-MM-DD>'",
  "description": "string",
  "encodingType": "application/geo+json",
  "location": {
    "type": "Point",
    "coordinates": [<lon>, <lat>]
  }
}
```

**Notes:**
- Coordinates always EPSG:4326 in GeoJSON `[lon, lat]` order. Bounds-checked: `-180 <= lon <= 180`, `-90 <= lat <= 90`; observations failing this are dropped (reason: `out_of_bounds_coordinates`).
- **Adapter coordinate-order normalization**: vendor adapters are responsible for emitting `(lon, lat)`. Adapters whose source returns `(lat, lon)` MUST swap inside the adapter. The SULO adapter performs a sanity check on first sample: if more than 50% of seed coordinates fall outside the Netherlands bounding box (`lon ∈ [3.0, 7.5], lat ∈ [50.5, 53.7]`) but their reverse falls inside it, the adapter logs a warning and aborts startup — forcing a human to confirm the orientation.
- A new Location is created (not patched) when a container is relocated; previous Locations are preserved via `HistoricalLocations`.

#### Sensor

```json
{
  "@iot.id": "integer — auto",
  "name": "string — 'SULO fill-level sensor <model> <firmware>'",
  "description": "string",
  "encodingType": "application/json",
  "metadata": {
    "manufacturer": "string",
    "model": "string",
    "firmware_version": "string",
    "principle": "ultrasonic | radar"
  }
}
```

#### ObservedProperty (fill level)

```json
{
  "@iot.id": "integer — auto",
  "name": "Fill level",
  "description": "Container fill level as a percentage of total capacity",
  "definition": "http://qudt.org/vocab/quantitykind/DimensionlessRatio"
}
```

**Notes:**
- `definition` URI must be a stable, dereferenceable ontology URI. QUDT is preferred; if the Topic #1 contractor specifies a different vocabulary, we re-align.

#### Datastream

```json
{
  "@iot.id": "integer — auto",
  "name": "string — 'Fill level — SULO Container <sulo_container_id>'",
  "description": "string",
  "unitOfMeasurement": {
    "name": "Percent",
    "symbol": "%",
    "definition": "http://www.opengis.net/def/uom/UCUM/0/%"
  },
  "observationType": "http://www.opengis.net/def/observationType/OGC-OM/2.0/OM_Measurement",
  "observedArea": {
    "type": "Point",
    "coordinates": [<lon>, <lat>]
  },
  "phenomenonTime": "ISO 8601 interval — managed by FROST",
  "resultTime": "ISO 8601 interval — managed by FROST",
  "properties": {
    "poll_interval_seconds": 900,
    "expected_cadence_seconds": 3600
  }
}
```

#### Observation

```json
{
  "@iot.id": "integer — auto",
  "phenomenonTime": "ISO 8601 UTC — when the physical measurement was taken (from SULO)",
  "resultTime": "ISO 8601 UTC — when SmartSULO recorded it (from SULO, may equal phenomenonTime)",
  "result": "number — fill level in [0, 100]",
  "resultQuality": {
    "validated_by": "clappform-translation-layer",
    "validation_version": "string"
  },
  "parameters": {
    "raw_sulo_observation_id": "string — opaque SmartSULO ID for traceability"
  }
}
```

**Notes:**
- All timestamps in UTC. The translation layer converts from any local TZ in SmartSULO responses.
- `result` is bounds-checked `[0, 100]` before writing. Failures dropped and logged.
- `parameters.raw_sulo_observation_id` enables the parity check (M4) to cross-reference.

#### FeatureOfInterest

```json
{
  "@iot.id": "integer — auto",
  "name": "string — 'Container location: SULO Container <sulo_container_id>'",
  "description": "string",
  "encodingType": "application/geo+json",
  "feature": {
    "type": "Point",
    "coordinates": [<lon>, <lat>]
  }
}
```

**Notes:**
- For containers, FeatureOfInterest mirrors the Thing's Location. For vehicles (Phase 2), FoI per observation will be the inferred point of measurement (TBD in ADR-003).

### B. Internal canonical entities (in-memory, between adapter and writer)

Owned by the translation layer; not persisted; not written to FROST. Strict typed dataclasses in Python.

```python
@dataclass(frozen=True)
class CanonicalThing:
    vendor_id: str          # 'sulo'
    vendor_native_id: str   # SULO container ID
    name: str               # 'SULO Container <id>'
    description: str
    location: tuple[float, float]  # (lon, lat) EPSG:4326
    properties: dict[str, str]

@dataclass(frozen=True)
class CanonicalDatastream:
    thing_vendor_native_id: str
    observed_property: ObservedPropertyEnum  # FILL_LEVEL | TEMPERATURE | BATTERY
    unit: UnitEnum                            # PERCENT | CELSIUS | VOLT
    sensor_metadata: dict[str, str]

@dataclass(frozen=True)
class CanonicalObservation:
    thing_vendor_native_id: str
    observed_property: ObservedPropertyEnum
    phenomenon_time: datetime  # UTC
    result_time: datetime      # UTC
    result: float
    raw_observation_id: str    # SULO opaque ID
```

### C. Internal state store (Postgres, owned by translation layer)

Schema (DDL-equivalent):

```sql
-- One row per (vendor, vendor_native_id) Thing discovered.
CREATE TABLE translation_state.thing (
    id              SERIAL PRIMARY KEY,
    vendor_id       TEXT NOT NULL,
    vendor_native_id TEXT NOT NULL,
    sta_thing_id    BIGINT,            -- @iot.id assigned by FROST after first upsert
    name            TEXT NOT NULL,
    first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (vendor_id, vendor_native_id)
);

-- One row per (Thing, ObservedProperty) — the poll cursor lives here.
CREATE TABLE translation_state.datastream (
    id              SERIAL PRIMARY KEY,
    thing_id        INTEGER NOT NULL REFERENCES translation_state.thing(id),
    observed_property TEXT NOT NULL,    -- enum value
    sta_datastream_id BIGINT,
    poll_cursor     TIMESTAMPTZ,        -- last phenomenon_time successfully written
    last_polled_at  TIMESTAMPTZ,
    last_written_at TIMESTAMPTZ,        -- used by freshness alert (F5)
    UNIQUE (thing_id, observed_property)
);

-- One row per attempted observation write; for idempotency and parity (M4).
CREATE TABLE translation_state.observation_write_log (
    id              BIGSERIAL PRIMARY KEY,
    datastream_id   INTEGER NOT NULL REFERENCES translation_state.datastream(id),
    phenomenon_time TIMESTAMPTZ NOT NULL,
    result          DOUBLE PRECISION NOT NULL,
    raw_observation_id TEXT NOT NULL,
    sta_observation_id BIGINT,
    written_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (datastream_id, phenomenon_time)  -- idempotency key
);

-- Single-row table holding the freshness-alert state machine.
CREATE TABLE translation_state.watchdog_state (
    id              INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    current_status  TEXT NOT NULL CHECK (current_status IN ('ok', 'stale_pending', 'stale')),
    since_ts        TIMESTAMPTZ NOT NULL,
    last_fired_ts   TIMESTAMPTZ
);
```

**Notes:**
- The `UNIQUE (datastream_id, phenomenon_time)` constraint is the idempotency guarantee. Re-processing a poll cycle never duplicates Observations.
- `last_written_at` is the source of truth for the freshness alert (F5).
- Retention on `observation_write_log`: keep 90 days, then rotate. Older entries don't affect idempotency because re-polling from SULO won't return observations that old.
- **Cursor initialization**: when a brand-new Datastream is discovered (first poll cycle ever for that Datastream), `poll_cursor` is seeded to `now() - 1 hour`. This produces a small one-cycle backfill window without ingesting any historical data. Bulk historical backfill remains an out-of-scope follow-on.
- An additional single-row table `translation_state.watchdog_state` persists the freshness-alert state machine (`current_status: 'ok' | 'stale_pending' | 'stale'`, `since_ts`, `last_fired_ts`).

---

## API Contract

### Outbound (Translation Layer → SmartSULO)

*Adapter-mediated. Concrete shape TBD pending Vincent's docs. Adapter contract is fixed (see F2); SULO-specific REST details are encoded inside the SULO adapter only.*

| Method | Path | Auth | Request | Response (assumed) |
|--------|------|------|---------|--------------------|
| GET | `<smartsulo_base>/containers` | API key header | none | List of containers with id, location, type |
| GET | `<smartsulo_base>/containers/<id>/observations?since=<iso8601>` | API key header | none | Paginated list of observations |

*The adapter handles pagination, rate limits, and timestamp-format quirks. Layer code never touches these.*

### Outbound (Translation Layer → FROST-Server, OGC STA v1.1)

Standard STA endpoints. Auth required for writes only.

| Method | Path | Auth | Purpose |
|--------|------|------|---------|
| GET | `/v1.1/<Entity>?$filter=name eq '<name>'` | No | Upsert lookup (cache-miss path) |
| POST | `/v1.1/Things` | Yes | Create Thing (on lookup miss) |
| POST | `/v1.1/Things(<id>)/Locations` | Yes | Add Location to existing Thing |
| POST | `/v1.1/Sensors` | Yes | Create Sensor (on lookup miss) |
| POST | `/v1.1/ObservedProperties` | Yes | Create ObservedProperty (on lookup miss) |
| POST | `/v1.1/Datastreams` | Yes | Create Datastream (on lookup miss) |
| POST | `/v1.1/FeaturesOfInterest` | Yes | Create FeatureOfInterest (on lookup miss) |
| POST | `/v1.1/Datastreams(<id>)/Observations` | Yes | Create Observation |
| GET | `/v1.1/Datastreams(<id>)/Observations?$filter=phenomenonTime eq <iso>` | No | Observation idempotency probe |

**Upsert algorithm** (for Thing/Location/Sensor/ObservedProperty/Datastream/FoI — STA has no native upsert):
1. Mapper queries `translation_state.*` for a cached `sta_*_id`.
2. Cache hit → use cached `@iot.id`.
3. Cache miss → `GET /v1.1/<Entity>?$filter=name eq '<escaped_name>'`. The name is URL-encoded and OData-escaped: every single-quote in the name is doubled (`'` → `''`) per OData v4 string-literal rules. Names are additionally length-checked (≤ 255 chars) before filter construction; oversize names are rejected as a permanent error.
4. Zero results → POST to create; capture returned `@iot.id`; write to state-store cache.
5. One result → use that `@iot.id`; write to state-store cache.
6. Multiple results (data corruption) → log + increment `sta_duplicate_entity_total{type=X}`; pick the lowest `@iot.id`; do not block ingestion.

Observations are always POST with a pre-write `phenomenonTime eq` probe; they are never PATCHed.

### Inbound (Vendor → Translation Layer, push mode — ADR-011)

*Used only by adapters configured as `push`. SULO is `poll` and does not use this. Served on a separate, Ingress-exposed listener (`PUSH_HTTP_ADDR`), never on the cluster-internal admin port.*

| Method | Path | Auth | Purpose |
|--------|------|------|---------|
| POST | `/ingest/{vendorID}` | Yes (per-vendor HMAC or bearer) | Submit a batch of observations for one vendor |

**Authentication**: preferred form is an HMAC-SHA256 signature over the raw request body using the per-vendor shared secret (`<VENDOR>_PUSH_HMAC_SECRET`), sent in `X-Signature: sha256=<hex>`; a bearer token is the simpler fallback. The `PushAdapter.Authenticate` method owns verification. Unsigned/invalid → `401`; unknown `vendorID` or vendor not in push mode → `404`.

**Request body** (vendor-agnostic envelope; the `PushAdapter.DecodePush` for each vendor maps its native shape into this):

```json
{
  "things": [
    { "vendor_native_id": "C-001", "name": "…", "location": { "lon": 5.29, "lat": 51.69 },
      "datastreams": [ { "observed_property": "FILL_LEVEL", "unit": "PERCENT", "expected_cadence_seconds": 3600 } ] }
  ],
  "observations": [
    { "vendor_native_id": "C-001", "observed_property": "FILL_LEVEL",
      "phenomenon_time": "2026-06-25T08:00:00Z", "result_time": "2026-06-25T08:00:01Z",
      "result": 42.5, "raw_observation_id": "abc-123" }
  ]
}
```

**Responses**:
- `202 Accepted` — batch validated and written (or idempotently skipped). Body: `{ accepted: n, skipped_idempotent: n, rejected: [ { raw_observation_id, reason } ] }`.
- `400` — body undecodable or envelope invalid (no observations processed).
- `401` — signature/token invalid. `404` — unknown vendor or vendor not in push mode. `413` — body exceeds size limit. `429` — rate-limited.

**Idempotency & ordering**: the `(datastream_id, phenomenon_time)` write-log key and the pre-write `phenomenonTime eq` probe dedupe re-sends; out-of-order and backfill batches are accepted (push mode does not apply the `before_cursor` rejection — ADR-011). Per-observation rejections (range, future-clock, missing result) are reported in the `rejected` array without failing the batch.

### Inbound (Translation Layer admin)

| Method | Path | Auth | Purpose |
|--------|------|------|---------|
| GET | `/healthz` | No (cluster-internal only) | Liveness — returns 200 |
| GET | `/healthz/freshness` | No (cluster-internal only) | Freshness status JSON: `{ status: "ok"\|"stale", max_last_written_at: iso, threshold_hours: 6 }` |
| GET | `/metrics` | No (cluster-internal only) | Prometheus exposition format (counters, gauges) |

**Error responses (FROST writes):**
- `409 Conflict` on idempotency violation: treat as success, advance cursor.
- `4xx` on validation: log, skip, do not retry, do not advance cursor for that observation but allow others in batch.
- `5xx` or network: retry per F4.

---

## MVP Scope & Phasing

### Phase 1 — Setup & Align (May 2026)

**Period**: 2026-05-22 to 2026-05-29 (1 week — tight; effective work starts immediately).

- Phase 1 design review and sign-off (this document).
- Vincent shares SmartSULO API specs; SULO adapter contract is reified against real endpoints.
- Topic #1 contractor contact established; OMS proposal sent.
- Testbed Kubernetes cluster provisioned; FROST-Server (fallback) deployed; Postgres state store deployed.
- CI/CD pipeline set up for the translation-layer container image.

**MVP cut for Phase 2 build**: containers + fill-level only.

### Phase 2 — Build & Connect (June 2026)

**Period**: 2026-06-01 to 2026-06-30.

- Translation layer code: scheduler, SULO adapter, OMS mapper, FROST writer, state store, freshness alert.
- Bring up first 10 containers end-to-end; validate against M3, M4.
- Scale to ~1,000 containers; validate M1, M2.
- **2026-06-15**: vehicle + RFID modelling decision deadline (ADR-003). Decision drives whether vehicles + RFID ship in July or slip to post-testbed.
- Parity report (M4) runs daily through the month.

### Phase 3 — Document & Demo (July 2026)

**Period**: 2026-07-01 to 2026-07-31.

- Reproducibility guide drafted and reviewed.
- Fictional second-vendor adapter walkthrough completed.
- Public demonstration at Geonovum testbed session.
- Final report, lessons learned, recommendations.
- Code repository (Apache 2.0) and guide (CC-BY 4.0) published to Geonovum's GitHub.

### Operational tail — August 2026 through January 2027 (6+ months)

- STA endpoint remains publicly readable.
- Translation layer continues steady-state polling.
- Freshness alert routes to Bowen (primary) and Tom (secondary).
- Weekly check-in cadence; structured logs retained for 90 days.

### Phase 2+ enhancements (deferred)

- **Vehicle Things + GPS Datastream**: modelling decided 2026-06-15; build only if decision permits inside-window completion.
- **RFID collection events**: blocked on GDPR review by Diego/DPO; not earlier than Phase 3 publication.
- **Webhook / MQTT push ingestion**: out of scope v1; design extension point in the scheduler module.
- **Backfill of historical SULO data prior to 2026-05-22**: deferred; documented in the guide.
- **Migration to Topic #1 central FROST**: dual-write capability built in v1 (F6); actual cutover happens when central is live.

### Future considerations (not committed)

- Per-municipality data scoping if multi-tenant access control becomes a requirement.
- Push-based ingestion (MQTT or webhook) if SULO exposes it and latency targets tighten.
- Adapter contributions from other vendors via PR to the public repo.

---

## Risk Assessment

| # | Risk | Probability | Impact | Mitigation |
|---|------|-------------|--------|------------|
| R1 | SmartSULO API shape, pagination, or rate limits not as assumed; adapter rewrite mid-build | M | M | Adapter is the only place this leaks; layer keeps shipping containers behind a feature flag while adapter is rewritten. Phase 1 deadline for SULO docs is hard: 2026-05-29. |
| R2 | Topic #1 contractor not available or chooses incompatible OMS choices | M | H | Build to clean FROST-Server defaults; OMS field mapping is config-driven (per-environment), not compiled in. Dual-write (F6) supports cutover without rewrite. |
| R3 | RFID + GDPR review blocks Phase 2 ship | H | M | RFID out of MVP. Phase 3 RFID delivery is best-effort. |
| R4 | Silent failure during the 6-month operational tail | M | H | Freshness alert (F5) — single most important operational safeguard. |
| R5 | Container scale exceeds 1,000 (reality is closer to 3,000+ across Afvalstoffendienst region) | M | M | Postgres state store and FROST-Server scale linearly to ~10x with index tuning; alert + capacity-plan if observation rate exceeds 100/sec sustained. |
| R6 | Demo-day endpoint hammered by reviewers and goes down | L | M | FROST read is cacheable at HTTP layer; add reverse-proxy CDN/cache only if observed. |
| R7 | Bowen / Yashvir bandwidth — both also work on parallel delivery commitments | M | M | Tom Greiffioen coordinates; testbed sprints planned around DTaaS commitments. |
| R8 | Phase 1 schedule slip (7 days, no slack) | H | M | Weekly Phase-1 status check-ins by Tom; proactive Geonovum notification if MVP slips into mid-June. |
| R9 | Topic #1's central FROST goes live mid-build, forcing re-pointing | L | L | F6 dual-write handles this transparently. |

---

## Dependencies & Blockers

**Dependencies** (must exist before development can fully proceed):

- **SmartSULO API access** — Owner: Vincent Esajas (SULO). ETA: by 2026-05-29 (end of Phase 1). Without this, adapter is stubbed.
- **Testbed Kubernetes cluster** — Owner: Clappform infra. ETA: by 2026-05-26. Without this, no deploy target.
- **FROST-Server image + Postgres** — Owner: Bowen. ETA: by 2026-05-29. Provisioned into the testbed cluster.
- **Topic #1 contact details** — Owner: Tom Greiffioen (via Geonovum). ETA: by 2026-05-29.

**Blockers** (would stop development if unresolved):

- **SULO API auth scheme not confirmed** — Resolution: Vincent confirms by 2026-05-26; default assumption is bearer-token API key.
- **OMS field-mapping disagreement with Topic #1** — Resolution: keep our mapping config-driven; align on first meeting.
- **Vehicle + RFID modelling decision** — Resolution: ADR-003 decided by 2026-06-15. If undecided, Phase 2 ships fill-level only.

---

## Appendix

### Glossary

- **CanonicalObservation**: Internal, vendor-agnostic dataclass used between the adapter and the OMS mapper.
- **DTaaS**: Digital Twin as a Service — Geonovum's framing for shared digital-twin infrastructure across Dutch municipalities.
- **FROST-Server**: Reference open-source server implementation of the OGC SensorThings API.
- **OMS**: Observations & Measurements Standard — the OGC data model for sensor observations; STA is one serialization of OMS.
- **OGC STA**: Open Geospatial Consortium SensorThings API v1.1 — the standard this project exposes.
- **Poll cursor**: A persisted `(datastream_id, last_phenomenon_time)` tuple that lets the layer resume after restarts without dropping or duplicating observations.
- **SmartSULO**: SULO's proprietary IoT platform that exposes sensor data via REST.
- **Thing**: STA entity representing a sensing platform (e.g., a waste container or vehicle).
- **Topic #1**: The wastewater use case in this same testbed, run by a different contractor; hosts the (eventual) central FROST server.

### References

- OGC SensorThings API v1.1 specification.
- OGC Observations & Measurements (OMS) Standard.
- Clappform PyPI wrapper: https://pypi.org/project/clappform/
- Clappform OGC Digital Twin repo (private): https://github.com/ClappFormOrg/OGC-Digital-twin
- Tender document: `Tender - Clappform - Geonovum 2026Testbed.pdf` (project root).
- Client contact: [redacted], Gemeente 's-Hertogenbosch ([redacted]).
- SULO contact: Vincent Esajas ([redacted]).

---

# Part 2: Architectural Design

---

## System Context

The translation layer sits between two systems Clappform does not own: SULO's SmartSULO platform (sensor source) and a FROST-Server instance (STA target). All of Clappform's logic lives in a single deployable artifact (the translation-layer container) plus a small Postgres state store inside the same testbed Kubernetes namespace. There is no UI; consumers interact with the FROST STA endpoint, not with the translation layer.

```
[ SULO SmartSULO API ]                           [ World — consumers ]
        ▲ HTTPS poll                                       │ HTTPS GET (anonymous)
        │ (every 15 min, per Datastream)                   ▼
        │
┌───────┴────────────────────────────────────────────────────────────┐
│  Testbed Kubernetes namespace (Clappform K8s, separate cluster)    │
│                                                                    │
│  ┌──────────────────────────┐  HTTPS POST   ┌─────────────────┐   │
│  │ translation-layer (pod)  │ ───────────▶  │ FROST-Server    │   │
│  │  - scheduler             │   (auth)      │  + PostGIS DB   │   │
│  │  - SULO adapter          │               │  (deployment)   │   │
│  │  - validator             │               └─────────────────┘   │
│  │  - OMS mapper            │                        ▲             │
│  │  - FROST writer          │                        │ HTTPS GET   │
│  │  - watchdog (freshness)  │                        │ (anon)      │
│  │  - /healthz, /metrics    │                        │             │
│  └─────────┬────────────────┘                        │             │
│            │                                          │             │
│            ▼                                          │             │
│  ┌──────────────────────────┐                         │             │
│  │ Postgres (state store)   │                         │             │
│  │  translation_state.*     │                         │             │
│  └──────────────────────────┘                         │             │
│                                                       │             │
└───────────────────────────────────────────────────────┼─────────────┘
                                                        │
                                                        ▼
                                            [ Ingress — public HTTPS ]
```

Two FROST-Server targets are supported via dual-write (F6): the local fallback (always) and, when available, Topic #1's central FROST.

---

## Component Responsibilities

| Component | Responsibility | Layer | Owner |
|-----------|---------------|-------|-------|
| `translation-layer.scheduler` | Drives the poll loop on a cron tick; orchestrates adapter → validator → mapper → writer per Datastream | Service | Bowen Harkema |
| `translation-layer.adapters.sulo` | Talks to SmartSULO REST; converts SULO payloads to `Canonical*` dataclasses; handles pagination + rate-limit backoff | Service | Yashvir Jhingur |
| `translation-layer.validator` | Range-checks, timestamp-checks, and rejects malformed CanonicalObservations; emits `data_quality_*` log events | Service | Yashvir Jhingur |
| `translation-layer.oms_mapper` | Maps `Canonical*` → STA entities (Thing/Location/Sensor/ObservedProperty/Datastream/Observation/FoI) | Service | Bowen Harkema |
| `translation-layer.frost_writer` | Upserts entities; idempotent on Observation by (datastream_id, phenomenon_time); supports dual-write | Service | Bowen Harkema |
| `translation-layer.state_store` | Postgres-backed access to `translation_state.*` tables; isolates SQL from business logic | Data | Bowen Harkema |
| `translation-layer.watchdog` | Runs separate cron tick every 30 min; checks freshness; posts alert payload on transition | Service | Bowen Harkema |
| `translation-layer.api` | `/healthz`, `/healthz/freshness`, `/metrics` HTTP handlers; cluster-internal only | Service | Bowen Harkema |
| `frost-server` (deployment) | Standard FROST-Server image; reachable inside cluster for writes, via Ingress for public reads | Infra | Bowen Harkema (config); Clappform infra (cluster) |
| `postgres-state` (deployment) | Postgres for `translation_state.*`; cluster-internal only | Infra | Bowen Harkema |
| `postgres-frost` (deployment) | Postgres + PostGIS for FROST-Server's own backing store; cluster-internal only | Infra | Clappform infra |
| `reproducibility-guide` | Markdown documentation in the public Geonovum repo | Docs | Bowen + Wybren + Tom |
| GIS validation | Confirms FeatureOfInterest / Location encoding correctness, EPSG:4326 conformance | Service (consultative) | Wybren Terpstra |

---

## Data Flow

The end-to-end flow for a single observation:

1. **Trigger**: Scheduler cron tick fires for Datastream `D`.
2. **Cursor read**: `state_store.get_poll_cursor(D)` → `last_phenomenon_time`.
3. **Adapter call**: `adapters.sulo.fetch_observations(D.vendor_native_id, since=last_phenomenon_time, limit=1000)`.
4. **Validation**: Each `CanonicalObservation` is range-checked (`0 ≤ result ≤ 100`), timestamp-checked (UTC, not in future, > cursor), and either accepted or dropped (logged with reason).
5. **Mapping**: Accepted observations are mapped to STA Observation payloads. The mapper resolves the FROST `@iot.id` for the Datastream from `state_store.get_sta_datastream_id`; if missing (first observation for that Datastream), the mapper upserts the full Thing/Location/Sensor/ObservedProperty/Datastream/FoI chain via the FROST writer.
6. **Write**: `frost_writer.write_observation` POSTs to `/v1.1/Datastreams(<id>)/Observations` for each configured FROST target. Pre-write idempotency probe: `GET /v1.1/Datastreams(<id>)/Observations?$filter=phenomenonTime eq <iso>` — skip if exists. On success, `state_store.record_observation_write(D, phenomenon_time, raw_observation_id, sta_observation_id)`.
7. **Cursor advance**: After all observations for the cycle are written, `state_store.advance_poll_cursor(D, max_written_phenomenon_time)`.
8. **Metrics**: Counters incremented (`observations_polled_total`, `observations_accepted_total`, `observations_written_total`, `observations_dropped_total{reason=*}`). Gauge updated (`observation_freshness_seconds`).

**Side effects:**
- On idempotency probe hit (409 / existing row): increment `observations_skipped_idempotent_total`, do not error.
- On FROST 4xx: log, increment `observations_dropped_total{reason="frost_4xx"}`, advance cursor past this observation.
- On FROST 5xx / network error: retry per F4. On exhaustion: cursor stays put; next cron tick re-polls; freshness alert eventually fires.

---

## Integration Contracts

### Contract 1: VendorAdapter

- **Provider**: Any vendor-specific module under `translation_layer.adapters.*`.
- **Consumer**: `translation_layer.scheduler`.
- **Contract**: See F2 above. Three required methods: `list_things`, `list_datastreams_for_thing`, `fetch_observations`. All return `Iterable[Canonical*]`. All can raise `VendorTransientError` or `VendorPermanentError`.
- **Error behavior**:
  - `VendorTransientError` → scheduler retries per F4.
  - `VendorPermanentError` → scheduler skips Datastream, logs, increments `vendor_permanent_errors_total{vendor=X}`. Cursor unchanged.
  - Adapter timeouts: 30 s per HTTP call inside the adapter; surface as `VendorTransientError`.

### Contract 2: FROST writer ↔ FROST-Server

- **Provider**: FROST-Server.
- **Consumer**: `translation_layer.frost_writer`.
- **Contract**: OGC STA v1.1 over HTTPS. Requests authenticated with HTTP Basic (`FROST_BASIC_AUTH_USER` / `FROST_BASIC_AUTH_PASSWORD`) or Bearer token (`FROST_WRITE_TOKEN`); Basic wins when a user is set. The credential is attached to every request — including the upsert GET probes — because a Basic-Auth-protected FROST-Server (e.g. WBD) rejects anonymous reads. Against a server with public reads, leave the credential unset. One credential applies to all targets in v1.
- **Error behavior**: 4xx logged and skipped (do not retry). 5xx and network errors retried per F4. Hard timeout per HTTP call: 15 s.

### Contract 3: state_store

- **Provider**: `translation_layer.state_store` (Postgres-backed).
- **Consumer**: scheduler, oms_mapper, frost_writer, watchdog.
- **Contract**: A small set of typed Python methods over the `translation_state.*` schema. All methods are transactional and idempotent on retry.
  - `get_poll_cursor(datastream_internal_id) -> datetime`
  - `advance_poll_cursor(datastream_internal_id, new_cursor: datetime) -> None`
  - `record_observation_write(...) -> None` (uses `UNIQUE (datastream_id, phenomenon_time)`)
  - `get_max_last_written_at() -> datetime` (used by watchdog)
  - `register_thing(...)`, `register_datastream(...)` — upsert helpers
- **Error behavior**: Connection failures propagate; scheduler treats as transient and retries the cycle. Unique-constraint violations on write log: caught and treated as success (idempotent).

### Contract 4: Freshness alert webhook

- **Provider**: External (email gateway or chat webhook URL, configured via `FRESHNESS_ALERT_WEBHOOK_URL`).
- **Consumer**: `translation_layer.watchdog`.
- **Contract**: HTTPS POST with JSON `{ status: "stale"|"recovered", max_last_written_at: iso, threshold_hours: 6, namespace: "geonovum-testbed", environment: "production"|"staging" }`.
- **Error behavior**: Watchdog logs failure and retries on next tick (every 30 min). No backoff escalation — keeping the watchdog simple is the point.

---

## Architecture Decision Records

*Initial ADRs from Phase 4 drafting. Additional ADRs may be added during Phase 7 audit.*

### ADR-001: Language and runtime

**Context**: The translation layer needs to be implementable, maintainable, and reproducible. Clappform already publishes a Python SDK on PyPI (`clappform`) and a private `OGC-Digital-twin` repo cited in the tender. The team (Bowen, Yashvir) has Python depth.

**Options considered**:
- Python 3.11+ — Pro: team fluency, existing wrapper, rich OGC ecosystem. Con: heavier base image than Go.
- Go — Pro: smallest image, strong typing, easy concurrent polling, single-binary deploy. Con: team less familiar; OGC STA libraries thinner.
- Node/TypeScript — Pro: STA reference clients exist. Con: ergonomic for I/O but loses some typed-dataclass discipline; team less aligned.

**Decision (revised 2026-05-22)**: **Go (≥ 1.25)**. Original decision was Python; revised at Bowen's direction.

**Rationale for the revision**:
- Single-binary deploy fits the testbed's "deploy a small thing reliably for 6 months unattended" constraint better than a Python runtime + virtualenv.
- Strong typing at compile time reduces a class of runtime errors that would otherwise need a `pydantic`-style validator.
- Goroutine-based concurrency is a clean fit for per-Datastream polling with independent backoff state.
- Smaller container image (≈ 20 MB scratch/distroless vs ≈ 150 MB Python+deps), which matters for reproducibility-guide adopters running on constrained municipal infra.

**Consequences**:
- The reproducibility-guide reference implementation is now Go. The guide explicitly notes that ports to Python/TS are welcome and the adapter contract (F2) is language-agnostic.
- Postgres access uses `github.com/jackc/pgx/v5` + `pgxpool`.
- HTTP uses `net/http` (no external HTTP framework needed at this size).
- Structured logging via the standard library `log/slog` (JSON handler to stdout).
- Metrics via `github.com/prometheus/client_golang/prometheus` + `promhttp`.
- The existing `clappform` PyPI wrapper and `OGC-Digital-twin` repo are not on the dependency path; they remain available for downstream Clappform tooling that needs to consume the FROST endpoint from Python.

### ADR-002: STA client — handroll vs library

**Context**: We need to POST/GET STA entities. Options range from existing community Python STA clients to hand-rolling against `httpx`.

**Options considered**:
- Existing community STA client — Pro: fewer lines of code. Con: dependency on a third-party project's release cadence; coverage of v1.1 features varies.
- Hand-rolled thin wrapper over `httpx` — Pro: zero external dependency surface, exact control of error handling, easy to embed idempotency probe. Con: more code we own.

**Decision**: **Hand-rolled thin wrapper**. We only need ~8 endpoints (Things, Locations, Sensors, ObservedProperties, Datastreams, FeaturesOfInterest, Observations, and a filter probe). Hand-rolling fits in ≤ 300 LOC and keeps the dependency surface minimal — important for a project published as a reproducibility artifact.

**Consequences**:
- We own the wrapper. Document it in the reproducibility guide so others can reuse it.
- If a high-quality community client emerges later, swapping in is straightforward (the wrapper interface is small).

### ADR-003: Vehicle + RFID modelling — DEFERRED

**Context**: Vehicles are moving Things with GPS as both `Location` (via `HistoricalLocations`) and potentially as Observations on a position Datastream. RFID collection events couple a vehicle and a container at a moment in time; this doesn't fit the standard `Thing → Datastream → Observation` shape cleanly.

**Options considered**:
- (A) Vehicle = Thing with HistoricalLocations; GPS also published as Observations on a 'position' Datastream; RFID event = Observation on a 'collection event' Datastream with `parameters.container_thing_id` linking to the container Thing.
- (B) Vehicle = Thing, GPS only via HistoricalLocations (no GPS Datastream); RFID event = Observation with FeatureOfInterest set to the picked-up container's location.
- (C) Defer vehicles + RFID to a later milestone; ship containers + fill-level only as MVP.

**Decision**: **DEFERRED to 2026-06-15**. MVP ships containers + fill-level only. Final modelling decision will be made by Bowen, Wybren, and Diego no later than 2026-06-15 and recorded here as an amendment to this ADR.

**Consequences**:
- Code organisation must keep the OMS mapper free of vehicle-specific assumptions so option A or B can drop in without refactor.
- If decision slips past 2026-06-15, vehicles + RFID become post-testbed work and are reflected as such in the July demo.

### ADR-004: Self-hosted FROST is the primary target; central FROST is a parallel write

**Context**: The tender prefers the central FROST hosted by Topic #1, with a self-hosted fallback. As of 2026-05-22 there is no contact with Topic #1 and no agreed schema. The testbed deliverables cannot block on this.

**Options considered**:
- Wait for Topic #1 before deploying anywhere — Pro: avoids dual-write complexity. Con: high schedule risk; we have ~9 days before Build.
- Self-hosted only; central is post-testbed migration — Pro: simplest. Con: leaves the tender's "preferred target" unaddressed at demo time.
- Self-hosted is primary; central is added via dual-write (F6) when available — Pro: meets the tender intent; cutover is config-only. Con: dual-write adds idempotency complexity (handled by per-target write logs).

**Decision**: **Self-hosted FROST is the primary target.** The translation layer is built with dual-write capability from day one (F6); when central FROST is available, it is added as a second target with no code change.

**Consequences**:
- Self-hosted FROST must be production-grade: stable URL, TLS, retention configured for 1 year.
- Dual-write idempotency is per-target; the write log table tracks `sta_observation_id` per target (extend the schema if a second target is added — initial schema supports it via a `target_label` column that we'll add when needed).
- The migration story is real and demonstrable, which itself becomes part of the reproducibility guide.

### ADR-005: Polling, not push, for v1

**Context**: Ingestion can be poll-based (Clappform pulls SmartSULO REST), push-based (SULO sends webhooks), or streaming (MQTT). Each has different latency and infra cost.

**Options considered**:
- Poll REST on schedule — Pro: simplest, no inbound endpoint, easy retry semantics, cheap on Clappform side. Con: latency = poll interval; chattier on SULO side at high frequency.
- MQTT subscribe — Pro: low latency, low per-observation cost. Con: long-lived connection management, backpressure, durable buffer.
- Webhook (SULO POSTs to Clappform) — Pro: event-driven. Con: inbound endpoint, auth on the SULO side, replay-on-outage complexity.

**Decision**: **Poll REST on schedule** (default 15 minutes per Datastream).

**Decision (amended 2026-06-25)**: Poll remains the default and is mandatory for the SULO MVP. **Push is now also supported as an additive, per-adapter source mode — see ADR-011.** This does not change the SULO MVP, which ships poll-only; it generalises the ingestion path so a vendor that prefers to push can do so without a second pipeline.

**Consequences**:
- Latency target M2 set to `poll_interval + 60s`; aligns with hourly fill-level readings (no operational need for sub-minute latency).
- If a future use case demands lower latency, an MQTT adapter can be added behind the existing `VendorAdapter` contract; the rest of the layer is unchanged.
- Push-mode latency is bounded by the vendor's send cadence, not our poll interval (ADR-011).

### ADR-006: Observability — logs + single freshness alert

**Context**: The system runs unattended for ≥ 6 months. A full observability stack (Prometheus + Grafana + PagerDuty) is overkill for a 1,000-container testbed; logs-only is operationally unsafe.

**Options considered**:
- Reuse Clappform's existing platform observability — Pro: lowest effort. Con: couples reproducibility artifact to Clappform internals (other adopters can't replicate it).
- Lightweight dedicated stack (logs + Prometheus + dashboard + alert) — Pro: self-contained and reproducible. Con: extra work, more moving parts.
- Logs only + single freshness alert + `/healthz/freshness` endpoint — Pro: minimal, reproducible, eliminates silent-failure risk. Con: weaker than full Prometheus but adequate for the scale.

**Decision**: **Structured logs (JSON to stdout) + a single freshness alert via webhook + `/healthz/freshness` endpoint**, with Prometheus `/metrics` exposed but not required to be scraped. Documented in the reproducibility guide as "minimum viable observability."

**Consequences**:
- Alert routing is configurable per environment (testbed vs. operational tail).
- If an issue is found in the tail that the freshness alert doesn't catch (e.g. quality degradation), we add a second alert in a minor revision; the schema supports it.

### ADR-007: Privacy posture — RFID treated as personal data until proven otherwise

**Context**: RFID transponder IDs may be PII under GDPR. Container locations might be sensitive in low-density residential placements.

**Decision**: **RFID is out of MVP. Before any RFID data is published, a legal review (Diego + Clappform DPO + Vincent) confirms the lawful basis and selects the publication form (raw, salted hash, or aliased). Container locations are limited to public street furniture in v1.**

**Consequences**:
- MVP scope is firmly fill-level only; no incidental publication of household-correlated data.
- The privacy review becomes a documented pre-condition for Phase 2 RFID work — written into the reproducibility guide as "things to do before publishing your second stream."

### ADR-008: Per-Datastream freshness threshold (with count-based trigger)

**Context**: The system must run unattended for ≥ 6 months. A global "no observations in 6h" alert misfires on slow-cadence Datastreams (e.g., low-fill containers may legitimately go 4+ hours between observations).

**Options considered**:
- Single global threshold (6h) — Pro: simple. Con: false positives on slow Datastreams; false negatives if SULO partially fails.
- Per-Datastream threshold + per-Datastream alert — Pro: precise. Con: alert flood on systemic outage (1,000 alerts at once).
- Per-Datastream threshold + count-based alert trigger — Pro: precise per Datastream, single alert on systemic outage. Con: more code.

**Decision**: **Per-Datastream threshold = `max(3 × expected_cadence_seconds, 1h)`. Alert fires when stale Datastream count exceeds `max(1, 1% of total)`** (after the 30-min hysteresis window).

**Consequences**:
- Each Datastream declares its `expected_cadence_seconds` in `properties` (already in schema).
- A new vendor adapter MUST populate `expected_cadence_seconds` accurately, or the freshness alert is mis-calibrated for its Datastreams. Documented in the reproducibility guide.
- The watchdog queries grow with the number of Datastreams; at 1,000 Datastreams this is trivial.

### ADR-009: Single-replica deployment with `Recreate` strategy

**Context**: The translation layer maintains advisory but not transactionally-coordinated poll cursors. The `UNIQUE (datastream_id, phenomenon_time)` constraint provides idempotency, but concurrent pollers would waste vendor-API quota and complicate reasoning.

**Options considered**:
- Multi-replica with leader election (e.g., K8s leader lock) — Pro: HA on pod failure. Con: complexity; testbed scale doesn't justify it.
- Single replica, `RollingUpdate` strategy — Pro: brief overlap during deploys. Con: overlap = two pollers competing.
- Single replica, `Recreate` strategy — Pro: simplest model; never two pods at once. Con: brief outage on every deploy (~60s).

**Decision**: **Single replica + `Recreate` strategy**, paired with a deployment-window policy (R-RUN-4) and DB-level idempotency as defense-in-depth.

**Consequences**:
- Image updates have ~60s of unavailability — acceptable for testbed scale and per the deployment-window policy.
- If HA becomes a requirement in a follow-on project, leader election is added without changing the data model.
- Cursor advancement is naturally serialized per Datastream; no distributed-coordination problem to solve.

### ADR-010: SULO API key rotation — two-active-keys pattern

**Context**: The 6-month operational tail requires at least one credential rotation. Single-key rotation requires a brief outage; two-active-keys is zero-downtime if SULO supports it.

**Options considered**:
- Single-key rotation with scheduled outage — Pro: works regardless of vendor capability. Con: each rotation costs ~2 min unavailability.
- Two-active-keys overlap — Pro: zero downtime; safer for credential transitions. Con: depends on vendor support (TBD-6).
- Long-lived single key, never rotate — Pro: zero operational work. Con: security anti-pattern; violates standard credential-management practice.

**Decision**: **Two-active-keys is the preferred procedure (R-RUN-1); single-key rotation (R-RUN-1b) is the documented fallback if SULO does not support two-active-keys.**

**Consequences**:
- Two-active-keys support is a Phase-1 capability question for Vincent (TBD-6).
- The translation layer's config supports `SULO_API_KEY` + `SULO_API_KEY_PENDING` with documented precedence semantics.
- If SULO cannot support two active keys, accept the 2-minute outage during scheduled rotation windows.

### ADR-011: Push ingestion as an additive, per-adapter source mode

**Context**: ADR-005 chose polling for v1 and noted a non-poll source could be added behind the `VendorAdapter` contract. We now want first-class support for vendors that prefer to **push** observations to us (webhook-style) rather than be polled, while keeping poll as the default. The two modes must coexist: each adapter is configured independently as poll *or* push, and a single deployment may run a mix.

The enabling insight is that everything below the adapter boundary — `validator`, `oms_mapper`, `frost_writer`, `state_store`, `watchdog` — is already transport-agnostic. It consumes `Canonical*` values and does not care whether they were pulled or pushed. The per-observation write path (validate → upsert entity chain → write per FROST target → record write log → meter) is not yet implemented (it is the connector-phase work referenced in `scheduler.runDatastreamCycle`); building it as a standalone, mode-agnostic **ingestion core** is what makes dual-mode cheap rather than a second pipeline.

**Options considered**:
- (A) Keep poll-only; reject push. Pro: no new public surface, no new security posture. Con: forces every vendor onto our poll cadence; some vendors have no pull API at all.
- (B) Replace poll with push. Pro: lower latency, less vendor-API chatter. Con: abandons the simplest, most reproducible mode; shifts retry/replay responsibility entirely onto each vendor; breaks the SULO MVP.
- (C) Support both as a per-adapter mode, funnelled through one shared ingestion core. Pro: each vendor uses whichever fits; one validation/mapping/idempotency path for both; reuses ~80% of existing code. Con: introduces a public inbound endpoint (new security surface); two adapter contracts to document.

**Decision**: **Option C.** Extract a transport-agnostic ingestion core (`internal/ingest`) that both modes call. Split the adapter contract into a **`PollAdapter`** (today's `Adapter`: `ListThings`, `ListDatastreamsForThing`, `FetchObservations`) and a **`PushAdapter`** (the inverse: `Authenticate(r, body)` + `DecodePush(body) → []CanonicalBatch`). The `Registry` records each adapter's mode. Poll adapters are driven by the scheduler as today; push adapters are dispatched by a new inbound HTTP listener at `POST /ingest/{vendorID}`.

**Consequences**:
- **Ingestion core first.** The connector-phase write path is built once in `internal/ingest`, mode-agnostic, and the scheduler is rewired to call it. This is net-positive work for poll regardless of push.
- **Idempotency is unchanged and central.** The `UNIQUE (datastream_id, phenomenon_time)` write-log key and the pre-write `phenomenonTime eq` probe already make duplicate/retried/out-of-order pushes safe. A vendor may safely re-send.
- **Cursor semantics differ by mode.** Poll keeps the monotonic poll cursor and the `before_cursor` rejection (validator). Push **does not** apply `before_cursor` — push may legitimately backfill or arrive out of order, so de-duplication is delegated to the write-log idempotency rather than the cursor. The validator gains a per-mode toggle for this single rule; all other validation (range, `in_future`, `missing_result`) is identical across modes.
- **New public security surface.** Until now the service exposes no public endpoint (admin is ClusterIP-only, ADR-006). Push requires a separate, internet-facing listener (`PUSH_HTTP_ADDR`, distinct from `HTTP_ADDR`) behind Ingress, with per-vendor authentication (bearer token or, preferred, HMAC signature over the raw body), request-size limits, and rate limiting. The admin listener stays cluster-internal; the two are never the same port.
- **Deployment invariant preserved.** Single-replica `Recreate` (ADR-009) still holds: poll's single-writer cursor invariant is unchanged, and push writes are idempotent. Horizontal scaling of a push-only deployment is possible later (writes are stateless given the write-log key) but is explicitly out of scope here.
- **Watchdog is unchanged.** Freshness is computed from `last_written_at` (ADR-008), so a push stream that goes silent alerts exactly like a poll stream that stops. Push adapters MUST still supply `expected_cadence_seconds` per Datastream (in the payload or a registration push) or the staleness threshold is mis-calibrated — same requirement as poll adapters.
- **Catalog discovery differs.** Poll adapters enumerate their catalog via `ListThings`/`ListDatastreamsForThing`. Push streams are registered on first sight by the ingestion core's existing upsert-on-first-observation path; a push vendor's stream is unknown (and unmonitored) until its first push, which is the accepted analogue of an undiscovered poll stream.
- **Config gains per-adapter mode.** `ADAPTER_<VENDOR>_MODE = poll | push` (default `poll`), plus `PUSH_HTTP_ADDR` and a per-vendor push secret (`<VENDOR>_PUSH_HMAC_SECRET`). The SULO adapter remains `poll`; the MVP is unaffected.
- **Reproducibility guide** documents both contracts and states that an adapter implements exactly one mode.

---

## Change Map

This is a greenfield project. There are no existing files to modify in this workspace.

**New files (initial draft):**

```
docs/sulo-sta-translation-layer-design.md          # this document
README.md                                          # project overview
LICENSE                                            # Apache-2.0
CODE_OF_CONDUCT.md                                 # standard
CONTRIBUTING.md                                    # contributor + reproducibility entry point
pyproject.toml                                     # Python project metadata
src/translation_layer/__init__.py
src/translation_layer/config.py                    # env-var-driven config
src/translation_layer/scheduler.py                 # poll loop orchestrator
src/translation_layer/canonical.py                 # Canonical* dataclasses
src/translation_layer/validator.py
src/translation_layer/oms_mapper.py
src/translation_layer/frost_writer.py
src/translation_layer/state_store.py
src/translation_layer/watchdog.py
src/translation_layer/api.py                       # /healthz, /metrics
src/translation_layer/adapters/__init__.py
src/translation_layer/adapters/base.py             # VendorAdapter protocol
src/translation_layer/adapters/sulo.py             # SULO adapter
src/translation_layer/adapters/example_vendor.py   # fictional second vendor (for the guide)
tests/                                             # pytest suites
deploy/k8s/                                        # Kustomize / Helm manifests
deploy/docker-compose.yml                          # local reproducibility starter
db/migrations/                                     # state-store migrations
docs/reproducibility-guide.md                      # the public CC-BY 4.0 guide
docs/runbook.md                                    # operational runbook for the 6-month tail
```

**Files to review (none — greenfield).**

**Consumers of new interfaces:**
- The STA endpoint (public): consumers are Geonovum reviewers, municipal twin developers, vendor engineers.
- The `VendorAdapter` interface: future vendor adapters.
- The reproducibility guide: anyone building a similar translation layer.

---

---

## Operational Runbook

*Required procedures for the 6-month tail (2026-08-01 through 2027-01-31). Procedures published as `docs/runbook.md` in the public repo.*

### R-RUN-1: SULO API key rotation (two-active-keys with overlap)

**Trigger**: Quarterly schedule (set calendar reminder), or on any compromise notice from SULO.

**Precondition**: SULO platform supports two simultaneously-valid API keys (confirm with Vincent during Phase 1; if not supported, fall back to the single-key procedure documented as R-RUN-1b).

**Procedure**:
1. Request a new API key from Vincent. The old key remains valid.
2. Add the new key to the `clappform-sulo-credentials` K8s Secret as `SULO_API_KEY_PENDING`.
3. Update the translation-layer Deployment to read `SULO_API_KEY_PENDING` first, fall back to `SULO_API_KEY`. Roll deployment.
4. Confirm a successful poll cycle has used the new key (log line `vendor=sulo auth_key_id=<new>`).
5. Promote `SULO_API_KEY_PENDING` to `SULO_API_KEY` in the Secret; remove `SULO_API_KEY_PENDING`. Roll deployment.
6. Notify Vincent to invalidate the old key on SULO's side.

**Failure modes**:
- If step 4 never reports the new key id: revert the env-var precedence and investigate. Old key still works.

### R-RUN-1b: SULO API key rotation (single-key fallback)

**When**: only if SULO does not support two-active-keys.

**Procedure**:
1. Schedule a deployment window per R-RUN-4.
2. Scale `translation-layer` Deployment to 0 replicas.
3. Update `SULO_API_KEY` in the K8s Secret.
4. Ask Vincent to activate the new key on SULO's side.
5. Scale back to 1 replica.
6. Observations published by SULO during the ~2-minute window are re-fetched on resume (cursor unchanged).

### R-RUN-2: State-store retention and rotation

**Trigger**: Weekly cron job inside the translation layer (runs Sunday 02:00 UTC).

**Procedure (automated)**:
- Delete rows in `translation_state.observation_write_log` where `written_at < now() - INTERVAL '90 days'`.
- Run `VACUUM ANALYZE translation_state.observation_write_log`.
- Emit metric `state_store_rows_purged_total`.

**Manual escalation**: if `pg_database_size('translation_state') > 80% of PVC`, run the manual procedure documented in `docs/runbook.md#state-store-emergency-purge`.

### R-RUN-3: FROST-Server backup and restore

**Backup**:
- Daily `pg_dump` of FROST-Server's backing Postgres at 03:00 UTC.
- Dumps stored in the testbed cluster's object storage with 30-day retention.
- Weekly off-cluster copy to a Clappform-managed S3-equivalent for disaster recovery (retained 90 days).

**Restore**:
- Documented step-by-step in `docs/runbook.md#frost-restore`. Includes RTO ≤ 4 hours, RPO ≤ 24 hours.

**Verification**:
- Quarterly restore drill into a scratch namespace; confirms STA endpoint returns the expected entity counts. Documented as a calendar event for Bowen.

### R-RUN-4: Deployment window policy

To meet the 6-month operational commitment while running a single-replica deployment (acceptable for testbed scale, see ADR-006):

- **Standard windows**: Tuesday–Thursday, 10:00–15:00 CET (Amsterdam time).
- **Window duration**: each image update is expected to cause ≤ 60 seconds of unavailability (pod recreate + readiness probe).
- **Notice for outages > 5 minutes**: 24h advance notification to Geonovum and Topic #1 contractor via the bi-weekly sync channel.
- **Emergency outside-window deploys**: permitted only on `severity: critical` incidents; post-hoc notification.
- **No deploys**: Fridays, weekends, Dutch national holidays.

### R-RUN-5: Freshness alert response

**On receipt of `status: stale` alert**:
1. Open `GET /healthz/freshness` in the monitoring tooling — read the example stale Datastream names.
2. Inspect logs: `kubectl logs -n geonovum-testbed deployment/translation-layer --since=4h | grep -E '(error|warn|VendorPermanentError)'`.
3. Triage classification:
   - Single vendor's Datastreams all stale → vendor adapter or vendor outage (e.g., `vendor=sulo` auth errors → run R-RUN-1 emergency path).
   - All Datastreams stale → translation layer itself, FROST writer, or network egress.
   - Scattered subset → likely vendor-side per-device issues; coordinate with Vincent.
4. Document the incident with timestamps in the operational log.

---

## Open Phase-1 TBDs

The following decisions are explicit Phase 1 deliverables (owner: Tom Greiffioen, deadline: **2026-05-29**). Each becomes an ADR amendment when resolved.

| ID | Item | Owner | Deadline | Default if unresolved |
|----|------|-------|----------|-----------------------|
| TBD-1 | Cluster operational ownership Aug 2026 – Jan 2027 | Tom | 2026-05-29 | Clappform platform-ops team that runs the Afvalstoffendienst production deployment |
| TBD-2 | RFID privacy review date and outcome | Diego + Clappform DPO + Vincent | 2026-06-08 (before ADR-003 deadline) | Block RFID publication until completed |
| TBD-3 | M5 reproducibility validator at July demo | Tom | 2026-06-30 | External municipal engineer recruited by Tom; not previously involved in this project |
| TBD-4 | Reproducibility-guide path in Geonovum public repo | Tom | 2026-05-29 | Placeholder path; updated in a follow-on commit |
| TBD-5 | Alert webhook destination (SMTP / chat URL) | Bowen + Tom | 2026-05-29 | SMTP-to-email to [redacted] + [redacted] |
| TBD-6 | SmartSULO API contract details (auth scheme, pagination, timestamp format, rate limits, two-active-keys support) | Vincent | 2026-05-29 | SULO adapter stubbed against assumed REST shape; integration test deferred |
| TBD-7 | Topic #1 contractor contact + OMS pre-alignment | Tom (via Geonovum) | 2026-05-29 | Design proceeds in isolation; F6 dual-write supports later cutover |

---

## Adversarial Review Outcomes

*Phase 6 challenges and dispositions. Every Critical item is resolved; every Significant item has a response.*

| # | Severity | Challenge | Disposition |
|---|----------|-----------|-------------|
| C1 | Critical | Phase 1 timeline: 7 days, parallel commitments, no slack | **Accepted as Known Risk** (R8 in risk table). Tom commits to weekly Phase 1 status check-ins. If Phase 1 slips, MVP slips into mid-June with proactive Geonovum notification. |
| C2 | Critical | STA `$filter=name eq` apostrophe escaping | **Resolved**: filter algorithm now specifies OData v4 string-literal escaping (single-quote doubling) + name length cap of 255. |
| C3 | Critical | 6h freshness threshold assumes 1/h cadence | **Resolved**: per-Datastream threshold = `max(3 × expected_cadence_seconds, 1h)`. Count-based alert trigger. |
| C4 | Critical | SULO key rotation procedure undefined | **Resolved**: R-RUN-1 (two-active-keys) and R-RUN-1b (single-key fallback) documented in Operational Runbook. Two-active-keys support is a Phase 1 question for Vincent (TBD-6). |
| C5 | Critical | State-store disk growth + rotation undefined | **Resolved**: R-RUN-2 documents weekly 90-day-retention purge as an in-layer cron + manual escalation path. |
| C6 | Critical | FROST Postgres backup strategy undefined | **Resolved**: R-RUN-3 documents daily `pg_dump`, weekly off-cluster, RTO 4h / RPO 24h, quarterly restore drill. |
| S1 | Significant | SULO coordinate order ambiguity (lon,lat vs lat,lon) | **Resolved**: adapter coordinate-order rule + first-sample NL-bbox sanity check documented in Location notes. |
| S2 | Significant | Cluster ownership Aug 2026 – Jan 2027 | **Open TBD-1**, deadline 2026-05-29. |
| S3 | Significant | Single-replica + Recreate = outage per update | **Resolved**: R-RUN-4 deployment-window policy (Tue–Thu 10:00–15:00 CET, ≤ 60s expected impact, 24h notice for > 5 min). |
| S4 | Significant | RFID privacy review owner / date | **Open TBD-2**, deadline 2026-06-08 (gates ADR-003 modeling). |
| S5 | Significant | M2 freshness target assumes unmeasured SULO latency | **Resolved**: M2 explicitly calibrated against measured SULO latency during Phase 2 week 1; initial target is provisional. |
| S6 | Significant | M5 reproducibility validator | **Open TBD-3**, deadline 2026-06-30. |
| S7 | Significant | Topic #1 schema-incompatibility risk for dual-write | **Acknowledged**: F6 solves transport (dual-write); schema alignment is a config-driven layer in the OMS mapper (vocabulary URIs, name templates, unit definitions are env-var-overridable). The OMS *entity types* are STA-spec-locked. Worst case: a second mapping config is added for the central FROST target. |
| S8 | Significant | Reproducibility guide acceptance criterion | **Resolved**: M5 + TBD-3 jointly cover this. |

---

## Implementation Contract

*Generated by the Phase 5 developer assumption audit. Every item here was explicitly resolved — zero open questions.*

### Data shapes confirmed
- **STA `Thing.name`**: format `"<Vendor> Container <vendor_native_id>"`. Vendor segment is title-case of `vendor_id`. Identity key for upsert. Nullable: no. Stable across the entity's lifetime.
- **STA `Thing.properties.vendor_native_id`**: string, immutable, vendor-supplied. Nullable: no. Cross-reference to vendor platform.
- **STA `Thing.properties.waste_type`**: open string enum, lowercased and stripped on ingest. No validation against the guidance list (`restwaste | gft | papier | pmd | glas | textiel`).
- **STA `Observation.result`**: float in `[0, 100]` inclusive for fill-level Datastreams. Validation failure reason: `out_of_range`.
- **STA timestamps**: all UTC, ISO 8601. Source timestamps in any other TZ are converted to UTC inside the adapter.
- **Internal `ObservedPropertyEnum`**: `FILL_LEVEL` only in v1. `TEMPERATURE` and `BATTERY` reserved as enum stubs but **not** ingested in v1.
- **Internal `UnitEnum`**: `PERCENT` only in v1.

### External identifiers confirmed
- **STA `Thing.name`** template: `"<Vendor> Container <vendor_native_id>"`. Variables: `<Vendor>` derived from `vendor_id` (server-derived), `<vendor_native_id>` vendor-supplied. Collision rule: prevented by `UNIQUE (vendor_id, vendor_native_id)` in state store and by the vendor segment in `name`. Forbidden inputs: empty or whitespace-only `vendor_native_id` rejected before name construction.
- **STA `Location.name`** template: `"Location of <Vendor> Container <vendor_native_id> at <YYYY-MM-DD UTC>"`. Date in UTC. Collision rule: same container relocating twice on the same UTC day reuses the existing Location (matched by exact name); the second movement on the same day is accepted as an in-day update only if coordinates differ — otherwise treated as a no-op.
- **STA `Sensor.name`** template: `"<Vendor> fill-level sensor <model> <firmware>"`. Multiple Things may share a Sensor entity when model+firmware match — this is intentional.
- **STA `Datastream.name`** template: `"Fill level — <Vendor> Container <vendor_native_id>"`.
- **STA `FeatureOfInterest.name`** template: `"Container location: <Vendor> Container <vendor_native_id>"`.
- **STA `ObservedProperty.definition`** for fill level: `http://qudt.org/vocab/quantitykind/DimensionlessRatio`. Subject to re-alignment with Topic #1 if they specify a different ontology.

### API contracts confirmed
- **STA upsert algorithm** for non-Observation entities: state-store cache lookup → `GET ?$filter=name eq '<name>'` on miss → POST if zero results → cache. On multiple results, log + use lowest `@iot.id`. Never PATCH for upsert.
- **Observation idempotency probe**: `GET /v1.1/Datastreams(<id>)/Observations?$filter=phenomenonTime eq <iso>`. Termination: expected 0 or 1 results. On >1, log `sta_duplicate_observation_total` and skip the write.
- **FROST** auth: HTTP Basic (`FROST_BASIC_AUTH_USER` / `FROST_BASIC_AUTH_PASSWORD`) or Bearer (`FROST_WRITE_TOKEN`); Basic wins when a user is set. Attached to all requests (writes and upsert GET probes) so a Basic-Auth-protected server is reachable; leave unset for servers with public reads.
- **FROST 4xx** responses on write: log, skip, advance cursor past the observation, do not retry.
- **FROST 5xx / network**: retry per F4 (5s initial, 2x multiplier, 5 attempts, ~75s total).
- **HTTP timeouts**: 30s per adapter call to SULO; 15s per FROST call.

### State machines confirmed
- **`translation_state.watchdog_state.current_status`**: states = `ok | stale_pending | stale`. Transitions:
  - `ok → stale_pending` when watchdog tick observes `now() - max(last_written_at) > 6h`.
  - `stale_pending → stale` after a second consecutive tick with the same condition (≥ 30 min sustained). On entry, fire `stale` alert.
  - `stale → ok` on the first tick where `now() - max(last_written_at) ≤ 6h`. On entry, fire `recovered` alert.
  - `stale_pending → ok` if condition clears before the second tick (silent — no alert).
- **Datastream lifecycle**: no formal status field in v1. Datastreams are either discoverable from the vendor or not. Deregistration is out of scope for v1.

### Business rules confirmed
- **Cursor advance**: cursor advances to `max(phenomenonTime)` across successfully written observations and definitively-rejected observations (reasons: `out_of_range`, `in_future`, `before_cursor`, `missing_result`, `frost_4xx`). Cursor does not advance past observations failing for retryable reasons.
- **Cursor initialization** on first ever observation for a new Datastream: `now() - 1 hour`.
- **Clock-skew tolerance**: reject observations with `phenomenonTime > now() + 5 min` (reason: `in_future`).
- **Fill-level bounds**: `result ∈ [0, 100]` inclusive. Values outside dropped (reason: `out_of_range`).
- **Late-arriving observations**: `phenomenonTime ≤ cursor` is dropped (reason: `before_cursor`) — already covered by the previous cycle.
- **Poll cadence**: 15 minutes per Datastream, configurable via env var `POLL_INTERVAL_SECONDS` (single global value in v1; per-Datastream override deferred).

### Error behaviors confirmed
- **Vendor adapter errors**: see F2 error classification table — every HTTP/error condition maps to exactly one of `VendorTransientError` (retry) or `VendorPermanentError` (skip Datastream this cycle).
- **Partial batch failure**: see cursor advance rule above. The cycle continues processing remaining observations even if one fails; only the cursor position is affected.
- **Concurrent pods**: prevented at deployment layer (`replicas: 1`, `strategy.type: Recreate`). Defense in depth: `UNIQUE` constraint on `observation_write_log`.

### Access control confirmed
- **FROST read**: anonymous (no auth on GET).
- **FROST write**: bearer token, held only by translation layer.
- **SULO read**: API key in `clappform-sulo-credentials` Secret; rotated quarterly by Vincent.
- **Translation-layer admin endpoints** (`/healthz*`, `/metrics`): cluster-internal only, enforced by `Service.type: ClusterIP` with no Ingress rule.

### UI behaviors confirmed
- N/A — no UI surface in v1.

### Side effects confirmed
- **Freshness alert fire** → HTTPS POST to `FRESHNESS_ALERT_WEBHOOK_URL` with payload `{ status: "stale"|"recovered", max_last_written_at: iso, threshold_hours: 6, namespace: "geonovum-testbed" }`. Stateful (fires on transition only). Webhook destination is TBD until Phase 1 finalizes.
- **Successful Observation write** → row appended to `translation_state.observation_write_log` (idempotent on conflict).

### Config and environment confirmed
| Variable | Purpose | Example |
|----------|---------|---------|
| `SULO_API_BASE_URL` | SmartSULO REST root | `https://api.sulo.example.com/v1` |
| `SULO_API_KEY` | SmartSULO auth header value | (secret) |
| `FROST_TARGETS` | Comma-separated list of FROST base URLs to dual-write to | `https://frost.testbed.clappform.com/v1.1` (later: `,https://frost.geonovum.example/v1.1`) |
| `FROST_WRITE_TOKEN` | Bearer token for FROST auth; used when no Basic user is set; one value applied to all targets in v1; per-target creds deferred | (secret) |
| `FROST_BASIC_AUTH_USER` | HTTP Basic username for FROST auth; when set, Basic is used instead of Bearer | `write` |
| `FROST_BASIC_AUTH_PASSWORD` | HTTP Basic password for FROST auth | (secret) |
| `MQTT_ENABLED` | Publish Observation creates over MQTT instead of HTTP POST (entity upserts stay HTTP) | `false` |
| `MQTT_BROKER_URL` | MQTT broker URL; `wss://` (WebSocket/TLS), `ws://`, `tcp://`, or `ssl://` | `wss://sta.wbd-rd.nl/mqtt` |
| `MQTT_TOPIC_PREFIX` | STA version segment mirrored in publish topics | `v1.1` |
| `MQTT_QOS` | MQTT publish QoS (0/1/2) | `1` |
| `FROST_TLS_INSECURE_SKIP_VERIFY` | Disable TLS verification for FROST + MQTT (testbed only; for self-signed / hostname-mismatched certs) | `false` |
| `STATE_STORE_DSN` | Postgres connection string for `translation_state` | `postgresql://user:pw@postgres-state:5432/translation_state` |
| `POLL_INTERVAL_SECONDS` | Default poll cadence per Datastream | `900` |
| `FRESHNESS_THRESHOLD_HOURS` | Staleness threshold for the freshness alert | `6` |
| `FRESHNESS_ALERT_WEBHOOK_URL` | Destination for freshness alert payloads | TBD (Phase 1) |
| `LOG_LEVEL` | `DEBUG` / `INFO` / `WARNING` / `ERROR` | `INFO` |
| `CURSOR_INIT_LOOKBACK_SECONDS` | Initial cursor seed for newly discovered Datastreams | `3600` |
| `CLOCK_SKEW_TOLERANCE_SECONDS` | Allowed future-clock skew on `phenomenonTime` | `300` |

### Migration/rollout confirmed
- **Migration**: greenfield deploy. No existing data. Backwards-compatible: N/A.
- **Initial deployment**: testbed Kubernetes namespace, single replica, `Recreate` strategy, manifests published in `deploy/k8s/`.
- **Continuous rollout**: through Phase 2 (June 2026), features land via image updates. Each release tagged in git and the container image.
- **Rollback**: K8s Deployment rollback to previous container image. Cursor state in Postgres is preserved across rollbacks — no data lost or duplicated.
- **FROST cutover (fallback → central)**: append the central FROST URL to `FROST_TARGETS`; layer dual-writes; once parity is verified, remove the fallback URL. The fallback FROST stays running for the 6-month tail unless central explicitly takes over.

---

*Phase 6 (Adversarial Review) and Phase 7 (Architecture Decision Audit) outputs appear below as they are produced.*

---
