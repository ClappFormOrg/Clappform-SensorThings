package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters/collaborall"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters/collaborall/source"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/frost"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

type fakePoster struct {
	envs []collaborall.Envelope
}

func (f *fakePoster) post(_ context.Context, env collaborall.Envelope) (ingestResponse, error) {
	f.envs = append(f.envs, env)
	return ingestResponse{Accepted: len(env.Observations)}, nil
}

func TestChunkEnvelopesSplitsUnderCap(t *testing.T) {
	st := source.DiscoveredStream{
		Thing:              canonical.Thing{VendorID: "collaborall", VendorNativeID: "10", Name: "Thing A"},
		Datastream:         canonical.Datastream{ThingVendorNativeID: "10", ObservedProperty: canonical.FillLevel, Unit: canonical.Percent},
		SourceDatastreamID: 100,
	}
	base := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	var obs []canonical.Observation
	for i := 0; i < 20; i++ {
		obs = append(obs, canonical.Observation{
			ThingVendorNativeID: "10",
			ObservedProperty:    canonical.FillLevel,
			PhenomenonTime:      base.Add(time.Duration(i) * time.Minute),
			ResultTime:          base.Add(time.Duration(i) * time.Minute),
			Result:              float64(i),
			RawObservationID:    fmt.Sprintf("100:%d", i),
		})
	}

	// Tiny cap to force multiple chunks.
	envs := chunkEnvelopes(st, obs, 400)
	if len(envs) < 2 {
		t.Fatalf("want multiple chunks, got %d", len(envs))
	}

	var total int
	for _, e := range envs {
		total += len(e.Observations)
		if len(e.Things) != 1 || e.Things[0].Thing.VendorNativeID != "10" {
			t.Fatalf("each envelope must carry the stream's thing: %+v", e.Things)
		}
		b, _ := json.Marshal(e)
		if len(b) > 400 && len(e.Observations) > 1 {
			t.Fatalf("multi-obs chunk exceeds cap: %d bytes", len(b))
		}
	}
	if total != 20 {
		t.Fatalf("chunks must cover all observations: got %d", total)
	}
}

// filteringSourceServer returns fill-level observations newer than the
// $filter phenomenonTime, so cursor advancement can be exercised.
func filteringSourceServer(t *testing.T) *httptest.Server {
	t.Helper()
	all := []struct {
		id int
		ts string
		r  float64
	}{
		{1, "2026-07-22T10:00:00Z", 10},
		{2, "2026-07-22T10:05:00Z", 20},
		{3, "2026-07-22T10:10:00Z", 30},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Things":
			_, _ = w.Write([]byte(`{"value":[{"@iot.id":10,"name":"A","Locations":[{"location":{"type":"Point","coordinates":[5.29,51.69]}}]}]}`))
		case r.URL.Path == "/Things(10)/Datastreams":
			_, _ = w.Write([]byte(`{"value":[{"@iot.id":100,"name":"ds","unitOfMeasurement":{"symbol":"%"},"Sensor":{"@iot.id":3,"name":"sensor-a"},"ObservedProperty":{"name":"Fill level"}}]}`))
		case strings.HasPrefix(r.URL.Path, "/Datastreams(100)/Observations"):
			filter := r.URL.Query().Get("$filter")
			var sb strings.Builder
			sb.WriteString(`{"value":[`)
			first := true
			for _, o := range all {
				if filter != "" {
					// crude: only include if ts string sorts after the filter's time
					ts := extractFilterTime(filter)
					if o.ts <= ts {
						continue
					}
				}
				if !first {
					sb.WriteString(",")
				}
				first = false
				fmt.Fprintf(&sb, `{"@iot.id":%d,"phenomenonTime":%q,"result":%g}`, o.id, o.ts, o.r)
			}
			sb.WriteString(`]}`)
			_, _ = w.Write([]byte(sb.String()))
		default:
			http.Error(w, "unexpected: "+r.URL.Path, http.StatusNotFound)
		}
	}))
}

func extractFilterTime(filter string) string {
	// filter looks like: phenomenonTime gt 2026-07-22T10:05:00Z
	i := strings.LastIndex(filter, " ")
	if i < 0 {
		return ""
	}
	return filter[i+1:]
}

func TestRunOnceAdvancesCursorAndIsIdempotent(t *testing.T) {
	srv := filteringSourceServer(t)
	defer srv.Close()

	reader := source.New(frost.NewClient(srv.URL, frost.Auth{}, false, 5*time.Second), source.Config{}, nil)
	cursorFile := filepath.Join(t.TempDir(), "cursors.json")
	cursors, err := loadCursorStore(cursorFile)
	if err != nil {
		t.Fatalf("loadCursorStore: %v", err)
	}
	sink := &fakePoster{}
	now := func() time.Time { return time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC) }

	// First cycle: lookback window covers all 3 observations.
	res, err := runOnce(context.Background(), reader, sink, cursors, 6*time.Hour, now, discardLogger())
	if err != nil {
		t.Fatalf("runOnce #1: %v", err)
	}
	if res.Accepted != 3 {
		t.Fatalf("cycle #1 want 3 accepted, got %d", res.Accepted)
	}
	if got := cursors.get("100"); !got.Equal(time.Date(2026, 7, 22, 10, 10, 0, 0, time.UTC)) {
		t.Fatalf("cursor not advanced to max phenomenonTime: %v", got)
	}

	// Second cycle: cursor now excludes all → nothing new posted.
	before := len(sink.envs)
	res2, err := runOnce(context.Background(), reader, sink, cursors, 6*time.Hour, now, discardLogger())
	if err != nil {
		t.Fatalf("runOnce #2: %v", err)
	}
	if res2.Accepted != 0 || len(sink.envs) != before {
		t.Fatalf("cycle #2 should post nothing new: accepted=%d posts_delta=%d", res2.Accepted, len(sink.envs)-before)
	}
}
