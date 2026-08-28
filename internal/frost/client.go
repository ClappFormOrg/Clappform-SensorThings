// Package frost implements a thin OGC SensorThings API v1.1 client
// scoped to the entity types the translation layer needs to upsert
// and the Observation POST path. Per ADR-002 we hand-roll the client
// over net/http rather than depend on an external library.
package frost

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Entity names the STA collection paths we touch.
type Entity string

const (
	EntityThings             Entity = "Things"
	EntityLocations          Entity = "Locations"
	EntitySensors            Entity = "Sensors"
	EntityObservedProperties Entity = "ObservedProperties"
	EntityDatastreams        Entity = "Datastreams"
	EntityFeaturesOfInterest Entity = "FeaturesOfInterest"
	EntityObservations       Entity = "Observations"
)

// Auth carries the write credential for a FROST target. FROST-Server
// supports HTTP Basic or Bearer Token (design doc §Security); a Target
// uses exactly one. When BasicUser is set, Basic Auth wins; otherwise a
// non-empty Token is sent as a Bearer header. A zero Auth means no
// credential is attached (anonymous).
type Auth struct {
	Token         string // Bearer token
	BasicUser     string // Basic Auth username
	BasicPassword string // Basic Auth password
}

// Client talks STA HTTP to a single FROST-Server target.
type Client struct {
	BaseURL string
	Auth    Auth
	HTTP    *http.Client
}

// NewClient returns a Client. timeout bounds each HTTP request; a
// non-positive value falls back to 15s. When insecureSkipVerify is true,
// TLS certificate verification is disabled — testbed only, for servers
// with self-signed or hostname-mismatched certificates.
func NewClient(baseURL string, auth Auth, insecureSkipVerify bool, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	httpClient := &http.Client{Timeout: timeout}
	if insecureSkipVerify {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // testbed opt-in (FROST_TLS_INSECURE_SKIP_VERIFY)
		}
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Auth:    auth,
		HTTP:    httpClient,
	}
}

// applyAuth attaches the configured credential to req. Basic Auth takes
// precedence over Bearer. Unlike the earlier Bearer-only path (writes
// only), auth is attached to every request: a Basic-Auth-protected
// FROST-Server rejects the upsert GET probes otherwise, and a write
// credential that also carries read scope is harmless on reads.
func (c *Client) applyAuth(req *http.Request) {
	switch {
	case c.Auth.BasicUser != "":
		req.SetBasicAuth(c.Auth.BasicUser, c.Auth.BasicPassword)
	case c.Auth.Token != "":
		req.Header.Set("Authorization", "Bearer "+c.Auth.Token)
	}
}

// EscapeODataString applies OData v4 string-literal escaping
// (per the upsert-algorithm rule in Part 2 of the design doc):
// every single-quote in the input is doubled. The caller is
// responsible for URL-encoding the wrapping query parameter.
//
// Names are length-checked elsewhere; an empty name is a programming
// error and is returned as the empty string.
func EscapeODataString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// FindByName issues GET /<Entity>?$filter=name eq '<escaped>'
// and returns the @iot.id of the single match, or 0 if no match.
// On more than one match, returns the lowest @iot.id and the
// DuplicateError so the caller can record the data-quality event.
func (c *Client) FindByName(ctx context.Context, entity Entity, name string) (int64, error) {
	if name == "" {
		return 0, errors.New("frost: empty name")
	}
	if len(name) > 255 {
		return 0, fmt.Errorf("frost: name too long (%d > 255)", len(name))
	}

	q := url.Values{}
	q.Set("$filter", fmt.Sprintf("name eq '%s'", EscapeODataString(name)))
	q.Set("$select", "@iot.id")
	q.Set("$top", "10")

	u := fmt.Sprintf("%s/%s?%s", c.BaseURL, entity, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, fmt.Errorf("frost: build find request: %w", err)
	}
	c.applyAuth(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, NewTransientHTTPError(0, fmt.Errorf("frost: GET %s: %w", entity, err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 500 {
		return 0, NewTransientHTTPError(resp.StatusCode, fmt.Errorf("frost: GET %s returned %d", entity, resp.StatusCode))
	}
	if resp.StatusCode >= 400 {
		return 0, NewPermanentHTTPError(resp.StatusCode, fmt.Errorf("frost: GET %s returned %d", entity, resp.StatusCode))
	}

	var out struct {
		Value []struct {
			ID int64 `json:"@iot.id"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, NewTransientHTTPError(resp.StatusCode, fmt.Errorf("frost: decode find response: %w", err))
	}

	switch len(out.Value) {
	case 0:
		return 0, nil
	case 1:
		return out.Value[0].ID, nil
	default:
		// Multiple matches — pick the lowest @iot.id, signal duplicate
		// so caller can meter sta_duplicate_entity_total.
		lowest := out.Value[0].ID
		for _, v := range out.Value[1:] {
			if v.ID < lowest {
				lowest = v.ID
			}
		}
		return lowest, &DuplicateError{Entity: string(entity), Name: name, Count: len(out.Value)}
	}
}

// Post issues POST /<path> with the given JSON body and returns the
// created entity's @iot.id from the Location header. Auth is added
// from c.Auth.
func (c *Client) Post(ctx context.Context, path string, body any) (int64, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("frost: marshal body: %w", err)
	}

	u := fmt.Sprintf("%s%s", c.BaseURL, ensureLeadingSlash(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(buf))
	if err != nil {
		return 0, fmt.Errorf("frost: build post request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.applyAuth(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, NewTransientHTTPError(0, fmt.Errorf("frost: POST %s: %w", path, err))
	}
	defer func() { _ = resp.Body.Close() }()

	body256, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode == http.StatusCreated:
		// OGC STA returns the canonical entity URI in Location; parse @iot.id
		// from the trailing segment.
		loc := resp.Header.Get("Location")
		id, ok := parseIotIDFromLocation(loc)
		if !ok {
			// Some FROST builds return the body with @iot.id; fall back.
			id, ok = parseIotIDFromBody(body256)
		}
		if !ok {
			return 0, NewTransientHTTPError(resp.StatusCode,
				fmt.Errorf("frost: POST %s 201 without parseable @iot.id (Location=%q)", path, loc))
		}
		return id, nil
	case resp.StatusCode == http.StatusConflict:
		// 409 — caller decides whether to treat as success (Observation
		// idempotency) or as data-quality (entity upsert race).
		return 0, &ConflictError{Status: resp.StatusCode, Body: string(body256)}
	case resp.StatusCode >= 500:
		return 0, NewTransientHTTPError(resp.StatusCode,
			fmt.Errorf("frost: POST %s returned %d: %s", path, resp.StatusCode, string(body256)))
	case resp.StatusCode >= 400:
		return 0, NewPermanentHTTPError(resp.StatusCode,
			fmt.Errorf("frost: POST %s returned %d: %s", path, resp.StatusCode, string(body256)))
	default:
		return 0, NewTransientHTTPError(resp.StatusCode,
			fmt.Errorf("frost: POST %s unexpected status %d", path, resp.StatusCode))
	}
}

// ObservationExists checks whether (datastreamID, phenomenonTime) is
// already in FROST. Used as the pre-write idempotency probe.
func (c *Client) ObservationExists(ctx context.Context, datastreamID int64, phenomenonTime time.Time) (bool, error) {
	q := url.Values{}
	q.Set("$filter", fmt.Sprintf("phenomenonTime eq %s", phenomenonTime.UTC().Format(time.RFC3339Nano)))
	q.Set("$select", "@iot.id")
	q.Set("$top", "1")

	u := fmt.Sprintf("%s/Datastreams(%d)/Observations?%s", c.BaseURL, datastreamID, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, fmt.Errorf("frost: build observation-exists request: %w", err)
	}
	c.applyAuth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false, NewTransientHTTPError(0, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 500 {
		return false, NewTransientHTTPError(resp.StatusCode, fmt.Errorf("frost: observation-exists %d", resp.StatusCode))
	}
	if resp.StatusCode >= 400 {
		return false, NewPermanentHTTPError(resp.StatusCode, fmt.Errorf("frost: observation-exists %d", resp.StatusCode))
	}

	var out struct {
		Value []struct {
			ID int64 `json:"@iot.id"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, NewTransientHTTPError(resp.StatusCode, err)
	}
	return len(out.Value) > 0, nil
}

func ensureLeadingSlash(p string) string {
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

func parseIotIDFromLocation(loc string) (int64, bool) {
	// Locations look like ".../Things(42)" — find the trailing "(<n>)".
	open := strings.LastIndex(loc, "(")
	close := strings.LastIndex(loc, ")")
	if open < 0 || close < 0 || close < open {
		return 0, false
	}
	var id int64
	if _, err := fmt.Sscanf(loc[open+1:close], "%d", &id); err != nil {
		return 0, false
	}
	return id, true
}

func parseIotIDFromBody(body []byte) (int64, bool) {
	var out struct {
		ID int64 `json:"@iot.id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.ID == 0 {
		return 0, false
	}
	return out.ID, true
}
