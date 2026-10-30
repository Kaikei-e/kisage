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

// TestCounterexamples20_54_RetiredCreatedOverlapBlocksBothModes verifies ADR 0003 §4
// and Counterexamples #20, #54: ancestor/descendant overlap between D and a retired Origin=Created
// path emits K204 MigrationRequired and blocks both Apply and Prune.
func TestCounterexamples20_54_RetiredCreatedOverlapBlocksBothModes(t *testing.T) {
	// v1 had /a/b (Origin=Created), v2 declares /a = 1 (ancestor leaf).
	decl := mustDecl(t, `a = 1`)
	store := ownership.NewStore()
	eOldCreated, _ := ownership.NewSettledCreated(actorA, lkal.Int(10), 1)
	store = mustPut(t, store, "/a/b", eOldCreated)

	snap := mustSnap(t, map[string]observation.Observed{
		"/a":   observation.PresentRecord(),
		"/a/b": observation.MustPresentLeaf(lkal.Int(10)),
	})
	in := plan.Input{
		Resource:    testResID,
		Actor:       actorA,
		Declaration: decl,
		Snapshot:    snap,
		Store:       store,
		Scope:       fullScope(t),
	}

	for _, mode := range []plan.Mode{plan.ModeApply, plan.ModePrune} {
		res, err := plan.Plan(in, mode)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Gate.Blocked {
			t.Fatalf("mode %v: expected K204 MigrationRequired to block gate", mode)
		}
		if !hasDiagnosticAtPath(res.Gate.Diagnostics, "/a", model.CodeMigrationRequired) {
			t.Fatalf("mode %v: missing K204 on declared path /a: %+v", mode, res.Gate.Diagnostics)
		}
		if !hasDiagnosticAtPath(res.Gate.Diagnostics, "/a/b", model.CodeMigrationRequired) {
			t.Fatalf("mode %v: missing K204 on retired Created path /a/b: %+v", mode, res.Gate.Diagnostics)
		}
	}
}

// TestCounterexamples49_55_56_TwoStepRetiredModifiedStructuralMigration verifies ADR 0003 §4
// and Counterexamples #49, #55, #56:
//   - When declared p overlaps only retired Origin=Modified paths, p is StateDeferredUntilRelease (K603)
//     even if p or its ancestors currently have K202 StructureConflict or K301 Unknown (#55, #56).
//   - 1st Apply unmanages the retired Origin=Modified entry without being blocked by p.
//   - 2nd Apply evaluates p normally once the retired Origin=Modified entry is gone.
func TestCounterexamples49_55_56_TwoStepRetiredModifiedStructuralMigration(t *testing.T) {
	// Case A (#49 & #55): v1 had /a (Origin=Modified, Present(Leaf("legacy"))), v2 declares /a/b = 1 where /a/b is Unknown.
	decl := mustDecl(t, `
a {
  b = 1
}
`)
	store := ownership.NewStore()
	eOldMod, _ := ownership.NewSettledModified(actorA, lkal.String("legacy"), 1)
	store = mustPut(t, store, "/a", eOldMod)

	snap1 := mustSnap(t, map[string]observation.Observed{
		"/a":   observation.MustPresentLeaf(lkal.String("legacy")),
		"/a/b": observation.UnknownObserved("parent is still a leaf"),
	})
	sc := fullScope(t)

	res1, err := plan.Plan(plan.Input{
		Resource:    testResID,
		Actor:       actorA,
		Declaration: decl,
		Snapshot:    snap1,
		Store:       store,
		Scope:       sc,
	}, plan.ModeApply)
	if err != nil {
		t.Fatal(err)
	}
	if res1.Gate.Blocked {
		t.Fatalf("Counterexample #55 failed: 1st Apply was blocked by DeferredUntilRelease path: %+v", res1.Gate.Diagnostics)
	}
	if len(res1.Gate.MetadataCandidates) != 1 || res1.Gate.MetadataCandidates[0].Action != scope.ActionUnmanage {
		t.Fatalf("expected 1 Unmanage metadata candidate on /a in 1st Apply, got %+v", res1.Gate)
	}
	prAB, ok := findPathResult(res1.Gate.PathResults, "/a/b")
	if !ok || prAB.StateClass != plan.StateDeferredUntilRelease || prAB.Decision != plan.DecisionDeferred {
		t.Fatalf("expected /a/b to be DeferredUntilRelease, got %+v", prAB)
	}
	// Diagnostics on /a/b must retain K603 plus K202/K301 without blocking 1st Apply.
	if !hasDiagnosticAtPath(res1.Gate.Diagnostics, "/a/b", model.CodeDeferredUntilRelease) {
		t.Fatalf("missing K603 on /a/b in 1st Apply: %+v", res1.Gate.Diagnostics)
	}

	// Commit 1st Apply (unmanages /a).
	store2, err := res1.ApplyToStore()
	if err != nil {
		t.Fatal(err)
	}
	// If environment was not yet migrated, 2nd Apply now classifies /a/b as a normal Blocker (K202/K301).
	res2Unmigrated, err := plan.Plan(plan.Input{
		Resource:    testResID,
		Actor:       actorA,
		Declaration: decl,
		Snapshot:    snap1,
		Store:       store2,
		Scope:       sc,
	}, plan.ModeApply)
	if err != nil {
		t.Fatal(err)
	}
	if !res2Unmigrated.Gate.Blocked {
		t.Fatalf("expected 2nd Apply on unmigrated environment to block normally")
	}

	// Once /a is a Record and /a/b is Absent (#56), 2nd Apply creates /a/b = 1 cleanly.
	snap2 := mustSnap(t, map[string]observation.Observed{
		"/a":   observation.PresentRecord(),
		"/a/b": observation.AbsentObserved(),
	})
	res2Migrated, err := plan.Plan(plan.Input{
		Resource:    testResID,
		Actor:       actorA,
		Declaration: decl,
		Snapshot:    snap2,
		Store:       store2,
		Scope:       sc,
	}, plan.ModeApply)
	if err != nil {
		t.Fatal(err)
	}
	if res2Migrated.Gate.Blocked || len(res2Migrated.Gate.Changes) != 1 {
		t.Fatalf("expected 2nd Apply on migrated environment to plan 1 Set change, got %+v", res2Migrated.Gate)
	}
}

// TestCounterexamples51_52_53_ModeExternalAnomalyIsolationAndAbsentCreated verifies:
//   - #51: Independent retired Created anomaly (Drift/Unknown) is Deferred in Apply, Blocker in Prune.
//   - #52: Declared conflict or retired Modified Unknown is Blocker in Apply, Deferred in Prune.
//   - #53: Confirmed Absent retired Created leaf is Candidate(ClearRecord) in Apply; Unknown retired Created leaf is Deferred in Apply without checking Own.
func TestCounterexamples51_52_53_ModeExternalAnomalyIsolationAndAbsentCreated(t *testing.T) {
	decl := mustDecl(t, `
app {
  ok_key = "v"
}
`)
	store := ownership.NewStore()
	eDriftedCre, _ := ownership.NewSettledCreated(actorA, lkal.String("old"), 1)
	eUnknownCre, _ := ownership.NewSettledCreated(actorA, lkal.String("old2"), 1)
	eAbsentCre, _ := ownership.NewSettledCreated(actorA, lkal.String("gone"), 1)
	store = mustPut(t, store, "/ret/drifted_cre", eDriftedCre)
	store = mustPut(t, store, "/ret/unknown_cre", eUnknownCre)
	store = mustPut(t, store, "/ret/absent_cre", eAbsentCre)

	snap := mustSnap(t, map[string]observation.Observed{
		"/app":             observation.PresentRecord(),
		"/app/ok_key":      observation.AbsentObserved(),
		"/ret":             observation.PresentRecord(),
		"/ret/drifted_cre": observation.MustPresentLeaf(lkal.String("tampered")),
		"/ret/unknown_cre": observation.UnknownObserved("timeout"),
		"/ret/absent_cre":  observation.AbsentObserved(),
	})

	// Grant Set+Own on /app/ok_key and Own on /ret/absent_cre; grant NO permissions on /ret/drifted_cre or /ret/unknown_cre (#53).
	r1, _ := scope.NewExactRule(model.MustParsePath("/app/ok_key"), scope.NewPermissionSet(scope.PermSet, scope.PermOwn))
	r2, _ := scope.NewExactRule(model.MustParsePath("/ret/absent_cre"), scope.NewPermissionSet(scope.PermOwn))
	sc := scope.MustNew(r1, r2)

	// Counterexample #51 & #53: Apply succeeds (not blocked by retired Created anomalies, no K401 on unknown_cre, clears absent_cre).
	resApply, err := plan.Plan(plan.Input{
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
	if resApply.Gate.Blocked || hasDiagnosticCode(resApply.Gate.Diagnostics, model.CodePolicyDenied) {
		t.Fatalf("Counterexamples #51/#53 failed in Apply: blocked=%v diags=%+v", resApply.Gate.Blocked, resApply.Gate.Diagnostics)
	}
	if len(resApply.Gate.Changes) != 1 || len(resApply.Gate.MetadataCandidates) != 1 {
		t.Fatalf("expected 1 Set on /app/ok_key and 1 ClearRecord on /ret/absent_cre, got %+v", resApply.Gate)
	}

	// Counterexample #52: In Prune, a declared conflict on /app/ok_key and Unknown on retired Modified /ret/mod_unk
	// are Deferred (do not block Prune), so a prunable retired Created leaf can still be deleted.
	storePrune := ownership.NewStore()
	ePrunableCre, _ := ownership.NewSettledCreated(actorA, lkal.String("del-me"), 1)
	eRetModUnk, _ := ownership.NewSettledModified(actorA, lkal.String("mod"), 1)
	storePrune = mustPut(t, storePrune, "/ret/prunable", ePrunableCre)
	storePrune = mustPut(t, storePrune, "/ret/mod_unk", eRetModUnk)
	snapPrune := mustSnap(t, map[string]observation.Observed{
		"/app":          observation.PresentRecord(),
		"/app/ok_key":   observation.MustPresentLeaf(lkal.String("unmanaged-conflict")),
		"/ret":          observation.PresentRecord(),
		"/ret/prunable": observation.MustPresentLeaf(lkal.String("del-me")),
		"/ret/mod_unk":  observation.UnknownObserved("unavailable"),
	})
	scPrune := exactScope(t, "/ret/prunable", scope.PermDelete, scope.PermOwn)
	resPrune, err := plan.Plan(plan.Input{
		Resource:    testResID,
		Actor:       actorA,
		Declaration: decl,
		Snapshot:    snapPrune,
		Store:       storePrune,
		Scope:       scPrune,
	}, plan.ModePrune)
	if err != nil {
		t.Fatal(err)
	}
	if resPrune.Gate.Blocked || len(resPrune.Gate.Changes) != 1 {
		t.Fatalf("Counterexample #52 failed: Prune was blocked by Apply-only anomalies: %+v", resPrune.Gate)
	}
}
