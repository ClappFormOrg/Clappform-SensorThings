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

	"github.com/clappformorg/geonovum-sta-translation/internal/canonical"
	"github.com/clappformorg/geonovum-sta-translation/internal/frost"
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
		Name:        ThingName(t.VendorID, t.VendorNativeID),
		Description: t.Description,
		Properties:  props,
	}
}

// LocationPayload builds the Location for a Thing at the time the
// Location was first observed (now in UTC for upsert idempotency on
// "same day, same coords").
func (m *Mapper) LocationPayload(t canonical.Thing, when time.Time) frost.Location {
	return frost.Location{
		Name:         LocationName(t.VendorID, t.VendorNativeID, when),
		Description:  fmt.Sprintf("Location of %s container %s", titleCase(t.VendorID), t.VendorNativeID),
		EncodingType: frost.EncodingGeoJSON,
		Location:     frost.NewPoint(t.Location.Lon, t.Location.Lat),
	}
}

// SensorPayload extracts the Sensor for a Datastream from
// SensorMetadata. The adapter populates these keys; defaults below
// handle missing values without failing.
func (m *Mapper) SensorPayload(t canonical.Thing, d canonical.Datastream) frost.Sensor {
	model := d.SensorMetadata["model"]
	firmware := d.SensorMetadata["firmware_version"]
	if model == "" {
		model = "unknown"
	}
	if firmware == "" {
		firmware = "unknown"
	}
	return frost.Sensor{
		Name:         SensorName(t.VendorID, model, firmware),
		Description:  "Vendor-supplied fill-level sensor",
		EncodingType: frost.EncodingJSON,
		Metadata:     d.SensorMetadata,
	}
}

// ObservedPropertyPayload returns the OP for a Datastream. v1 only
// supports FillLevel; others are reserved.
func (m *Mapper) ObservedPropertyPayload(op canonical.ObservedProperty) frost.ObservedProperty {
	switch op {
	case canonical.FillLevel:
		return frost.ObservedProperty{
			Name:        "Fill level",
			Description: "Container fill level as a percentage of total capacity",
			Definition:  m.cfg.FillLevelObservedPropertyDefinition,
		}
	default:
		// Reserved; not ingested in v1. Provide a defensible default
		// so a future expansion just adds the cases.
		return frost.ObservedProperty{
			Name:        humanProperty(op),
			Description: string(op),
			Definition:  "",
		}
	}
}

// DatastreamPayload builds the Datastream entity referencing the
// already-resolved Thing / Sensor / ObservedProperty @iot.ids.
func (m *Mapper) DatastreamPayload(
	t canonical.Thing,
	d canonical.Datastream,
	thingID, sensorID, observedPropertyID int64,
) frost.Datastream {
	return frost.Datastream{
		Name:        DatastreamName(t.VendorID, t.VendorNativeID, d.ObservedProperty),
		Description: fmt.Sprintf("%s observations for %s container %s",
			humanProperty(d.ObservedProperty), titleCase(t.VendorID), t.VendorNativeID),
		UnitOfMeasurement: m.unitPayload(d.Unit),
		ObservationType:   m.cfg.ObservationTypeMeasurement,
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
		Name:         FoIName(t.VendorID, t.VendorNativeID),
		Description:  fmt.Sprintf("Geographic location of %s container %s", titleCase(t.VendorID), t.VendorNativeID),
		EncodingType: frost.EncodingGeoJSON,
		Feature:      frost.NewPoint(t.Location.Lon, t.Location.Lat),
	}
}

// ObservationPayload builds the STA Observation body for an accepted
// canonical observation. foiID may be 0 (omits the FoI reference, so
// FROST will reuse the Datastream's default).
func (m *Mapper) ObservationPayload(o canonical.Observation, foiID int64) frost.Observation {
	body := frost.Observation{
		PhenomenonTime: o.PhenomenonTime.UTC().Format(time.RFC3339Nano),
		ResultTime:     o.ResultTime.UTC().Format(time.RFC3339Nano),
		Result:         o.Result,
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

func (m *Mapper) unitPayload(u canonical.Unit) frost.UnitOfMeasurement {
	switch u {
	case canonical.Percent:
		return frost.UnitOfMeasurement{Name: "Percent", Symbol: "%", Definition: m.cfg.PercentUnitDefinition}
	case canonical.Celsius:
		return frost.UnitOfMeasurement{Name: "Degree Celsius", Symbol: "°C", Definition: "http://www.opengis.net/def/uom/UCUM/0/Cel"}
	case canonical.Volt:
		return frost.UnitOfMeasurement{Name: "Volt", Symbol: "V", Definition: "http://www.opengis.net/def/uom/UCUM/0/V"}
	default:
		return frost.UnitOfMeasurement{Name: string(u)}
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
