package frost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Target is one FROST-Server endpoint to dual-write to (F6).
// Each Target has its own client + state cache.
type Target struct {
	Label  string // e.g. "fallback" or "central"
	Client *Client
	// MQTT, when non-nil, carries Observation creates over MQTT instead
	// of an HTTP POST. Entity upserts and the idempotency probe still use
	// Client (HTTP) because MQTT publish returns no @iot.id or status.
	MQTT ObservationPublisher
}

// EntityCache resolves the @iot.id for a (Entity, name) pair without
// repeating the GET-by-filter probe. It is per-Target so a second
// target gets its own cache.
type EntityCache interface {
	Get(entity Entity, name string) (id int64, ok bool)
	Put(entity Entity, name string, id int64)
}

// MemEntityCache is the simplest cache: an in-memory map. It is
// rebuilt at startup by calling GetOrCreate for each entity the
// scheduler needs. Lookups remain authoritative because the FROST
// GET-by-filter is the fallback on cache miss.
type MemEntityCache struct {
	m map[string]int64
}

// NewMemEntityCache returns an empty in-memory cache.
func NewMemEntityCache() *MemEntityCache {
	return &MemEntityCache{m: map[string]int64{}}
}

func (c *MemEntityCache) key(e Entity, name string) string { return string(e) + "|" + name }

func (c *MemEntityCache) Get(e Entity, name string) (int64, bool) {
	id, ok := c.m[c.key(e, name)]
	return id, ok
}

func (c *MemEntityCache) Put(e Entity, name string, id int64) {
	c.m[c.key(e, name)] = id
}

// Writer wraps one Target with the entity-upsert and observation-write
// algorithms from F1 and the design doc's API contract section.
type Writer struct {
	Target Target
	Cache  EntityCache
	Logger *slog.Logger
}

// NewWriter returns a Writer for the given Target with a fresh cache.
func NewWriter(t Target, logger *slog.Logger) *Writer {
	return &Writer{Target: t, Cache: NewMemEntityCache(), Logger: logger}
}

// GetOrCreate runs the upsert algorithm for a single entity:
//   1. Cache lookup → return.
//   2. GET ?$filter=name eq '<escaped>' → return on single hit.
//   3. POST to create → return new @iot.id.
//
// payloadBuilder is a closure so the caller can defer the cost of
// building the payload until step 3 (most calls are cache hits).
func (w *Writer) GetOrCreate(
	ctx context.Context,
	entity Entity,
	name string,
	postPath string,
	payloadBuilder func() any,
) (int64, error) {
	if id, ok := w.Cache.Get(entity, name); ok {
		return id, nil
	}

	id, err := w.Target.Client.FindByName(ctx, entity, name)
	if err != nil {
		var dup *DuplicateError
		if errors.As(err, &dup) {
			// Multiple matches — id is the lowest; log and proceed.
			w.Logger.Warn("sta duplicate entity",
				slog.String("entity", dup.Entity),
				slog.String("name", dup.Name),
				slog.Int("count", dup.Count),
				slog.String("target", w.Target.Label),
			)
			w.Cache.Put(entity, name, id)
			return id, nil
		}
		return 0, err
	}
	if id > 0 {
		w.Cache.Put(entity, name, id)
		return id, nil
	}

	// Cache miss + filter miss → create.
	id, err = w.Target.Client.Post(ctx, postPath, payloadBuilder())
	if err != nil {
		var ce *ConflictError
		if errors.As(err, &ce) {
			// Concurrent creation race — refetch once.
			id, err = w.Target.Client.FindByName(ctx, entity, name)
			if err != nil {
				return 0, err
			}
			if id == 0 {
				return 0, fmt.Errorf("frost: %s %q reported 409 but missing on refetch", entity, name)
			}
			w.Cache.Put(entity, name, id)
			return id, nil
		}
		return 0, err
	}
	w.Cache.Put(entity, name, id)
	return id, nil
}

// PostObservation POSTs to /Datastreams(<dsID>)/Observations with the
// pre-write idempotency probe.
//
// Returns:
//   - (id, true, nil) on a fresh successful create
//   - (0, false, nil) on idempotency hit (already present)
//   - (0, false, err) on failure (Transient or Permanent)
func (w *Writer) PostObservation(
	ctx context.Context,
	staDatastreamID int64,
	phenomenonTime time.Time,
	payload any,
) (int64, bool, error) {
	exists, err := w.Target.Client.ObservationExists(ctx, staDatastreamID, phenomenonTime)
	if err != nil {
		return 0, false, err
	}
	if exists {
		return 0, false, nil
	}

	// MQTT write path (F-MQTT): publish the create instead of POSTing.
	// The probe above already established the observation is absent, so a
	// successful publish counts as a create. MQTT returns no @iot.id, so
	// the id is 0 (callers only record it).
	if w.Target.MQTT != nil {
		if err := w.Target.MQTT.PublishObservation(ctx, staDatastreamID, payload); err != nil {
			return 0, false, err
		}
		return 0, true, nil
	}

	path := fmt.Sprintf("/Datastreams(%d)/Observations", staDatastreamID)
	id, err := w.Target.Client.Post(ctx, path, payload)
	if err != nil {
		var ce *ConflictError
		if errors.As(err, &ce) {
			// Racy duplicate — treat as already-present (idempotency holds).
			return 0, false, nil
		}
		return 0, false, err
	}
	return id, true, nil
}
