// Package source reads Sensors/Datastreams/Observations from a Collaborall
// FROST-Server and maps them into the translation layer's canonical types
// for faithful passthrough replication. It is used by cmd/collaborall-reader;
// the translation-layer service never imports it (the service only decodes
// the resulting Envelope).
package source

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/frost"
)

// watchSet is a membership test over Sensor identities. A Sensor matches if
// its name OR its stringified @iot.id is in the set. An empty set matches
// every Sensor (watch all).
type watchSet struct {
	m map[string]struct{}
}

func newWatchSet(tokens []string) watchSet {
	m := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t != "" {
			m[t] = struct{}{}
		}
	}
	return watchSet{m: m}
}

// all reports whether the set watches everything (empty = watch all).
func (w watchSet) all() bool { return len(w.m) == 0 }

// matches reports whether a Sensor is watched.
func (w watchSet) matches(s *frost.SensorDTO) bool {
	if w.all() {
		return true
	}
	if s == nil {
		return false
	}
	if _, ok := w.m[s.Name]; ok {
		return true
	}
	_, ok := w.m[strconv.FormatInt(s.IotID, 10)]
	return ok
}

// coordFromLocations returns the (lon, lat) of the first expanded Location
// with a Point geometry. STA/GeoJSON order is [lon, lat] — same as
// canonical.Coord — so no swap is needed. ok=false when absent.
func coordFromLocations(locs []frost.LocationDTO) (canonical.Coord, bool) {
	for _, l := range locs {
		if len(l.Location.Coordinates) >= 2 {
			return canonical.Coord{Lon: l.Location.Coordinates[0], Lat: l.Location.Coordinates[1]}, true
		}
	}
	return canonical.Coord{}, false
}

// stringifyProperties flattens a source properties object into the
// map[string]string canonical.Thing carries. Non-string values are
// JSON-encoded so nothing is lost.
func stringifyProperties(in map[string]any) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch s := v.(type) {
		case string:
			out[k] = s
		case nil:
			out[k] = ""
		default:
			if b, err := json.Marshal(v); err == nil {
				out[k] = string(b)
			}
		}
	}
	return out
}

// sanitizeUnit blanks the source's placeholder unit fields ("...") so they
// don't leak into the destination FROST.
func sanitizeUnit(s string) string {
	if strings.TrimSpace(s) == "..." {
		return ""
	}
	return s
}

// parseSTATime parses an STA time value that may be an instant
// ("2026-01-02T03:04:05Z") or an interval ("start/end"), taking the start.
// Returns the zero time and ok=false when unparseable/empty.
func parseSTATime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// numericResult decodes an STA result as a float64 when it is a JSON number.
// Non-numeric results (bool/string/array/object) yield ok=false; the raw
// value is still replicated verbatim via canonical.Observation.ResultRaw.
func numericResult(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, false
	}
	return f, true
}
