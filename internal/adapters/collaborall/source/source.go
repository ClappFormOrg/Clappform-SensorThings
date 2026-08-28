package source

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters/collaborall"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/frost"
)

// DefaultObservationPageLimit bounds how many observations a single
// FetchObservations call returns per stream per cycle.
const DefaultObservationPageLimit = 1000

// Config configures a SourceReader.
type Config struct {
	// WatchSensors lists source Sensors to replicate, by name or @iot.id.
	// Empty means replicate every Sensor.
	WatchSensors []string

	// ObservationPageLimit caps observations fetched per stream per cycle.
	// Non-positive uses DefaultObservationPageLimit.
	ObservationPageLimit int
}

// SourceReader reads and maps a Collaborall FROST-Server into canonical
// types for faithful passthrough. Safe for sequential use by the reader
// binary.
type SourceReader struct {
	client    *frost.Client
	watch     watchSet
	pageLimit int
	logger    *slog.Logger
}

// New returns a SourceReader over client.
func New(client *frost.Client, cfg Config, logger *slog.Logger) *SourceReader {
	limit := cfg.ObservationPageLimit
	if limit <= 0 {
		limit = DefaultObservationPageLimit
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SourceReader{
		client:    client,
		watch:     newWatchSet(cfg.WatchSensors),
		pageLimit: limit,
		logger:    logger,
	}
}

// DiscoveredStream links a canonical Thing + Datastream to the source ids
// needed to fetch its observations and track a per-stream cursor.
type DiscoveredStream struct {
	Thing              canonical.Thing
	Datastream         canonical.Datastream
	SourceThingID      int64
	SourceDatastreamID int64
}

// Discover lists every watched (Thing, Datastream) on the source, mapped to
// canonical types for passthrough. Datastreams whose Sensor is not watched
// are skipped.
func (r *SourceReader) Discover(ctx context.Context) ([]DiscoveredStream, error) {
	rawThings, err := r.client.GetAll(ctx, "/Things", expandQuery("Locations"), 0)
	if err != nil {
		return nil, fmt.Errorf("collaborall: list things: %w", err)
	}

	var out []DiscoveredStream
	for _, rt := range rawThings {
		var td frost.ThingDTO
		if err := json.Unmarshal(rt, &td); err != nil {
			r.logger.Warn("collaborall: skip undecodable thing", slog.Any("err", err))
			continue
		}
		ct := r.toCanonicalThing(td)

		path := fmt.Sprintf("/Things(%d)/Datastreams", td.IotID)
		rawDS, err := r.client.GetAll(ctx, path, expandQuery("Sensor", "ObservedProperty"), 0)
		if err != nil {
			return nil, fmt.Errorf("collaborall: list datastreams for thing %d: %w", td.IotID, err)
		}

		for _, rd := range rawDS {
			var dd frost.DatastreamDTO
			if err := json.Unmarshal(rd, &dd); err != nil {
				r.logger.Warn("collaborall: skip undecodable datastream", slog.Any("err", err))
				continue
			}
			if !r.watch.matches(dd.Sensor) {
				continue
			}
			out = append(out, DiscoveredStream{
				Thing:              ct,
				Datastream:         toCanonicalDatastream(ct.VendorNativeID, dd),
				SourceThingID:      td.IotID,
				SourceDatastreamID: dd.IotID,
			})
		}
	}
	return out, nil
}

// FetchObservations returns up to limit observations for the stream with a
// phenomenonTime strictly after since (ascending). Results are replicated
// verbatim (numeric or not); untimestamped or empty-result rows are skipped.
// limit<=0 uses the reader's page limit.
func (r *SourceReader) FetchObservations(ctx context.Context, ds DiscoveredStream, since time.Time, limit int) ([]canonical.Observation, error) {
	if limit <= 0 {
		limit = r.pageLimit
	}
	q := url.Values{}
	q.Set("$orderby", "phenomenonTime asc")
	q.Set("$top", strconv.Itoa(limit))
	if !since.IsZero() {
		q.Set("$filter", fmt.Sprintf("phenomenonTime gt %s", since.UTC().Format(time.RFC3339Nano)))
	}

	path := fmt.Sprintf("/Datastreams(%d)/Observations", ds.SourceDatastreamID)
	raws, err := r.client.GetAll(ctx, path, q, limit)
	if err != nil {
		return nil, fmt.Errorf("collaborall: fetch observations for datastream %d: %w", ds.SourceDatastreamID, err)
	}
	if len(raws) > limit {
		raws = raws[:limit]
	}

	out := make([]canonical.Observation, 0, len(raws))
	for _, raw := range raws {
		var od frost.ObservationDTO
		if err := json.Unmarshal(raw, &od); err != nil {
			r.logger.Warn("collaborall: skip undecodable observation", slog.Any("err", err))
			continue
		}
		pt, ok := parseSTATime(od.PhenomenonTime)
		if !ok {
			continue
		}
		if len(od.Result) == 0 {
			continue
		}
		rt, ok := parseSTATime(od.ResultTime)
		if !ok {
			rt = pt
		}
		// Replicate the result verbatim; also parse a numeric value for the
		// write log (0 when non-numeric).
		numeric, _ := numericResult(od.Result)
		out = append(out, canonical.Observation{
			ThingVendorNativeID: ds.Thing.VendorNativeID,
			ObservedProperty:    ds.Datastream.ObservedProperty,
			PhenomenonTime:      pt,
			ResultTime:          rt,
			Result:              numeric,
			ResultRaw:           append(json.RawMessage(nil), od.Result...),
			RawObservationID:    fmt.Sprintf("%d:%d", ds.SourceDatastreamID, od.IotID),
		})
	}
	return out, nil
}

func (r *SourceReader) toCanonicalThing(td frost.ThingDTO) canonical.Thing {
	t := canonical.Thing{
		VendorID:       collaborall.VendorID,
		VendorNativeID: strconv.FormatInt(td.IotID, 10),
		Name:           td.Name,
		Description:    td.Description,
		Properties:     stringifyProperties(td.Properties),
	}
	if c, ok := coordFromLocations(td.Locations); ok {
		t.Location = c
	}
	return t
}

// toCanonicalDatastream builds a passthrough canonical Datastream. The
// stream key (ObservedProperty) is the source Datastream name, which is
// unique within a Thing; the human ObservedProperty entity is described by
// the ObservedProperty* fields.
func toCanonicalDatastream(vendorNativeID string, dd frost.DatastreamDTO) canonical.Datastream {
	key := dd.Name
	if key == "" {
		key = "ds-" + strconv.FormatInt(dd.IotID, 10)
	}
	d := canonical.Datastream{
		ThingVendorNativeID:    vendorNativeID,
		ObservedProperty:       canonical.ObservedProperty(key),
		SensorMetadata:         sensorMetadata(dd.Sensor),
		ExpectedCadenceSeconds: cadenceSeconds(dd.Properties),

		Name:            dd.Name,
		Description:     dd.Description,
		UnitName:        sanitizeUnit(dd.UnitOfMeasurement.Name),
		UnitSymbol:      sanitizeUnit(dd.UnitOfMeasurement.Symbol),
		UnitDefinition:  sanitizeUnit(dd.UnitOfMeasurement.Definition),
		ObservationType: dd.ObservationType,
	}
	if dd.ObservedProperty != nil {
		d.ObservedPropertyName = dd.ObservedProperty.Name
		d.ObservedPropertyDefinition = dd.ObservedProperty.Definition
	}
	if dd.Sensor != nil {
		d.SensorName = dd.Sensor.Name
	}
	return d
}

// expandQuery builds a $expand query for the given nav properties.
func expandQuery(navs ...string) url.Values {
	q := url.Values{}
	q.Set("$expand", strings.Join(navs, ","))
	return q
}

func sensorMetadata(s *frost.SensorDTO) map[string]string {
	out := map[string]string{}
	if s == nil {
		return out
	}
	out["source_sensor_name"] = s.Name
	out["source_sensor_id"] = strconv.FormatInt(s.IotID, 10)

	var obj map[string]any
	if err := json.Unmarshal(s.Metadata, &obj); err == nil && obj != nil {
		for k, v := range stringifyProperties(obj) {
			out[k] = v
		}
	} else {
		var str string
		if err := json.Unmarshal(s.Metadata, &str); err == nil && str != "" {
			out["metadata"] = str
		}
	}
	return out
}

func cadenceSeconds(props map[string]any) int {
	v, ok := props["expected_cadence_seconds"]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return 0
}
