// Package canonical holds the vendor-agnostic types that flow between
// adapters, validator, OMS mapper, and FROST writer. Vendor-specific
// shapes never escape an adapter; everything downstream sees only these
// types.
package canonical

import "time"

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
type Datastream struct {
	ThingVendorNativeID    string
	ObservedProperty       ObservedProperty
	Unit                   Unit
	SensorMetadata         map[string]string
	ExpectedCadenceSeconds int
}

// Observation is one reading from a vendor.
// PhenomenonTime and ResultTime are always UTC.
type Observation struct {
	ThingVendorNativeID string
	ObservedProperty    ObservedProperty
	PhenomenonTime      time.Time
	ResultTime          time.Time
	Result              float64
	RawObservationID    string
}
