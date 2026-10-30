package plan_test

import (
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
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
