// Package oms maps vendor-agnostic canonical types to OGC STA entity
// payloads per the OMS data-model decisions in the design doc.
// Vocabulary URIs and unit codes can be overridden by Config so the
// translation layer can re-align with Topic #1's choices without code
// changes.
package oms

import (
	"fmt"
	"strings"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/frost"
)

// Config holds the vocabulary / unit URIs the mapper uses. Defaults
// match the Implementation Contract. Override via env vars at startup
// to align with a different OMS dialect.
type Config struct {
	// FillLevelObservedPropertyDefinition is the URI for ObservedProperty.definition.
	FillLevelObservedPropertyDefinition string

	// PercentUnitDefinition is the UCUM URI for "%" used in unitOfMeasurement.
	PercentUnitDefinition string

	// ObservationTypeMeasurement is the URI for Datastream.observationType.
	ObservationTypeMeasurement string

	// ClappformSourceSystem labels the source platform in Thing.properties.
	// Defaults to "smartsulo" for the SULO adapter; adapters may override.
	ClappformSourceSystem string

	// EntityNamePrefix is prepended to every FROST entity name this layer
	// creates (Thing, Location, Sensor, ObservedProperty, Datastream,
	// FeatureOfInterest). It marks our data in a FROST-Server shared with
	// other parties and keeps our name-based upsert from colliding with
	// theirs. Default "" (no prefix); set e.g. "CF_" for Collaborall.
	EntityNamePrefix string
}

// DefaultConfig returns the Implementation-Contract defaults.
func DefaultConfig() Config {
	return Config{
		FillLevelObservedPropertyDefinition: frost.QUDTDimensionlessRatio,
		PercentUnitDefinition:               frost.UCUMPercentDefinition,
		ObservationTypeMeasurement:          frost.ObservationTypeMeasurement,
		ClappformSourceSystem:               "smartsulo",
	}
}

// Mapper produces STA entity payloads. It is stateless and safe for
// concurrent use.
type Mapper struct {
	cfg Config
}

// New returns a Mapper using cfg.
func New(cfg Config) *Mapper { return &Mapper{cfg: cfg} }

// The *EntityName methods are the single source of truth for the FROST
// entity name of each entity in the upsert chain: ingest.resolveChain uses
// them for the GET-by-name probe and the payload builders use them for the
// created entity, so the two always agree. Each applies the configured
// EntityNamePrefix and prefers the canonical Passthrough* names (faithful
// copy) over the synthesised fill-level names.

// ThingEntityName is the FROST Thing.name for t.
func (m *Mapper) ThingEntityName(t canonical.Thing) string {
	base := t.Name
	if base == "" {
		base = ThingName(t.VendorID, t.VendorNativeID)
	}
	return m.cfg.EntityNamePrefix + base
}

// LocationEntityName is the FROST Location.name for t at when (UTC).
func (m *Mapper) LocationEntityName(t canonical.Thing, when time.Time) string {
	return m.cfg.EntityNamePrefix + LocationName(t.VendorID, t.VendorNativeID, when)
}

// SensorEntityName is the FROST Sensor.name for (t, d).
func (m *Mapper) SensorEntityName(t canonical.Thing, d canonical.Datastream) string {
	if d.SensorName != "" {
		return m.cfg.EntityNamePrefix + d.SensorName
	}
	model, firmware := sensorModelFirmware(d)
	return m.cfg.EntityNamePrefix + SensorName(t.VendorID, model, firmware)
}

// ObservedPropertyEntityName is the FROST ObservedProperty.name for d.
func (m *Mapper) ObservedPropertyEntityName(d canonical.Datastream) string {
	if d.ObservedPropertyName != "" {
		return m.cfg.EntityNamePrefix + d.ObservedPropertyName
	}
	return m.cfg.EntityNamePrefix + humanProperty(d.ObservedProperty)
}

// DatastreamEntityName is the FROST Datastream.name for (t, d).
func (m *Mapper) DatastreamEntityName(t canonical.Thing, d canonical.Datastream) string {
	if d.Name != "" {
		return m.cfg.EntityNamePrefix + d.Name
	}
	return m.cfg.EntityNamePrefix + DatastreamName(t.VendorID, t.VendorNativeID, d.ObservedProperty)
}

// FoIEntityName is the FROST FeatureOfInterest.name for t.
func (m *Mapper) FoIEntityName(t canonical.Thing) string {
	return m.cfg.EntityNamePrefix + FoIName(t.VendorID, t.VendorNativeID)
}

func sensorModelFirmware(d canonical.Datastream) (model, firmware string) {
	model = d.SensorMetadata["model"]
	firmware = d.SensorMetadata["firmware_version"]
	if model == "" {
		model = "unknown"
	}
	if firmware == "" {
		firmware = "unknown"
	}
	return model, firmware
}

// ThingName builds the canonical "<Vendor> Container <vendor_native_id>"
// name per the Implementation Contract. The Vendor segment is the
// title-case of vendorID.
func ThingName(vendorID, vendorNativeID string) string {
	return fmt.Sprintf("%s Container %s", titleCase(vendorID), vendorNativeID)
}

// LocationName builds the dated Location name. dateUTC must be in UTC.
func LocationName(vendorID, vendorNativeID string, dateUTC time.Time) string {
	return fmt.Sprintf("Location of %s Container %s at %s",
		titleCase(vendorID), vendorNativeID, dateUTC.UTC().Format("2006-01-02"))
}

// SensorName builds the Sensor.name. Multiple Things may share a Sensor
// entity when (vendor, model, firmware) match — intentional per the
// Implementation Contract.
func SensorName(vendorID, model, firmware string) string {
	return fmt.Sprintf("%s fill-level sensor %s %s",
		titleCase(vendorID), model, firmware)
}

// DatastreamName builds the per-Datastream name.
func DatastreamName(vendorID, vendorNativeID string, op canonical.ObservedProperty) string {
	return fmt.Sprintf("%s — %s Container %s",
		humanProperty(op), titleCase(vendorID), vendorNativeID)
}

// FoIName builds the FeatureOfInterest name for a container.
func FoIName(vendorID, vendorNativeID string) string {
	return fmt.Sprintf("Container location: %s Container %s",
		titleCase(vendorID), vendorNativeID)
}

// ObservedPropertyName returns the human-facing name for an OP.
func ObservedPropertyName(op canonical.ObservedProperty) string {
	return humanProperty(op)
}

// ThingPayload returns the STA Thing JSON body for a canonical Thing.
func (m *Mapper) ThingPayload(t canonical.Thing) frost.Thing {
	props := mergeProperties(map[string]string{
		"vendor":                  t.VendorID,
		"vendor_native_id":        t.VendorNativeID,
		"clappform_source_system": m.cfg.ClappformSourceSystem,
		"first_seen_at":           time.Now().UTC().Format(time.RFC3339),
	}, t.Properties)

	return frost.Thing{
		Name:        m.ThingEntityName(t),
		Description: t.Description,
		Properties:  props,
	}
}

// LocationPayload builds the Location for a Thing at the time the
// Location was first observed (now in UTC for upsert idempotency on
// "same day, same coords").
func (m *Mapper) LocationPayload(t canonical.Thing, when time.Time) frost.Location {
	return frost.Location{
		Name:         m.LocationEntityName(t, when),
		Description:  fmt.Sprintf("Location of %s %s", titleCase(t.VendorID), t.VendorNativeID),
		EncodingType: frost.EncodingGeoJSON,
		Location:     frost.NewPoint(t.Location.Lon, t.Location.Lat),
	}
}

// SensorPayload extracts the Sensor for a Datastream from
// SensorMetadata. The adapter populates these keys; defaults below
// handle missing values without failing.
func (m *Mapper) SensorPayload(t canonical.Thing, d canonical.Datastream) frost.Sensor {
	return frost.Sensor{
		Name:         m.SensorEntityName(t, d),
		Description:  "Vendor-supplied sensor",
		EncodingType: frost.EncodingJSON,
		Metadata:     d.SensorMetadata,
	}
}

// ObservedPropertyPayload returns the ObservedProperty entity for a
// Datastream. Passthrough vendors carry the source name/definition; the
// fill-level default is used otherwise.
func (m *Mapper) ObservedPropertyPayload(d canonical.Datastream) frost.ObservedProperty {
	if d.ObservedPropertyName != "" {
		return frost.ObservedProperty{
			Name:        m.ObservedPropertyEntityName(d),
			Description: d.ObservedPropertyName,
			Definition:  d.ObservedPropertyDefinition,
		}
	}
	if d.ObservedProperty == canonical.FillLevel {
		return frost.ObservedProperty{
			Name:        m.ObservedPropertyEntityName(d),
			Description: "Container fill level as a percentage of total capacity",
			Definition:  m.cfg.FillLevelObservedPropertyDefinition,
		}
	}
	return frost.ObservedProperty{
		Name:        m.ObservedPropertyEntityName(d),
		Description: string(d.ObservedProperty),
		Definition:  "",
	}
}

// DatastreamPayload builds the Datastream entity referencing the
// already-resolved Thing / Sensor / ObservedProperty @iot.ids.
func (m *Mapper) DatastreamPayload(
	t canonical.Thing,
	d canonical.Datastream,
	thingID, sensorID, observedPropertyID int64,
) frost.Datastream {
	description := d.Description
	if description == "" {
		description = fmt.Sprintf("%s observations for %s %s",
			humanProperty(d.ObservedProperty), titleCase(t.VendorID), t.VendorNativeID)
	}
	observationType := d.ObservationType
	if observationType == "" {
		observationType = m.cfg.ObservationTypeMeasurement
	}
	return frost.Datastream{
		Name:              m.DatastreamEntityName(t, d),
		Description:       description,
		UnitOfMeasurement: m.unitPayload(d),
		ObservationType:   observationType,
		Thing:             frost.EntityRef{IotID: thingID},
		Sensor:            frost.EntityRef{IotID: sensorID},
		ObservedProperty:  frost.EntityRef{IotID: observedPropertyID},
		Properties: map[string]any{
			"expected_cadence_seconds": d.ExpectedCadenceSeconds,
		},
	}
}

// FoIPayload mirrors a Thing's location as the FeatureOfInterest for
// fill-level observations. For vehicles (deferred per ADR-003) this
// will change.
func (m *Mapper) FoIPayload(t canonical.Thing) frost.FeatureOfInterest {
	return frost.FeatureOfInterest{
		Name:         m.FoIEntityName(t),
		Description:  fmt.Sprintf("Geographic location of %s %s", titleCase(t.VendorID), t.VendorNativeID),
		EncodingType: frost.EncodingGeoJSON,
		Feature:      frost.NewPoint(t.Location.Lon, t.Location.Lat),
	}
}

// ObservationPayload builds the STA Observation body for an accepted
// canonical observation. foiID may be 0 (omits the FoI reference, so
// FROST will reuse the Datastream's default).
func (m *Mapper) ObservationPayload(o canonical.Observation, foiID int64) frost.Observation {
	// Passthrough vendors carry the verbatim STA result (possibly
	// non-numeric); fill-level vendors use the numeric Result.
	var result any = o.Result
	if o.ResultRaw != nil {
		result = o.ResultRaw
	}
	body := frost.Observation{
		PhenomenonTime: o.PhenomenonTime.UTC().Format(time.RFC3339Nano),
		ResultTime:     o.ResultTime.UTC().Format(time.RFC3339Nano),
		Result:         result,
		Parameters: map[string]any{
			"raw_observation_id": o.RawObservationID,
		},
		ResultQuality: map[string]any{
			"validated_by":       "clappform-translation-layer",
			"validation_version": "v1",
		},
	}
	if foiID > 0 {
		body.FeatureOfInterest = &frost.EntityRef{IotID: foiID}
	}
	return body
}

func (m *Mapper) unitPayload(d canonical.Datastream) frost.UnitOfMeasurement {
	// Passthrough: reproduce the source unit verbatim when any field is set.
	if d.UnitName != "" || d.UnitSymbol != "" || d.UnitDefinition != "" {
		return frost.UnitOfMeasurement{Name: d.UnitName, Symbol: d.UnitSymbol, Definition: d.UnitDefinition}
	}
	switch d.Unit {
	case canonical.Percent:
		return frost.UnitOfMeasurement{Name: "Percent", Symbol: "%", Definition: m.cfg.PercentUnitDefinition}
	case canonical.Celsius:
		return frost.UnitOfMeasurement{Name: "Degree Celsius", Symbol: "°C", Definition: "http://www.opengis.net/def/uom/UCUM/0/Cel"}
	case canonical.Volt:
		return frost.UnitOfMeasurement{Name: "Volt", Symbol: "V", Definition: "http://www.opengis.net/def/uom/UCUM/0/V"}
	default:
		return frost.UnitOfMeasurement{Name: string(d.Unit)}
	}
}

func humanProperty(op canonical.ObservedProperty) string {
	switch op {
	case canonical.FillLevel:
		return "Fill level"
	case canonical.Temperature:
		return "Temperature"
	case canonical.Battery:
		return "Battery"
	default:
		return string(op)
	}
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	// ASCII-only: vendor_id is lowercase ASCII per adapter contract.
	return strings.ToUpper(s[:1]) + s[1:]
}

func mergeProperties(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
