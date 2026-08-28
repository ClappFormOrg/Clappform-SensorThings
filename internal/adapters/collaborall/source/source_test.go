package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters/collaborall"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/frost"
)

// newSourceServer serves a small canned Collaborall FROST tree:
//
//	Thing 10 "Thing A" @ [5.29,51.69]
//	  ├─ Datastream 100 "Pomp-1 druk"  Sensor sensor-a(3)  OP "Druk"        unit kPa
//	  ├─ Datastream 101 "humidity"     Sensor sensor-b(4)  OP "air_humidity" unit %
//	  └─ Datastream 102 "motion"       Sensor sensor-c(5)  OP "motion"      unit null (TruthObservation)
//
// gotObsFilter captures the $filter sent to the observations endpoint.
func newSourceServer(t *testing.T, gotObsFilter *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/Things":
			_, _ = w.Write([]byte(`{"value":[
				{"@iot.id":10,"name":"Thing A","description":"d","properties":{"area":"x","n":7},
				 "Locations":[{"@iot.id":1,"name":"loc","location":{"type":"Point","coordinates":[5.29,51.69]}}]}
			]}`))
		case p == "/Things(10)/Datastreams":
			_, _ = w.Write([]byte(`{"value":[
				{"@iot.id":100,"name":"Pomp-1 druk","description":"pump pressure",
				 "unitOfMeasurement":{"name":"Kilopascal","symbol":"kPa","definition":"http://qudt.org/vocab/unit/KiloPA"},
				 "observationType":"http://www.opengis.net/def/observationType/OGC-OM/2.0/OM_Measurement",
				 "Sensor":{"@iot.id":3,"name":"sensor-a","metadata":{"model":"M1"}},
				 "ObservedProperty":{"name":"Druk","definition":"http://qudt.org/vocab/quantitykind/Pressure"}},
				{"@iot.id":101,"name":"humidity",
				 "unitOfMeasurement":{"name":"percent","symbol":"%","definition":"u"},
				 "observationType":"http://www.opengis.net/def/observationType/OGC-OM/2.0/OM_Measurement",
				 "Sensor":{"@iot.id":4,"name":"sensor-b"},
				 "ObservedProperty":{"name":"air_humidity","definition":"d"}},
				{"@iot.id":102,"name":"motion",
				 "unitOfMeasurement":{"name":null,"symbol":null,"definition":null},
				 "observationType":"http://www.opengis.net/def/observationType/OGC-OM/2.0/OM_TruthObservation",
				 "Sensor":{"@iot.id":5,"name":"sensor-c"},
				 "ObservedProperty":{"name":"motion","definition":"d"}}
			]}`))
		case strings.HasPrefix(p, "/Datastreams(100)/Observations"):
			if gotObsFilter != nil {
				*gotObsFilter = r.URL.Query().Get("$filter")
			}
			_, _ = w.Write([]byte(`{"value":[
				{"@iot.id":1,"phenomenonTime":"2026-07-22T10:00:00Z","resultTime":"2026-07-22T10:00:01Z","result":73.5},
				{"@iot.id":2,"phenomenonTime":"2026-07-22T10:05:00Z","result":"n/a"},
				{"@iot.id":3,"phenomenonTime":"2026-07-22T10:10:00Z","result":80}
			]}`))
		case strings.HasPrefix(p, "/Datastreams(102)/Observations"):
			_, _ = w.Write([]byte(`{"value":[
				{"@iot.id":9,"phenomenonTime":"2026-07-22T10:00:00Z","result":true}
			]}`))
		default:
			_, _ = w.Write([]byte(`{"value":[]}`))
		}
	}))
}

func newReader(t *testing.T, srv *httptest.Server, cfg Config) *SourceReader {
	t.Helper()
	c := frost.NewClient(srv.URL, frost.Auth{}, false, 5*time.Second)
	return New(c, cfg, nil)
}

func TestDiscoverEmptyWatchListReplicatesAllStreams(t *testing.T) {
	srv := newSourceServer(t, nil)
	defer srv.Close()
	r := newReader(t, srv, Config{})

	streams, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// Passthrough: all three phenomena replicate (no fill-level filtering).
	if len(streams) != 3 {
		t.Fatalf("want 3 streams, got %d", len(streams))
	}

	got := streams[0]
	if got.Thing.VendorID != collaborall.VendorID || got.Thing.VendorNativeID != "10" {
		t.Fatalf("thing identity: %+v", got.Thing)
	}
	if got.Thing.Location.Lon != 5.29 || got.Thing.Location.Lat != 51.69 {
		t.Fatalf("coordinate order wrong: %+v", got.Thing.Location)
	}
	if got.Thing.Properties["area"] != "x" || got.Thing.Properties["n"] != "7" {
		t.Fatalf("properties stringify: %+v", got.Thing.Properties)
	}
	// Passthrough datastream detail preserved.
	d := got.Datastream
	if string(d.ObservedProperty) != "Pomp-1 druk" { // stream key = source ds name
		t.Fatalf("stream key: %q", d.ObservedProperty)
	}
	if d.Name != "Pomp-1 druk" || d.ObservedPropertyName != "Druk" {
		t.Fatalf("passthrough names: %+v", d)
	}
	if d.UnitSymbol != "kPa" || d.UnitName != "Kilopascal" {
		t.Fatalf("unit passthrough: %+v", d)
	}
	if !strings.Contains(d.ObservationType, "OM_Measurement") {
		t.Fatalf("observationType passthrough: %q", d.ObservationType)
	}
	if d.SensorMetadata["source_sensor_name"] != "sensor-a" || d.SensorMetadata["model"] != "M1" {
		t.Fatalf("sensor metadata: %+v", d.SensorMetadata)
	}
	if got.SourceDatastreamID != 100 {
		t.Fatalf("source datastream id: %d", got.SourceDatastreamID)
	}
}

func TestDiscoverNullUnitSanitised(t *testing.T) {
	srv := newSourceServer(t, nil)
	defer srv.Close()
	r := newReader(t, srv, Config{WatchSensors: []string{"sensor-c"}})

	streams, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(streams) != 1 {
		t.Fatalf("want 1 stream, got %d", len(streams))
	}
	d := streams[0].Datastream
	if d.UnitName != "" || d.UnitSymbol != "" {
		t.Fatalf("null unit should map to empty, got %+v", d)
	}
	if !strings.Contains(d.ObservationType, "TruthObservation") {
		t.Fatalf("observationType: %q", d.ObservationType)
	}
}

func TestDiscoverWatchByName(t *testing.T) {
	srv := newSourceServer(t, nil)
	defer srv.Close()
	r := newReader(t, srv, Config{WatchSensors: []string{"sensor-a"}})

	streams, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(streams) != 1 || streams[0].SourceDatastreamID != 100 {
		t.Fatalf("want only ds 100 (sensor-a), got %+v", streams)
	}
}

func TestDiscoverWatchByID(t *testing.T) {
	srv := newSourceServer(t, nil)
	defer srv.Close()
	r := newReader(t, srv, Config{WatchSensors: []string{"4"}}) // sensor-b @iot.id

	streams, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(streams) != 1 || streams[0].SourceDatastreamID != 101 {
		t.Fatalf("want only ds 101 (sensor-b by id), got %+v", streams)
	}
}

func TestDiscoverWatchExcludesUnlisted(t *testing.T) {
	srv := newSourceServer(t, nil)
	defer srv.Close()
	r := newReader(t, srv, Config{WatchSensors: []string{"nonexistent"}})

	streams, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(streams) != 0 {
		t.Fatalf("want 0 streams, got %d", len(streams))
	}
}

func TestFetchObservationsReplicatesNumericAndNonNumeric(t *testing.T) {
	var gotFilter string
	srv := newSourceServer(t, &gotFilter)
	defer srv.Close()
	r := newReader(t, srv, Config{WatchSensors: []string{"sensor-a"}})

	streams, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	since := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	obs, err := r.FetchObservations(context.Background(), streams[0], since, 0)
	if err != nil {
		t.Fatalf("FetchObservations: %v", err)
	}

	// All three replicate verbatim, including the non-numeric "n/a".
	if len(obs) != 3 {
		t.Fatalf("want 3 observations, got %d", len(obs))
	}
	if obs[0].Result != 73.5 || string(obs[0].ResultRaw) != "73.5" {
		t.Fatalf("numeric passthrough: %+v", obs[0])
	}
	if obs[1].Result != 0 || string(obs[1].ResultRaw) != `"n/a"` {
		t.Fatalf("non-numeric passthrough: raw=%s result=%v", obs[1].ResultRaw, obs[1].Result)
	}
	if obs[0].RawObservationID != "100:1" {
		t.Fatalf("raw id format: %q", obs[0].RawObservationID)
	}
	if string(obs[0].ObservedProperty) != "Pomp-1 druk" || obs[0].ThingVendorNativeID != "10" {
		t.Fatalf("observation identity: %+v", obs[0])
	}
	if !obs[1].ResultTime.Equal(obs[1].PhenomenonTime) {
		t.Fatalf("resultTime fallback failed: %+v", obs[1])
	}
	if !strings.Contains(gotFilter, "phenomenonTime gt") {
		t.Fatalf("since not forwarded as $filter: %q", gotFilter)
	}
}

func TestFetchObservationsBooleanResult(t *testing.T) {
	srv := newSourceServer(t, nil)
	defer srv.Close()
	r := newReader(t, srv, Config{WatchSensors: []string{"sensor-c"}})

	streams, _ := r.Discover(context.Background())
	obs, err := r.FetchObservations(context.Background(), streams[0], time.Time{}, 0)
	if err != nil {
		t.Fatalf("FetchObservations: %v", err)
	}
	if len(obs) != 1 || string(obs[0].ResultRaw) != "true" {
		t.Fatalf("boolean result passthrough failed: %+v", obs)
	}
}

func TestFetchObservationsZeroSinceOmitsFilter(t *testing.T) {
	var gotFilter string
	srv := newSourceServer(t, &gotFilter)
	defer srv.Close()
	r := newReader(t, srv, Config{WatchSensors: []string{"sensor-a"}})

	streams, _ := r.Discover(context.Background())
	if _, err := r.FetchObservations(context.Background(), streams[0], time.Time{}, 0); err != nil {
		t.Fatalf("FetchObservations: %v", err)
	}
	if gotFilter != "" {
		t.Fatalf("zero since should omit $filter, got %q", gotFilter)
	}
}
