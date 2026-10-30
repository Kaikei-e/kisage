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

// Counterexample #45: quarantine must preserve Pending even when Target matches.
func TestCounterexample45_QuarantinePreventsVirtualRecovery(t *testing.T) {
	p := model.MustParsePath("/x")
	pending, err := ownership.NewPendingCreate(actorA, "batch", lkal.Int(1), 1)
	if err != nil {
		t.Fatal(err)
	}
	in := plan.Input{
		Resource: testResID, Actor: actorA, Declaration: mustDecl(t, "x = 1"),
		Snapshot: mustSnap(t, map[string]observation.Observed{"/x": observation.MustPresentLeaf(lkal.Int(1))}),
		Store:    mustPut(t, ownership.NewStore(), "/x", pending), Scope: fullScope(t), Quarantined: true,
	}
	preview, err := plan.PlanPreview(in)
	if err != nil {
		t.Fatal(err)
	}
	apply, err := plan.Plan(in, plan.ModeApply)
	if err != nil {
		t.Fatal(err)
	}
	prune, err := plan.Plan(in, plan.ModePrune)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []*plan.Result{apply, prune, preview.Apply, preview.Prune} {
		if !result.Gate.Blocked || result.Gate.HasWork() || result.Recovery.HasCandidates() || result.Recovery.RequiresStabilize {
			t.Fatalf("quarantine produced work: %+v", result)
		}
		entry, ok := result.Recovery.VirtualStore.Get(testResID, p)
		if !ok || !entry.HasPending() || entry.Revision != pending.Revision {
			t.Fatalf("quarantine changed Pending: %+v", entry)
		}
		if _, err := result.ApplyToStore(); err == nil {
			t.Fatal("quarantined plan applied to store")
		}
	}
	ex, err := plan.ExplainPath(in, p)
	if err != nil {
		t.Fatal(err)
	}
	if ex.ApplyExecution != plan.ExecBlocked || ex.PruneExecution != plan.ExecBlocked || !hasDiagnosticCode(ex.Diagnostics, model.CodeResourceQuarantined) {
		t.Fatalf("explain omitted quarantine gate: %+v", ex)
	}
}

// Counterexample #41: a Pending at another path must remain visible in explain.
func TestCounterexample41_ExplainIncludesRecoveryBlocker(t *testing.T) {
	pending, err := ownership.NewPendingCreate(actorA, "batch", lkal.Int(1), 1)
	if err != nil {
		t.Fatal(err)
	}
	settled, err := ownership.NewSettledModified(actorA, lkal.Int(1), 1)
	if err != nil {
		t.Fatal(err)
	}
	store := mustPut(t, ownership.NewStore(), "/pending", pending)
	store = mustPut(t, store, "/x", settled)
	ex, err := plan.ExplainPath(plan.Input{
		Resource: testResID, Actor: actorA, Declaration: mustDecl(t, "x = 1"), Store: store, Scope: fullScope(t),
		Snapshot: mustSnap(t, map[string]observation.Observed{"/x": observation.MustPresentLeaf(lkal.Int(1))}),
	}, model.MustParsePath("/x"))
	if err != nil {
		t.Fatal(err)
	}
	if ex.ApplyExecution != plan.ExecBlocked || ex.PruneExecution != plan.ExecBlocked || !hasDiagnosticAtPath(ex.Diagnostics, "/pending", model.CodeObservationUnavailable) {
		t.Fatalf("explain omitted unresolved recovery: %+v", ex)
	}
}

// Counterexample #38: require FILE and retain the remanagement warning.
func TestCounterexample38_ResolutionRequiresDeclaration(t *testing.T) {
	p := model.MustParsePath("/x")
	settled, err := ownership.NewSettledModified(actorA, lkal.Int(1), 1)
	if err != nil {
		t.Fatal(err)
	}
	store := mustPut(t, ownership.NewStore(), "/x", settled)
	snap := mustSnap(t, map[string]observation.Observed{"/x": observation.MustPresentLeaf(lkal.Int(1))})
	token, err := plan.ComputeConditionToken(testResID, p, snap)
	if err != nil {
		t.Fatal(err)
	}
	req := plan.ResolutionRequest{Resource: testResID, Actor: actorA, Path: p, ExpectedToken: token, ExpectedRevision: 1, Store: store, Snapshot: snap, Scope: fullScope(t)}
	for _, resolve := range []func(plan.ResolutionRequest) (*plan.ResolutionPlan, error){plan.PlanAdopt, plan.PlanRelease} {
		_, err := resolve(req)
		assertDiagCode(t, err, model.CodeInvalidInput)
		if !store.Has(testResID, p) || store.Revision(testResID, p) != 1 {
			t.Fatal("rejected resolution changed store")
		}
	}
	req.Declaration = mustDecl(t, "x = 1")
	released, err := plan.PlanRelease(req)
	if err != nil {
		t.Fatal(err)
	}
	if !released.DeclaredInFile || !released.RemanageOnNextApply || len(released.Diagnostics) == 0 {
		t.Fatalf("missing remanagement warning: %+v", released)
	}
	req.Declaration = mustDecl(t, "")
	released, err = plan.PlanRelease(req)
	if err != nil {
		t.Fatal(err)
	}
	if released.DeclaredInFile || released.RemanageOnNextApply {
		t.Fatal("empty declaration treated as declaring x")
	}
}

// TestAuthorizeAdjustsNextActionByGrantedScope verifies that Stage 4 (Authorize) preserves
// Blocker PrimaryCode (K206/K203) without emitting spurious K401 (Counterexample #39), while
// replacing unexecutable "adopt -> apply" / "release -> apply" NextAction guidance when the
// path is outside granted Scope or lacks Resolve permission.
func TestAuthorizeAdjustsNextActionByGrantedScope(t *testing.T) {
	decl := mustDecl(t, `
local {
  cookie_domain = "override.example.com"
}
app {
  rate_limit = "7.5s"
  deleted_key = "v"
}
`)
	store := ownership.NewStore()
	eDel, err := ownership.NewSettledCreated(actorA, lkal.String("v"), 1)
	if err != nil {
		t.Fatal(err)
	}
	store = mustPut(t, store, "/app/deleted_key", eDel)

	snap := mustSnap(t, map[string]observation.Observed{
		"/local":               observation.PresentRecord(),
		"/local/cookie_domain": observation.MustPresentLeaf(lkal.String("local.example.com")),
		"/app":                 observation.PresentRecord(),
		"/app/rate_limit":      observation.MustPresentLeaf(lkal.String("1.0s")),
		"/app/deleted_key":     observation.AbsentObserved(),
	})

	// Grant Set+Own on /app subtree only (no Resolve on /app, and no permissions at all on /local).
	appRule, err := scope.NewSubtreeRule(model.MustParsePath("/app"), scope.NewPermissionSet(scope.PermSet, scope.PermOwn))
	if err != nil {
		t.Fatal(err)
	}
	sc := scope.MustNew(appRule)

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
	if hasDiagnosticCode(res.Diagnostics, model.CodePolicyDenied) {
		t.Fatalf("Counterexample #39 violated: Blocker emitted K401 PolicyDenied: %+v", res.Diagnostics)
	}

	prLocal, ok := findPathResult(res.Gate.PathResults, "/local/cookie_domain")
	if !ok || prLocal.PrimaryCode != model.CodeUnownedValueConflict || len(prLocal.Diagnostics) != 1 {
		t.Fatalf("expected K206 on /local/cookie_domain, got %+v", prLocal)
	}
	if prLocal.Diagnostics[0].NextAction != plan.NextActionOutsideScope {
		t.Fatalf("expected NextActionOutsideScope on out-of-scope K206, got %q", prLocal.Diagnostics[0].NextAction)
	}

	prApp, ok := findPathResult(res.Gate.PathResults, "/app/rate_limit")
	if !ok || prApp.PrimaryCode != model.CodeUnownedValueConflict || len(prApp.Diagnostics) != 1 {
		t.Fatalf("expected K206 on /app/rate_limit, got %+v", prApp)
	}
	if prApp.Diagnostics[0].NextAction != plan.NextActionAdoptRequiresResolve {
		t.Fatalf("expected NextActionAdoptRequiresResolve when Resolve is not granted, got %q", prApp.Diagnostics[0].NextAction)
	}

	prDel, ok := findPathResult(res.Gate.PathResults, "/app/deleted_key")
	if !ok || prDel.PrimaryCode != model.CodeExternalDrift || len(prDel.Diagnostics) != 1 {
		t.Fatalf("expected K203 on /app/deleted_key, got %+v", prDel)
	}
	if prDel.Diagnostics[0].NextAction != plan.NextActionReleaseRequiresResolve {
		t.Fatalf("expected NextActionReleaseRequiresResolve when Resolve is not granted, got %q", prDel.Diagnostics[0].NextAction)
	}
}
