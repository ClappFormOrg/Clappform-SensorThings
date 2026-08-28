package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/frost"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/oms"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/state"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/validator"
)

// fakeStore implements only the two Store methods ProcessStream calls;
// the embedded nil interface panics if anything else is invoked (nothing
// else is, in this test).
type fakeStore struct {
	state.Store
	mu      sync.Mutex
	written int

	staThingIDs map[int64]int64 // local thing id → STA @iot.id
	staDSIDs    map[int64]int64 // local datastream id → STA @iot.id
}

func (f *fakeStore) SetSTAThingID(_ context.Context, thingID, staID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.staThingIDs == nil {
		f.staThingIDs = map[int64]int64{}
	}
	f.staThingIDs[thingID] = staID
	return nil
}

func (f *fakeStore) SetSTADatastreamID(_ context.Context, dsID, staID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.staDSIDs == nil {
		f.staDSIDs = map[int64]int64{}
	}
	f.staDSIDs[dsID] = staID
	return nil
}

func (f *fakeStore) ObservationExists(context.Context, int64, time.Time) (bool, error) {
	return false, nil
}

func (f *fakeStore) RecordObservationWrite(_ context.Context, _ int64, _ time.Time, _ float64, _ string, _ int64) error {
	f.mu.Lock()
	f.written++
	f.mu.Unlock()
	return nil
}

// fakeFROST records POST bodies per collection path and answers the
// upsert probes with "not found" so every entity is created.
type fakeFROST struct {
	mu     sync.Mutex
	id     int64
	posts  map[string][]json.RawMessage // path prefix → bodies
	server *httptest.Server
}

func newFakeFROST() *fakeFROST {
	f := &fakeFROST{posts: map[string][]json.RawMessage{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeFROST) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		// FindByName / ObservationExists → empty.
		_, _ = w.Write([]byte(`{"value":[]}`))
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.id++
	id := f.id
	f.posts[r.URL.Path] = append(f.posts[r.URL.Path], json.RawMessage(body))
	f.mu.Unlock()

	w.Header().Set("Location", fmt.Sprintf("%s(%d)", r.URL.Path, id))
	w.WriteHeader(http.StatusCreated)
}

func (f *fakeFROST) bodiesFor(pathContains string) []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []json.RawMessage
	for p, bodies := range f.posts {
		if strings.Contains(p, pathContains) {
			out = append(out, bodies...)
		}
	}
	return out
}

func namesFrom(bodies []json.RawMessage) []string {
	var names []string
	for _, b := range bodies {
		var v struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(b, &v)
		names = append(names, v.Name)
	}
	return names
}

func contains(ss []string, want string) bool {
	return slices.Contains(ss, want)
}

// TestProcessStreamPassthroughWithPrefix is the wired end-to-end check:
// a passthrough Datastream + a non-numeric observation flow through the
// mapper (CF_ prefix) and land at the FROST target with source names and a
// verbatim result.
func TestProcessStreamPassthroughWithPrefix(t *testing.T) {
	ff := newFakeFROST()
	defer ff.server.Close()

	omsCfg := oms.DefaultConfig()
	omsCfg.EntityNamePrefix = "CF_"
	mapper := oms.New(omsCfg)

	client := frost.NewClient(ff.server.URL, frost.Auth{}, false, 5*time.Second)
	writer := frost.NewWriter(frost.Target{Label: "test", Client: client}, slog.New(slog.DiscardHandler))
	store := &fakeStore{}
	proc := New(mapper, []*frost.Writer{writer}, store, validator.DefaultConfig(), slog.New(slog.DiscardHandler))

	thing := canonical.Thing{VendorID: "collaborall", VendorNativeID: "10", Name: "Thing A", Location: canonical.Coord{Lon: 5.29, Lat: 51.69}}
	ds := canonical.Datastream{
		ThingVendorNativeID:  "10",
		ObservedProperty:     "motion",
		Name:                 "motion",
		ObservedPropertyName: "motion",
		ObservationType:      "http://www.opengis.net/def/observationType/OGC-OM/2.0/OM_TruthObservation",
		SensorName:           "24E124707E427318",
	}
	pt := time.Now().UTC().Add(-time.Hour)
	obs := []canonical.Observation{{
		ThingVendorNativeID: "10",
		ObservedProperty:    "motion",
		PhenomenonTime:      pt,
		ResultTime:          pt,
		ResultRaw:           json.RawMessage("true"), // non-numeric
		RawObservationID:    "17:9",
	}}

	res, err := proc.ProcessStream(context.Background(), "collaborall", 1, 1, thing, ds, time.Time{}, obs)
	if err != nil {
		t.Fatalf("ProcessStream: %v", err)
	}
	if res.Accepted != 1 || res.WrittenPerTarget["test"] != 1 {
		t.Fatalf("expected 1 accepted+written, got %+v", res)
	}

	// Entity names carry the CF_ prefix and the source names.
	if got := namesFrom(ff.bodiesFor("/Things")); !contains(got, "CF_Thing A") {
		t.Fatalf("Thing name: %v", got)
	}
	if got := namesFrom(ff.bodiesFor("/ObservedProperties")); !contains(got, "CF_motion") {
		t.Fatalf("ObservedProperty name: %v", got)
	}
	if got := namesFrom(ff.bodiesFor("/Sensors")); !contains(got, "CF_24E124707E427318") {
		t.Fatalf("Sensor name: %v", got)
	}

	// The observation POST carries the verbatim boolean result.
	obsBodies := ff.bodiesFor("/Observations")
	if len(obsBodies) != 1 || !strings.Contains(string(obsBodies[0]), `"result":true`) {
		t.Fatalf("observation result not verbatim: %s", obsBodies)
	}
	if store.written != 1 {
		t.Fatalf("write log rows = %d", store.written)
	}
}

// The server-assigned Thing and Datastream ids must be written back to the
// state store, so a local row can be joined to the FROST entity it created
// without re-resolving it by name.
func TestProcessStreamRecordsSTAEntityIDs(t *testing.T) {
	ff := newFakeFROST()
	defer ff.server.Close()

	mapper := oms.New(oms.DefaultConfig())
	client := frost.NewClient(ff.server.URL, frost.Auth{}, false, 5*time.Second)
	writer := frost.NewWriter(frost.Target{Label: "test", Client: client}, slog.New(slog.DiscardHandler))
	store := &fakeStore{}
	proc := New(mapper, []*frost.Writer{writer}, store, validator.DefaultConfig(), slog.New(slog.DiscardHandler))

	thing := canonical.Thing{VendorID: "sulo", VendorNativeID: "514419", Location: canonical.Coord{Lon: 2.73, Lat: 51.14}}
	ds := canonical.Datastream{
		ThingVendorNativeID: "514419",
		ObservedProperty:    canonical.FillLevel,
		Unit:                canonical.Percent,
	}
	pt := time.Now().UTC().Add(-time.Hour)
	obs := []canonical.Observation{{
		ThingVendorNativeID: "514419",
		ObservedProperty:    canonical.FillLevel,
		PhenomenonTime:      pt,
		ResultTime:          pt,
		Result:              42,
		RawObservationID:    "514419@1",
	}}

	const localThingID, localDSID = 77, 88
	if _, err := proc.ProcessStream(context.Background(), "sulo", localThingID, localDSID, thing, ds, time.Time{}, obs); err != nil {
		t.Fatalf("ProcessStream: %v", err)
	}

	staThing, ok := store.staThingIDs[localThingID]
	if !ok || staThing == 0 {
		t.Fatalf("sta thing id not recorded for local id %d: %v", localThingID, store.staThingIDs)
	}
	staDS, ok := store.staDSIDs[localDSID]
	if !ok || staDS == 0 {
		t.Fatalf("sta datastream id not recorded for local id %d: %v", localDSID, store.staDSIDs)
	}
	// They address different entities, so the fake's id sequence must have
	// handed out different values — a sign we are not recording one id twice.
	if staThing == staDS {
		t.Errorf("Thing and Datastream recorded the same @iot.id (%d)", staThing)
	}
}

// A stream that yields no accepted observations creates no entities, so
// there is nothing to cross-reference and the setters must not be called.
func TestProcessStreamRecordsNoIDsWhenNothingAccepted(t *testing.T) {
	ff := newFakeFROST()
	defer ff.server.Close()

	mapper := oms.New(oms.DefaultConfig())
	client := frost.NewClient(ff.server.URL, frost.Auth{}, false, 5*time.Second)
	writer := frost.NewWriter(frost.Target{Label: "test", Client: client}, slog.New(slog.DiscardHandler))
	store := &fakeStore{}
	proc := New(mapper, []*frost.Writer{writer}, store, validator.DefaultConfig(), slog.New(slog.DiscardHandler))

	thing := canonical.Thing{VendorID: "sulo", VendorNativeID: "500196"}
	ds := canonical.Datastream{ThingVendorNativeID: "500196", ObservedProperty: canonical.FillLevel, Unit: canonical.Percent}

	res, err := proc.ProcessStream(context.Background(), "sulo", 1, 1, thing, ds, time.Time{}, nil)
	if err != nil {
		t.Fatalf("ProcessStream: %v", err)
	}
	if res.Accepted != 0 {
		t.Fatalf("expected nothing accepted, got %+v", res)
	}
	if len(store.staThingIDs) != 0 || len(store.staDSIDs) != 0 {
		t.Errorf("no entities were created, so no ids should be recorded: things=%v ds=%v",
			store.staThingIDs, store.staDSIDs)
	}
}
