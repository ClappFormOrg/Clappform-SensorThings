package frost

import "fmt"

// HTTPError is an HTTP-status-bearing error returned by Client. Use
// errors.As to inspect Status; use IsTransient / IsPermanent for retry
// classification.
type HTTPError struct {
	Status    int
	Err       error
	Transient bool
}

func (e *HTTPError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("frost http error: %v", e.Err)
	}
	return fmt.Sprintf("frost http %d: %v", e.Status, e.Err)
}

func (e *HTTPError) Unwrap() error { return e.Err }

// NewTransientHTTPError wraps a retryable HTTP failure (network, 5xx,
// or unparseable but recoverable response).
func NewTransientHTTPError(status int, err error) *HTTPError {
	return &HTTPError{Status: status, Err: err, Transient: true}
}

// NewPermanentHTTPError wraps a non-retryable HTTP failure (4xx other
// than 409 or 429 — those have dedicated types).
func NewPermanentHTTPError(status int, err error) *HTTPError {
	return &HTTPError{Status: status, Err: err, Transient: false}
}

// ConflictError represents a 409 from FROST. The caller decides
// semantics: for Observation POST, treat as success (idempotency hit);
// for entity upsert, treat as data-quality (concurrent creation
// race — meter and continue).
type ConflictError struct {
	Status int
	Body   string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("frost conflict (409): %s", e.Body)
}

// DuplicateError signals that a name-filter lookup returned more than
// one match. Caller should meter sta_duplicate_entity_total and
// continue with the lowest @iot.id (already returned alongside this
// error).
type DuplicateError struct {
	Entity string
	Name   string
	Count  int
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("frost: %d entities matched name=%q for %s; using lowest @iot.id",
		e.Count, e.Name, e.Entity)
}

// IsTransient reports whether err warrants F4 retry.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if he, ok := err.(*HTTPError); ok {
		return he.Transient
	}
	return false
}

// IsPermanent reports whether err is a hard 4xx (excluding 409/429).
func IsPermanent(err error) bool {
	if err == nil {
		return false
	}
	if he, ok := err.(*HTTPError); ok {
		return !he.Transient
	}
	return false
}
