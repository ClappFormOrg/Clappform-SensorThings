# SULO source (REEN CMS REST API v3) — adapter findings

Date: 2026-07-30, verified against live credentials 2026-08-28.

> **Redacted for publication.** SULO's platform is the REEN CMS REST API, and
> REEN issues its API guide to customers and partners under confidentiality
> terms. This document therefore describes the *behaviours we had to design
> against* and omits anything that would amount to republishing REEN's
> reference material: no verbatim quotations, no field-level value tables, no
> request limits, and no account, device or asset identifiers belonging to
> SULO's customer. Where a behaviour is only meaningful with the vendor's own
> wording, it is paraphrased and attributed. Clappform holds the unredacted
> version; anything further needs REEN's consent.

The entry point is <https://api.reen.com/guide/>, which renders as a
JavaScript single-page application. The served HTML contains no documentation,
so the machine-readable OpenAPI description behind it has to be fetched
directly. That is worth knowing before estimating an integration against it:
reading the vendor's documentation was the largest single cost of this adapter
(see `topic2-lessons-findings-and-results.md` §3.1).

## Endpoint and auth

- **Base URL**: `https://api.reen.com/api/3`
- **Auth**: no API key exists. `POST /session` exchanges the account's username
  and password for a token, sent as the **`X-Token`** header on every later
  request. These are the same credentials used to sign in to the web
  interface, and not every account carries API rights.
- **Customer scope**: an account with rights over several customer accounts
  must name the target in an `X-Customer` header, otherwise the request applies
  to the account's own customer.
- **Archived instances** are excluded by default, and a request header opts
  into including them. We do not send it: an archived slot should stop
  producing a Datastream.
- The token is a JWT carrying an expiry roughly two weeks out. The client
  ignores it and re-authenticates on any `401` instead, which also covers a
  token revoked before it expires.

This invalidated the design doc's API-key assumptions (ADR-010 / R-RUN-1,
TBD-6). Both are annotated as superseded, and `SULO_API_KEY`,
`SULO_API_KEY_PENDING` and `Config.ActiveSULOKey()` were removed; they had no
consumer.

## Domain — this *is* fill level

Unlike the Collaborall source (mixed environmental telemetry, no fill level at
all), REEN is a waste-container CMS. `FILL_LEVEL` in percent is the native
phenomenon, so the canonical enum and the OMS mapper's fill-level defaults
apply directly. No passthrough naming is needed.

## Entity mapping

| REEN | canonical | note |
|------|-----------|------|
| container slot | `Thing` (`VendorNativeID` = slot id) | see below |
| site latitude / longitude | `Thing.Location` | REEN is (lat, lon); `canonical.Coord` is (lon, lat), swapped in the adapter |
| fill-level value | `Observation.Result` (percent) | |
| fill-level timestamp | `PhenomenonTime` **and** `ResultTime` | REEN timestamps an estimate once |
| device brand / model / serial | `Datastream.SensorMetadata` | reached from the slot through the container it holds |
| content type name | `Datastream.Description` | the waste fraction, e.g. "Restafval" |

**Why the slot and not the container.** The vendor documentation is explicit
that fill-level history and trends belong to the container *slot* rather than
to the physical container occupying it. A container can be swapped out without
breaking the measurement series, so the slot is the stable sensing platform.
Mapping the container instead would fragment every stream the first time a bin
is replaced.

**Thing.name is deliberately left empty** so `oms.ThingEntityName` synthesises
the Implementation-Contract name (`Sulo Container <slot id>`), which is the
same string `scheduler.runAdapterCycle` writes to the state store via
`oms.ThingName`. The REEN slot and site names go to `Description` and
`Properties` instead, so the state store and FROST never disagree on a Thing's
identity.

## Three REEN behaviours the adapter has to absorb

### 1. Predicted (future-dated) values, the one that bites

The fill-level collection mixes matured estimates with **forecasts**. The
vendor documentation states the rule that makes them separable: a predicted
value never carries a past timestamp, so a forecast is always dated in the
future at the moment you read it.

Left alone this is silent data loss rather than noise:

- The validator rejects a future reading as `in_future`.
- `IsDefinitivelyRejected(ReasonInFuture)` is **true**, so
  `ingest.ProcessStream` advances `MaxPhenomenonTime` to the rejected row
  (`internal/ingest/ingest.go`).
- The scheduler commits that as the poll cursor.
- A forecast dated three days out pushes the cursor three days ahead, and
  every real measurement arriving until then is dropped as `before_cursor`.

The adapter discards any row newer than the current time before the batch
reaches the ingest core. Covered by
`TestFetchObservationsDropsPredictedFutureRows`, and written up as a general
finding in `topic2-lessons-findings-and-results.md` §4.11.

### 2. Confidence is a data-quality flag, not a score

Each estimate carries a confidence value. It is not a probability: it encodes
*why* the analytics produced that number, distinguishing a normal reading, a
reading taken at the edge of the sensor's usable range, a value interpolated
because no measurement arrived, and a measurement the platform judged
erroneous.

Only the last of those is unusable. `SULO_MIN_CONFIDENCE` defaults to dropping
exactly that class and keeping the rest, including the interpolated values,
which are legitimate estimates and are what keeps a stream continuous between
sensor reports. Raising the floor to accept measured values only is supported
and will thin the series.

### 3. Ordering, paging and loose typing

- Responses are **newest-first**. The cursor arithmetic wants oldest-first, so
  results are sorted ascending. Offset paging over a live data set can repeat a
  row, so rows are also de-duplicated by timestamp.
- The API caps how many instances one request returns and offers a
  limit/offset pair plus `after`/`until` timestamp bounds in ISO-8601 UTC.
  `after` is exclusive, which matches our cursor semantics exactly. Our client
  pages within the vendor's cap.
- Two fields are declared as strings in the specification while carrying
  numbers, one of them the fill-level percentage itself. Both decode leniently
  (`flexInt64` / `flexFloat64`) so either shape works. This is the kind of
  detail worth checking before trusting a generated client.

## Poll shape and request cost

Registered as a **`PollAdapter`**, not push: REEN is a public HTTPS pull API,
so the standalone-reader design the Collaborall integration needed
(self-signed certificate on a separate network) buys nothing here. The
scheduler drives it and the same ingest core does the writing.

Per cycle the adapter issues one request each for container slots, sites,
linked devices and content types, plus paging, then **one fill-level request
per slot**. Discovery results are cached in the adapter, so the per-Thing
`ListDatastreamsForThing` calls cost nothing.

The per-slot fetch is the API's designed access pattern and keeps each
stream's cursor independent, at the cost of N requests per cycle for N slots.
If that becomes a rate-limit problem, the batching lever is the customer-wide
fill-level endpoint, which returns every slot in one response, in exchange for
reconciling that single response against per-stream cursors. Not done: it is
unnecessary at testbed scale and it trades away clean per-stream isolation.

Sites are required rather than enrichment, because without coordinates there
is no Location or FeatureOfInterest to write. Devices and content types are
best-effort: a failure there degrades Sensor metadata and descriptions, and is
logged and swallowed rather than stalling ingestion.

**Slots whose site has no coordinates are skipped**, with a warning naming them
each cycle. This feeds a geospatial API, where a container pinned to Point(0,0)
in the Gulf of Guinea is worse than a container that is absent and loudly
reported. The fix is to set the site's coordinates at the vendor.

## Available but not ingested

- Device temperature and battery percentage map to the reserved
  `canonical.Temperature` and `Battery` properties, but they are
  **current-state scalars, not time series**. There is no history endpoint for
  them, so polling would produce one observation per cycle with no real
  phenomenon time. They are carried as Sensor metadata instead, and
  `scheduler.runAdapterCycle` also filters to `FillLevel` in v1.
- Service events (collections and deliveries), alerts, plans, site-level
  aggregate fill level, and vehicle GPS. Out of scope per ADR-003.

## Confirmed against the live API

Run on 2026-08-28 with `cmd/sulo-probe`, a read-only diagnostic that exercises
the same calls the scheduler makes (session, discovery, observation fetch) and
writes nowhere. Asset and account identifiers are omitted here; slots are
labelled A, B and C.

| Item | Result |
|------|--------|
| Session auth | Accepted. One login served the whole run, so the token cache holds. |
| Scale | **51 container slots across 27 sites, 50 linked devices.** |
| Estate | Municipal waste containers in Nieuwpoort, Belgium, across four waste fractions. |
| Sensor | **Two** device models, one of them on a single slot. REEN exposes no firmware version, so each model collapses to one shared STA Sensor entity. A first pass over three slots suggested a single model, a reminder that fleet homogeneity has to be surveyed rather than sampled. |
| Cadence | **Hourly**, a 1h00m00s median gap over a 7-day window and roughly 179 readings per slot per week. |
| Publication lag | Separate from cadence, and the finding that mattered: the delay before a reading becomes readable runs from minutes to about five hours depending on the slot. See §4.13 of the report. |
| Coordinates | All 51 slots have site coordinates, so none were skipped. The lat/lon swap is confirmed correct against reality: the output lands on the Belgian coast where the estate is, and an unswapped pair would land off the Somali coast. |
| Response shapes | As specified, including the two numeric-valued string fields, so the lenient decoders earn their place. |
| Forecast filter | Did **not** fire on the per-slot endpoint; no future-dated rows appeared in the sampled windows. The guard stays, because the documentation states forecasts can appear in this class of response and the cost of being wrong is silent data loss. |
| Confidence filter | No erroneous-class rows in the sampled windows either. Same reasoning. |
| Completeness | Every row the source exposed inside the window we cover was written: a timestamp-level diff of source against our write log across all 49 reporting slots found zero missing. |
| Coverage of the fleet | 48 of 51 slots reach STA. The three that do not are **not** transport failures. |

**Why three slots never publish.** Re-checked against the source after four
hours of polling, the three split into two unrelated causes:

| Slot | Readings at source, 7 days | Sensor installed | Cause |
|------|---------------------------|------------------|-------|
| A | 0 | no | No device is linked, so the slot has never produced a reading. |
| B | 0 | no | The same. Both are asset records for containers that are not instrumented. |
| C | **173** | yes | Has data, but its newest reading is about 13 hours old. `CURSOR_INIT_LOOKBACK_SECONDS=3600` makes the first fetch window one hour wide, so it returns empty and the slot is never published. |

The first two are correct behaviour. The third generalises past this vendor:
an entity is created in STA by its first observation, so a container whose
sensor is already dead stays invisible rather than appearing and looking stale.
See `topic2-lessons-findings-and-results.md` §4.12.

Published downstream to the WBD FROST server: the full entity chain for each
slot that got through, meaning a Thing, Location, FeatureOfInterest and
Datastream per slot, plus one shared Sensor per device model and one shared
ObservedProperty. Coverage was partial and self-healing, 10 slots then 17 then
48 without intervention, because the client-side TLS-intercepting proxy drops a
fraction of connections and name-keyed upsert completed the missing slots on
later cycles without duplicating the finished ones.

## Open items

1. **`SULO_EXPECTED_CADENCE_SECONDS` is set from publication lag, not cadence.**
   REEN publishes hourly, but the delay before a reading becomes readable runs
   to about five hours on the slowest containers. The watchdog alarms at three
   times this value, so `3600` flagged 13 of 51 healthy streams as stale and
   `14400` is set instead. Both quantities are per-deployment, so re-measure
   with `cmd/sulo-probe` rather than copying the number.
2. **Rate limits are undocumented.** The client honours `429` and
   `Retry-After` (clamped to `adapters.MaxRetryAfter`), but the real ceiling is
   unknown.
3. **Vendor id stays `sulo`**, not `reen`. SULO is the vendor the testbed
   integrates with, and REEN is the platform it runs on. Every `SULO_*`
   environment variable, the design doc and the state-store rows already use
   `sulo`.
