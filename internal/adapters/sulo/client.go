package sulo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters"
)

// Client defaults and hard limits.
const (
	// DefaultHTTPTimeout bounds a single REEN HTTP request.
	DefaultHTTPTimeout = 30 * time.Second

	// DefaultPageSize is the per-request "limit" used when paging. REEN
	// defaults to 100 instances per response; asking for more cuts the
	// number of round-trips on customers with many container slots.
	DefaultPageSize = 500

	// MaxPageSize is REEN's documented hard ceiling on the number of
	// instances a single request may return.
	MaxPageSize = 30000

	// maxPages bounds a single paged call so a server that keeps returning
	// full pages (e.g. one that ignores "offset") cannot spin forever.
	maxPages = 200

	// maxErrorBodyBytes caps how much of an error response we read into an
	// error message.
	maxErrorBodyBytes = 4 << 10
)

// client is a session-authenticated REEN REST client.
//
// REEN issues no static API key: POST /session trades username+password
// for a token sent as "X-Token" on every later request. The token is a JWT
// carrying an "exp" claim (14 days, as issued in August 2026), but the
// client deliberately does not parse or track it: expiry is the server's to
// decide and may change, and a token can be revoked before it expires. So
// the client treats HTTP 401 as "token stale", re-logs in, and retries the
// request once — which is correct whatever the lifetime turns out to be.
//
// Re-login is single-flight. Datastreams are polled concurrently by the
// scheduler, so many goroutines can hit a stale token at the same instant.
// session() holds mu across the login round-trip, so latecomers block and
// then reuse the fresh token instead of each forcing their own login. The
// gen counter makes invalidation idempotent: a goroutine that saw a 401 on
// generation N only clears the token if generation N is still current, so
// it cannot throw away a token another goroutine just obtained.
type client struct {
	baseURL    string
	username   string
	password   string
	customerID string
	pageSize   int
	http       *http.Client
	logger     *slog.Logger

	mu    sync.Mutex
	token string
	gen   uint64
}

// newClient returns a client for the given REEN deployment. baseURL may or
// may not already include the "/api/3" version segment.
func newClient(baseURL, username, password, customerID string, timeout time.Duration, pageSize int, logger *slog.Logger) *client {
	if timeout <= 0 {
		timeout = DefaultHTTPTimeout
	}
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &client{
		baseURL:    normalizeBaseURL(baseURL),
		username:   username,
		password:   password,
		customerID: customerID,
		pageSize:   pageSize,
		http:       &http.Client{Timeout: timeout},
		logger:     logger,
	}
}

// normalizeBaseURL trims trailing slashes and appends the API version
// segment when the caller left it off, so SULO_API_BASE_URL accepts both
// "https://api.reen.com" and "https://api.reen.com/api/3".
func normalizeBaseURL(raw string) string {
	b := strings.TrimRight(strings.TrimSpace(raw), "/")
	if b == "" {
		return ""
	}
	if strings.HasSuffix(b, APIVersionPath) {
		return b
	}
	return b + APIVersionPath
}

// session returns a usable token and the generation it belongs to,
// logging in when no token is cached.
func (c *client) session(ctx context.Context) (string, uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" {
		return c.token, c.gen, nil
	}
	tok, err := c.login(ctx)
	if err != nil {
		return "", 0, err
	}
	c.token = tok
	c.gen++
	return c.token, c.gen, nil
}

// invalidate drops the cached token, but only if it is still the one the
// caller was using — otherwise another goroutine has already refreshed it.
func (c *client) invalidate(gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen == gen {
		c.token = ""
	}
}

// login performs POST /session. Credential rejection is permanent (no
// amount of retrying fixes a wrong password); anything else is classified
// by the usual rules. Callers must hold c.mu.
func (c *client) login(ctx context.Context) (string, error) {
	if c.username == "" || c.password == "" {
		return "", &adapters.PermanentError{Err: errors.New("sulo: SULO_API_USERNAME/SULO_API_PASSWORD not configured")}
	}

	body, err := json.Marshal(map[string]string{
		"username": c.username,
		"password": c.password,
	})
	if err != nil {
		return "", &adapters.PermanentError{Err: fmt.Errorf("sulo: encode session request: %w", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/session", bytes.NewReader(body))
	if err != nil {
		return "", &adapters.PermanentError{Err: fmt.Errorf("sulo: build session request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	c.applyCustomerScope(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", &adapters.TransientError{Err: fmt.Errorf("sulo: session request: %w", err)}
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", &adapters.TransientError{Err: fmt.Errorf("sulo: read session response: %w", err)}
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", &adapters.PermanentError{
			Err: fmt.Errorf("sulo: session rejected (HTTP %d)%s: check SULO_API_USERNAME/SULO_API_PASSWORD and that the account has API rights",
				resp.StatusCode, describeBody(raw)),
		}
	}
	if err := classifyStatus("/session", resp.StatusCode, resp.Header, raw); err != nil {
		return "", err
	}

	var sr sessionResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return "", &adapters.TransientError{Err: fmt.Errorf("sulo: decode session response: %w", err)}
	}
	if sr.Session.Token == "" {
		return "", &adapters.PermanentError{Err: errors.New("sulo: session response carried no token")}
	}

	c.logger.Info("sulo: session established",
		slog.Int64("customer", sr.Session.Customer),
		slog.Int64("user", sr.Session.User),
	)
	return sr.Session.Token, nil
}

// get issues an authenticated GET and decodes the JSON body into out.
// A 401 triggers one re-login-and-retry; a second 401 is permanent.
func (c *client) get(ctx context.Context, path string, q url.Values, out any) error {
	// Two attempts: the first may burn a stale token, the second uses a
	// freshly minted one.
	for attempt := 0; ; attempt++ {
		tok, gen, err := c.session(ctx)
		if err != nil {
			return err
		}

		status, header, raw, err := c.do(ctx, path, q, tok)
		if err != nil {
			return err
		}

		if status == http.StatusUnauthorized && attempt == 0 {
			c.invalidate(gen)
			c.logger.Debug("sulo: token rejected, re-authenticating", slog.String("path", path))
			continue
		}
		if err := classifyStatus(path, status, header, raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, out); err != nil {
			// A body we cannot parse is treated as transient per the error
			// classification table in F2 (JSON parse error → retry).
			return &adapters.TransientError{Err: fmt.Errorf("sulo: decode %s response: %w", path, err)}
		}
		return nil
	}
}

// do performs one GET and returns the raw status, headers and body.
// Only transport failures come back as an error; HTTP status handling is
// the caller's job so it can special-case 401.
func (c *client) do(ctx context.Context, path string, q url.Values, token string) (int, http.Header, []byte, error) {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, nil, &adapters.PermanentError{Err: fmt.Errorf("sulo: build request for %s: %w", path, err)}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Token", token)
	c.applyCustomerScope(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, &adapters.TransientError{Err: fmt.Errorf("sulo: GET %s: %w", path, err)}
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, &adapters.TransientError{Err: fmt.Errorf("sulo: read %s response: %w", path, err)}
	}
	return resp.StatusCode, resp.Header, raw, nil
}

// applyCustomerScope sets the X-Customer header when a customer scope is
// configured. Required only for user accounts with rights over multiple
// customer accounts; otherwise REEN defaults to the user's own customer.
func (c *client) applyCustomerScope(req *http.Request) {
	if c.customerID != "" {
		req.Header.Set("X-Customer", c.customerID)
	}
}

// paged issues GET path repeatedly with limit/offset until decode reports
// a short page, total reaches max, or the page cap trips. decode receives
// each raw page body and returns how many instances it contained.
//
// max <= 0 means "no caller-imposed cap" — paging then runs until the
// server returns a short page.
func (c *client) paged(ctx context.Context, path string, base url.Values, max int, decode func(page []byte) (int, error)) error {
	total := 0
	for range maxPages {
		size := c.pageSize
		if max > 0 {
			remaining := max - total
			if remaining <= 0 {
				return nil
			}
			if remaining < size {
				size = remaining
			}
		}

		q := cloneValues(base)
		q.Set("limit", strconv.Itoa(size))
		if total > 0 {
			q.Set("offset", strconv.Itoa(total))
		}

		var raw json.RawMessage
		if err := c.get(ctx, path, q, &raw); err != nil {
			return err
		}

		n, err := decode(raw)
		if err != nil {
			return err
		}
		total += n

		// A page shorter than requested means we reached the end. n == 0
		// also stops us, which is what protects against a server that
		// ignores "offset" and keeps replaying the same rows.
		if n < size {
			return nil
		}
	}
	c.logger.Warn("sulo: page cap reached, results truncated",
		slog.String("path", path),
		slog.Int("pages", maxPages),
		slog.Int("instances", total),
	)
	return nil
}

// classifyStatus maps a REEN HTTP status onto the adapter error contract
// (F2). nil means the response is a success.
//
//	401/403        → permanent (bad credential or insufficient rights)
//	404            → permanent (resource genuinely gone)
//	429            → transient, honoring Retry-After
//	other 4xx      → permanent (a malformed request will not fix itself)
//	5xx            → transient
func classifyStatus(path string, status int, header http.Header, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}

	detail := describeBody(body)
	switch {
	case status == http.StatusTooManyRequests:
		return &adapters.TransientError{
			Err:        fmt.Errorf("sulo: %s rate limited (HTTP 429)%s", path, detail),
			RetryAfter: adapters.ClampRetryAfter(parseRetryAfter(header.Get("Retry-After"))),
		}
	case status >= 500:
		return &adapters.TransientError{Err: fmt.Errorf("sulo: %s server error (HTTP %d)%s", path, status, detail)}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &adapters.PermanentError{Err: fmt.Errorf("sulo: %s not authorized (HTTP %d)%s", path, status, detail)}
	case status == http.StatusNotFound:
		return &adapters.PermanentError{Err: fmt.Errorf("sulo: %s not found (HTTP 404)%s", path, detail)}
	default:
		return &adapters.PermanentError{Err: fmt.Errorf("sulo: %s failed (HTTP %d)%s", path, status, detail)}
	}
}

// parseRetryAfter reads a Retry-After header in either supported form:
// delay-seconds, or an HTTP date. Returns 0 when absent or unparseable.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// describeBody renders a REEN error body as a short parenthetical suffix
// for an error message, preferring the structured message field over raw
// JSON. Returns "" when there is nothing useful to show.
func describeBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var er errorResponse
	if err := json.Unmarshal(body, &er); err == nil {
		if msg := er.message(); msg != "" {
			return ": " + truncate(msg, 200)
		}
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return ""
	}
	if len(s) > maxErrorBodyBytes {
		s = s[:maxErrorBodyBytes]
	}
	return ": " + truncate(strings.Join(strings.Fields(s), " "), 200)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// cloneValues copies base so per-page mutations never leak between pages.
func cloneValues(base url.Values) url.Values {
	q := make(url.Values, len(base)+2)
	for k, vs := range base {
		q[k] = append([]string(nil), vs...)
	}
	return q
}
