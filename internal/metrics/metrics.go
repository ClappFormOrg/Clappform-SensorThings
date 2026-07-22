// Package metrics exposes the Prometheus counters and gauges used
// throughout the translation layer. The handler is mounted on
// /metrics by internal/api.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Registry is the local registry. Tests can substitute their own.
var Registry = prometheus.NewRegistry()

var factory = promauto.With(Registry)

// Counters and gauges named to match the design doc.
var (
	ObservationsPolledTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "observations_polled_total",
		Help: "Observations returned by adapters, by vendor.",
	}, []string{"vendor"})

	ObservationsAcceptedTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "observations_accepted_total",
		Help: "Observations that passed validation, by vendor.",
	}, []string{"vendor"})

	ObservationsWrittenTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "observations_written_total",
		Help: "Observations successfully written to FROST, by vendor and target.",
	}, []string{"vendor", "target"})

	ObservationsDroppedTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "observations_dropped_total",
		Help: "Observations dropped, by reason and vendor.",
	}, []string{"reason", "vendor"})

	ObservationsSkippedIdempotentTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "observations_skipped_idempotent_total",
		Help: "Observations skipped because they were already present in FROST.",
	}, []string{"vendor", "target"})

	VendorTransientErrorsTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "vendor_transient_errors_total",
		Help: "Transient vendor errors, by vendor.",
	}, []string{"vendor"})

	VendorPermanentErrorsTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "vendor_permanent_errors_total",
		Help: "Permanent vendor errors, by vendor.",
	}, []string{"vendor"})

	STADuplicateEntityTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "sta_duplicate_entity_total",
		Help: "FROST returned more than one match for a name-filter lookup, by entity type.",
	}, []string{"type", "target"})

	StateStoreRowsPurgedTotal = factory.NewCounter(prometheus.CounterOpts{
		Name: "state_store_rows_purged_total",
		Help: "Observation write log rows purged by retention (R-RUN-2).",
	})

	ObservationFreshnessSeconds = factory.NewGaugeVec(prometheus.GaugeOpts{
		Name: "observation_freshness_seconds",
		Help: "now() - max(resultTime) per Datastream, in seconds.",
	}, []string{"vendor", "observed_property"})

	DatastreamsStale = factory.NewGauge(prometheus.GaugeOpts{
		Name: "datastreams_stale",
		Help: "Number of Datastreams currently above their freshness threshold (ADR-008).",
	})

	DatastreamsTotal = factory.NewGauge(prometheus.GaugeOpts{
		Name: "datastreams_total",
		Help: "Number of Datastreams tracked by the translation layer.",
	})

	WatchdogAlertFiresTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "watchdog_alert_fires_total",
		Help: "Freshness alert firings, by transition (stale / recovered).",
	}, []string{"transition"})

	WatchdogAlertWebhookErrorsTotal = factory.NewCounter(prometheus.CounterOpts{
		Name: "watchdog_alert_webhook_errors_total",
		Help: "Failures POSTing the freshness alert webhook payload.",
	})
)
