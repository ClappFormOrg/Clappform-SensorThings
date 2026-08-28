package collaborall

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters"
)

// PushAdapter is the in-service half of the Collaborall integration. It
// authenticates and decodes batches posted by the reader binary to
// POST /ingest/collaborall. It performs no FROST reads — the reader owns
// all source-specific logic.
type PushAdapter struct {
	secret string
}

var _ adapters.PushAdapter = (*PushAdapter)(nil)

// NewPush returns a PushAdapter that accepts requests bearing secret.
// The secret must be non-empty; an empty secret rejects every request.
func NewPush(secret string) *PushAdapter {
	return &PushAdapter{secret: secret}
}

// VendorID returns the {vendorID} path segment: "collaborall".
func (a *PushAdapter) VendorID() string { return VendorID }

// Authenticate requires an "Authorization: Bearer <secret>" header,
// compared in constant time. A mismatch (or an unconfigured secret) is a
// PermanentError so the listener returns 401.
func (a *PushAdapter) Authenticate(r *http.Request, _ []byte) error {
	if a.secret == "" {
		return &adapters.PermanentError{Err: errors.New("collaborall: ingest secret not configured")}
	}
	got := r.Header.Get("Authorization")
	want := "Bearer " + a.secret
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return &adapters.PermanentError{Err: errors.New("collaborall: invalid ingest secret")}
	}
	return nil
}

// DecodePush parses the JSON Envelope into a canonical DecodedBatch. A
// malformed body is a PermanentError so the listener returns 400.
func (a *PushAdapter) DecodePush(_ context.Context, body []byte) (adapters.DecodedBatch, error) {
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return adapters.DecodedBatch{}, &adapters.PermanentError{Err: fmt.Errorf("collaborall: decode envelope: %w", err)}
	}
	return env.ToBatch(), nil
}
