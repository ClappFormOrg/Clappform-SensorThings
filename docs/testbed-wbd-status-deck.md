---
marp: true
title: WBD Connection Validation — Status & Next Steps
paginate: true
---
# SULO → OGC SensorThings Translation Layer

## Connection validation with the Brabantse Delta FROST-Server

Geonovum 2026 Testbed · Topic #2
Status update — 2026-07-22

---

## Where we are in one line

We proved the **entire write path works against the live WBD server** — using synthetic
data, before touching a real sensor.

**Adapter → Ingest → Validate → FROST writer → WBD FROST-Server** ✅

---

## What we set out to validate

- Can the translation layer **reach, authenticate, and write** to WBD's SensorThings API?
- Does the full pipeline behave end-to-end **before** the SULO connector exists?
- Do it with data that is **obviously synthetic** — no risk of polluting real records.

**Approach:** a built-in *dummy adapter* — 5 fake waste containers around 's-Hertogenbosch,
deterministic fill-level readings, every entity tagged `synthetic = true`.

---

## The pipeline we exercised

```
Dummy adapter  →  Ingest core  →  Validator  →  FROST writer  →  WBD FROST-Server
 (5 containers)   (dedup /        (clock skew,   (upsert-by-name   (sta.wbd-rd.nl,
                   cursor)         ranges)        + idempotency)     Basic Auth)
```

- Orchestrated with `docker compose` (state store + translation layer)
- HTTPS / OGC STA v1.1 REST write path (MQTT path built, tested separately)

---

## Result: end-to-end confirmed ✅

- **5 / 5** synthetic containers written to WBD and read back
- Full property set preserved (source system, vendor ids, synthetic marker, timestamps)
- Created as `restricted: false` — the `write` account can create entities
- **Full chain verified:** Thing → Datastream (id 663, unit `%`) → **16 Observations**
- Readings span 08:20–09:35Z at 5-min cadence; each stamped with validation provenance
  and an idempotency key (duplicate-safe re-polling)

> First real proof that our layer and WBD's server speak the same language.

---

## Things registered on WBD

Common to all: `vendor=dummy` · `source=smartsulo` · `area=s-hertogenbosch-dummy` · `synthetic=true`

| `@iot.id` | Name                       | Native ID  | First seen           |
| ----------: | -------------------------- | ---------- | -------------------- |
|          96 | Dummy Container DUMMY-0001 | DUMMY-0001 | 2026-07-17 09:17:40Z |
|          97 | Dummy Container DUMMY-0003 | DUMMY-0003 | 2026-07-17 09:17:40Z |
|          98 | Dummy Container DUMMY-0004 | DUMMY-0004 | 2026-07-17 09:17:40Z |
|          99 | Dummy Container DUMMY-0002 | DUMMY-0002 | 2026-07-17 09:17:40Z |
|         100 | Dummy Container DUMMY-0005 | DUMMY-0005 | 2026-07-17 09:17:40Z |

---

## Hurdles we cleared along the way

**1. Connection — our own network filter, not WBD**
TLS-intercepting proxy on our side substituted the certificate (cert-name mismatch) and
dropped connections. → Fix: allowlist `sta.wbd-rd.nl`; testbed workaround in place.

**2. Authentication — Basic Auth**
Credentials now sent on *every* request (WBD authenticates reads too), and faulty creds
surface oddly. → *detail next slide.*

**3. WBD runs a customized FROST-Server**
Extra entities (`Projects`, `DeviceSecrets`, `restricted` flag) → project-scoped auth
extension. Confirmed: standard writes are accepted without Project association.

---

## Finding in focus — Authentication

**Change we made: authenticate *every* request, not just writes.**
Our client originally sent credentials only on write (POST) calls. WBD authenticates
**reads too** — including the upsert `GET` probes the writer uses to look up existing
entities. Without credentials on reads, the whole upsert flow stalled. Now Basic Auth
is attached to every request.

**Finding: faulty credentials do *not* return a clean `401`.**
A missing / empty password is rejected with an **HTTP 500**, not a 401 — WBD's
`BasicAuthFilter` splits the decoded `user:pass` on `:`, and an empty password leaves a
one-element array → `ArrayIndexOutOfBoundsException`.

> Operational rule of thumb: a **500** from this endpoint almost always means a
> **missing or malformed credential**, not a server outage. Check the secret first.

---

## Status scorecard

| Capability                          | Status                         |
| ----------------------------------- | ------------------------------ |
| Reach WBD endpoint (TLS)            | ✅ (via network workaround)    |
| Basic Auth                          | ✅                             |
| Correct STA base path               | ✅                             |
| Entity upsert (Things, Datastreams) | ✅                             |
| Observation write (HTTP)            | ✅                             |
| Observation write (MQTT /`wss`)   | ✅ validated (publishes persist, no dupes) |
| Real SULO sensor data               | ⏭️ next                      |

---

## Next plan — connect the real Brabantse Delta sensors

**1. Network hardening**
Allowlist `sta.wbd-rd.nl` on our side; disable the TLS skip-verify workaround so
production traffic verifies WBD's certificate genuinely.

**2. Build the SULO adapter**
Swap the dummy adapter for the real SULO connector — same canonical interface, so the
ingest → validate → FROST path is unchanged and already proven.

**3. Map real containers**
Real waste containers → Things/Datastreams; fill-level → Observations; verify against
WBD's project/authorization model.

---

## Next plan — cont.

**4. Validate MQTT push path**
Confirm the `wss://sta.wbd-rd.nl/mqtt` publish path end-to-end as an alternative to HTTP.

**5. Clean up the testbed**
Remove the 5 synthetic containers (ids 96–100) from the shared WBD server.

**6. Dual-write / freshness**
Exercise dual-write to a fallback FROST and the freshness watchdog before go-live.

---

## Asks & dependencies

- **Network team:** allowlist `sta.wbd-rd.nl` (removes the TLS workaround).
- **WBD:** confirm whether production Things should be scoped to a specific **Project**.
- **SULO:** API access / credentials to begin the real adapter.

---

## Summary

- The translation layer **writes to WBD end-to-end today** — validated with synthetic data.
- The remaining blockers are **integration details**, not architecture.
- Clear path from here to **live Brabantse Delta sensors**.

**Questions?**

---

## Appendix — how to verify it yourself

Endpoint: `https://sta.wbd-rd.nl/FROST-Server/v1.1` · Basic Auth (`write`)

All three checks below were run live on 2026-07-17. `%24` is the URL-encoded `$`;
`-k` tolerates the network filter's substituted certificate (testbed only).

Responses are trimmed to the meaningful fields (navigation links removed) — the
**counts and key values are exactly as returned**.

---

## Appendix — Check 1: the containers (Things)

```powershell
curl.exe -k -u "write:$env:FROST_PASSWORD" `
  "https://sta.wbd-rd.nl/FROST-Server/v1.1/Things?%24count=true&%24filter=startswith(name,%27Dummy%27)"
```

```json
{
  "@iot.count": 5,
  "value": [
    {
      "@iot.id": 96,
      "name": "Dummy Container DUMMY-0001",
      "description": "Synthetic waste container for end-to-end validation (not a real sensor)",
      "properties": {
        "area": "s-hertogenbosch-dummy",
        "clappform_source_system": "smartsulo",
        "first_seen_at": "2026-07-17T09:17:40Z",
        "synthetic": "true",
        "vendor": "dummy",
        "vendor_native_id": "DUMMY-0001"
      },
      "restricted": false
    }
    /* … Things 97–100 (DUMMY-0003, -0004, -0002, -0005) … */
  ]
}
```

---

## Appendix — Check 2: the stream (Datastream)

```powershell
curl.exe -k -u "write:$env:FROST_PASSWORD" `
  "https://sta.wbd-rd.nl/FROST-Server/v1.1/Things(96)/Datastreams?%24count=true"
```

```json
{
  "@iot.count": 1,
  "value": [
    {
      "@iot.id": 663,
      "name": "Fill level — Dummy Container DUMMY-0001",
      "observationType": ".../OM_Measurement",
      "unitOfMeasurement": { "name": "Percent", "symbol": "%",
        "definition": ".../UCUM/0/%" },
      "observedArea": { "type": "Point", "coordinates": [5.2913, 51.6978] },
      "phenomenonTime": "2026-07-17T08:20:00Z/2026-07-17T09:35:00Z",
      "resultTime":     "2026-07-17T08:20:00Z/2026-07-17T09:35:00Z",
      "properties": { "expected_cadence_seconds": 300 },
      "restricted": false
    }
  ]
}
```

---

## Appendix — Check 3: the readings (Observations)

```powershell
curl.exe -k -u "write:$env:FROST_PASSWORD" `
  "https://sta.wbd-rd.nl/FROST-Server/v1.1/Datastreams(663)/Observations?%24count=true&%24top=1"
```

```json
{
  "@iot.count": 16,
  "value": [
    {
      "@iot.id": 1729126,
      "phenomenonTime": "2026-07-17T08:20:00Z",
      "resultTime":     "2026-07-17T08:20:00Z",
      "result": 39.711133301307875,
      "resultQuality": {
        "validated_by": "clappform-translation-layer",
        "validation_version": "v1"
      },
      "parameters": { "raw_observation_id": "DUMMY-0001@1784276400" }
    }
  ]
}
```

`@iot.count: 16` = the full 1-hour back-fill; `resultQuality` = validation provenance;
`raw_observation_id` = idempotency key.
