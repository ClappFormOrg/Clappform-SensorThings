// Command collaborall-reader reads Sensors/Datastreams/Observations from a
// Collaborall FROST-Server and POSTs them, as canonical batches, to the
// translation-layer's push endpoint (POST /ingest/collaborall). Keeping the
// FROST-read logic in this separate binary leaves the translation-layer
// service itself decoupled from the source's quirks (self-signed cert,
// custom entities); the service only decodes the resulting Envelope.
//
// It owns its own "since" cursor per source Datastream (push mode has no
// server-side cursor). Re-sends after a crash are safe: the service's
// write-log deduplicates on (datastream, phenomenon_time).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters/collaborall/source"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/frost"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/logging"
)

func main() {
	if err := run(); err != nil {
		slog.Default().Error("fatal", slog.Any("err", err))
		os.Exit(1)
	}
}

type config struct {
	baseURL     string
	basicUser   string
	basicPass   string
	token       string
	insecure    bool
	httpTimeout time.Duration

	watchSensors []string
	pageLimit    int

	ingestURL     string
	ingestSecret  string
	ingestTimeout time.Duration

	pollInterval time.Duration
	lookback     time.Duration
	cursorFile   string
	once         bool

	logLevel slog.Level
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	logger := logging.New(cfg.logLevel)
	logger.Info("collaborall-reader starting",
		slog.String("source", cfg.baseURL),
		slog.String("sink", cfg.ingestURL),
		slog.Int("watch_sensors", len(cfg.watchSensors)),
		slog.Duration("poll_interval", cfg.pollInterval),
	)

	client := frost.NewClient(cfg.baseURL, frost.Auth{
		Token:         cfg.token,
		BasicUser:     cfg.basicUser,
		BasicPassword: cfg.basicPass,
	}, cfg.insecure, cfg.httpTimeout)

	reader := source.New(client, source.Config{
		WatchSensors:         cfg.watchSensors,
		ObservationPageLimit: cfg.pageLimit,
	}, logger)

	cursors, err := loadCursorStore(cfg.cursorFile)
	if err != nil {
		return fmt.Errorf("load cursor store %q: %w", cfg.cursorFile, err)
	}

	sink := &httpSink{
		url:    cfg.ingestURL,
		secret: cfg.ingestSecret,
		client: &http.Client{Timeout: cfg.ingestTimeout},
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cycle := func() {
		res, err := runOnce(ctx, reader, sink, cursors, cfg.lookback, time.Now, logger)
		if err != nil {
			logger.Error("poll cycle completed with errors", slog.Any("err", err))
		}
		logger.Info("poll cycle complete",
			slog.Int("streams", res.Streams),
			slog.Int("posts", res.Posts),
			slog.Int("accepted", res.Accepted),
			slog.Int("skipped_idempotent", res.Skipped),
			slog.Int("rejected", res.Rejected),
		)
	}

	// Run one cycle immediately, then on the poll interval.
	cycle()
	if cfg.once {
		return nil
	}

	t := time.NewTicker(cfg.pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("shutdown signal received")
			return nil
		case <-t.C:
			cycle()
		}
	}
}

func loadConfig() (config, error) {
	c := config{
		httpTimeout:   30 * time.Second,
		ingestTimeout: 120 * time.Second,
		pollInterval:  15 * time.Minute,
		lookback:      1 * time.Hour,
		cursorFile:    "collaborall-cursors.json",
		logLevel:      slog.LevelInfo,
	}
	var errs []error

	c.baseURL = getenv("COLLABORALL_FROST_BASE_URL", "")
	if c.baseURL == "" {
		errs = append(errs, errors.New("COLLABORALL_FROST_BASE_URL is required"))
	}
	c.basicUser = getenv("COLLABORALL_BASIC_AUTH_USER", "")
	c.basicPass = getenv("COLLABORALL_BASIC_AUTH_PASSWORD", "")
	c.token = getenv("COLLABORALL_TOKEN", "")
	c.insecure = boolenv("COLLABORALL_TLS_INSECURE_SKIP_VERIFY", false)

	c.ingestURL = getenv("API_INGEST_URL", "")
	if c.ingestURL == "" {
		errs = append(errs, errors.New("API_INGEST_URL is required (e.g. https://svc/ingest/collaborall)"))
	}
	c.ingestSecret = getenv("COLLABORALL_INGEST_SECRET", "")
	if c.ingestSecret == "" {
		errs = append(errs, errors.New("COLLABORALL_INGEST_SECRET is required"))
	}

	c.watchSensors = splitList(getenv("COLLABORALL_WATCH_SENSORS", ""))
	c.once = boolenv("READER_ONCE", false)

	if v := getenv("COLLABORALL_HTTP_TIMEOUT_SECONDS", ""); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			errs = append(errs, errors.New("COLLABORALL_HTTP_TIMEOUT_SECONDS: must be a positive integer"))
		} else {
			c.httpTimeout = time.Duration(n) * time.Second
		}
	}
	// Separate, more generous timeout for the sink POST: a single POST drives
	// the whole synchronous write-through on the translation-layer (cold
	// entity-chain upserts + a per-observation idempotency probe against the
	// remote FROST target), so it legitimately runs far longer than a source
	// read. Keep it comfortably under READER_POLL_INTERVAL_SECONDS.
	if v := getenv("INGEST_HTTP_TIMEOUT_SECONDS", ""); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			errs = append(errs, errors.New("INGEST_HTTP_TIMEOUT_SECONDS: must be a positive integer"))
		} else {
			c.ingestTimeout = time.Duration(n) * time.Second
		}
	}
	if v := getenv("READER_POLL_INTERVAL_SECONDS", ""); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			errs = append(errs, errors.New("READER_POLL_INTERVAL_SECONDS: must be a positive integer"))
		} else {
			c.pollInterval = time.Duration(n) * time.Second
		}
	}
	if v := getenv("READER_CURSOR_LOOKBACK_SECONDS", ""); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 0 {
			errs = append(errs, errors.New("READER_CURSOR_LOOKBACK_SECONDS: must be a non-negative integer"))
		} else {
			c.lookback = time.Duration(n) * time.Second
		}
	}
	if v := getenv("COLLABORALL_OBSERVATION_PAGE_LIMIT", ""); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			errs = append(errs, errors.New("COLLABORALL_OBSERVATION_PAGE_LIMIT: must be a positive integer"))
		} else {
			c.pageLimit = n
		}
	}
	if v := getenv("READER_CURSOR_FILE", ""); v != "" {
		c.cursorFile = v
	}
	switch strings.ToUpper(getenv("LOG_LEVEL", "INFO")) {
	case "DEBUG":
		c.logLevel = slog.LevelDebug
	case "INFO":
		c.logLevel = slog.LevelInfo
	case "WARN", "WARNING":
		c.logLevel = slog.LevelWarn
	case "ERROR":
		c.logLevel = slog.LevelError
	default:
		errs = append(errs, errors.New("LOG_LEVEL: unknown level"))
	}

	return c, errors.Join(errs...)
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

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

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
