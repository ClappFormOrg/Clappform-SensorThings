package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters/collaborall"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters/collaborall/source"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
)

// maxEnvelopeBytes keeps each POST comfortably under the push endpoint's
// 1 MiB cap (internal/api.MaxPushBytes), leaving headroom for headers and
// estimation error.
const maxEnvelopeBytes = 900 * 1024

// poster is the sink runOnce writes to (httpSink in production; a fake in tests).
type poster interface {
	post(ctx context.Context, env collaborall.Envelope) (ingestResponse, error)
}

// runResult summarises one poll cycle for logging.
type runResult struct {
	Streams  int
	Posts    int
	Accepted int
	Skipped  int
	Rejected int
}

// runOnce discovers watched streams, fetches new observations per stream,
// posts them in size-bounded chunks, and advances each stream's cursor only
// after all its chunks post successfully. A per-stream failure is logged and
// leaves that cursor put (safe to retry — the write-log dedups); the first
// such error is returned so the caller can surface it.
func runOnce(
	ctx context.Context,
	reader *source.SourceReader,
	sink poster,
	cursors *cursorStore,
	lookback time.Duration,
	now func() time.Time,
	logger *slog.Logger,
) (runResult, error) {
	streams, err := reader.Discover(ctx)
	if err != nil {
		return runResult{}, err
	}

	var res runResult
	res.Streams = len(streams)
	var firstErr error

	for _, st := range streams {
		key := strconv.FormatInt(st.SourceDatastreamID, 10)
		since := cursors.get(key)
		if since.IsZero() {
			since = now().Add(-lookback)
		}

		obs, err := reader.FetchObservations(ctx, st, since, 0)
		if err != nil {
			logger.Error("fetch observations", slog.String("stream", key), slog.Any("err", err))
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(obs) == 0 {
			continue
		}

		streamOK := true
		for _, env := range chunkEnvelopes(st, obs, maxEnvelopeBytes) {
			resp, err := sink.post(ctx, env)
			if err != nil {
				logger.Error("post envelope", slog.String("stream", key), slog.Any("err", err))
				if firstErr == nil {
					firstErr = err
				}
				streamOK = false
				break
			}
			res.Posts++
			res.Accepted += resp.Accepted
			res.Skipped += resp.SkippedIdempotent
			res.Rejected += len(resp.Rejected)
		}
		if !streamOK {
			continue // leave the cursor put; retry next cycle
		}

		if err := cursors.advance(key, maxPhenomenonTime(obs)); err != nil {
			logger.Warn("advance cursor", slog.String("stream", key), slog.Any("err", err))
		}
	}
	return res, firstErr
}

// chunkEnvelopes splits a stream's observations into envelopes that each
// marshal to under maxBytes. Every envelope carries the stream's Thing +
// Datastream so the service can register them (idempotently) before writing.
func chunkEnvelopes(st source.DiscoveredStream, obs []canonical.Observation, maxBytes int) []collaborall.Envelope {
	tws := collaborall.ThingWithStreams{
		Thing:       st.Thing,
		Datastreams: []canonical.Datastream{st.Datastream},
	}
	base := collaborall.Envelope{Things: []collaborall.ThingWithStreams{tws}}

	overhead := jsonLen(base)
	perObs := 1
	if len(obs) > 0 {
		withOne := collaborall.Envelope{
			Things:       []collaborall.ThingWithStreams{tws},
			Observations: obs[:1],
		}
		if d := jsonLen(withOne) - overhead; d > perObs {
			perObs = d
		}
	}
	n := (maxBytes - overhead) / perObs
	if n < 1 {
		n = 1
	}

	var envs []collaborall.Envelope
	for i := 0; i < len(obs); i += n {
		end := i + n
		if end > len(obs) {
			end = len(obs)
		}
		envs = append(envs, collaborall.Envelope{
			Things:       []collaborall.ThingWithStreams{tws},
			Observations: obs[i:end],
		})
	}
	return envs
}

func jsonLen(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

func maxPhenomenonTime(obs []canonical.Observation) time.Time {
	max := obs[0].PhenomenonTime
	for _, o := range obs[1:] {
		if o.PhenomenonTime.After(max) {
			max = o.PhenomenonTime
		}
	}
	return max
}
