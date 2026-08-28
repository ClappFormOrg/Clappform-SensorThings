package frost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestGetCollectionDecodesValue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"value":[{"@iot.id":1,"name":"a"},{"@iot.id":2,"name":"b"}]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, Auth{}, false, 5*time.Second)
	items, next, err := c.GetCollection(context.Background(), "/Things", nil)
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if next != "" {
		t.Fatalf("want no nextLink, got %q", next)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
}

func TestGetAllFollowsNextLink(t *testing.T) {
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("$skip") {
		case "": // page 1
			_, _ = fmt.Fprintf(w, `{"value":[{"@iot.id":1}],"@iot.nextLink":%q}`, base+"/Things?$skip=1")
		case "1": // page 2
			_, _ = fmt.Fprintf(w, `{"value":[{"@iot.id":2}],"@iot.nextLink":%q}`, base+"/Things?$skip=2")
		default: // last page
			_, _ = w.Write([]byte(`{"value":[{"@iot.id":3}]}`))
		}
	}))
	defer srv.Close()
	base = srv.URL

	c := NewClient(srv.URL, Auth{}, false, 5*time.Second)
	items, err := c.GetAll(context.Background(), "/Things", nil, 0)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("want 3 items across pages, got %d", len(items))
	}
}

func TestGetAllRespectsCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always advertise a next page; the cap must stop the loop.
		_, _ = fmt.Fprintf(w, `{"value":[{"@iot.id":1},{"@iot.id":2}],"@iot.nextLink":%q}`, "Things?$skip=99")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, Auth{}, false, 5*time.Second)
	items, err := c.GetAll(context.Background(), "/Things", nil, 2)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("cap should stop after first page (2 items), got %d", len(items))
	}
}

func TestGetCollectionStatusClassification(t *testing.T) {
	tests := []struct {
		status        int
		wantTransient bool
		wantPermanent bool
	}{
		{http.StatusInternalServerError, true, false},
		{http.StatusBadGateway, true, false},
		{http.StatusUnauthorized, false, true},
		{http.StatusNotFound, false, true},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			c := NewClient(srv.URL, Auth{}, false, 5*time.Second)
			_, _, err := c.GetCollection(context.Background(), "/Things", nil)
			if err == nil {
				t.Fatal("want error")
			}
			if IsTransient(err) != tc.wantTransient || IsPermanent(err) != tc.wantPermanent {
				t.Fatalf("status %d: transient=%v permanent=%v", tc.status, IsTransient(err), IsPermanent(err))
			}
		})
	}
}

func TestGetCollectionSendsAuthAndQuery(t *testing.T) {
	var gotAuth, gotFilter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotFilter = r.URL.Query().Get("$filter")
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, Auth{BasicUser: "u", BasicPassword: "p"}, false, 5*time.Second)
	q := url.Values{}
	q.Set("$filter", "phenomenonTime gt 2026-01-01T00:00:00Z")
	if _, _, err := c.GetCollection(context.Background(), "/Observations", q); err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if gotAuth == "" {
		t.Fatal("basic auth not attached to read request")
	}
	if gotFilter == "" {
		t.Fatal("$filter not forwarded")
	}
}

// ensure DTOs decode a representative expanded payload
func TestDatastreamDTODecode(t *testing.T) {
	raw := `{"@iot.id":7,"name":"ds","unitOfMeasurement":{"name":"Percent","symbol":"%","definition":"u"},
	"Sensor":{"@iot.id":3,"name":"sensor-a","metadata":{"model":"M1"}},
	"ObservedProperty":{"name":"Fill level","definition":"d"}}`
	var d DatastreamDTO
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Sensor == nil || d.Sensor.Name != "sensor-a" || d.ObservedProperty == nil || d.UnitOfMeasurement.Symbol != "%" {
		t.Fatalf("unexpected decode: %+v", d)
	}
}
