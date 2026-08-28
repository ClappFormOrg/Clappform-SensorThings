package frost

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingRT is a fake http.RoundTripper: every POST is counted and returns
// 201 with a Location header; every GET (the FindByName probe) reports "not
// found" after a small delay that widens the concurrent-miss window.
type countingRT struct {
	mu    sync.Mutex
	posts int
	gets  int
}

func (r *countingRT) counts() (postN, getN int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.posts, r.gets
}

func (r *countingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost {
		r.mu.Lock()
		r.posts++
		r.mu.Unlock()
		h := http.Header{}
		h.Set("Location", "http://fake/Sensors(1)")
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     h,
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	}
	// GET = FindByName probe. Sleep so every racing goroutine clears the probe
	// before any POST lands, then report an empty result set (not found).
	r.mu.Lock()
	r.gets++
	r.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"value":[]}`)),
		Request:    req,
	}, nil
}

// TestGetOrCreateConcurrentDedup asserts that many goroutines resolving the
// SAME shared entity name concurrently collapse to exactly one POST — the
// regression guard for the duplicate ObservedProperty/Sensor bug. Run under
// `-race` it also guards the entity-cache map against concurrent access.
func TestGetOrCreateConcurrentDedup(t *testing.T) {
	rt := &countingRT{}
	c := NewClient("http://fake", Auth{}, false, 5*time.Second)
	c.HTTP.Transport = rt

	w := NewWriter(Target{Label: "test", Client: c}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	const n = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	ids := make([]int64, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all goroutines together
			ids[i], errs[i] = w.GetOrCreate(
				context.Background(),
				EntitySensors,
				"Shared Sensor",
				"/Sensors",
				func() any { return map[string]any{"name": "Shared Sensor"} },
			)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	posts, _ := rt.counts()
	if posts != 1 {
		t.Fatalf("expected exactly 1 POST for a shared entity under concurrency, got %d "+
			"(upsert race not deduped)", posts)
	}
	for i, id := range ids {
		if id != 1 {
			t.Fatalf("goroutine %d got id %d, want 1 (all callers should share the created id)", i, id)
		}
	}
}
