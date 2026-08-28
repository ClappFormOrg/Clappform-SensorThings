// Package api serves the cluster-internal admin endpoints:
//   - GET /healthz             liveness
//   - GET /healthz/freshness   freshness state machine snapshot (F5)
//   - GET /metrics             Prometheus exposition
//
// All endpoints are unauthenticated; isolation is enforced at the
// Kubernetes Service layer (ClusterIP-only, no Ingress) per the
// Implementation Contract.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/metrics"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/state"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/watchdog"
)

// Server is the admin HTTP server.
type Server struct {
	Addr     string
	Store    state.Store
	Watchdog *watchdog.Watchdog
	Logger   *slog.Logger

	srv *http.Server
}

// New returns a Server bound to addr.
func New(addr string, store state.Store, w *watchdog.Watchdog, logger *slog.Logger) *Server {
	return &Server{Addr: addr, Store: store, Watchdog: w, Logger: logger}
}

// ListenAndServe starts the admin HTTP server. Returns when the server
// stops; pair with Shutdown for graceful termination.
func (s *Server) ListenAndServe() error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /healthz/freshness", s.handleFreshness)
	mux.Handle("GET /metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))

	s.srv = &http.Server{
		Addr:              s.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.Logger.Info("admin http server listening", slog.String("addr", s.Addr))
	if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown gracefully stops the server. Safe to call after ListenAndServe.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := s.Store.Ping(ctx); err != nil {
		http.Error(w, "state store unreachable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleFreshness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	snap, err := s.Watchdog.CurrentSnapshot(ctx)
	if err != nil {
		s.Logger.Error("freshness snapshot", slog.Any("err", err))
		http.Error(w, "snapshot failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}
