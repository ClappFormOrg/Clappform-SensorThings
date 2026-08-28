# Collaborall STA source: Step 1 inspection findings

Date: 2026-07-22. Source inspected live via the read-only curl probes in the plan.

## Endpoint

- **Base URL**: `https://sta-server.collaborall.net/v1.1`
- **Auth**: HTTP Basic (same pattern as WBD).
- Entities carry `@iot.id`, `@iot.selfLink` and nav links. Recorded here as a
  FROST-Server; corrected later in the testbed to an independent PHP/Laravel STA
  implementation (see section 1.5 of the topic 2 report).

## Scale

- **46 ObservedProperties**, **460 Datastreams** (`@iot.count`).
- Many Datastreams share a Sensor (one node → many streams), and many share an
  ObservedProperty across Sensors.

## Domain — NOT waste-container fill level

This is a **mixed multi-phenomenon environmental / industrial monitoring** network.
There is **no fill-level data at all**. Observed phenomena include:

- **Pump telemetry**: `Druk` (pressure, kPa), `Debiet` (flow rate, m³/h).
- **Outdoor node (SHT31)**: `air_temperature` (°C), `air_humidity` (%), `battery_voltage` (V).
- **Indoor module**: `temperature_indoor` (°C), `internal_humidity` (%), `co2_levels` (ppm),
  `gauge_pressure`/`absolute_pressure` (mbar), `battery_level` (percent),
  `motion` (boolean), `coarse/fine_airborne_particles` PM10/PM2.5 (μg/m³),
  `tvoc`, `light_level` (lux band index 0–5), `internal_noise_levels`.
- **Seismic**: `ground_acceleration_east_west` / `_north_south` / `_vertical`.
- **Weather station**: `Temperature`, `Relative humidity`, `PM2.5 concentration`,
  `Air temperature`, `CO2 concentration`, `Air pressure`, `Wind speed`,
  `Wind direction`, `Precipitation`, `Solar radiation`.

## Result / observation types vary (not all numeric)

- `OM_Measurement` — numeric (most streams). ✅ fits `result float64`.
- `OM_TruthObservation` — **boolean** (e.g. `motion`, unit null). ❌ not numeric.
- `OM_CountObservation` — **integer** (e.g. `light_level`). ✅ numeric-compatible.

## Data-quality notes

- **Duplicate ObservedProperty names** with different `@iot.id`/definitions
  (e.g. several `co2_levels`, `gauge_pressure`, `battery_level`, `temperature_indoor`),
  from different vocab sources (qudt / wikipedia / dbpedia / cf).
- Some `unitOfMeasurement` fields are placeholders (`"..."`) or `null`.

## Conclusion — this is Branch B

The plan's Branch A (fill-level only) does **not apply**: with the current
fill-level-only filter and the fill-level-centric OMS mapper, the reader would
map **zero** of the 460 Datastreams. Replicating this source requires either a
faithful passthrough mapping or a deliberately chosen subset. **This needs a
scope decision before proceeding** (see the questions raised with the user).

## Naming

Resolved. The vendor id was `collaboroll` when we wrote this, against a host of
`sta-server.collaborall.net`. We aligned it to the source: `VendorID` is now
`collaborall`, the ingest path is `/ingest/collaborall`, and destination Things
carry `Collaborall` names.
