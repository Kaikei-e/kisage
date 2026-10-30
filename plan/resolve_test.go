package plan_test

import (
	"errors"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/scope"
)

// TestCounterexamples36_37_ExplainAdoptAndReleaseResolutionFlows verifies ADR 0003 §3
// and Counterexamples #36, #37:
//   - #36: UnownedValueConflict (K206) and DriftChanged (K203) are resolved via explain -> adopt -> apply,
//     and adopting an Origin=Created entry downgrades Origin to Modified with K602.
//   - #37: DriftDeleted (K203 where leaf is Absent) is resolved via explain -> release -> apply.
func TestCounterexamples36_37_ExplainAdoptAndReleaseResolutionFlows(t *testing.T) {
	decl := mustDecl(t, `
app {
  drifted_val = "desired"
  deleted_val = "recreate-me"
}
`)
	store := ownership.NewStore()
	eCreated1, _ := ownership.NewSettledCreated(actorA, lkal.String("old-1"), 1)
	eCreated2, _ := ownership.NewSettledCreated(actorA, lkal.String("recreate-me"), 1)
	store = mustPut(t, store, "/app/drifted_val", eCreated1)
	store = mustPut(t, store, "/app/deleted_val", eCreated2)

	snap := mustSnap(t, map[string]observation.Observed{
		"/app":             observation.PresentRecord(),
		"/app/drifted_val": observation.MustPresentLeaf(lkal.String("external-edit")),
		"/app/deleted_val": observation.AbsentObserved(),
	})
	sc := fullScope(t)
	in := plan.Input{
		Resource:    testResID,
		Actor:       actorA,
		Declaration: decl,
		Snapshot:    snap,
		Store:       store,
		Scope:       sc,
	}

	// 1. Counterexample #36: Explain /app/drifted_val, then PlanAdopt.
	pDrifted := model.MustParsePath("/app/drifted_val")
	expDrift, err := plan.ExplainPath(in, pDrifted)
	if err != nil {
		t.Fatal(err)
	}
	if expDrift.ConditionToken.IsEmpty() || expDrift.OwnerRevision != 1 {
		t.Fatalf("unexpected explain output: tok=%q rev=%d", expDrift.ConditionToken, expDrift.OwnerRevision)
	}
	adoptRes, err := plan.PlanAdopt(plan.ResolutionRequest{
		Resource:         testResID,
		Actor:            actorA,
		Path:             pDrifted,
		ExpectedToken:    expDrift.ConditionToken,
		ExpectedRevision: expDrift.OwnerRevision,
		Declaration:      decl,
		Snapshot:         snap,
		Store:            store,
		Scope:            sc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if adoptRes.After == nil || adoptRes.After.Origin != ownership.OriginModified {
		t.Fatalf("Counterexample #36 failed: expected Origin=Modified after adopt, got %+v", adoptRes.After)
	}
	if !hasDiagnosticCode(adoptRes.Diagnostics, model.CodeExplicitDeletionRightWaived) {
		t.Fatalf("expected K602 warning when adopting over Origin=Created, got %+v", adoptRes.Diagnostics)
	}

	// 2. Counterexample #37: Explain /app/deleted_val, then PlanRelease.
	pDeleted := model.MustParsePath("/app/deleted_val")
	inAfterAdopt := in
	inAfterAdopt.Store = adoptRes.NextStore
	expDel, err := plan.ExplainPath(inAfterAdopt, pDeleted)
	if err != nil {
		t.Fatal(err)
	}
	releaseRes, err := plan.PlanRelease(plan.ResolutionRequest{
		Resource:         testResID,
		Actor:            actorA,
		Path:             pDeleted,
		ExpectedToken:    expDel.ConditionToken,
		ExpectedRevision: expDel.OwnerRevision,
		Declaration:      decl,
		Snapshot:         snap,
		Store:            adoptRes.NextStore,
		Scope:            sc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !releaseRes.DeclaredInFile || !releaseRes.RemanageOnNextApply {
		t.Fatalf("expected RemanageOnNextApply=true when releasing declared path")
	}

	// 3. Subsequent Apply now plans Set on both paths without blockers!
	resApply, err := plan.Plan(plan.Input{
		Resource:    testResID,
		Actor:       actorA,
		Declaration: decl,
		Snapshot:    snap,
		Store:       releaseRes.NextStore,
		Scope:       sc,
	}, plan.ModeApply)
	if err != nil {
		t.Fatal(err)
	}
	if resApply.Gate.Blocked || len(resApply.Gate.Changes) != 2 {
		t.Fatalf("expected 2 Set changes after adopt + release, got %+v", resApply.Gate)
	}
}

// TestCounterexamples38_50_TokenRevisionScopeAndOverlapPreconditions verifies ADR 0003 §3
// and Counterexamples #38, #50:
//   - #38: Token mismatch (value/type or ancestor change), stale OwnerRevision, or missing Resolve permission is rejected.
//   - #50: PlanAdopt rejects ancestor/descendant overlap in σ_A until the overlapping entry is released.
func TestCounterexamples38_50_TokenRevisionScopeAndOverlapPreconditions(t *testing.T) {
	decl := mustDecl(t, `
a {
  b = 10
}
`)
	pAB := model.MustParsePath("/a/b")
	snap := mustSnap(t, map[string]observation.Observed{
		"/a":   observation.PresentRecord(),
		"/a/b": observation.MustPresentLeaf(lkal.Int(99)),
	})
	tok, err := plan.ComputeConditionToken(testResID, pAB, snap)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Counterexample #38: Missing PermResolve (PermRegular only has Set|Delete|Own) -> K401 PolicyDenied.
	rNoResolve, _ := scope.NewRootRule(scope.PermRegular)
	scNoResolve := scope.MustNew(rNoResolve)
	_, err = plan.PlanAdopt(plan.ResolutionRequest{
		Resource:         testResID,
		Actor:            actorA,
		Path:             pAB,
		ExpectedToken:    tok,
		ExpectedRevision: 0,
		Declaration:      decl,
		Snapshot:         snap,
		Store:            ownership.NewStore(),
		Scope:            scNoResolve,
	})
	assertDiagCode(t, err, model.CodePolicyDenied)

	// 2. Counterexample #38: Token mismatch when observed leaf changes from Int(99) to String("99") -> K203.
	snapChangedType := mustSnap(t, map[string]observation.Observed{
		"/a":   observation.PresentRecord(),
		"/a/b": observation.MustPresentLeaf(lkal.String("99")),
	})
	_, err = plan.PlanAdopt(plan.ResolutionRequest{
		Resource:         testResID,
		Actor:            actorA,
		Path:             pAB,
		ExpectedToken:    tok,
		ExpectedRevision: 0,
		Declaration:      decl,
		Snapshot:         snapChangedType,
		Store:            ownership.NewStore(),
		Scope:            fullScope(t),
	})
	assertDiagCode(t, err, model.CodeExternalDrift)

	// 3. Counterexample #38: Stale revision 0 after unowned -> owned -> released cycle (revision is now 2) -> K201.
	storeCycled := ownership.NewStore()
	eTemp, _ := ownership.NewSettledModified(actorA, lkal.Int(99), 1)
	storeCycled = mustPut(t, storeCycled, "/a/b", eTemp)
	storeCycled, _ = storeCycled.Delete(testResID, pAB)
	_, err = plan.PlanAdopt(plan.ResolutionRequest{
		Resource:         testResID,
		Actor:            actorA,
		Path:             pAB,
		ExpectedToken:    tok,
		ExpectedRevision: 0,
		Declaration:      decl,
		Snapshot:         snap,
		Store:            storeCycled,
		Scope:            fullScope(t),
	})
	assertDiagCode(t, err, model.CodeOwnershipConflict)

	// 4. Counterexample #50: Self-owned ancestor /a in σ_A rejects PlanAdopt on descendant /a/b with K201.
	storeOverlap := ownership.NewStore()
	eAnc, _ := ownership.NewSettledModified(actorA, lkal.Int(1), 1)
	storeOverlap = mustPut(t, storeOverlap, "/a", eAnc)
	_, err = plan.PlanAdopt(plan.ResolutionRequest{
		Resource:         testResID,
		Actor:            actorA,
		Path:             pAB,
		ExpectedToken:    tok,
		ExpectedRevision: 0,
		Declaration:      decl,
		Snapshot:         snap,
		Store:            storeOverlap,
		Scope:            fullScope(t),
	})
	assertDiagCode(t, err, model.CodeOwnershipConflict)
}

func assertDiagCode(t *testing.T, err error, want model.DiagnosticCode) {
	t.Helper()
	var diag model.Diagnostic
	if !errors.As(err, &diag) || diag.Code != want {
		t.Fatalf("got error %v, want Diagnostic with code %s", err, want)
	}
}
