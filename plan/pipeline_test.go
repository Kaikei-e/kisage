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

// TestCounterexamples02_11_12_57_CreationAdoptionAndConvergence verifies ADR 0003 §1–§2
// and Counterexamples #2, #11, #12, #57.
func TestCounterexamples02_11_12_57_CreationAdoptionAndConvergence(t *testing.T) {
	decl := mustDecl(t, `
app {
  created_key = "alpha"
  adopted_key = 42
}
`)
	snap := mustSnap(t, map[string]observation.Observed{
		"/app":             observation.PresentRecord(),
		"/app/created_key": observation.AbsentObserved(),
		"/app/adopted_key": observation.MustPresentLeaf(lkal.Int(42)),
	})
	sc := fullScope(t)
	store := ownership.NewStore()
	in := plan.Input{
		Resource:    testResID,
		Actor:       actorA,
		Declaration: decl,
		Snapshot:    snap,
		Store:       store,
		Scope:       sc,
	}

	preview, err := plan.PlanPreview(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := preview.DetailedExitCode(); got != 2 {
		t.Fatalf("DetailedExitCode() = %d, want 2", got)
	}

	res, err := plan.Plan(in, plan.ModeApply)
	if err != nil {
		t.Fatal(err)
	}
	if res.Gate.Blocked || len(res.Gate.Changes) != 1 || len(res.Gate.MetadataCandidates) != 1 {
		t.Fatalf("expected 1 env change and 1 metadata candidate, got %+v", res.Gate)
	}
	// Counterexample #2: created_key is PendingOpSet with Expected=Absent and ActionCreate.
	if res.Gate.Changes[0].Op != ownership.PendingOpSet || !res.Gate.Changes[0].Expected.IsAbsent() {
		t.Fatalf("expected creation Set op from Absent, got %+v", res.Gate.Changes[0])
	}
	// Counterexample #11: adopted_key is ActionAdoptModified metadata candidate with Origin=Modified.
	metaCand := res.Gate.MetadataCandidates[0]
	if metaCand.Action != scope.ActionAdoptModified || metaCand.After == nil || metaCand.After.Origin != ownership.OriginModified {
		t.Fatalf("expected ActionAdoptModified + OriginModified, got %+v", metaCand)
	}

	// Counterexample #57: When ONLY metadata-only adoption remains, RequiresDurableIntent and RequiresApplyBatch are false,
	// while RequiresStabilize is true.
	snapOnlyAdopt := mustSnap(t, map[string]observation.Observed{
		"/app":             observation.PresentRecord(),
		"/app/created_key": observation.MustPresentLeaf(lkal.String("alpha")),
		"/app/adopted_key": observation.MustPresentLeaf(lkal.Int(42)),
	})
	resAdoptOnly, err := plan.Plan(plan.Input{
		Resource:    testResID,
		Actor:       actorA,
		Declaration: decl,
		Snapshot:    snapOnlyAdopt,
		Store:       store,
		Scope:       sc,
	}, plan.ModeApply)
	if err != nil {
		t.Fatal(err)
	}
	if resAdoptOnly.Gate.RequiresDurableIntent() || resAdoptOnly.Gate.RequiresApplyBatch() || !resAdoptOnly.Gate.RequiresStabilize() {
		t.Fatalf("Counterexample #57 failed: metadata-only batch must skip DurableIntent & ApplyBatch but require Stabilize: %+v", resAdoptOnly.Gate)
	}

	// Commit Result to store and re-run on converged snapshot (Counterexample #12: Fixed-Point).
	nextStore, err := res.ApplyToStore()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.MatchA(decl, snapOnlyAdopt, nextStore, testResID, actorA) {
		t.Fatalf("MatchA = false, want true after apply")
	}
	inConverged := plan.Input{
		Resource:    testResID,
		Actor:       actorA,
		Declaration: decl,
		Snapshot:    snapOnlyAdopt,
		Store:       nextStore,
		Scope:       sc,
	}
	res2, err := plan.Plan(inConverged, plan.ModeApply)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Gate.HasWork() || res2.Convergence != plan.ConvergenceConverged || res2.Execution != plan.ExecNoOp {
		t.Fatalf("expected idempotent 0-work fixed point on 2nd apply, got gate=%+v conv=%v exec=%v", res2.Gate, res2.Convergence, res2.Execution)
	}
	previewConverged, err := plan.PlanPreview(inConverged)
	if err != nil {
		t.Fatal(err)
	}
	if got := previewConverged.DetailedExitCode(); got != 0 {
		t.Fatalf("converged DetailedExitCode() = %d, want 0", got)
	}
}

// TestCounterexamples03_08_19_21_22_30_31_48_BlockersAndGate verifies Unknown (#3), Foreign Conflict (#8, #30),
// Repeated Conflict Stop (#19), Foreign Actor Isolation (#21), Drift (#22), and All-or-Nothing Gate (#31, #48).
func TestCounterexamples03_08_19_21_22_30_31_48_BlockersAndGate(t *testing.T) {
	decl := mustDecl(t, `
app {
  valid_create = "ok"
  unknown_leaf = "v"
  foreign_leaf = "v"
  drifted_leaf = "expected"
  conflict_leaf = "desired"
}
`)
	store := ownership.NewStore()
	foreignEntry, _ := ownership.NewSettledCreated(actorB, lkal.String("v"), 1)
	unrelatedForeign, _ := ownership.NewSettledCreated(actorB, lkal.String("other"), 1)
	driftedEntry, _ := ownership.NewSettledCreated(actorA, lkal.String("expected"), 1)
	store = mustPut(t, store, "/app/foreign_leaf", foreignEntry)
	store = mustPut(t, store, "/unrelated/b_only", unrelatedForeign)
	store = mustPut(t, store, "/app/drifted_leaf", driftedEntry)

	snap := mustSnap(t, map[string]observation.Observed{
		"/app":               observation.PresentRecord(),
		"/app/valid_create":  observation.AbsentObserved(),
		"/app/unknown_leaf":  observation.UnknownObserved("permission denied"),
		"/app/foreign_leaf":  observation.AbsentObserved(),
		"/app/drifted_leaf":  observation.MustPresentLeaf(lkal.String("tampered")),
		"/app/conflict_leaf": observation.MustPresentLeaf(lkal.String("unmanaged-diff")),
	})
	sc := fullScope(t)

	for run := 1; run <= 2; run++ {
		res, err := plan.Plan(plan.Input{
			Resource:    testResID,
			Actor:       actorA,
			Declaration: decl,
			Snapshot:    snap,
			Store:       store,
			Scope:       sc,
		}, plan.ModeApply)
		if err != nil {
			t.Fatal(err)
		}
		// Counterexamples #31 & #48: All-or-nothing gate suppresses valid_create while blockers exist.
		if !res.Gate.Blocked || len(res.Gate.Changes) != 0 || len(res.Gate.MetadataCandidates) != 0 {
			t.Fatalf("run %d: expected blocked gate with 0 executable work, got %+v", run, res.Gate)
		}
		if len(res.Gate.Suppressed) != 1 || !res.Gate.Suppressed[0].Path.Equal(model.MustParsePath("/app/valid_create")) {
			t.Fatalf("run %d: expected /app/valid_create in Suppressed, got %+v", run, res.Gate.Suppressed)
		}
		if !hasDiagnosticAtPath(res.Gate.Diagnostics, "/app/unknown_leaf", model.CodeObservationUnavailable) {
			t.Fatalf("run %d: missing K301 on /app/unknown_leaf", run)
		}
		if !hasDiagnosticAtPath(res.Gate.Diagnostics, "/app/foreign_leaf", model.CodeOwnershipConflict) {
			t.Fatalf("run %d: missing K201 on /app/foreign_leaf", run)
		}
		if !hasDiagnosticAtPath(res.Gate.Diagnostics, "/app/drifted_leaf", model.CodeExternalDrift) {
			t.Fatalf("run %d: missing K203 on /app/drifted_leaf", run)
		}
		if !hasDiagnosticAtPath(res.Gate.Diagnostics, "/app/conflict_leaf", model.CodeUnownedValueConflict) {
			t.Fatalf("run %d: missing K206 on /app/conflict_leaf", run)
		}
		if _, found := findPathResult(res.Gate.PathResults, "/unrelated/b_only"); found {
			t.Fatalf("run %d: Counterexample #21 violated: unrelated foreign path entered evaluation scope", run)
		}
	}
}

// TestCounterexamples05_29_39_RetiredHandlingAndNoSpuriousPolicyDenied verifies:
//   - #5: Retired Origin=Modified is unmanaged in Apply (ActionUnmanage) without touching environment.
//   - #29: Retired Origin=Created present in Apply is Deferred (K604 PruneRequired), then deleted in Prune.
//   - #39: Converged (NoOp) and Deferred paths never check Scope or emit K401 PolicyDenied.
func TestCounterexamples05_29_39_RetiredHandlingAndNoSpuriousPolicyDenied(t *testing.T) {
	decl := mustDecl(t, `
app {
  active = "stay"
}
`)
	store := ownership.NewStore()
	eActive, _ := ownership.NewSettledCreated(actorA, lkal.String("stay"), 1)
	eRetMod, _ := ownership.NewSettledModified(actorA, lkal.String("adopted-old"), 2)
	eRetCre, _ := ownership.NewSettledCreated(actorA, lkal.String("created-old"), 3)
	store = mustPut(t, store, "/app/active", eActive)
	store = mustPut(t, store, "/app/ret_mod", eRetMod)
	store = mustPut(t, store, "/app/ret_cre", eRetCre)

	snap := mustSnap(t, map[string]observation.Observed{
		"/app":         observation.PresentRecord(),
		"/app/active":  observation.MustPresentLeaf(lkal.String("stay")),
		"/app/ret_mod": observation.MustPresentLeaf(lkal.String("adopted-old")),
		"/app/ret_cre": observation.MustPresentLeaf(lkal.String("created-old")),
	})

	// Counterexample #39: Scope ONLY grants Own on /app/ret_mod; no permissions on /app/active (NoOp) or /app/ret_cre (Deferred).
	scRestricted := exactScope(t, "/app/ret_mod", scope.PermOwn)

	resApply, err := plan.Plan(plan.Input{
		Resource:    testResID,
		Actor:       actorA,
		Declaration: decl,
		Snapshot:    snap,
		Store:       store,
		Scope:       scRestricted,
	}, plan.ModeApply)
	if err != nil {
		t.Fatal(err)
	}
	if resApply.Gate.Blocked || hasDiagnosticCode(resApply.Gate.Diagnostics, model.CodePolicyDenied) {
		t.Fatalf("Counterexample #39 failed: unexpected K401 or blocker in Apply: %+v", resApply.Gate.Diagnostics)
	}
	if len(resApply.Gate.Changes) != 0 || len(resApply.Gate.MetadataCandidates) != 1 {
		t.Fatalf("Counterexample #5 failed: expected 0 env changes and 1 Unmanage metadata candidate, got %+v", resApply.Gate)
	}
	if !hasDiagnosticAtPath(resApply.Gate.Diagnostics, "/app/ret_cre", model.CodePruneRequired) {
		t.Fatalf("Counterexample #29 failed: expected K604 PruneRequired on /app/ret_cre, got %+v", resApply.Gate.Diagnostics)
	}

	storeAfterApply, err := resApply.ApplyToStore()
	if err != nil {
		t.Fatal(err)
	}
	if plan.MatchA(decl, snap, storeAfterApply, testResID, actorA) {
		t.Fatalf("Counterexample #29 failed: MatchA must be false while PruneRequired retired Created remains in σ_A")
	}

	scPrune := exactScope(t, "/app/ret_cre", scope.PermDelete, scope.PermOwn)
	resPrune, err := plan.Plan(plan.Input{
		Resource:    testResID,
		Actor:       actorA,
		Declaration: decl,
		Snapshot:    snap,
		Store:       storeAfterApply,
		Scope:       scPrune,
	}, plan.ModePrune)
	if err != nil {
		t.Fatal(err)
	}
	if resPrune.Gate.Blocked || len(resPrune.Gate.Changes) != 1 || resPrune.Gate.Changes[0].Op != ownership.PendingOpDelete {
		t.Fatalf("expected 1 Delete op in Prune, got %+v", resPrune.Gate)
	}
}
