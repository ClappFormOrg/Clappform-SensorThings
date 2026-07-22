package frost

// STA entity payload shapes used by the OMS mapper. Field names match
// the OGC SensorThings API v1.1 wire format exactly.

// GeoJSONPoint is the Location.location / FeatureOfInterest.feature
// payload. Coordinates are [longitude, latitude] in EPSG:4326 per
// GeoJSON.
type GeoJSONPoint struct {
	Type        string    `json:"type"` // always "Point"
	Coordinates []float64 `json:"coordinates"`
}

// NewPoint builds a GeoJSON Point. lon and lat are expected to be
// already-validated by the caller.
func NewPoint(lon, lat float64) GeoJSONPoint {
	return GeoJSONPoint{Type: "Point", Coordinates: []float64{lon, lat}}
}

// UnitOfMeasurement matches STA's Datastream.unitOfMeasurement.
type UnitOfMeasurement struct {
	Name       string `json:"name"`
	Symbol     string `json:"symbol"`
	Definition string `json:"definition"`
}

// Thing payload. Inline Locations is optional; we POST Locations
// separately via /Things(id)/Locations so the mapper omits it here.
type Thing struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Properties  map[string]string `json:"properties,omitempty"`
}

// Location payload (encodingType "application/geo+json").
type Location struct {
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	EncodingType string       `json:"encodingType"`
	Location     GeoJSONPoint `json:"location"`
}

// Sensor payload (encodingType "application/json"; metadata is an
// inline object, not a URL).
type Sensor struct {
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	EncodingType string            `json:"encodingType"`
	Metadata     map[string]string `json:"metadata"`
}

// ObservedProperty payload.
type ObservedProperty struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Definition  string `json:"definition"`
}

// Datastream payload. Cross-entity refs are written via the
// nested-link form: {"@iot.id": <n>}.
type Datastream struct {
	Name              string              `json:"name"`
	Description       string              `json:"description"`
	UnitOfMeasurement UnitOfMeasurement   `json:"unitOfMeasurement"`
	ObservationType   string              `json:"observationType"`
	Thing             EntityRef           `json:"Thing"`
	Sensor            EntityRef           `json:"Sensor"`
	ObservedProperty  EntityRef           `json:"ObservedProperty"`
	Properties        map[string]any      `json:"properties,omitempty"`
}

// FeatureOfInterest payload.
type FeatureOfInterest struct {
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	EncodingType string       `json:"encodingType"`
	Feature      GeoJSONPoint `json:"feature"`
}

// Observation payload. PhenomenonTime and ResultTime are RFC3339
// strings; the OMS mapper sets them. Result is the raw measurement.
type Observation struct {
	PhenomenonTime    string         `json:"phenomenonTime"`
	ResultTime        string         `json:"resultTime"`
	Result            float64        `json:"result"`
	FeatureOfInterest *EntityRef     `json:"FeatureOfInterest,omitempty"`
	Parameters        map[string]any `json:"parameters,omitempty"`
	ResultQuality     map[string]any `json:"resultQuality,omitempty"`
}

// EntityRef is the {"@iot.id": <n>} reference form STA uses for
// cross-entity linkage on POST.
type EntityRef struct {
	IotID int64 `json:"@iot.id"`
}

// Standard STA / OMS URIs used as defaults. These are config-overridable
// via env vars per S7 in the adversarial review (config-driven mapping
// for Topic #1 alignment).
const (
	EncodingGeoJSON              = "application/geo+json"
	EncodingJSON                 = "application/json"
	ObservationTypeMeasurement   = "http://www.opengis.net/def/observationType/OGC-OM/2.0/OM_Measurement"
	UCUMPercentDefinition        = "http://www.opengis.net/def/uom/UCUM/0/%"
	QUDTDimensionlessRatio       = "http://qudt.org/vocab/quantitykind/DimensionlessRatio"
)
