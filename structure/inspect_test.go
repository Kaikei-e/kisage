package structure_test

import (
	"errors"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/structure"
)

func assertDiagnosticCode(t *testing.T, err error, want model.DiagnosticCode) model.Diagnostic {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	var diag model.Diagnostic
	if !errors.As(err, &diag) {
		t.Fatalf("expected model.Diagnostic (want %s), got %T: %v", want, err, err)
	}
	if diag.Code != want {
		t.Fatalf("expected diagnostic code %s, got %s (%v)", want, diag.Code, diag)
	}
	return diag
}

// TestCounterexample10_LeafRecordStructureConflict verifies Counterexample #10 (ADR 0001 §4):
// Existing Leaf vs. Record conflicts on target or proper ancestor are rejected with K202 (StructureConflict)
// without mutating existing leaves.
func TestCounterexample10_LeafRecordStructureConflict(t *testing.T) {
	t.Run("ancestor is Leaf when setting descendant", func(t *testing.T) {
		f, err := lkal.Parse("target.lkal", []byte("@lkal \"0.1\"\na = 1\n"))
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}

		p := model.MustParsePath("/a/b")
		err = structure.ApplyLeafSet(f, p, lkal.Int(2))
		diag := assertDiagnosticCode(t, err, model.CodeStructureConflict)
		if !diag.Path.Equal(p) {
			t.Fatalf("expected diagnostic path %s, got %s", p, diag.Path)
		}

		got, err := f.Get("a")
		if err != nil || !lkal.Equal(got, lkal.Int(1)) {
			t.Fatalf("expected /a = 1 to be preserved, got %v (err=%v)", got, err)
		}
	})

	t.Run("target is Record when setting Leaf", func(t *testing.T) {
		f, err := lkal.Parse("target.lkal", []byte("@lkal \"0.1\"\na {\n  b = 1\n}\n"))
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}

		p := model.MustParsePath("/a")
		err = structure.ApplyLeafSet(f, p, lkal.Int(99))
		diag := assertDiagnosticCode(t, err, model.CodeStructureConflict)
		if !diag.Path.Equal(p) {
			t.Fatalf("expected diagnostic path %s, got %s", p, diag.Path)
		}

		got, err := f.Get("a/b")
		if err != nil || !lkal.Equal(got, lkal.Int(1)) {
			t.Fatalf("expected /a/b = 1 to be preserved, got %v (err=%v)", got, err)
		}
	})

	t.Run("target is Record when deleting Leaf", func(t *testing.T) {
		f, err := lkal.Parse("target.lkal", []byte("@lkal \"0.1\"\na {\n  b = 1\n}\n"))
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}

		p := model.MustParsePath("/a")
		err = structure.ApplyLeafDelete(f, p)
		assertDiagnosticCode(t, err, model.CodeStructureConflict)

		got, err := f.Get("a/b")
		if err != nil || !lkal.Equal(got, lkal.Int(1)) {
			t.Fatalf("expected /a/b = 1 to be preserved, got %v (err=%v)", got, err)
		}
	})
}

// TestCounterexample03_UnknownObservationHold verifies Counterexample #3 (ADR 0001 §3–4):
// Unknown on target or proper ancestor emits K301 (ObservationUnavailable) and blocks Set/Delete.
func TestCounterexample03_UnknownObservationHold(t *testing.T) {
	pAB := model.MustParsePath("/a/b")

	t.Run("missing snapshot entry emits K301", func(t *testing.T) {
		snap := observation.NewSnapshot()
		res := structure.InspectPath(snap, pAB)
		if !res.HasUnknown() || res.CanSet() || res.CanDelete() {
			t.Fatalf("expected Unknown check to block Set and Delete: %+v", res)
		}
		assertDiagnosticCode(t, res.Err(), model.CodeObservationUnavailable)
	})

	t.Run("target Unknown with Present(Record) ancestor emits K301", func(t *testing.T) {
		snap := observation.NewSnapshot()
		snap.Set(model.MustParsePath("/a"), observation.PresentRecord())
		snap.Set(pAB, observation.UnknownObserved("permission denied on /a/b"))

		err := structure.ValidateSet(snap, pAB, lkal.Int(1))
		assertDiagnosticCode(t, err, model.CodeObservationUnavailable)

		err = structure.ValidateDelete(snap, pAB)
		assertDiagnosticCode(t, err, model.CodeObservationUnavailable)
	})
}

// TestCounterexample10_AbsentTargetNeverSkipsAncestorCheck verifies Counterexample #10 (ADR 0001 §4):
// Even when o(p) == Absent, proper ancestors q ≺ p must still be checked for Leaf, Unknown, and Opaque.
func TestCounterexample10_AbsentTargetNeverSkipsAncestorCheck(t *testing.T) {
	pA := model.MustParsePath("/a")
	pAB := model.MustParsePath("/a/b")

	t.Run("o(/a/b)=Absent with o(/a)=Present(Leaf) emits K202", func(t *testing.T) {
		snap := observation.NewSnapshot()
		snap.Set(pA, observation.MustPresentLeaf(lkal.Int(1)))
		snap.Set(pAB, observation.AbsentObserved())

		res := structure.InspectPath(snap, pAB)
		if res.CanSet() {
			t.Fatalf("CanSet must be false when proper ancestor /a is Leaf")
		}
		assertDiagnosticCode(t, res.Err(), model.CodeStructureConflict)
	})

	t.Run("o(/a/b)=Absent with o(/a)=Unknown emits K301", func(t *testing.T) {
		snap := observation.NewSnapshot()
		snap.Set(pA, observation.UnknownObserved("timeout reading /a"))
		snap.Set(pAB, observation.AbsentObserved())

		res := structure.InspectPath(snap, pAB)
		if res.CanSet() {
			t.Fatalf("CanSet must be false when proper ancestor /a is Unknown")
		}
		assertDiagnosticCode(t, res.Err(), model.CodeObservationUnavailable)
	})

	t.Run("o(/a/b)=Absent with o(/a)=Present(Opaque) emits K302", func(t *testing.T) {
		snap := observation.NewSnapshot()
		snap.Set(pA, observation.PresentOpaque("custom binary blob at /a"))
		snap.Set(pAB, observation.AbsentObserved())

		res := structure.InspectPath(snap, pAB)
		if res.CanSet() {
			t.Fatalf("CanSet must be false when proper ancestor /a is Opaque")
		}
		assertDiagnosticCode(t, res.Err(), model.CodeUnrepresentable)
	})

	t.Run("K301 takes precedence over K202/K302 on the same path", func(t *testing.T) {
		pABC := model.MustParsePath("/a/b/c")
		snap := observation.NewSnapshot()
		snap.Set(pA, observation.MustPresentLeaf(lkal.Int(1)))
		snap.Set(pAB, observation.UnknownObserved("RBAC denied on /a/b"))
		snap.Set(pABC, observation.PresentOpaque("opaque at /a/b/c"))

		res := structure.InspectPath(snap, pABC)
		if res.PrimaryCode != model.CodeObservationUnavailable {
			t.Fatalf("expected PrimaryCode K301, got %s", res.PrimaryCode)
		}
		assertDiagnosticCode(t, res.Err(), model.CodeObservationUnavailable)
	})
}

// TestCounterexample10_OpaqueTargetOrAncestorRejected verifies Counterexample #10 (ADR 0001 §3–4):
// Present(Opaque(reason)) on target or proper ancestor is rejected with K302 (Unrepresentable).
func TestCounterexample10_OpaqueTargetOrAncestorRejected(t *testing.T) {
	pA := model.MustParsePath("/a")
	pAB := model.MustParsePath("/a/b")

	t.Run("target is Opaque", func(t *testing.T) {
		snap := observation.NewSnapshot()
		snap.Set(pA, observation.PresentRecord())
		snap.Set(pAB, observation.PresentOpaque("non-UTF8 binary secret"))

		err := structure.ValidateSet(snap, pAB, lkal.String("override"))
		assertDiagnosticCode(t, err, model.CodeUnrepresentable)

		err = structure.ValidateDelete(snap, pAB)
		assertDiagnosticCode(t, err, model.CodeUnrepresentable)
	})

	t.Run("proper ancestor is Opaque", func(t *testing.T) {
		snap := observation.NewSnapshot()
		snap.Set(pA, observation.PresentOpaque("unsupported custom CRD array-of-objects"))
		snap.Set(pAB, observation.MustPresentLeaf(lkal.Int(1)))

		err := structure.ValidateSet(snap, pAB, lkal.Int(2))
		assertDiagnosticCode(t, err, model.CodeUnrepresentable)

		err = structure.ValidateDelete(snap, pAB)
		assertDiagnosticCode(t, err, model.CodeUnrepresentable)
	})
}
