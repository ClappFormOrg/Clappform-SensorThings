// This file serves the public, Ingress-exposed push ingestion endpoint
// (ADR-011): POST /ingest/{vendorID}. It is deliberately a SEPARATE
// http.Server from the cluster-internal admin server in server.go — the
// admin endpoints (/healthz, /metrics) must never share a listener with
// the internet-facing push surface.
package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/clappformorg/geonovum-sta-translation/internal/adapters"
	"github.com/clappformorg/geonovum-sta-translation/internal/canonical"
	"github.com/clappformorg/geonovum-sta-translation/internal/ingest"
	"github.com/clappformorg/geonovum-sta-translation/internal/oms"
	"github.com/clappformorg/geonovum-sta-translation/internal/state"
)

// MaxPushBytes bounds an inbound push body (413 above it).
const MaxPushBytes = 1 << 20 // 1 MiB

// PushServer serves POST /ingest/{vendorID} for push-mode adapters.
type PushServer struct {
	Addr      string
	Registry  *adapters.Registry
	Store     state.Store
	Processor *ingest.Processor
	Logger    *slog.Logger

	srv *http.Server
}

// NewPushServer returns a PushServer bound to addr.
func NewPushServer(addr string, reg *adapters.Registry, store state.Store, proc *ingest.Processor, logger *slog.Logger) *PushServer {
	return &PushServer{Addr: addr, Registry: reg, Store: store, Processor: proc, Logger: logger}
}

// ListenAndServe starts the push server. Returns when the server stops;
// pair with Shutdown for graceful termination.
func (s *PushServer) ListenAndServe() error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /ingest/{vendorID}", s.handleIngest)

	s.srv = &http.Server{
		Addr:              s.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
	}
	s.Logger.Info("push http server listening", slog.String("addr", s.Addr))
	if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown gracefully stops the server. Safe to call after ListenAndServe.
func (s *PushServer) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

type rejectedItem struct {
	RawObservationID string `json:"raw_observation_id"`
	Reason           string `json:"reason"`
}

type pushResponse struct {
	Accepted          int            `json:"accepted"`
	SkippedIdempotent int            `json:"skipped_idempotent"`
	Rejected          []rejectedItem `json:"rejected"`
}

func (s *PushServer) handleIngest(w http.ResponseWriter, r *http.Request) {
	vendorID := r.PathValue("vendorID")
	adapter := s.Registry.PushAdapter(vendorID)
	if adapter == nil {
		// Unknown vendor, or the vendor is registered in poll mode.
		http.Error(w, "unknown push vendor", http.StatusNotFound)
		return
	}
	log := s.Logger.With(slog.String("vendor", vendorID))

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxPushBytes))
	if err != nil {
		// MaxBytesReader signals an oversize body here.
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	if err := adapter.Authenticate(r, body); err != nil {
		log.Warn("push authentication failed", slog.Any("err", err))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	batch, err := adapter.DecodePush(r.Context(), body)
	if err != nil {
		log.Warn("push decode failed", slog.Any("err", err))
		http.Error(w, "undecodable body", http.StatusBadRequest)
		return
	}

	resp, status := s.ingestBatch(r.Context(), log, vendorID, batch)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// streamRef is a resolved (Thing, Datastream) plus its state-store ids.
type streamRef struct {
	thing   canonical.Thing
	ds      canonical.Datastream
	thingID int64
	dsID    int64
}

// ingestBatch registers the batch's Things/Datastreams, then runs each
// (Thing, Datastream) group through the ingest core with a zero cursor
// (push mode disables the before_cursor rule — ADR-011). It returns the
// response body and the HTTP status to send.
func (s *PushServer) ingestBatch(ctx context.Context, log *slog.Logger, vendorID string, batch adapters.DecodedBatch) (pushResponse, int) {
	var resp pushResponse

	// 1. Register Things/Datastreams; build the (nativeID|OP) → streamRef map.
	//    v1 ingests fill-level only (ADR-003), so non-fill-level streams are
	//    not registered and their observations are reported as rejected.
	streams := map[string]*streamRef{}
	for _, dt := range batch.Things {
		thingID, err := s.Store.UpsertThing(ctx, dt.Thing.VendorID, dt.Thing.VendorNativeID,
			oms.ThingName(dt.Thing.VendorID, dt.Thing.VendorNativeID))
		if err != nil {
			log.Error("push upsert thing", slog.Any("err", err), slog.String("vendor_native_id", dt.Thing.VendorNativeID))
			return resp, http.StatusServiceUnavailable
		}
		for _, d := range dt.Datastreams {
			if d.ObservedProperty != canonical.FillLevel {
				continue
			}
			dsID, err := s.Store.UpsertDatastream(ctx, thingID, string(d.ObservedProperty), d.ExpectedCadenceSeconds)
			if err != nil {
				log.Error("push upsert datastream", slog.Any("err", err))
				return resp, http.StatusServiceUnavailable
			}
			streams[streamKey(dt.Thing.VendorNativeID, d.ObservedProperty)] = &streamRef{
				thing: dt.Thing, ds: d, thingID: thingID, dsID: dsID,
			}
		}
	}

	// 2. Group observations by stream; flag any that reference no known
	//    fill-level Datastream.
	grouped := map[string][]canonical.Observation{}
	for _, o := range batch.Observations {
		key := streamKey(o.ThingVendorNativeID, o.ObservedProperty)
		if _, ok := streams[key]; !ok {
			resp.Rejected = append(resp.Rejected, rejectedItem{RawObservationID: o.RawObservationID, Reason: "unknown_datastream"})
			continue
		}
		grouped[key] = append(grouped[key], o)
	}

	// 3. Ingest each stream. A transient failure on any stream means the
	//    batch is not fully durable; tell the vendor to retry (idempotency
	//    makes a full re-send safe).
	for key, obs := range grouped {
		ref := streams[key]
		res, err := s.Processor.ProcessStream(ctx, vendorID, ref.thingID, ref.dsID, ref.thing, ref.ds, time.Time{}, obs)
		if err != nil {
			log.Error("push ingest stream", slog.Any("err", err), slog.String("stream", key))
			return resp, http.StatusServiceUnavailable
		}
		resp.Accepted += res.Accepted
		resp.SkippedIdempotent += res.SkippedIdempotent
		for _, rj := range res.Rejected {
			resp.Rejected = append(resp.Rejected, rejectedItem{
				RawObservationID: rj.Observation.RawObservationID,
				Reason:           string(rj.Reason),
			})
		}
	}

	return resp, http.StatusAccepted
}

func streamKey(vendorNativeID string, op canonical.ObservedProperty) string {
	return vendorNativeID + "|" + string(op)
}
