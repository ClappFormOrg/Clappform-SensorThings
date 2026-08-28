// Command translation-layer is the SULO → OGC SensorThings API
// translation service for the Geonovum 2026 testbed (Topic #2).
//
// See docs/sulo-sta-translation-layer-design.md for the full design;
// ADR-001 (revised) confirms Go as the implementation language.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters/collaborall"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters/dummy"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters/sulo"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/api"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/config"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/frost"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/ingest"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/logging"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/oms"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/scheduler"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/state"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/validator"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/watchdog"
)

func main() {
	if err := run(); err != nil {
		// Print and exit non-zero. slog may not be initialized yet on
		// config-load failure, so use stderr directly via slog.Default.
		slog.Default().Error("fatal", slog.Any("err", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := logging.New(cfg.LogLevel)
	logger.Info("starting translation layer",
		slog.String("http_addr", cfg.HTTPAddr),
		slog.Int("frost_targets", len(cfg.FROSTTargets)),
		slog.Duration("poll_interval", cfg.PollInterval),
	)

	// Top-level context cancels on SIGINT / SIGTERM. All long-running
	// loops use this context.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// State store first — everything depends on it.
	store, err := state.NewPGStore(ctx, cfg.StateStoreDSN)
	if err != nil {
		return err
	}
	defer store.Close()
	logger.Info("state store ready")

	// FROST writers (one per target). Empty list is allowed — base
	// startup before a target is configured does no work.
	omsCfg := oms.DefaultConfig()
	omsCfg.EntityNamePrefix = cfg.EntityNamePrefix
	mapper := oms.New(omsCfg)
	frostAuth := frost.Auth{
		Token:         cfg.FROSTWriteToken,
		BasicUser:     cfg.FROSTBasicAuthUser,
		BasicPassword: cfg.FROSTBasicAuthPassword,
	}

	// Optional MQTT write path (F-MQTT): one publisher shared across
	// targets (single broker in v1; per-target brokers deferred). When
	// enabled, Observation creates are published over MQTT; entity
	// upserts stay on HTTP. Auth reuses the FROST Basic credential.
	var mqttPub *frost.MQTTPublisher
	if cfg.MQTTEnabled {
		var err error
		mqttPub, err = frost.NewMQTTPublisher(frost.MQTTConfig{
			BrokerURL:          cfg.MQTTBrokerURL,
			Username:           cfg.FROSTBasicAuthUser,
			Password:           cfg.FROSTBasicAuthPassword,
			TopicPrefix:        cfg.MQTTTopicPrefix,
			QoS:                byte(cfg.MQTTQoS),
			InsecureSkipVerify: cfg.FROSTInsecureSkipVerify,
		}, logger)
		if err != nil {
			return err
		}
		defer mqttPub.Close()
		logger.Info("frost mqtt write path enabled",
			slog.String("broker", cfg.MQTTBrokerURL),
			slog.Int("qos", cfg.MQTTQoS),
		)
	}

	var writers []*frost.Writer
	for _, t := range cfg.FROSTTargets {
		c := frost.NewClient(t, frostAuth, cfg.FROSTInsecureSkipVerify, cfg.FROSTHTTPTimeout)
		target := frost.Target{Label: t, Client: c}
		if mqttPub != nil {
			target.MQTT = mqttPub
		}
		writers = append(writers, frost.NewWriter(target, logger))
	}

	// Ingestion core — the transport-agnostic write path shared by poll
	// and push modes (ADR-011).
	vcfg := validator.Config{
		ClockSkewTolerance: cfg.ClockSkewTolerance,
		Now:                time.Now,
	}
	processor := ingest.New(mapper, writers, store, vcfg, logger)

	// Adapter registry — connectors register here (RegisterPoll /
	// RegisterPush). Empty registry at startup is valid (scheduler tick
	// and push dispatch become no-ops).
	registry := adapters.NewRegistry()

	// Synthetic adapter for end-to-end validation (opt-in; never in
	// production). Emits fake fill-level data so the full pipeline can be
	// exercised before the SULO adapter exists.
	if cfg.DummyAdapterEnabled {
		registry.RegisterPoll(dummy.New(cfg.DummyThingsCount, cfg.DummyCadence, time.Now))
		logger.Warn("dummy adapter ENABLED — synthetic data only, do not use in production",
			slog.Int("things", cfg.DummyThingsCount),
			slog.Duration("cadence", cfg.DummyCadence),
		)
	}

	// SULO poll adapter (REEN CMS REST API v3). Registered when the REEN
	// session credentials are configured; the scheduler then pulls
	// container-slot fill levels every PollInterval.
	if cfg.SULOEnabled() {
		a, err := sulo.New(sulo.Config{
			BaseURL:                cfg.SULOAPIBaseURL,
			Username:               cfg.SULOUsername,
			Password:               cfg.SULOPassword,
			CustomerID:             cfg.SULOCustomerID,
			HTTPTimeout:            cfg.SULOHTTPTimeout,
			PageSize:               cfg.SULOPageSize,
			ObservationPageLimit:   cfg.SULOObservationPageLimit,
			MinConfidence:          cfg.SULOMinConfidence,
			ExpectedCadenceSeconds: int(cfg.SULOExpectedCadence.Seconds()),
		}, logger)
		if err != nil {
			return err
		}
		registry.RegisterPoll(a)
		logger.Info("sulo poll adapter registered",
			slog.String("vendor", sulo.VendorID),
			slog.String("base_url", cfg.SULOAPIBaseURL),
		)
	}

	// Collaborall push decoder: accepts batches from the standalone
	// cmd/collaborall-reader on POST /ingest/collaborall. Registered only
	// when a shared secret is configured; requires PUSH_HTTP_ADDR to serve.
	if cfg.CollaborallIngestSecret != "" {
		registry.RegisterPush(collaborall.NewPush(cfg.CollaborallIngestSecret))
		if cfg.PushHTTPAddr == "" {
			logger.Warn("collaborall push adapter registered but PUSH_HTTP_ADDR is empty — the ingest endpoint will not serve")
		} else {
			logger.Info("collaborall push adapter registered", slog.String("vendor", collaborall.VendorID))
		}
	}

	// Scheduler (drives poll-mode adapters).
	sch := scheduler.New(
		registry,
		store,
		processor,
		logger,
		cfg.PollInterval,
		cfg.CursorInitLookback,
	)

	// Watchdog.
	wd := watchdog.New(store, cfg.FreshnessAlertWebhookURL, "geonovum-testbed", logger)

	// Admin HTTP server (cluster-internal).
	srv := api.New(cfg.HTTPAddr, store, wd, logger)

	// Push HTTP server (public; ADR-011). Started only when configured.
	var pushSrv *api.PushServer
	if cfg.PushHTTPAddr != "" {
		pushSrv = api.NewPushServer(cfg.PushHTTPAddr, registry, store, processor, logger)
	}

	// Run loops concurrently.
	var wg sync.WaitGroup
	errCh := make(chan error, 4)

	wg.Add(3)
	go func() {
		defer wg.Done()
		if err := sch.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errCh <- err
		}
	}()
	go func() {
		defer wg.Done()
		if err := wd.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errCh <- err
		}
	}()
	go func() {
		defer wg.Done()
		if err := srv.ListenAndServe(); err != nil {
			errCh <- err
		}
	}()
	if pushSrv != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := pushSrv.ListenAndServe(); err != nil {
				errCh <- err
			}
		}()
	}

	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown error", slog.Any("err", err))
	}
	if pushSrv != nil {
		if err := pushSrv.Shutdown(shutdownCtx); err != nil {
			logger.Warn("push http shutdown error", slog.Any("err", err))
		}
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			logger.Error("background loop error", slog.Any("err", err))
		}
	}
	logger.Info("shutdown complete")
	return nil
}
