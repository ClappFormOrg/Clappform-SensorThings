package collaborall

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
)

func TestPushAdapterAuthenticate(t *testing.T) {
	a := NewPush("s3cret")

	tests := []struct {
		name   string
		header string
		wantOK bool
	}{
		{"valid", "Bearer s3cret", true},
		{"wrong secret", "Bearer nope", false},
		{"missing bearer prefix", "s3cret", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/ingest/collaborall", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			err := a.Authenticate(r, nil)
			if tc.wantOK && err != nil {
				t.Fatalf("want accept, got %v", err)
			}
			if !tc.wantOK {
				if err == nil {
					t.Fatal("want reject, got accept")
				}
				if !adapters.IsPermanent(err) {
					t.Fatalf("want PermanentError, got %T", err)
				}
			}
		})
	}
}

func TestPushAdapterEmptySecretRejectsAll(t *testing.T) {
	a := NewPush("")
	r := httptest.NewRequest("POST", "/ingest/collaborall", nil)
	r.Header.Set("Authorization", "Bearer ")
	if err := a.Authenticate(r, nil); err == nil {
		t.Fatal("empty configured secret must reject every request")
	}
}

func TestPushAdapterDecodeRoundTrip(t *testing.T) {
	pt := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	batch := adapters.DecodedBatch{
		Things: []adapters.DecodedThing{{
			Thing: canonical.Thing{
				VendorID:       VendorID,
				VendorNativeID: "42",
				Name:           "Source Thing",
				Location:       canonical.Coord{Lon: 5.29, Lat: 51.69},
				Properties:     map[string]string{"area": "test"},
			},
			Datastreams: []canonical.Datastream{{
				ThingVendorNativeID: "42",
				ObservedProperty:    canonical.FillLevel,
				Unit:                canonical.Percent,
				SensorMetadata:      map[string]string{"model": "X"},
			}},
		}},
		Observations: []canonical.Observation{{
			ThingVendorNativeID: "42",
			ObservedProperty:    canonical.FillLevel,
			PhenomenonTime:      pt,
			ResultTime:          pt,
			Result:              73.5,
			RawObservationID:    "7:1",
		}},
	}

	body, err := json.Marshal(FromBatch(batch))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	a := NewPush("x")
	got, err := a.DecodePush(context.Background(), body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(got.Things) != 1 || got.Things[0].Thing.VendorNativeID != "42" {
		t.Fatalf("things round-trip mismatch: %+v", got.Things)
	}
	if len(got.Things[0].Datastreams) != 1 || got.Things[0].Datastreams[0].ObservedProperty != canonical.FillLevel {
		t.Fatalf("datastreams round-trip mismatch: %+v", got.Things[0].Datastreams)
	}
	if len(got.Observations) != 1 {
		t.Fatalf("want 1 observation, got %d", len(got.Observations))
	}
	o := got.Observations[0]
	if o.Result != 73.5 || !o.PhenomenonTime.Equal(pt) || o.RawObservationID != "7:1" {
		t.Fatalf("observation round-trip mismatch: %+v", o)
	}
}

func TestPushAdapterDecodeBadJSON(t *testing.T) {
	a := NewPush("x")
	_, err := a.DecodePush(context.Background(), []byte("{not json"))
	if err == nil {
		t.Fatal("want decode error")
	}
	if !adapters.IsPermanent(err) {
		t.Fatalf("want PermanentError, got %T", err)
	}
}
