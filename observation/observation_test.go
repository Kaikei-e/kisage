package observation_test

import (
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
)

// TestCounterexample47_ObservedEmptyRecordIsPresentRecord verifies the observation side of Counterexample #47 (ADR 0001 §2):
// Observed empty Record in target environment o is treated normally as Present(Record) without K102.
func TestCounterexample47_ObservedEmptyRecordIsPresentRecord(t *testing.T) {
	f, err := lkal.Parse("target.lkal", []byte("@lkal \"0.1\"\na {}\n"))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	pA := model.MustParsePath("/a")
	pAB := model.MustParsePath("/a/b")
	snap, err := observation.ObserveFile(f, pAB)
	if err != nil {
		t.Fatalf("ObserveFile must not fail on observed empty Record: %v", err)
	}

	obsA := snap.Get(pA)
	if !obsA.IsPresent() || !obsA.IsRecord() {
		t.Fatalf("expected /a to be Present(Record), got %s", obsA)
	}
	obsAB := snap.Get(pAB)
	if !obsAB.IsAbsent() {
		t.Fatalf("expected /a/b to be Absent, got %s", obsAB)
	}
}

// TestCounterexample03_UnknownObservationFailClosed verifies Counterexample #3 (ADR 0001 §3):
// Missing or uninitialized observations default to Unknown and never to Absent.
func TestCounterexample03_UnknownObservationFailClosed(t *testing.T) {
	pAB := model.MustParsePath("/a/b")

	t.Run("missing snapshot entry defaults to Unknown", func(t *testing.T) {
		snap := observation.NewSnapshot()
		obs := snap.Get(pAB)
		if !obs.IsUnknown() {
			t.Fatalf("missing observation must be Unknown, got %s", obs)
		}
	})

	t.Run("zero-value Observed is Unknown", func(t *testing.T) {
		var zero observation.Observed
		if !zero.IsUnknown() || zero.IsAbsent() || zero.IsPresent() {
			t.Fatalf("zero-value Observed must be Unknown, got %s", zero)
		}
	})

	t.Run("PresentObserved with uninitialized Node fails closed to Unknown", func(t *testing.T) {
		invalidPresent := observation.PresentObserved(observation.Node{})
		if !invalidPresent.IsUnknown() {
			t.Fatalf("expected PresentObserved(Node{}) to fail closed to Unknown, got %s", invalidPresent)
		}
	})
}

func TestObservationEquality(t *testing.T) {
	obs1 := observation.MustPresentLeaf(lkal.Decimal("1.20"))
	obs2 := observation.MustPresentLeaf(lkal.Decimal("1.2"))
	if !obs1.Equal(obs2) {
		t.Fatalf("expected canonical Decimal leaves to be equal: %s vs %s", obs1, obs2)
	}
}
