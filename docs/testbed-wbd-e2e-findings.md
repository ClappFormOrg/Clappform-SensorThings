# End-to-End Validation Against the Brabantse Delta FROST-Server — Findings

**Testbed**: Geonovum 2026, Topic #2 (SULO → OGC SensorThings API translation layer)
**Target server**: Waterschap Brabantse Delta (WBD), `https://sta.wbd-rd.nl/FROST-Server/v1.1`
**Date of test**: 2026-07-17
**Method**: synthetic *dummy adapter* driving the full translation pipeline

---

## 1. Objective

Validate the complete write path of the translation layer — **adapter → ingest core →
validation → FROST writer → FROST-Server** — against the live WBD SensorThings API
endpoint, *before* the real SULO connector exists. The dummy adapter emits deterministic,
clearly-labelled synthetic waste containers and fill-level observations so the pipeline can
be exercised without a real data source and without polluting the target with data that
could be mistaken for production.

## 2. Test environment and method

| Aspect | Value |
|---|---|
| Pipeline | Dummy poll adapter → ingest → validator → FROST HTTP writer |
| Orchestration | `docker compose` (state-store Postgres + translation layer), local override targeting WBD |
| Synthetic set | 5 containers, 5-minute cadence, 1-hour initial back-fill |
| Auth | HTTP Basic, user `write` |
| Transport | HTTPS (OGC STA v1.1 REST); MQTT path available but disabled for this run |
| Write mode | Entity upsert-by-name + observation idempotency probe, then create |

The dummy adapter is opt-in (`DUMMY_ADAPTER_ENABLED`) and every entity it produces is tagged
`properties.synthetic = "true"` and named `Dummy Container …` for unambiguous identification
and later cleanup.

## 3. Findings

### 3.1 Connectivity — client-side network filtering (not a server defect)

Initial connection attempts from the Clappform network failed at the TLS layer, with two
symptoms interleaved across requests:

- `tls: failed to verify certificate: x509: certificate is not valid for any names, but wanted to match sta.wbd-rd.nl`
- intermittent `EOF` during the TLS handshake

**Root cause: a TLS-intercepting proxy / content filter on the client-side (Clappform)
network.** The filter terminated the outbound TLS session and presented a *substituted*
certificate that did not carry `sta.wbd-rd.nl` as a subject-alternative name (hence the
`x509` name-mismatch), and dropped a fraction of connections mid-handshake (the `EOF`s).
This is a client-network artefact — **the WBD server and its certificate were not at
fault**, which was confirmed once the traffic path was addressed.

**Remediation.**
- *Correct fix (production):* allowlist `sta.wbd-rd.nl` in the network filter so the
  outbound TLS session reaches WBD unmodified and certificate verification succeeds
  genuinely end-to-end.
- *Testbed workaround (used here):* an opt-in `FROST_TLS_INSECURE_SKIP_VERIFY` flag lets
  the client proceed through the intercepting proxy. This is acceptable only for synthetic
  testbed traffic and must never be enabled in production, as it disables authentication of
  the server identity.

**Lesson for the paper:** transport-layer failures on a corporate network can masquerade as
a server misconfiguration (a "bad certificate"). Distinguishing *client-side interception*
from a *genuine server cert problem* required inspecting the exact `x509` error and
correlating it with the intermittent connection drops.

### 3.2 Authentication — Basic Auth, with a server-side edge case

The WBD server uses HTTP Basic authentication (`write` account). Two observations:

- The translation layer originally attached credentials only to write (POST) requests. WBD
  authenticates **all** requests including the upsert `GET` probes, so the client was
  updated to attach Basic credentials to every request.
- An **empty password** does not yield a clean `401`. FROST-Server's `BasicAuthFilter`
  splits the decoded `user:pass` string on `:` and assumes two fields; with an empty
  password it produces a one-element array and throws
  `ArrayIndexOutOfBoundsException` → **HTTP 500**. Operationally: a 500 from this endpoint
  most often means a *missing/empty credential*, not a server outage.

### 3.3 Server profile — a customized FROST-Server

The WBD deployment is **not** a stock FROST-Server. Thing entities expose non-standard
navigation properties and an authorization field beyond the OGC STA core:

- Non-standard collections: `Configurations`, `ControlledDevices`, `DeviceSecrets`,
  `Projects`.
- A `restricted` boolean on each entity.

This indicates a **project-scoped authorization / device-management extension**. A concern
going in was whether new entities would need explicit Project association or be blocked by
the `restricted` model. **This proved not to be the case:** standard STA `POST` of a new
Thing was accepted and created with `restricted: false`, with no Project association
required. The `write` account has entity-creation rights.

### 3.4 End-to-end entity registration — success

All five synthetic containers were written to WBD and read back with the full property set
produced by the mapper (source-system tag, vendor identifiers, synthetic marker, first-seen
timestamp). See §4.

### 3.5 Concurrency observation

The pipeline processes orders/things across two worker threads. The assigned server ids do
**not** follow native-id order (`@iot.id 96 → DUMMY-0001`, `97 → DUMMY-0003`,
`98 → DUMMY-0004`, `99 → DUMMY-0002`, `100 → DUMMY-0005`): creation was concurrent, so id
assignment reflects completion order, not input order. This is expected and harmless
(entities are resolved by name, not id), but worth noting for anyone reconciling ids.

### 3.6 Observation write over MQTT — validated, and a throughput insight

The layer also supports publishing Observation creates over **MQTT** (`wss://sta.wbd-rd.nl/mqtt`)
instead of HTTP POST; entity upserts and the idempotency probe stay on HTTP. This path was
exercised end-to-end:

- **Publishes persist.** With the MQTT path enabled, the observation count on datastream 663
  climbed steadily (16 → 455 → 1016 → …) — confirming publish → FROST-persist works over
  MQTT. (MQTT publish is fire-and-forget, so persistence was verified by REST read-back, not
  by the publish call.)
- **No duplicates.** The stored observations formed a *contiguous* 5-minute series from the
  first timestamp; the count equalled the number of distinct cadence ticks in the window, so
  the probe + cursor dedup held over MQTT despite the absence of a server-side uniqueness
  guarantee.
- **Restart-safe gap recovery.** Because the state-store cursor persisted across a multi-day
  gap between runs, the adapter idempotently **backfilled the whole gap** over MQTT with no
  duplicates — evidence that ingestion is restart-safe and gap-filling.

**Throughput insight (architectural).** Every observation — even on the MQTT path — is gated
by a *synchronous* HTTP idempotency probe (`GET …/Observations?$filter=phenomenonTime eq …`)
before publishing. During the multi-day backfill, that probe (not the MQTT publish) became
the bottleneck: under burst load through the latency-adding intercepting proxy it repeatedly
hit the HTTP client timeout, while MQTT publish kept pace. Two consequences:

- The FROST HTTP request timeout is now **configurable** (`FROST_HTTP_TIMEOUT_SECONDS`,
  default 15s) to absorb high-latency network paths.
- Future optimization: when the persisted cursor already guarantees an observation is new,
  the probe is redundant and could be skipped — removing the synchronous HTTP round-trip from
  the MQTT fast path entirely.

## 4. Results — Things registered on WBD

Five Things created (2026-07-17T09:17:40Z). Common properties for all rows:
`vendor = dummy`, `clappform_source_system = smartsulo`, `area = s-hertogenbosch-dummy`,
`synthetic = true`, `restricted = false`.

| `@iot.id` | name | vendor_native_id | first_seen_at |
|---:|---|---|---|
| 96 | Dummy Container DUMMY-0001 | DUMMY-0001 | 2026-07-17T09:17:40Z |
| 97 | Dummy Container DUMMY-0003 | DUMMY-0003 | 2026-07-17T09:17:40Z |
| 98 | Dummy Container DUMMY-0004 | DUMMY-0004 | 2026-07-17T09:17:40Z |
| 99 | Dummy Container DUMMY-0002 | DUMMY-0002 | 2026-07-17T09:17:40Z |
| 100 | Dummy Container DUMMY-0005 | DUMMY-0005 | 2026-07-17T09:17:40Z |

Query used (URL-encoded `$` as `%24`):

```
GET /FROST-Server/v1.1/Things?$count=true&$filter=startswith(name,'Dummy')
→ {"@iot.count": 5, ...}
```

Each Thing carries one fill-level Datastream, confirmed on WBD. Example — container 96
(`DUMMY-0001`) → Datastream `@iot.id 663`:

- `name`: "Fill level — Dummy Container DUMMY-0001"
- `unitOfMeasurement`: Percent (`%`, UCUM)
- `observationType`: `OM_Measurement`
- `observedArea`: Point `[5.2913, 51.6978]` (the container's location)
- `properties.expected_cadence_seconds`: 300

**Observations verified — 16 stored** on datastream 663, spanning
`2026-07-17T08:20:00Z … 09:35:00Z` at the 5-minute cadence (the 1-hour initial back-fill).
Each stored observation also confirms two pipeline behaviours:

- `resultQuality`: `{ validated_by: "clappform-translation-layer", validation_version: "v1" }`
  — the validation stage ran and stamped provenance on the record.
- `parameters.raw_observation_id`: e.g. `DUMMY-0001@1784276400` — the vendor idempotency key
  is preserved on the stored observation, the basis for duplicate-safe re-polling.

The full **Thing → Datastream → Observation** chain is therefore validated end-to-end on
WBD, including validation provenance and idempotency metadata.

## 5. Recommendations and follow-ups

1. **Network:** allowlist `sta.wbd-rd.nl` in the Clappform network filter, then disable
   `FROST_TLS_INSECURE_SKIP_VERIFY` so production traffic verifies the WBD certificate
   genuinely.
2. **Credential hygiene:** treat a `500` from the auth filter as a likely empty/missing
   credential; validate the secret is populated before deployment.
3. **Server profile:** document the WBD custom extension (`Projects`/`restricted`) in the
   integration contract; confirm long-term whether writes should be scoped to a specific
   Project rather than left unrestricted.
4. **Cleanup:** remove the five synthetic Things (ids 96–100) and their datastreams/
   observations from the shared WBD server after validation.
5. **Next:** exercise the optional MQTT publish path (`wss://sta.wbd-rd.nl/mqtt`) for
   observation writes, and repeat the run against the real SULO adapter when available.

## Appendix A — configuration used

| Env var | Value (testbed) |
|---|---|
| `FROST_TARGETS` | `https://sta.wbd-rd.nl/FROST-Server/v1.1` |
| `FROST_BASIC_AUTH_USER` | `write` |
| `FROST_BASIC_AUTH_PASSWORD` | *(supplied out-of-band; not stored in repo)* |
| `FROST_TLS_INSECURE_SKIP_VERIFY` | `true` *(testbed workaround for §3.1)* |
| `DUMMY_ADAPTER_ENABLED` | `true` |
| `DUMMY_THINGS_COUNT` | `5` |
| `DUMMY_CADENCE_SECONDS` | `300` |
| `MQTT_ENABLED` | `false` *(HTTP write path for this run)* |

## Appendix B — verification queries

```
# Count + list the synthetic containers
GET /FROST-Server/v1.1/Things?$count=true&$filter=startswith(name,'Dummy')

# Fill-level datastream for a container
GET /FROST-Server/v1.1/Things(96)/Datastreams?$count=true

# Observations for that datastream (newest first)
GET /FROST-Server/v1.1/Datastreams(N)/Observations?$count=true&$top=3&$orderby=phenomenonTime desc
```
