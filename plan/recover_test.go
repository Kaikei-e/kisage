package plan_test

import (
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/scope"
)

// TestCounterexamples06_07_23_25_44_PureRecoverResolvedCases verifies ADR 0003 §1, ADR 0004 §3,
// and Counterexamples #6, #7, #23, #25, #44:
//   - Pending Create (Origin=None) with observed == Target downgrades to Origin=Modified with K601 (#6, #23, #44).
//   - PendingCreate with observed == Absent rolls back by removing the reservation (#7).
//   - PendingSetUpdate on Origin=Created with observed == Target preserves Origin=Created; with observed == Expected restores Previous.
//   - PendingDelete with observed == Absent clears the record; with observed == Expected restores Previous (#25).
func TestCounterexamples06_07_23_25_44_PureRecoverResolvedCases(t *testing.T) {
	store := ownership.NewStore()

	// 1. PendingCreate where Target was written before crash (#6, #23, #44 -> Origin=Modified + K601).
	pCreateApplied, _ := ownership.NewPendingCreate(actorA, "b1", lkal.String("created-v"), 1)
	store = mustPut(t, store, "/app/create_applied", pCreateApplied)

	// 2. PendingCreate where crash happened before write (#7 -> Rollback removes reservation).
	pCreateRolledBack, _ := ownership.NewPendingCreate(actorA, "b1", lkal.String("never-written"), 1)
	store = mustPut(t, store, "/app/create_rolled_back", pCreateRolledBack)

	// 3. PendingSetUpdate on Origin=Created where Target was applied (preserves Origin=Created).
	settledCre, _ := ownership.NewSettledCreated(actorA, lkal.Int(1), 1)
	pUpdateApplied, _ := ownership.BeginSetUpdate(settledCre, "b1", lkal.Int(2), 2)
	store = mustPut(t, store, "/app/update_applied", pUpdateApplied)

	// 4. PendingDelete where Delete completed (Absent -> ClearDelete).
	pDeleteCleared, _ := ownership.BeginDelete(settledCre, "b1", 2)
	store = mustPut(t, store, "/app/delete_cleared", pDeleteCleared)

	// 5. PendingDelete where Delete did not happen (Expected -> Rollback restores Origin=Created).
	pDeleteRolledBack, _ := ownership.BeginDelete(settledCre, "b1", 2)
	store = mustPut(t, store, "/app/delete_rolled_back", pDeleteRolledBack)

	snap := mustSnap(t, map[string]observation.Observed{
		"/app":                    observation.PresentRecord(),
		"/app/create_applied":     observation.MustPresentLeaf(lkal.String("created-v")),
		"/app/create_rolled_back": observation.AbsentObserved(),
		"/app/update_applied":     observation.MustPresentLeaf(lkal.Int(2)),
		"/app/delete_cleared":     observation.AbsentObserved(),
		"/app/delete_rolled_back": observation.MustPresentLeaf(lkal.Int(1)),
	})

	rep, err := plan.PureRecover(store, testResID, actorA, snap, fullScope(t))
	if err != nil {
		t.Fatal(err)
	}
	if rep.HasUnresolved() || len(rep.Candidates) != 5 || !rep.RequiresStabilize || rep.Persisted {
		t.Fatalf("expected 5 resolved candidates and 0 unresolved (Persisted=false), got %+v", rep)
	}
	// Verify K601 CreatedOriginUnverified diagnostic on /app/create_applied.
	if !hasDiagnosticAtPath(rep.Diagnostics, "/app/create_applied", model.CodeCreatedOriginUnverified) {
		t.Fatalf("expected K601 on /app/create_applied, got %+v", rep.Diagnostics)
	}
	gotCreateApplied, ok := rep.VirtualStore.Get(testResID, model.MustParsePath("/app/create_applied"))
	if !ok || !gotCreateApplied.IsSettled() || gotCreateApplied.Origin != ownership.OriginModified {
		t.Fatalf("expected Origin=Modified on recovered create_applied, got %+v", gotCreateApplied)
	}
	if rep.VirtualStore.Has(testResID, model.MustParsePath("/app/create_rolled_back")) {
		t.Fatalf("expected create_rolled_back reservation to be removed from VirtualStore")
	}
	gotUpdateApplied, ok := rep.VirtualStore.Get(testResID, model.MustParsePath("/app/update_applied"))
	if !ok || !gotUpdateApplied.IsSettled() || gotUpdateApplied.Origin != ownership.OriginCreated {
		t.Fatalf("expected Origin=Created preserved on update_applied, got %+v", gotUpdateApplied)
	}
	if rep.VirtualStore.Has(testResID, model.MustParsePath("/app/delete_cleared")) {
		t.Fatalf("expected delete_cleared to be removed from VirtualStore")
	}
	gotDeleteRolledBack, ok := rep.VirtualStore.Get(testResID, model.MustParsePath("/app/delete_rolled_back"))
	if !ok || !gotDeleteRolledBack.IsSettled() || gotDeleteRolledBack.Origin != ownership.OriginCreated {
		t.Fatalf("expected Origin=Created restored on delete_rolled_back, got %+v", gotDeleteRolledBack)
	}
	// Caller's original store must remain completely unmutated.
	origEntry, _ := store.Get(testResID, model.MustParsePath("/app/create_applied"))
	if !origEntry.HasPending() {
		t.Fatalf("caller store was mutated by PureRecover")
	}
}

// TestCounterexamples24_25_40_UnresolvedRecoveryAndScopeCheck verifies ADR 0003 §1, ADR 0004 §3,
// and Counterexamples #24, #25, #40:
//   - Unknown observation retains Pending with K301.
//   - Third-party leaf value or ancestor structure conflict retains Pending with K205 (#24, #25).
//   - Missing PermOwn in Scope rejects recovery candidate with K401 PolicyDenied and retains Pending in VirtualStore (#40).
//   - Foreign Actor B's Pending entry is ignored by Actor A's PureRecover (#40).
func TestCounterexamples24_25_40_UnresolvedRecoveryAndScopeCheck(t *testing.T) {
	store := ownership.NewStore()
	pUnk, _ := ownership.NewPendingCreate(actorA, "b1", lkal.Int(1), 1)
	pThirdVal, _ := ownership.NewPendingCreate(actorA, "b1", lkal.Int(1), 1)
	pAncConflict, _ := ownership.NewPendingCreate(actorA, "b1", lkal.Int(1), 1)
	pNoOwn, _ := ownership.NewPendingCreate(actorA, "b1", lkal.Int(1), 1)
	pForeignB, _ := ownership.NewPendingCreate(actorB, "b2", lkal.Int(9), 1)

	store = mustPut(t, store, "/app/unk", pUnk)
	store = mustPut(t, store, "/app/third", pThirdVal)
	store = mustPut(t, store, "/bad_anc/leaf", pAncConflict)
	store = mustPut(t, store, "/denied/no_own", pNoOwn)
	store = mustPut(t, store, "/foreign/b", pForeignB)

	snap := mustSnap(t, map[string]observation.Observed{
		"/app":           observation.PresentRecord(),
		"/app/unk":       observation.UnknownObserved("timeout"),
		"/app/third":     observation.MustPresentLeaf(lkal.Int(999)),
		"/bad_anc":       observation.MustPresentLeaf(lkal.String("not-a-record")),
		"/bad_anc/leaf":  observation.AbsentObserved(),
		"/denied":        observation.PresentRecord(),
		"/denied/no_own": observation.AbsentObserved(),
	})

	// Grant Own only under /app and /bad_anc, NOT under /denied.
	r1, _ := scope.NewSubtreeRule(model.MustParsePath("/app"), scope.PermRegular)
	r2, _ := scope.NewSubtreeRule(model.MustParsePath("/bad_anc"), scope.PermRegular)
	sc := scope.MustNew(r1, r2)

	rep, err := plan.PureRecover(store, testResID, actorA, snap, sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Candidates) != 0 || len(rep.Unresolved) != 4 {
		t.Fatalf("expected 0 candidates and 4 unresolved items (foreign B ignored), got candidates=%d unresolved=%d", len(rep.Candidates), len(rep.Unresolved))
	}
	if !hasDiagnosticAtPath(rep.Diagnostics, "/app/unk", model.CodeObservationUnavailable) {
		t.Fatalf("missing K301 on /app/unk: %+v", rep.Diagnostics)
	}
	if !hasDiagnosticAtPath(rep.Diagnostics, "/app/third", model.CodePendingConflict) {
		t.Fatalf("missing K205 on /app/third: %+v", rep.Diagnostics)
	}
	if !hasDiagnosticAtPath(rep.Diagnostics, "/bad_anc/leaf", model.CodePendingConflict) {
		t.Fatalf("Counterexample #24 failed: missing K205 on ancestor structural conflict /bad_anc/leaf: %+v", rep.Diagnostics)
	}
	if !hasDiagnosticAtPath(rep.Diagnostics, "/denied/no_own", model.CodePolicyDenied) {
		t.Fatalf("Counterexample #40 failed: missing K401 on /denied/no_own lacking Own: %+v", rep.Diagnostics)
	}
	// Verify denied/no_own still has Pending in VirtualStore.
	deniedEntry, ok := rep.VirtualStore.Get(testResID, model.MustParsePath("/denied/no_own"))
	if !ok || !deniedEntry.HasPending() {
		t.Fatalf("expected /denied/no_own Pending to remain in VirtualStore when Own is denied")
	}
}

// TestCounterexample41_PlanPreviewIndependentModesOnSameVirtualStore verifies ADR 0003 §1
// and Counterexample #41: PlanPreview runs PureRecover once and evaluates Apply and Prune
// independently against the same VirtualStore (never simulating Apply -> Prune sequentially).
func TestCounterexample41_PlanPreviewIndependentModesOnSameVirtualStore(t *testing.T) {
	// Setup: /a is retired Origin=Modified, /a/b is declared in D.
	// In Apply, /a is unmanaged and /a/b is DeferredUntilRelease (K603).
	// In Prune (evaluated on the SAME virtual store where /a is STILL present, NOT after Apply),
	// /a is Deferred and /a/b is also Deferred (neither mode mutates the store or runs after the other).
	decl := mustDecl(t, `
a {
  b = "new"
}
`)
	store := ownership.NewStore()
	eMod, _ := ownership.NewSettledModified(actorA, lkal.String("old"), 1)
	eCre, _ := ownership.NewSettledCreated(actorA, lkal.String("del"), 2)
	store = mustPut(t, store, "/a", eMod)
	store = mustPut(t, store, "/ret/cre", eCre)

	snap := mustSnap(t, map[string]observation.Observed{
		"/a":       observation.PresentRecord(),
		"/a/b":     observation.AbsentObserved(),
		"/ret":     observation.PresentRecord(),
		"/ret/cre": observation.MustPresentLeaf(lkal.String("del")),
	})
	preview, err := plan.PlanPreview(plan.Input{
		Resource:    testResID,
		Actor:       actorA,
		Declaration: decl,
		Snapshot:    snap,
		Store:       store,
		Scope:       fullScope(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.DetailedExitCode() != 2 {
		t.Fatalf("expected DetailedExitCode 2, got %d", preview.DetailedExitCode())
	}
	// Both Apply and Prune must see /a in their PathResults from the same VirtualStore.
	if _, ok := findPathResult(preview.Apply.Gate.PathResults, "/a"); !ok {
		t.Fatalf("expected /a in Apply PathResults")
	}
	if _, ok := findPathResult(preview.Prune.Gate.PathResults, "/a"); !ok {
		t.Fatalf("Counterexample #41 failed: /a missing from Prune PathResults (Prune must use the same VirtualStore, not post-Apply store)")
	}
}
