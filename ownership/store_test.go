package ownership_test

import (
	"slices"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/projection"
)

// TestCounterexample21_ActorProjectionAndEvaluationScope verifies ADR 0002 §1 and Counterexample #21:
//   - ProjectActor extracts σ_A (only entries with Manager == A).
//   - ComputeEvaluationScope returns M_A(d, σ) = D ∪ dom(σ_A).
//   - Actor B's non-overlapping paths in dom(σ) \ dom(σ_A) are excluded from M_A(d, σ) and produce no conflicts.
func TestCounterexample21_ActorProjectionAndEvaluationScope(t *testing.T) {
	const resID ownership.ResourceID = "res-main"
	actorA := ownership.ActorID("agent-a")
	actorB := ownership.ActorID("agent-b")

	pDeclA := model.MustParsePath("/app/name")
	pRetiredA := model.MustParsePath("/app/legacy")
	pUnrelatedB := model.MustParsePath("/other/secret")

	entryA, err := ownership.NewSettledCreated(actorA, lkal.String("old"), 1)
	if err != nil {
		t.Fatal(err)
	}
	entryB, err := ownership.NewSettledCreated(actorB, lkal.String("b-val"), 1)
	if err != nil {
		t.Fatal(err)
	}

	store := ownership.NewStore()
	store, err = store.Put(resID, pRetiredA, entryA)
	if err != nil {
		t.Fatal(err)
	}
	store, err = store.Put(resID, pUnrelatedB, entryB)
	if err != nil {
		t.Fatal(err)
	}

	decl, err := projection.ParseDeclaration("a.lkal", []byte("@lkal \"0.1\"\napp {\n  name = \"kisage\"\n}\n"))
	if err != nil {
		t.Fatal(err)
	}

	sigmaA, err := ownership.ProjectActor(store, resID, actorA)
	if err != nil {
		t.Fatal(err)
	}
	if sigmaA.Len() != 1 || !sigmaA.Has(pRetiredA) || sigmaA.Has(pUnrelatedB) {
		t.Fatalf("unexpected σ_A domain: %v", sigmaA.Domain())
	}

	evalPaths := ownership.ComputeEvaluationScope(decl, sigmaA)
	wantPaths := []model.Path{pRetiredA, pDeclA}
	if !slices.EqualFunc(evalPaths, wantPaths, model.Path.Equal) {
		t.Fatalf("M_A(d, σ) = %v, want %v", evalPaths, wantPaths)
	}

	diags := ownership.InspectDeclaredConflicts(store, resID, actorA, decl)
	if len(diags) != 0 {
		t.Fatalf("expected 0 conflicts from unrelated Actor B path, got %v", diags)
	}

	// Verify Actor B's entry in store remains completely intact.
	gotB, ok := store.Get(resID, pUnrelatedB)
	if !ok || !gotB.Equal(entryB) {
		t.Fatalf("Actor B entry was not preserved: %+v (ok=%v)", gotB, ok)
	}
}

// TestCounterexamples08And30_ForeignOwnershipAndOverlapExclusivity verifies ADR 0002 §1 and Counterexamples #8 & #30:
//   - #8: If a declared path is owned by another Actor in σ, it is K201 OwnershipConflict even if observation is Absent.
//   - #30: Same-path (even with equal value), proper ancestor, or proper descendant ownership by another Actor
//     produces K201 OwnershipConflict and never creates shared ownership or dual reservation in σ.
func TestCounterexamples08And30_ForeignOwnershipAndOverlapExclusivity(t *testing.T) {
	const resID ownership.ResourceID = "res-main"
	actorA := ownership.ActorID("agent-a")
	actorB := ownership.ActorID("agent-b")

	entryB, err := ownership.NewSettledCreated(actorB, lkal.Int(8080), 1)
	if err != nil {
		t.Fatal(err)
	}
	store := ownership.NewStore()
	store, err = store.Put(resID, model.MustParsePath("/service/http"), entryB)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Counterexample #8 & #30 (same path, even if observation is Absent or value is identical 8080):
	pSame := model.MustParsePath("/service/http")
	diag, conflict := ownership.DiagnoseForeignConflict(store, resID, actorA, pSame)
	if !conflict || diag.Code != model.CodeOwnershipConflict || !diag.Path.Equal(pSame) {
		t.Fatalf("expected K201 on same path owned by Actor B, got %+v (conflict=%v)", diag, conflict)
	}
	entryASameVal, err := ownership.NewSettledModified(actorA, lkal.Int(8080), 2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Put(resID, pSame, entryASameVal)
	assertDiagnosticCode(t, err, model.CodeOwnershipConflict)

	// 2. Counterexample #30 (Actor B owns proper ancestor /service/http, Actor A declares descendant /service/http/port):
	pDescendant := model.MustParsePath("/service/http/port")
	diag, conflict = ownership.DiagnoseForeignConflict(store, resID, actorA, pDescendant)
	if !conflict || diag.Code != model.CodeOwnershipConflict || !diag.Path.Equal(pDescendant) {
		t.Fatalf("expected K201 when Actor B owns proper ancestor, got %+v", diag)
	}
	_, err = store.Put(resID, pDescendant, entryASameVal)
	assertDiagnosticCode(t, err, model.CodeOwnershipConflict)

	// 3. Counterexample #30 (Actor B owns proper descendant /service/http, Actor A declares ancestor /service):
	pAncestor := model.MustParsePath("/service")
	diag, conflict = ownership.DiagnoseForeignConflict(store, resID, actorA, pAncestor)
	if !conflict || diag.Code != model.CodeOwnershipConflict || !diag.Path.Equal(pAncestor) {
		t.Fatalf("expected K201 when Actor B owns proper descendant, got %+v", diag)
	}
	_, err = store.Put(resID, pAncestor, entryASameVal)
	assertDiagnosticCode(t, err, model.CodeOwnershipConflict)
}

// TestCounterexample40_ForeignPendingIsolationAndOverlapRejection verifies ADR 0002 §1, ADR 0004 §3, and Counterexample #40:
//   - While Actor B has an unresolved Pending reservation (including Origin=None), Actor A's σ_A has no Pending paths
//     and Actor A can update non-overlapping paths while preserving B's Pending reservation.
//   - If Actor A targets a path overlapping B's Pending reservation, it is rejected with K201 OwnershipConflict.
func TestCounterexample40_ForeignPendingIsolationAndOverlapRejection(t *testing.T) {
	const resID ownership.ResourceID = "res-main"
	actorA := ownership.ActorID("agent-a")
	actorB := ownership.ActorID("agent-b")

	pPendingB := model.MustParsePath("/worker/queue/size")
	pNonOverlappingA := model.MustParsePath("/worker/concurrency")
	pOverlappingA := model.MustParsePath("/worker/queue")

	pendingB, err := ownership.NewPendingCreate(actorB, "batch-b", lkal.Int(100), 1)
	if err != nil {
		t.Fatal(err)
	}
	store := ownership.NewStore()
	store, err = store.Put(resID, pPendingB, pendingB)
	if err != nil {
		t.Fatal(err)
	}

	// Actor A's projection σ_A has zero Pending paths so B's Pending does not halt A's recovery gate.
	sigmaA, err := ownership.ProjectActor(store, resID, actorA)
	if err != nil {
		t.Fatal(err)
	}
	if len(sigmaA.PendingPaths()) != 0 {
		t.Fatalf("expected 0 pending paths in σ_A, got %v", sigmaA.PendingPaths())
	}

	// Non-overlapping path for Actor A succeeds and preserves Actor B's Pending reservation.
	if _, conflict := ownership.DiagnoseForeignConflict(store, resID, actorA, pNonOverlappingA); conflict {
		t.Fatal("unexpected conflict on non-overlapping path")
	}
	entryA, err := ownership.NewSettledCreated(actorA, lkal.Int(4), 1)
	if err != nil {
		t.Fatal(err)
	}
	store, err = store.Put(resID, pNonOverlappingA, entryA)
	if err != nil {
		t.Fatalf("expected non-overlapping Put by Actor A to succeed, got %v", err)
	}
	gotB, ok := store.Get(resID, pPendingB)
	if !ok || !gotB.Equal(pendingB) {
		t.Fatalf("Actor B Pending reservation was not preserved: %+v", gotB)
	}

	// Overlapping path (ancestor /worker/queue of B's /worker/queue/size) is rejected with K201.
	diag, conflict := ownership.DiagnoseForeignConflict(store, resID, actorA, pOverlappingA)
	if !conflict || diag.Code != model.CodeOwnershipConflict {
		t.Fatalf("expected K201 on overlapping Pending path, got %+v (conflict=%v)", diag, conflict)
	}
	_, err = store.Put(resID, pOverlappingA, entryA)
	assertDiagnosticCode(t, err, model.CodeOwnershipConflict)
}

// TestCounterexample38_MonotonicRevisionAcrossUnownedAndReleasedStates verifies ADR 0003 §3 and Counterexample #38:
//   - Unowned paths have an explicit revision (starting at 0).
//   - Creating, updating, and deleting/releasing an entry monotonically increments the path's revision,
//     preventing a stale --owner-revision=0 from matching after an unowned -> owned -> released cycle.
func TestCounterexample38_MonotonicRevisionAcrossUnownedAndReleasedStates(t *testing.T) {
	const resID ownership.ResourceID = "res-main"
	actorA := ownership.ActorID("agent-a")
	p := model.MustParsePath("/feature/flag")

	store := ownership.NewStore()
	if rev := store.Revision(resID, p); rev != 0 {
		t.Fatalf("expected initial unowned revision 0, got %d", rev)
	}
	if err := store.VerifyRevision(resID, p, 0); err != nil {
		t.Fatalf("expected VerifyRevision(0) to pass initially: %v", err)
	}

	entry, err := ownership.NewSettledModified(actorA, lkal.Bool(true), 0)
	if err != nil {
		t.Fatal(err)
	}
	store, err = store.Put(resID, p, entry)
	if err != nil {
		t.Fatal(err)
	}
	if rev := store.Revision(resID, p); rev != 1 {
		t.Fatalf("expected revision 1 after Put, got %d", rev)
	}

	// Delete (release) the entry so the path is unowned again; revision must advance to 2, not reset to 0!
	store, err = store.Delete(resID, p)
	if err != nil {
		t.Fatal(err)
	}
	if store.Has(resID, p) {
		t.Fatal("expected entry to be deleted")
	}
	if rev := store.Revision(resID, p); rev != 2 {
		t.Fatalf("expected unowned revision after release to be 2, got %d", rev)
	}
	assertDiagnosticCode(t, store.VerifyRevision(resID, p, 0), model.CodeOwnershipConflict)
	assertDiagnosticCode(t, store.VerifyRevision(resID, p, 1), model.CodeOwnershipConflict)
	if err := store.VerifyRevision(resID, p, 2); err != nil {
		t.Fatalf("expected VerifyRevision(2) to succeed: %v", err)
	}
}

