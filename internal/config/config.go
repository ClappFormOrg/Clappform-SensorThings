// Package config loads runtime configuration from environment variables.
// Variable names match the Implementation Contract in the design doc.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the resolved runtime configuration. Fields use Go-friendly
// types (time.Duration, []string) derived from the raw env strings.
type Config struct {
	// SULO adapter inputs. SULO's sensor platform is served by the REEN
	// CMS REST API v3 (https://api.reen.com/guide/), which authenticates
	// with the same username/password used for the web UI: POST /session
	// exchanges them for a token sent as the X-Token header. There is no
	// static API-key scheme, so no key rotation applies — rotate the
	// password instead.
	//
	// The adapter is registered only when base URL, username and password
	// are all present (see SULOEnabled); leaving them empty starts the
	// service with no SULO adapter, which is valid.
	SULOAPIBaseURL string
	SULOUsername   string
	SULOPassword   string

	// SULOCustomerID sets the X-Customer scope header. Needed only when
	// the REEN account has rights over several customer accounts and the
	// target is not its own.
	SULOCustomerID string

	// SULO tuning. Zero values fall back to the adapter's defaults.
	SULOHTTPTimeout          time.Duration
	SULOPageSize             int           // instances per paged REEN request
	SULOObservationPageLimit int           // fill-level rows per slot per cycle
	SULOMinConfidence        int           // REEN analytics confidence floor
	SULOExpectedCadence      time.Duration // per-Datastream freshness expectation (ADR-008)

	// FROST target(s). Comma-separated list; first entry is the primary
	// fallback FROST, additional entries are dual-write targets (F6).
	FROSTTargets []string

	// FROST write credential. FROST-Server accepts HTTP Basic or Bearer
	// (design doc §Security); one credential is applied to every target
	// in v1 (per-target creds deferred). Basic Auth wins when
	// FROSTBasicAuthUser is set; otherwise FROSTWriteToken is sent as a
	// Bearer token. All empty = anonymous.
	FROSTWriteToken        string
	FROSTBasicAuthUser     string
	FROSTBasicAuthPassword string

	// FROSTInsecureSkipVerify disables TLS certificate verification for
	// FROST (HTTP) and MQTT (wss) connections. TESTBED ONLY — a server
	// with a self-signed or hostname-mismatched cert (e.g. the WBD
	// endpoint) is unreachable otherwise. Never enable in production.
	FROSTInsecureSkipVerify bool

	// FROSTHTTPTimeout bounds each FROST HTTP request (upsert probes and
	// observation POSTs). Raise it when a latency-adding network path
	// (e.g. an intercepting proxy) makes the default too tight.
	FROSTHTTPTimeout time.Duration

	// FROST MQTT write path. When MQTTEnabled, Observation creates are
	// published to MQTTBrokerURL (e.g. wss://sta.wbd-rd.nl/mqtt) instead
	// of HTTP POST; entity upserts still use HTTP. Auth reuses the FROST
	// Basic credential (WBD uses the same Basic Auth on both servers).
	MQTTEnabled     bool
	MQTTBrokerURL   string
	MQTTTopicPrefix string // default "v1.1"
	MQTTQoS         int    // 0/1/2; default 1

	// State store DSN (Postgres connection string).
	StateStoreDSN string

	// Polling and freshness behavior.
	PollInterval             time.Duration
	FreshnessThreshold       time.Duration // global fallback; per-Datastream override is the rule (ADR-008)
	FreshnessAlertWebhookURL string

	// Validation knobs.
	CursorInitLookback time.Duration
	ClockSkewTolerance time.Duration

	// Logging and HTTP.
	LogLevel slog.Level
	HTTPAddr string // cluster-internal admin listener (/healthz, /metrics)

	// Push ingestion (ADR-011). PushHTTPAddr is the public, Ingress-exposed
	// listener for POST /ingest/{vendorID}; empty disables the push server.
	// Never set it equal to HTTPAddr.
	PushHTTPAddr string

	// CollaborallIngestSecret is the shared secret the collaborall push
	// adapter requires on POST /ingest/collaborall (sent by the
	// cmd/collaborall-reader binary as "Authorization: Bearer <secret>").
	// Empty leaves the collaborall push adapter unregistered.
	CollaborallIngestSecret string

	// EntityNamePrefix is prepended to every FROST entity name this layer
	// creates, to mark our data in a FROST-Server shared with other parties
	// and avoid name-based upsert collisions. Empty = no prefix; set e.g.
	// "CF_" for the Collaborall replication run.
	EntityNamePrefix string

	// Operational.
	StateStoreRetentionDays int

	// Dummy adapter (end-to-end validation only — never enable in
	// production). When enabled, a synthetic poll adapter emitting fake
	// fill-level data is registered at startup.
	DummyAdapterEnabled bool
	DummyThingsCount    int
	DummyCadence        time.Duration
}

// Defaults returns Config with the Implementation-Contract defaults
// applied for every field that has one.
func Defaults() Config {
	return Config{
		PollInterval:            15 * time.Minute,
		FreshnessThreshold:      6 * time.Hour,
		CursorInitLookback:      1 * time.Hour,
		ClockSkewTolerance:      5 * time.Minute,
		LogLevel:                slog.LevelInfo,
		HTTPAddr:                ":8080",
		StateStoreRetentionDays: 90,
	}
}

// Load reads env vars and returns a populated Config. Errors are
// joined so callers see every missing or malformed value at once.
func Load() (Config, error) {
	c := Defaults()
	var errs []error

	c.SULOAPIBaseURL = getenv("SULO_API_BASE_URL", "")
	c.SULOUsername = getenv("SULO_API_USERNAME", "")
	c.SULOPassword = getenv("SULO_API_PASSWORD", "")
	c.SULOCustomerID = getenv("SULO_CUSTOMER_ID", "")

	for _, f := range []struct {
		key    string
		min    int
		assign func(int)
	}{
		{"SULO_HTTP_TIMEOUT_SECONDS", 1, func(n int) { c.SULOHTTPTimeout = time.Duration(n) * time.Second }},
		{"SULO_PAGE_SIZE", 1, func(n int) { c.SULOPageSize = n }},
		{"SULO_OBSERVATION_PAGE_LIMIT", 1, func(n int) { c.SULOObservationPageLimit = n }},
		{"SULO_MIN_CONFIDENCE", 0, func(n int) { c.SULOMinConfidence = n }},
		{"SULO_EXPECTED_CADENCE_SECONDS", 1, func(n int) { c.SULOExpectedCadence = time.Duration(n) * time.Second }},
	} {
		n, err := intenv(f.key, f.min)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if n != nil {
			f.assign(*n)
		}
	}

	// Partial SULO credentials are an operator mistake, not an opt-out:
	// fail loudly rather than silently starting with no SULO adapter.
	if n := countSet(c.SULOAPIBaseURL, c.SULOUsername, c.SULOPassword); n != 0 && n != 3 {
		errs = append(errs, errors.New(
			"SULO_API_BASE_URL, SULO_API_USERNAME and SULO_API_PASSWORD must be set together (REEN authenticates with username/password via POST /session), or all left empty to disable the SULO adapter"))
	}

	targets := getenv("FROST_TARGETS", "")
	if targets != "" {
		for _, t := range strings.Split(targets, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				c.FROSTTargets = append(c.FROSTTargets, t)
			}
		}
	}

	c.FROSTWriteToken = getenv("FROST_WRITE_TOKEN", "")
	c.FROSTBasicAuthUser = getenv("FROST_BASIC_AUTH_USER", "")
	c.FROSTBasicAuthPassword = getenv("FROST_BASIC_AUTH_PASSWORD", "")
	c.FROSTInsecureSkipVerify = boolenv("FROST_TLS_INSECURE_SKIP_VERIFY", false)

	c.FROSTHTTPTimeout = 15 * time.Second
	if v := getenv("FROST_HTTP_TIMEOUT_SECONDS", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("FROST_HTTP_TIMEOUT_SECONDS: must be a positive integer"))
		} else {
			c.FROSTHTTPTimeout = time.Duration(n) * time.Second
		}
	}

	c.MQTTEnabled = boolenv("MQTT_ENABLED", false)
	c.MQTTBrokerURL = getenv("MQTT_BROKER_URL", "")
	c.MQTTTopicPrefix = getenv("MQTT_TOPIC_PREFIX", "v1.1")
	c.MQTTQoS = 1
	if v := getenv("MQTT_QOS", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 2 {
			errs = append(errs, fmt.Errorf("MQTT_QOS: must be 0, 1, or 2"))
		} else {
			c.MQTTQoS = n
		}
	}
	if c.MQTTEnabled && c.MQTTBrokerURL == "" {
		errs = append(errs, errors.New("MQTT_BROKER_URL is required when MQTT_ENABLED is true"))
	}
	c.StateStoreDSN = getenv("STATE_STORE_DSN", "")
	c.FreshnessAlertWebhookURL = getenv("FRESHNESS_ALERT_WEBHOOK_URL", "")
	c.HTTPAddr = getenv("HTTP_ADDR", c.HTTPAddr)
	c.PushHTTPAddr = getenv("PUSH_HTTP_ADDR", "")
	c.CollaborallIngestSecret = getenv("COLLABORALL_INGEST_SECRET", "")
	c.EntityNamePrefix = getenv("ENTITY_NAME_PREFIX", "")

	c.DummyAdapterEnabled = boolenv("DUMMY_ADAPTER_ENABLED", false)
	c.DummyThingsCount = 5
	if v := getenv("DUMMY_THINGS_COUNT", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("DUMMY_THINGS_COUNT: must be a positive integer"))
		} else {
			c.DummyThingsCount = n
		}
	}
	c.DummyCadence = 5 * time.Minute
	if v := getenv("DUMMY_CADENCE_SECONDS", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("DUMMY_CADENCE_SECONDS: must be a positive integer"))
		} else {
			c.DummyCadence = time.Duration(n) * time.Second
		}
	}

	if v := getenv("POLL_INTERVAL_SECONDS", ""); v != "" {
		s, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("POLL_INTERVAL_SECONDS: %w", err))
		} else {
			c.PollInterval = time.Duration(s) * time.Second
		}
	}

	if v := getenv("FRESHNESS_THRESHOLD_HOURS", ""); v != "" {
		s, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("FRESHNESS_THRESHOLD_HOURS: %w", err))
		} else {
			c.FreshnessThreshold = time.Duration(s) * time.Hour
		}
	}

	if v := getenv("CURSOR_INIT_LOOKBACK_SECONDS", ""); v != "" {
		s, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("CURSOR_INIT_LOOKBACK_SECONDS: %w", err))
		} else {
			c.CursorInitLookback = time.Duration(s) * time.Second
		}
	}

	if v := getenv("CLOCK_SKEW_TOLERANCE_SECONDS", ""); v != "" {
		s, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("CLOCK_SKEW_TOLERANCE_SECONDS: %w", err))
		} else {
			c.ClockSkewTolerance = time.Duration(s) * time.Second
		}
	}

	if v := getenv("STATE_STORE_RETENTION_DAYS", ""); v != "" {
		s, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("STATE_STORE_RETENTION_DAYS: %w", err))
		} else if s < 1 {
			errs = append(errs, fmt.Errorf("STATE_STORE_RETENTION_DAYS: must be >= 1"))
		} else {
			c.StateStoreRetentionDays = s
		}
	}

	switch strings.ToUpper(getenv("LOG_LEVEL", "INFO")) {
	case "DEBUG":
		c.LogLevel = slog.LevelDebug
	case "INFO":
		c.LogLevel = slog.LevelInfo
	case "WARN", "WARNING":
		c.LogLevel = slog.LevelWarn
	case "ERROR":
		c.LogLevel = slog.LevelError
	default:
		errs = append(errs, fmt.Errorf("LOG_LEVEL: unknown level"))
	}

	if c.StateStoreDSN == "" {
		errs = append(errs, errors.New("STATE_STORE_DSN is required"))
	}

	// The public push listener must never share the admin listener (ADR-011):
	// admin endpoints are cluster-internal only.
	if c.PushHTTPAddr != "" && c.PushHTTPAddr == c.HTTPAddr {
		errs = append(errs, errors.New("PUSH_HTTP_ADDR must differ from HTTP_ADDR (admin endpoints are cluster-internal)"))
	}

	return c, errors.Join(errs...)
}

// SULOEnabled reports whether the SULO (REEN) poll adapter has everything
// it needs to be registered. Load guarantees these three are set together.
func (c Config) SULOEnabled() bool {
	return c.SULOAPIBaseURL != "" && c.SULOUsername != "" && c.SULOPassword != ""
}

// intenv parses an optional integer env var, enforcing a minimum. It
// returns nil when the variable is unset so callers can keep their own
// default rather than encoding it here.
func intenv(key string, min int) (*int, error) {
	v := getenv(key, "")
	if v == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min {
		return nil, fmt.Errorf("%s: must be an integer >= %d", key, min)
	}
	return &n, nil
}

// countSet returns how many of the given values are non-empty. Used to
// detect partially-configured credential groups.
func countSet(values ...string) int {
	n := 0
	for _, v := range values {
		if v != "" {
			n++
		}
	}
	return n
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// boolenv parses a boolean env var. Accepts 1/true/yes/on (case-insensitive)
// as true; anything else (including unset) returns fallback.
func boolenv(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
