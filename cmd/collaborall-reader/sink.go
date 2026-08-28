package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters/collaborall"
)

// ingestResponse mirrors the push endpoint's JSON response body
// (internal/api/push.go pushResponse).
type ingestResponse struct {
	Accepted          int `json:"accepted"`
	SkippedIdempotent int `json:"skipped_idempotent"`
	Rejected          []struct {
		RawObservationID string `json:"raw_observation_id"`
		Reason           string `json:"reason"`
	} `json:"rejected"`
}

// httpSink POSTs envelopes to the translation-layer push endpoint.
type httpSink struct {
	url    string
	secret string
	client *http.Client
}

// post sends one envelope and returns the decoded response. A non-2xx
// status is an error (the caller leaves the cursor put and retries).
func (s *httpSink) post(ctx context.Context, env collaborall.Envelope) (ingestResponse, error) {
	body, err := json.Marshal(env)
	if err != nil {
		return ingestResponse{}, fmt.Errorf("marshal envelope: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return ingestResponse{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.secret)

	resp, err := s.client.Do(req)
	if err != nil {
		return ingestResponse{}, fmt.Errorf("post to %s: %w", s.url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ingestResponse{}, fmt.Errorf("push endpoint returned %d: %s", resp.StatusCode, string(raw))
	}

	var out ingestResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		// Accepted but undecodable body — treat as success with no counts.
		return ingestResponse{}, nil
	}
	return out, nil
}
