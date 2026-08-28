// Package adapters defines the vendor adapter contracts (F2 in the design
// doc). Vendor-specific implementations live in subpackages
// (e.g. adapters/sulo). The rest of the translation layer depends only
// on this package.
//
// An adapter operates in exactly one source mode (ADR-011):
//   - PollAdapter — the layer pulls observations on a schedule (SULO).
//   - PushAdapter — the vendor pushes observations to the layer's
//     /ingest/{vendorID} endpoint; the adapter authenticates and decodes
//     the inbound payload into canonical types.
//
// Both modes feed the same transport-agnostic ingest core.
package adapters

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
)

// PollAdapter is the contract a poll-mode vendor implementation must
// satisfy. The scheduler drives these.
type PollAdapter interface {
	// VendorID returns the stable identifier used in Thing.name templates
	// and state-store rows (e.g. "sulo"). Must be lowercase ASCII.
	VendorID() string

	// ListThings returns every Thing this vendor exposes. Called
	// periodically by the scheduler to pick up newly-provisioned
	// containers.
	ListThings(ctx context.Context) ([]canonical.Thing, error)

	// ListDatastreamsForThing returns the streams for a given Thing.
	// In v1 this is typically a single FillLevel Datastream per
	// container; in Phase 2 it may include vehicle GPS, RFID, etc.
	ListDatastreamsForThing(ctx context.Context, vendorNativeID string) ([]canonical.Datastream, error)

	// FetchObservations pulls observations since the cursor for the
	// given (Thing, ObservedProperty). Adapter is responsible for
	// pagination, rate-limit backoff, and timestamp normalization
	// to UTC.
	FetchObservations(
		ctx context.Context,
		vendorNativeID string,
		op canonical.ObservedProperty,
		since time.Time,
		limit int,
	) ([]canonical.Observation, error)
}

// PushAdapter is the contract a push-mode vendor implementation must
// satisfy (ADR-011). The push HTTP listener dispatches POST
// /ingest/{vendorID} to the matching adapter: Authenticate first, then
// DecodePush. Vendor-specific wire shape and auth scheme are encoded
// entirely inside the adapter; the rest of the layer sees only the
// canonical types in DecodedBatch.
type PushAdapter interface {
	// VendorID returns the stable identifier (lowercase ASCII); it is the
	// {vendorID} path segment on the ingest endpoint.
	VendorID() string

	// Authenticate verifies an inbound request against the per-vendor
	// secret. body is the already-read raw request body (so the adapter
	// can verify an HMAC signature over it). Returns a PermanentError to
	// reject (401); nil to accept.
	Authenticate(r *http.Request, body []byte) error

	// DecodePush parses a verified request body into canonical Things,
	// their Datastreams, and Observations. Returns a PermanentError when
	// the body is undecodable (400).
	DecodePush(ctx context.Context, body []byte) (DecodedBatch, error)
}

// DecodedBatch is the canonical result of decoding one push request.
// Things carry their Datastreams for first-sight registration; the
// Observations reference a (vendorNativeID, observedProperty) that must
// resolve to one of those Datastreams.
type DecodedBatch struct {
	Things       []DecodedThing
	Observations []canonical.Observation
}

// DecodedThing pairs a Thing with the Datastreams declared for it.
type DecodedThing struct {
	Thing       canonical.Thing
	Datastreams []canonical.Datastream
}

// TransientError signals a fault the scheduler should retry per F4.
// Common cases: HTTP 5xx, network error, JSON parse error, HTTP 429
// (with RetryAfter), or any unclassified error.
type TransientError struct {
	Err        error
	RetryAfter time.Duration // honor Retry-After when set; clamped to ≤ 5 min
}

func (e *TransientError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("transient (retry after %s): %v", e.RetryAfter, e.Err)
	}
	return fmt.Sprintf("transient: %v", e.Err)
}

func (e *TransientError) Unwrap() error { return e.Err }

// PermanentError signals a fault that retrying will not fix. The
// scheduler skips the Datastream for this cycle and does not advance
// the cursor. Common cases: HTTP 401/403, HTTP 404 on a known
// Datastream, vendor-confirmed unrecoverable 400.
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string {
	return fmt.Sprintf("permanent: %v", e.Err)
}

func (e *PermanentError) Unwrap() error { return e.Err }

// IsTransient reports whether err should be retried.
func IsTransient(err error) bool {
	var te *TransientError
	return errors.As(err, &te)
}

// IsPermanent reports whether err is non-retryable.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// MaxRetryAfter caps the retry-after hint a vendor can request, per
// the error classification table in F2.
const MaxRetryAfter = 5 * time.Minute

// ClampRetryAfter applies MaxRetryAfter (and rejects negative values).
func ClampRetryAfter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d > MaxRetryAfter {
		return MaxRetryAfter
	}
	return d
}

// Registry holds the adapters registered at startup, partitioned by mode
// (ADR-011). Order is stable across runs (insertion order). A vendor is
// registered in exactly one mode. Empty registry is valid: the scheduler
// and push listener simply do no work.
type Registry struct {
	poll         []PollAdapter
	push         []PushAdapter
	pushByVendor map[string]PushAdapter
	vendors      map[string]string // vendorID → mode, for duplicate detection
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		pushByVendor: map[string]PushAdapter{},
		vendors:      map[string]string{},
	}
}

// RegisterPoll adds a poll-mode adapter. Panics on duplicate VendorID
// across any mode (a programming error, not a runtime condition).
func (r *Registry) RegisterPoll(a PollAdapter) {
	r.claim(a.VendorID(), "poll")
	r.poll = append(r.poll, a)
}

// RegisterPush adds a push-mode adapter. Panics on duplicate VendorID
// across any mode.
func (r *Registry) RegisterPush(a PushAdapter) {
	id := a.VendorID()
	r.claim(id, "push")
	r.push = append(r.push, a)
	r.pushByVendor[id] = a
}

func (r *Registry) claim(vendorID, mode string) {
	if existing, dup := r.vendors[vendorID]; dup {
		panic(fmt.Sprintf("adapter for vendor %q already registered (%s)", vendorID, existing))
	}
	r.vendors[vendorID] = mode
}

// PollAdapters returns every registered poll adapter in insertion order.
func (r *Registry) PollAdapters() []PollAdapter { return r.poll }

// PushAdapters returns every registered push adapter in insertion order.
func (r *Registry) PushAdapters() []PushAdapter { return r.push }

// PushAdapter returns the push adapter for the given VendorID, or nil if
// no push adapter is registered under that id.
func (r *Registry) PushAdapter(vendorID string) PushAdapter { return r.pushByVendor[vendorID] }

// Len returns the total number of registered adapters across both modes.
func (r *Registry) Len() int { return len(r.poll) + len(r.push) }
