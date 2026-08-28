package frost

// Read-side of the FROST client: generic collection GETs with $expand /
// $filter / @iot.nextLink pagination, plus DTO structs that carry @iot.id
// and expanded navigation properties. Consumed by the collaborall source
// reader (cmd/collaborall-reader), NOT by the translation-layer service's
// write path — that only ever POSTs.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ThingDTO is a source STA Thing with (optionally $expand'd) Locations.
type ThingDTO struct {
	IotID       int64          `json:"@iot.id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Properties  map[string]any `json:"properties"`
	Locations   []LocationDTO  `json:"Locations"`
}

// LocationDTO is a source STA Location.
type LocationDTO struct {
	IotID    int64        `json:"@iot.id"`
	Name     string       `json:"name"`
	Location GeoJSONPoint `json:"location"`
}

// SensorDTO is a source STA Sensor. Metadata is kept raw because FROST
// builds vary between a JSON object and a plain string.
type SensorDTO struct {
	IotID    int64           `json:"@iot.id"`
	Name     string          `json:"name"`
	Metadata json.RawMessage `json:"metadata"`
}

// ObservedPropertyDTO is a source STA ObservedProperty.
type ObservedPropertyDTO struct {
	IotID      int64  `json:"@iot.id"`
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

// DatastreamDTO is a source STA Datastream with (optionally $expand'd)
// Sensor and ObservedProperty.
type DatastreamDTO struct {
	IotID             int64                `json:"@iot.id"`
	Name              string               `json:"name"`
	Description       string               `json:"description"`
	UnitOfMeasurement UnitOfMeasurement    `json:"unitOfMeasurement"`
	ObservationType   string               `json:"observationType"`
	Properties        map[string]any       `json:"properties"`
	Sensor            *SensorDTO           `json:"Sensor"`
	ObservedProperty  *ObservedPropertyDTO `json:"ObservedProperty"`
}

// ObservationDTO is a source STA Observation. Result is kept raw because
// STA permits number, string, boolean, array, or object results.
type ObservationDTO struct {
	IotID          int64           `json:"@iot.id"`
	PhenomenonTime string          `json:"phenomenonTime"`
	ResultTime     string          `json:"resultTime"`
	Result         json.RawMessage `json:"result"`
}

// GetCollection issues GET <BaseURL>/<path>?<q> and returns the raw items
// of the STA response envelope plus the @iot.nextLink (empty when there is
// no next page). Status is classified like the write methods: >=500 and
// transport errors are transient, other >=400 are permanent.
func (c *Client) GetCollection(ctx context.Context, path string, q url.Values) (items []json.RawMessage, nextLink string, err error) {
	u := c.BaseURL + ensureLeadingSlash(path)
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	return c.getCollectionURL(ctx, u)
}

// GetAll pages through a collection following @iot.nextLink until it is
// exhausted, or until at least cap items are collected (cap<=0 = no cap).
// The result may exceed cap by up to one page; callers that need an exact
// bound should slice the return value.
func (c *Client) GetAll(ctx context.Context, path string, q url.Values, cap int) ([]json.RawMessage, error) {
	items, next, err := c.GetCollection(ctx, path, q)
	if err != nil {
		return nil, err
	}
	all := items
	for next != "" {
		if cap > 0 && len(all) >= cap {
			break
		}
		items, next, err = c.getCollectionURL(ctx, next)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return all, nil
}

// getCollectionURL GETs an absolute (or server-relative) STA collection URL
// and decodes the { value, @iot.nextLink } envelope.
func (c *Client) getCollectionURL(ctx context.Context, fullURL string) (items []json.RawMessage, nextLink string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("frost: build get request: %w", err)
	}
	c.applyAuth(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", NewTransientHTTPError(0, fmt.Errorf("frost: GET %s: %w", fullURL, err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 500 {
		return nil, "", NewTransientHTTPError(resp.StatusCode, fmt.Errorf("frost: GET %s returned %d", fullURL, resp.StatusCode))
	}
	if resp.StatusCode >= 400 {
		return nil, "", NewPermanentHTTPError(resp.StatusCode, fmt.Errorf("frost: GET %s returned %d", fullURL, resp.StatusCode))
	}

	var out struct {
		Value    []json.RawMessage `json:"value"`
		NextLink string            `json:"@iot.nextLink"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", NewTransientHTTPError(resp.StatusCode, fmt.Errorf("frost: decode collection: %w", err))
	}

	return out.Value, c.resolveNextLink(out.NextLink), nil
}

// resolveNextLink normalises FROST's @iot.nextLink, which may be absolute
// or server-relative (e.g. "Things?$skip=100"), into a fetchable URL.
func (c *Client) resolveNextLink(next string) string {
	if next == "" {
		return ""
	}
	if strings.HasPrefix(next, "http://") || strings.HasPrefix(next, "https://") {
		return next
	}
	return c.BaseURL + "/" + strings.TrimLeft(next, "/")
}
