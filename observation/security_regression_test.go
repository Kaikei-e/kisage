package observation_test

import (
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
)

func TestSecurityRegression_GC08_DuplicateRecordKeyPurgesDescendantsAndOpaqueWitness(t *testing.T) {
	rec := lkal.Record{
		Fields: []lkal.Field{
			{
				Key: "a",
				Value: lkal.Record{
					Fields: []lkal.Field{
						{Key: "b", Value: lkal.String("ghost")},
					},
				},
			},
			{
				Key:   "a",
				Value: lkal.String("leaf-override"),
			},
		},
	}
	snap := observation.ObserveRecord(rec)
	if !snap.Get(model.MustParsePath("/a")).IsLeaf() {
		t.Fatalf("expected /a to be Present(Leaf), got %v", snap.Get(model.MustParsePath("/a")))
	}
	if snap.Has(model.MustParsePath("/a/b")) {
		t.Fatalf("expected shadowed descendant /a/b to be purged from snapshot, got %v", snap.Get(model.MustParsePath("/a/b")))
	}
	queriedSnap := observation.ObserveRecord(rec, model.MustParsePath("/a/b"))
	if !queriedSnap.Get(model.MustParsePath("/a/b")).IsAbsent() {
		t.Fatalf("expected queried shadowed descendant /a/b to be Absent, got %v", queriedSnap.Get(model.MustParsePath("/a/b")))
	}

	// Distinct heterogeneous lists must produce distinct Opaque witnesses so Observed.Equal is false.
	opaqueRec1 := lkal.Record{
		Fields: []lkal.Field{
			{Key: "x", Value: lkal.List{lkal.Int(1), lkal.String("a")}},
		},
	}
	opaqueRec2 := lkal.Record{
		Fields: []lkal.Field{
			{Key: "x", Value: lkal.List{lkal.Int(1), lkal.String("b")}},
		},
	}
	obs1 := observation.ObserveRecord(opaqueRec1).Get(model.MustParsePath("/x"))
	obs2 := observation.ObserveRecord(opaqueRec2).Get(model.MustParsePath("/x"))
	if !obs1.IsOpaque() || !obs2.IsOpaque() {
		t.Fatalf("expected both /x observations to be Opaque, got %v and %v", obs1, obs2)
	}
	if obs1.Equal(obs2) {
		t.Fatal("expected distinct Opaque values to not be Equal")
	}
}
