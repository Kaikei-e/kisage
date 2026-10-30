package verification_test

import (
	"os"
	"path/filepath"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/protocol"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
	"github.com/Kaikei-e/kisage/verification"
)

func verifierEnvironment(t *testing.T, content, declaration string, store *ownership.Store) verification.ConvergenceVerifyEnv {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target.lkal")
	if err := os.WriteFile(target, []byte("@lkal \"0.1\"\n"+content), 0o600); err != nil {
		t.Fatal(err)
	}
	physical, err := provider.InspectPhysicalIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := ownership.NewResourceRegistry(ownership.ResourceBinding{ID: testResID, Provider: ownership.ProviderLKALFile, ManagementRoot: root, TargetPath: target, StoreID: testStoreID, Physical: physical})
	if err != nil {
		t.Fatal(err)
	}
	prov, err := provider.NewLKALFileProvider(registry)
	if err != nil {
		t.Fatal(err)
	}
	locks, err := provider.NewFileLockManager(filepath.Join(root, "locks"))
	if err != nil {
		t.Fatal(err)
	}
	return verification.ConvergenceVerifyEnv{StoreID: testStoreID, Resource: testResID, Actor: actorA, Declaration: mustDecl(t, declaration), Scope: fullScope(t), Provider: prov, StoreRepo: protocol.NewMemoryStoreRepository(store, nil), Locks: locks}
}

func reconcileForVerification(t *testing.T, env verification.ConvergenceVerifyEnv, mode plan.Mode) *protocol.ReconcileResult {
	t.Helper()
	res, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{StoreID: env.StoreID, Resource: env.Resource, Actor: env.Actor, Mode: mode, BatchID: "verify", Declaration: env.Declaration, Scope: env.Scope, Provider: env.Provider, StoreRepo: env.StoreRepo, Locks: env.Locks})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestCreatedHistoryCannotBeOmittedOrInferredFromStores(t *testing.T) {
	p := model.MustParsePath("/x")
	e, err := ownership.NewSettledCreated(actorA, lkal.String("preexisting"), 1)
	if err != nil {
		t.Fatal(err)
	}
	created, err := ownership.NewStore().Put(testResID, p, e)
	if err != nil {
		t.Fatal(err)
	}
	if err := verification.VerifyProtocolInductiveInvariants(verification.ProtocolInvariantContext{Resource: testResID, AfterStore: created}); err == nil {
		t.Fatal("nil history bypassed Created verification")
	}
	result := &protocol.ReconcileResult{Resource: testResID, Actor: actorA, FinalStore: created}
	for _, history := range []map[string]ownership.ActorID{nil, {"/x": actorA}} {
		if _, err := verification.VerifyReconcileResultWithHistory(ownership.NewStore(), result, history); err == nil {
			t.Fatal("fabricated creation accepted without observed and committed creation")
		}
	}
	if err := verification.VerifyReconcileResultInvariants(created, result); err == nil {
		t.Fatal("loaded Created records silently treated as valid history")
	}
	if _, err := verification.VerifyReconcileResultWithHistory(created, result, map[string]ownership.ActorID{"/x": actorB}); err == nil {
		t.Fatal("foreign creation witness accepted")
	}
	if _, err := verification.VerifyReconcileResultWithHistory(created, result, map[string]ownership.ActorID{"/x": actorA}); err != nil {
		t.Fatalf("explicit initial history rejected: %v", err)
	}
}

// #2, #12, #29: legitimate creation yields a witness, NoOp preserves it and
// prune ends the session. Stale history cannot justify a later fabricated session.
func TestCreationWitnessesRequireTheCompleteExecutionEvidence(t *testing.T) {
	before := ownership.NewStore()
	env := verifierEnvironment(t, "", "x = \"new\"\n", before)
	res := reconcileForVerification(t, env, plan.ModeApply)
	history, err := verification.VerifyReconcileResultWithHistory(before, res, nil)
	if err != nil || history["/x"] != actorA {
		t.Fatalf("confirmed creation rejected: history=%v err=%v", history, err)
	}
	for _, mutation := range []string{"no-intent", "wrong-intent", "unknown-receipt", "no-before-observation", "no-validation", "no-durability", "no-commit", "wrong-last-applied"} {
		t.Run(mutation, func(t *testing.T) {
			broken := *res
			broken.NormalExecution = res.NormalExecution
			switch mutation {
			case "no-intent":
				broken.NormalExecution.IntentSaved = false
			case "wrong-intent":
				broken.NormalExecution.Intent = &protocol.DurableIntent{BatchID: "different"}
			case "unknown-receipt":
				r := *res.NormalExecution.Receipt
				r.Outcome = provider.ReceiptIndeterminate
				broken.NormalExecution.Receipt = &r
			case "no-before-observation":
				broken.NormalExecution.BeforeApplySnapshot = nil
			case "no-validation":
				broken.NormalExecution.VerifiedSnapshot = observation.NewSnapshot()
			case "no-durability":
				broken.NormalExecution.DurableSnapshot = nil
			case "no-commit":
				broken.NormalExecution.CommitStatus = protocol.CommitFailed
			case "wrong-last-applied":
				e, _ := broken.FinalStore.Get(testResID, model.MustParsePath("/x"))
				e.LastApplied = lkal.String("not-written")
				broken.FinalStore, _ = broken.FinalStore.Put(testResID, model.MustParsePath("/x"), e)
			}
			if _, err := verification.VerifyReconcileResultWithHistory(before, &broken, nil); err == nil {
				t.Fatal("incomplete creation evidence accepted")
			}
		})
	}
	noop := reconcileForVerification(t, env, plan.ModeApply)
	if _, err := verification.VerifyReconcileResultWithHistory(res.FinalStore, noop, history); err != nil {
		t.Fatalf("NoOp lost valid creation history: %v", err)
	}
	env.Declaration = mustDecl(t, "")
	pruned := reconcileForVerification(t, env, plan.ModePrune)
	ended, err := verification.VerifyReconcileResultWithHistory(noop.FinalStore, pruned, history)
	if err != nil || len(ended) != 0 {
		t.Fatalf("prune retained an ended creation session: %v %v", ended, err)
	}
	forged := &protocol.ReconcileResult{Resource: testResID, Actor: actorA, FinalStore: res.FinalStore}
	if _, err := verification.VerifyReconcileResultWithHistory(pruned.FinalStore, forged, history); err == nil {
		t.Fatal("stale history justified a new session without creation")
	}
}

// #6, #43, #59: actual recovery supplies the snapshots used before persistence;
// missing/conflicting durability evidence must not silently pass the checker.
func TestRecoveryInvariantsRequireActualDurabilityEvidence(t *testing.T) {
	p := model.MustParsePath("/app/x")
	pending, err := ownership.NewPendingCreate(actorA, "previous", lkal.String("target"), 1)
	if err != nil {
		t.Fatal(err)
	}
	before, err := ownership.NewStore().Put(testResID, p, pending)
	if err != nil {
		t.Fatal(err)
	}
	env := verifierEnvironment(t, "app {\n x = \"target\"\n}\n", "app {\n x = \"target\"\n}\n", before)
	res := reconcileForVerification(t, env, plan.ModeApply)
	if res.Recovery.PersistedCount != 1 {
		t.Fatalf("recovery fixture did not persist: %+v", res.Recovery)
	}
	if err := verification.VerifyReconcileResultInvariants(before, res); err != nil {
		t.Fatalf("durable recovery rejected: %v", err)
	}
	for _, mutation := range []string{"missing", "unknown-ancestor", "conflicting-value", "conflict-outcome"} {
		t.Run(mutation, func(t *testing.T) {
			broken := *res
			broken.Recovery = res.Recovery
			switch mutation {
			case "missing":
				broken.Recovery.DurableSnapshot = nil
			case "unknown-ancestor", "conflicting-value":
				snapshot := observation.NewSnapshot()
				snapshot.Set(p, res.Recovery.ObservedSnapshot.Get(p))
				if mutation == "conflicting-value" {
					snapshot.Set(model.MustParsePath("/app"), observation.PresentRecord())
					snapshot.Set(p, observation.MustPresentLeaf(lkal.String("other")))
				}
				broken.Recovery.DurableSnapshot = snapshot
			case "conflict-outcome":
				broken.Recovery.StabilizeOutcome = provider.StabilizeConflict
			}
			if err := verification.VerifyReconcileResultInvariants(before, &broken); err == nil {
				t.Fatal("invalid recovery durability evidence accepted")
			}
		})
	}
}

func TestAllPlanChecksIncludeBlockedAndModeIsolationCases(t *testing.T) {
	p := model.MustParsePath("/x")
	leaf := observation.NewSnapshot()
	leaf.Set(p, observation.MustPresentLeaf(lkal.String("external")))
	absent := observation.NewSnapshot()
	absent.Set(p, observation.AbsentObserved())
	foreign, _ := ownership.NewSettledModified(actorB, lkal.String("external"), 1)
	foreignStore, _ := ownership.NewStore().Put(testResID, p, foreign)
	pending, _ := ownership.NewPendingCreate(actorA, "previous", lkal.String("target"), 1)
	pendingStore, _ := ownership.NewStore().Put(testResID, p, pending)
	created, _ := ownership.NewSettledCreated(actorA, lkal.String("original"), 1)
	createdStore, _ := ownership.NewStore().Put(testResID, p, created)
	for _, tc := range []struct {
		name, declaration string
		snapshot          *observation.Snapshot
		store             *ownership.Store
		permissions       scope.Scope
	}{
		{"unknown", "x = \"target\"\n", observation.NewSnapshot(), ownership.NewStore(), fullScope(t)},
		{"foreign-owner", "x = \"target\"\n", leaf, foreignStore, fullScope(t)},
		{"unowned-conflict", "x = \"target\"\n", leaf, ownership.NewStore(), fullScope(t)},
		{"unresolved-declared-pending", "x = \"target\"\n", leaf, pendingStore, fullScope(t)},
		{"retired-created-drift", "", leaf, createdStore, fullScope(t)},
		{"retired-created-unknown", "", observation.NewSnapshot(), createdStore, fullScope(t)},
		{"permission-denied", "x = \"target\"\n", absent, ownership.NewStore(), scope.Scope{}},
		{"created-structure-migration", "x {\n child = \"target\"\n}\n", leaf, createdStore, fullScope(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := verification.VerifyAll11PlanTheorems(plan.Input{Resource: testResID, Actor: actorA, Declaration: mustDecl(t, tc.declaration), Snapshot: tc.snapshot, Store: tc.store, Scope: tc.permissions}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExplicitResolutionCheckDetectsNewUnrelatedOwnership(t *testing.T) {
	p := model.MustParsePath("/x")
	snapshot := observation.NewSnapshot()
	snapshot.Set(p, observation.MustPresentLeaf(lkal.String("existing")))
	token, err := plan.ComputeConditionToken(testResID, p, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	req := plan.ResolutionRequest{Resource: testResID, Actor: actorA, Path: p, ExpectedToken: token, Declaration: mustDecl(t, ""), Snapshot: snapshot, Store: ownership.NewStore(), Scope: fullScope(t)}
	resolved, err := plan.PlanAdopt(req)
	if err != nil {
		t.Fatal(err)
	}
	extra, _ := ownership.NewSettledModified(actorA, lkal.String("unrelated"), 1)
	resolved.NextStore, err = resolved.NextStore.Put("different-resource", model.MustParsePath("/unrelated"), extra)
	if err != nil {
		t.Fatal(err)
	}
	if err := verification.VerifyTheorem9ExplicitResolutionSafety(req, resolved, nil); err == nil {
		t.Fatal("resolution checker ignored newly introduced unrelated ownership")
	}
}
