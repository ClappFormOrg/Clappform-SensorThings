// Package canonical holds the vendor-agnostic types that flow between
// adapters, validator, OMS mapper, and FROST writer. Vendor-specific
// shapes never escape an adapter; everything downstream sees only these
// types.
package canonical

import (
	"encoding/json"
	"time"
)

// ObservedProperty enumerates the phenomena the translation layer can
// publish. v1 ingests FillLevel only; Temperature and Battery are reserved
// for future use.
type ObservedProperty string

const (
	FillLevel   ObservedProperty = "FILL_LEVEL"
	Temperature ObservedProperty = "TEMPERATURE"
	Battery     ObservedProperty = "BATTERY"
)

// Unit enumerates units of measurement.
type Unit string

const (
	Percent Unit = "PERCENT"
	Celsius Unit = "CELSIUS"
	Volt    Unit = "VOLT"
)

// Coord is an EPSG:4326 GeoJSON-order coordinate (lon, lat).
// Adapters whose source returns (lat, lon) must swap inside the adapter.
type Coord struct {
	Lon float64
	Lat float64
}

// Thing is the vendor-agnostic projection of a sensing platform — a waste
// container in v1; vehicles deferred per ADR-003.
type Thing struct {
	VendorID       string
	VendorNativeID string
	Name           string
	Description    string
	Location       Coord
	Properties     map[string]string
}

// Datastream is a (Thing, ObservedProperty) pair plus the metadata
// needed to publish observations under that stream.
//
// ObservedProperty is the per-Thing stream key (it identifies one
// Datastream within a Thing in the state store). For vendors with a fixed
// phenomenon (SULO/dummy) it is a canonical enum. For passthrough vendors
// (Collaborall) it carries a vendor-unique stream token, and the human
// ObservedProperty entity is described by the Passthrough* fields below.
//
// The Passthrough fields are optional. When set, the OMS mapper reproduces
// the source entity faithfully (using these names/units/type verbatim,
// with the configured entity-name prefix) instead of synthesising
// fill-level names. Empty fields fall back to the fill-level defaults, so
// existing poll adapters are unaffected.
type Datastream struct {
	ThingVendorNativeID    string
	ObservedProperty       ObservedProperty
	Unit                   Unit
	SensorMetadata         map[string]string
	ExpectedCadenceSeconds int

	// Passthrough entity detail (optional; Collaborall faithful copy).
	Name                       string // source Datastream name
	Description                string // source Datastream description
	ObservedPropertyName       string // source ObservedProperty name
	ObservedPropertyDefinition string // source ObservedProperty definition URI
	UnitName                   string // source unitOfMeasurement.name
	UnitSymbol                 string // source unitOfMeasurement.symbol
	UnitDefinition             string // source unitOfMeasurement.definition
	ObservationType            string // source Datastream.observationType URI
	SensorName                 string // source Sensor name
}

// Observation is one reading from a vendor.
// PhenomenonTime and ResultTime are always UTC.
//
// Result holds the numeric value (used by fill-level vendors and the
// state-store write log). ResultRaw, when non-nil, carries the verbatim
// STA result JSON for passthrough vendors whose results may be non-numeric
// (boolean OM_TruthObservation, integer OM_CountObservation, string, …).
// When ResultRaw is set the FROST Observation payload uses it verbatim and
// the validator skips numeric range checks.
type Observation struct {
	ThingVendorNativeID string
	ObservedProperty    ObservedProperty
	PhenomenonTime      time.Time
	ResultTime          time.Time
	Result              float64
	ResultRaw           json.RawMessage
	RawObservationID    string
}
