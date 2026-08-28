// Package watchdog implements the freshness alert state machine
// from F5 and ADR-008.
//
// State machine:
//
//	ok ──(stale tick)──> stale_pending ──(2nd stale tick)──> stale  → fire "stale"
//	                       │                                  │
//	                       └──(fresh tick)──> ok              └──(fresh tick)──> ok → fire "recovered"
//
// The "2nd stale tick" hysteresis prevents flapping when freshness
// oscillates around the threshold. Per-Datastream thresholds are
// computed inside Store.GetStalenessSnapshot.
package watchdog

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/metrics"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/state"
)

// CheckInterval is the watchdog tick cadence. 30 minutes per F5 +
// ADR-008.
const CheckInterval = 30 * time.Minute

// AlertPayload is the body POSTed to FRESHNESS_ALERT_WEBHOOK_URL on
// every state-transition fire. JSON-serialized.
type AlertPayload struct {
	Status         string    `json:"status"` // "stale" | "recovered"
	MaxLastWritten time.Time `json:"max_last_written_at"`
	StaleCount     int       `json:"stale_count"`
	TotalCount     int       `json:"total_count"`
	Examples       []string  `json:"examples"`
	Namespace      string    `json:"namespace"`
	FiredAt        time.Time `json:"fired_at"`
}

// Watchdog runs the periodic freshness check.
type Watchdog struct {
	Store      state.Store
	WebhookURL string
	Namespace  string
	HTTP       *http.Client
	Logger     *slog.Logger

	now func() time.Time
}

// New returns a Watchdog. webhookURL may be empty — in that case
// transitions are still logged but no HTTP POST is issued.
func New(store state.Store, webhookURL, namespace string, logger *slog.Logger) *Watchdog {
	return &Watchdog{
		Store:      store,
		WebhookURL: webhookURL,
		Namespace:  namespace,
		HTTP:       &http.Client{Timeout: 10 * time.Second},
		Logger:     logger,
		now:        time.Now,
	}
}

// Run ticks every CheckInterval until ctx is cancelled. One tick fires
// immediately at startup so /healthz/freshness reflects current state.
func (w *Watchdog) Run(ctx context.Context) error {
	t := time.NewTicker(CheckInterval)
	defer t.Stop()
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			w.tick(ctx)
		}
	}
}

// Snapshot is the read-only view exposed by /healthz/freshness.
type Snapshot struct {
	Status        state.Status `json:"status"`
	StaleCount    int          `json:"stale_count"`
	TotalCount    int          `json:"total_count"`
	SinceTS       time.Time    `json:"since_ts"`
	Examples      []string     `json:"examples"`
	ThresholdNote string       `json:"threshold_note"`
}

// CurrentSnapshot returns a fresh snapshot suitable for /healthz/freshness.
// It does not advance the state machine.
func (w *Watchdog) CurrentSnapshot(ctx context.Context) (Snapshot, error) {
	wd, err := w.Store.GetWatchdogState(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	snap, err := w.Store.GetStalenessSnapshot(ctx, w.now())
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		Status:        wd.CurrentStatus,
		StaleCount:    snap.Stale,
		TotalCount:    snap.Total,
		SinceTS:       wd.SinceTS,
		Examples:      snap.ExampleNames,
		ThresholdNote: "per-Datastream: max(3 * expected_cadence_seconds, 1h); alert if stale_count > max(1, 1% of total)",
	}, nil
}

func (w *Watchdog) tick(ctx context.Context) {
	now := w.now()
	snap, err := w.Store.GetStalenessSnapshot(ctx, now)
	if err != nil {
		w.Logger.Error("watchdog staleness snapshot", slog.Any("err", err))
		return
	}
	metrics.DatastreamsTotal.Set(float64(snap.Total))
	metrics.DatastreamsStale.Set(float64(snap.Stale))

	wd, err := w.Store.GetWatchdogState(ctx)
	if err != nil {
		w.Logger.Error("watchdog get state", slog.Any("err", err))
		return
	}

	staleNow := isStale(snap)
	next, transition := step(wd, staleNow, now)
	if next != wd {
		if err := w.Store.SetWatchdogState(ctx, next); err != nil {
			w.Logger.Error("watchdog set state", slog.Any("err", err))
			return
		}
		w.Logger.Info("watchdog state transition",
			slog.String("from", string(wd.CurrentStatus)),
			slog.String("to", string(next.CurrentStatus)),
			slog.Int("stale_count", snap.Stale),
			slog.Int("total_count", snap.Total),
		)
	}
	if transition != "" {
		w.fire(ctx, transition, snap, now)
	}
}

func isStale(snap state.StalenessSnapshot) bool {
	// Per ADR-008: alert if stale_count > max(1, 1% of total).
	threshold := snap.Total / 100
	if threshold < 1 {
		threshold = 1
	}
	return snap.Stale > threshold
}

// step applies the state-machine transitions and returns:
//   - the next WatchdogState
//   - the alert transition to fire ("stale" or "recovered"), or "" if none
func step(cur state.WatchdogState, staleNow bool, now time.Time) (state.WatchdogState, string) {
	switch cur.CurrentStatus {
	case state.StatusOK:
		if staleNow {
			return state.WatchdogState{CurrentStatus: state.StatusStalePending, SinceTS: now, LastFiredTS: cur.LastFiredTS}, ""
		}
		return cur, ""
	case state.StatusStalePending:
		if staleNow {
			// Second consecutive stale tick → fire.
			firedAt := now
			return state.WatchdogState{CurrentStatus: state.StatusStale, SinceTS: now, LastFiredTS: &firedAt}, "stale"
		}
		// Cleared before promotion.
		return state.WatchdogState{CurrentStatus: state.StatusOK, SinceTS: now, LastFiredTS: cur.LastFiredTS}, ""
	case state.StatusStale:
		if staleNow {
			return cur, ""
		}
		firedAt := now
		return state.WatchdogState{CurrentStatus: state.StatusOK, SinceTS: now, LastFiredTS: &firedAt}, "recovered"
	default:
		// Unknown status — reset to OK.
		return state.WatchdogState{CurrentStatus: state.StatusOK, SinceTS: now, LastFiredTS: cur.LastFiredTS}, ""
	}
}

func (w *Watchdog) fire(ctx context.Context, transition string, snap state.StalenessSnapshot, now time.Time) {
	metrics.WatchdogAlertFiresTotal.WithLabelValues(transition).Inc()

	if w.WebhookURL == "" {
		w.Logger.Warn("freshness alert (no webhook configured)",
			slog.String("transition", transition),
			slog.Int("stale_count", snap.Stale),
			slog.Int("total_count", snap.Total),
		)
		return
	}

	payload := AlertPayload{
		Status:     transition,
		StaleCount: snap.Stale,
		TotalCount: snap.Total,
		Examples:   snap.ExampleNames,
		Namespace:  w.Namespace,
		FiredAt:    now,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		w.Logger.Error("watchdog marshal payload", slog.Any("err", err))
		metrics.WatchdogAlertWebhookErrorsTotal.Inc()
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.WebhookURL, bytes.NewReader(buf))
	if err != nil {
		w.Logger.Error("watchdog build webhook request", slog.Any("err", err))
		metrics.WatchdogAlertWebhookErrorsTotal.Inc()
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.HTTP.Do(req)
	if err != nil {
		w.Logger.Error("watchdog post webhook", slog.Any("err", err))
		metrics.WatchdogAlertWebhookErrorsTotal.Inc()
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		w.Logger.Error("watchdog webhook non-2xx", slog.Int("status", resp.StatusCode))
		metrics.WatchdogAlertWebhookErrorsTotal.Inc()
		return
	}
	w.Logger.Info("freshness alert fired",
		slog.String("transition", transition),
		slog.Int("stale_count", snap.Stale),
		slog.Int("total_count", snap.Total),
	)
}
