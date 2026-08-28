package adapters

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
)

// stubPoll / stubPush are minimal adapters for registry tests.
type stubPoll struct{ id string }

func (s stubPoll) VendorID() string                                      { return s.id }
func (s stubPoll) ListThings(context.Context) ([]canonical.Thing, error) { return nil, nil }
func (s stubPoll) ListDatastreamsForThing(context.Context, string) ([]canonical.Datastream, error) {
	return nil, nil
}
func (s stubPoll) FetchObservations(context.Context, string, canonical.ObservedProperty, time.Time, int) ([]canonical.Observation, error) {
	return nil, nil
}

type stubPush struct{ id string }

func (s stubPush) VendorID() string                         { return s.id }
func (s stubPush) Authenticate(*http.Request, []byte) error { return nil }
func (s stubPush) DecodePush(context.Context, []byte) (DecodedBatch, error) {
	return DecodedBatch{}, nil
}

func TestRegistryPartitionsByMode(t *testing.T) {
	r := NewRegistry()
	r.RegisterPoll(stubPoll{id: "sulo"})
	r.RegisterPush(stubPush{id: "acme"})

	if got := len(r.PollAdapters()); got != 1 {
		t.Fatalf("PollAdapters len = %d, want 1", got)
	}
	if got := len(r.PushAdapters()); got != 1 {
		t.Fatalf("PushAdapters len = %d, want 1", got)
	}
	if r.Len() != 2 {
		t.Fatalf("Len = %d, want 2", r.Len())
	}
	if r.PushAdapter("acme") == nil {
		t.Fatal("PushAdapter(acme) = nil, want the registered push adapter")
	}
	// A poll vendor is not resolvable as a push adapter.
	if r.PushAdapter("sulo") != nil {
		t.Fatal("PushAdapter(sulo) should be nil — sulo is poll mode")
	}
	// Unknown vendor.
	if r.PushAdapter("nope") != nil {
		t.Fatal("PushAdapter(nope) should be nil")
	}
}

func TestRegistryRejectsDuplicateVendorAcrossModes(t *testing.T) {
	r := NewRegistry()
	r.RegisterPoll(stubPoll{id: "sulo"})

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic registering a push adapter with an already-claimed vendor id")
		}
	}()
	r.RegisterPush(stubPush{id: "sulo"}) // same id, different mode → panic
}
