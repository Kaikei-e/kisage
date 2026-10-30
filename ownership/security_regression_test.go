package ownership_test

import (
	"math"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
)

func TestSecurityRegression_GC04_PendingRevisionHeadroom(t *testing.T) {
	if _, err := ownership.NewPendingCreate("actor-a", "batch-1", lkal.String("v"), math.MaxUint64); err == nil {
		t.Fatal("expected NewPendingCreate at math.MaxUint64 to be rejected to preserve resolution revision headroom")
	}
	settled, err := ownership.NewSettledCreated("actor-a", lkal.String("v"), math.MaxUint64-1)
	if err != nil {
		t.Fatalf("unexpected error at MaxUint64-1: %v", err)
	}
	if _, err := ownership.BeginSetUpdate(settled, "batch-2", lkal.String("v2"), math.MaxUint64); err == nil {
		t.Fatal("expected BeginSetUpdate at math.MaxUint64 to be rejected")
	}
	store := ownership.NewStore()
	if _, err := store.Put("res-1", model.MustParsePath("/a/b"), settled); err != nil {
		t.Fatalf("expected settled entry at MaxUint64-1 to succeed: %v", err)
	}
}
