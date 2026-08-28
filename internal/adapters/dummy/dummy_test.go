package dummy

import (
	"context"
	"testing"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestListThingsCount(t *testing.T) {
	a := New(3, time.Minute, fixedClock(time.Unix(1_700_000_000, 0)))
	things, err := a.ListThings(context.Background())
	if err != nil {
		t.Fatalf("ListThings: %v", err)
	}
	if len(things) != 3 {
		t.Fatalf("got %d things, want 3", len(things))
	}
	for _, th := range things {
		if th.VendorID != VendorID {
			t.Errorf("VendorID = %q, want %q", th.VendorID, VendorID)
		}
		if th.VendorNativeID == "" {
			t.Error("empty VendorNativeID")
		}
	}
}

func TestFetchObservationsBoundedAndInRange(t *testing.T) {
	now := time.Unix(1_700_003_600, 0).UTC() // 1h after `since`
	a := New(1, 5*time.Minute, fixedClock(now))
	since := now.Add(-time.Hour)

	obs, err := a.FetchObservations(context.Background(), "DUMMY-0001", canonical.FillLevel, since, 1000)
	if err != nil {
		t.Fatalf("FetchObservations: %v", err)
	}
	// 1h window at 5-min cadence → ~12 readings.
	if len(obs) < 10 || len(obs) > 13 {
		t.Fatalf("got %d observations, want ~12", len(obs))
	}
	for i, o := range obs {
		if !o.PhenomenonTime.After(since) {
			t.Errorf("obs[%d] phenomenonTime %v not after cursor %v", i, o.PhenomenonTime, since)
		}
		if o.PhenomenonTime.After(now) {
			t.Errorf("obs[%d] phenomenonTime %v after now %v", i, o.PhenomenonTime, now)
		}
		if o.Result < 0 || o.Result > 100 {
			t.Errorf("obs[%d] result %v out of [0,100]", i, o.Result)
		}
		if i > 0 && !o.PhenomenonTime.After(obs[i-1].PhenomenonTime) {
			t.Errorf("obs not strictly increasing at %d", i)
		}
	}
}

func TestFetchObservationsDeterministic(t *testing.T) {
	now := time.Unix(1_700_003_600, 0).UTC()
	since := now.Add(-30 * time.Minute)
	a := New(1, 5*time.Minute, fixedClock(now))

	first, _ := a.FetchObservations(context.Background(), "DUMMY-0001", canonical.FillLevel, since, 1000)
	second, _ := a.FetchObservations(context.Background(), "DUMMY-0001", canonical.FillLevel, since, 1000)
	if len(first) != len(second) {
		t.Fatalf("non-deterministic count: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Result != second[i].Result || !first[i].PhenomenonTime.Equal(second[i].PhenomenonTime) {
			t.Fatalf("non-deterministic obs at %d: %+v vs %+v", i, first[i], second[i])
		}
		if first[i].RawObservationID != second[i].RawObservationID {
			t.Fatalf("non-deterministic raw id at %d", i)
		}
	}
}

func TestFetchObservationsIgnoresNonFillLevel(t *testing.T) {
	now := time.Unix(1_700_003_600, 0).UTC()
	a := New(1, 5*time.Minute, fixedClock(now))
	obs, err := a.FetchObservations(context.Background(), "DUMMY-0001", canonical.Temperature, now.Add(-time.Hour), 1000)
	if err != nil {
		t.Fatalf("FetchObservations: %v", err)
	}
	if len(obs) != 0 {
		t.Fatalf("expected no observations for non-fill-level OP, got %d", len(obs))
	}
}
