package protocol_test

import (
	"os"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/protocol"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
)

// TestCounterexamples04_18_42_45_46_60_BatchStagesQuarantineAndPostReleaseDrift verifies ADR 0004 §2, §4
// and Counterexamples #4, #18, #42, #45, #46, #60.
func TestCounterexamples04_18_42_45_46_60_BatchStagesQuarantineAndPostReleaseDrift(t *testing.T) {
	// Part 1: Counterexample #4 & #46 (Mixed batch where ApplyBatch returns NotApplied ->
	// rolls back environment Pending to PreviousEntry, does not save MetadataCandidates, returns SummaryBlocked).
	targetPath, prov, locks := setupFileProvider(t, `@lkal "0.1"
app {
  env_key = "initial"
  adopt_key = "match"
}
`)
	store := ownership.NewStore()
	settledEnv, _ := ownership.NewSettledCreated(actorA, lkal.String("initial"), 1)
	store, _ = store.Put(testResID, model.MustParsePath("/app/env_key"), settledEnv)
	repo := protocol.NewMemoryStoreRepository(store, nil)

	decl := mustDecl(t, `
app {
  env_key = "updated"
  adopt_key = "match"
}
`)
	// Tamper with the file right when StageIntentSave happens so ApplyBatch sees Expected mismatch -> NotApplied (#4, #46).
	repo.OnSaveStore = func(stage protocol.StoreCommitStage, next *ownership.Store) *protocol.CommitResult {
		if stage == protocol.StageIntentSave {
			_ = os.WriteFile(targetPath, []byte("@lkal \"0.1\"\napp {\n  env_key = \"concurrent-edit\"\n  adopt_key = \"match\"\n}\n"), 0o600)
		}
		return nil
	}

	resNotApplied, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     testStoreID,
		Resource:    testResID,
		Actor:       actorA,
		Mode:        plan.ModeApply,
		BatchID:     "b-not-applied",
		Declaration: decl,
		Scope:       fullScope(t),
		Provider:    prov,
		StoreRepo:   repo,
		Locks:       locks,
	})
	repo.OnSaveStore = nil
	if err != nil {
		t.Fatal(err)
	}
	if resNotApplied.Summary.Execution != protocol.SummaryBlocked {
		t.Fatalf("Counterexample #4/#46 failed: expected SummaryBlocked on NotApplied, got %v", resNotApplied.Summary.Execution)
	}
	// Verify /app/env_key was rolled back to settled PreviousEntry ("initial") and /app/adopt_key was NOT saved!
	eEnv, ok := resNotApplied.FinalStore.Get(testResID, model.MustParsePath("/app/env_key"))
	if !ok || !eEnv.IsSettled() || !model.EqualLeafValues(eEnv.LastApplied, lkal.String("initial")) {
		t.Fatalf("Counterexample #46 failed: /app/env_key not rolled back to PreviousEntry: %+v", eEnv)
	}
	if resNotApplied.FinalStore.Has(testResID, model.MustParsePath("/app/adopt_key")) {
		t.Fatalf("Counterexample #46 failed: MetadataCandidate /app/adopt_key must not be saved when ApplyBatch is NotApplied")
	}

	// Part 2: Counterexamples #18 & #45 (Provider returns Applied, but in-lock re-observation detects contract violation K501 ->
	// does NOT commit ownership, retains Pending, persists Quarantine K503, returns SummaryFailed).
	_, provCorrupt, locksCorrupt := setupFileProvider(t, `@lkal "0.1"
app {
  sibling = "untouched"
}
`)
	repoCorrupt := protocol.NewMemoryStoreRepository(ownership.NewStore(), nil)
	declCorrupt := mustDecl(t, `
app {
  key = "desired"
}
`)
	// Buggy provider corrupts the written value while returning Applied (#18, #45).
	provCorrupt.Hooks.MutatePatchedBytesForTest = func(resID ownership.ResourceID, batchID string, patched []byte) []byte {
		return []byte("@lkal \"0.1\"\napp {\n  key = \"wrong-value-written\"\n  sibling = \"untouched\"\n}\n")
	}

	resViol, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     testStoreID,
		Resource:    testResID,
		Actor:       actorA,
		Mode:        plan.ModeApply,
		BatchID:     "b-violation",
		Declaration: declCorrupt,
		Scope:       fullScope(t),
		Provider:    provCorrupt,
		StoreRepo:   repoCorrupt,
		Locks:       locksCorrupt,
	})
	provCorrupt.Hooks.MutatePatchedBytesForTest = nil
	if err != nil {
		t.Fatal(err)
	}
	if resViol.Summary.Execution != protocol.SummaryFailed || !resViol.NormalExecution.ContractViolation || !resViol.NormalExecution.QuarantinedNow {
		t.Fatalf("Counterexamples #18/#45 failed: expected SummaryFailed + ContractViolation + QuarantinedNow, got %+v", resViol.NormalExecution)
	}
	if !resViol.Summary.Counts.EnvironmentPossiblyModified || resViol.Summary.Counts.RemainingPending != 1 {
		t.Fatalf("Counterexample #45 failed: expected EnvironmentPossiblyModified=true and RemainingPending=1, got %+v", resViol.Summary.Counts)
	}
	qtLoaded, _ := repoCorrupt.LoadQuarantine()
	if !qtLoaded.IsQuarantined(testResID) {
		t.Fatalf("Counterexample #45 failed: expected Resource to be quarantined in repository")
	}
	ePending, ok := resViol.FinalStore.Get(testResID, model.MustParsePath("/app/key"))
	if !ok || !ePending.HasPending() {
		t.Fatalf("Counterexample #45 failed: expected Pending to remain uncommitted after K501 contract violation")
	}

	// Subsequent call on quarantined Resource is immediately Blocked with K503 and does NOT auto-recover Pending (#45).
	resSubsequent, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     testStoreID,
		Resource:    testResID,
		Actor:       actorA,
		Mode:        plan.ModeApply,
		BatchID:     "b-after-quarantine",
		Declaration: declCorrupt,
		Scope:       fullScope(t),
		Provider:    provCorrupt,
		StoreRepo:   repoCorrupt,
		Locks:       locksCorrupt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resSubsequent.Summary.Execution != protocol.SummaryBlocked || resSubsequent.Recovery.PersistedCount != 0 {
		t.Fatalf("Counterexample #45 failed: quarantined resource must block all operations and forbid normal recovery, got %+v", resSubsequent.Summary)
	}

	// Part 3: Counterexample #60 (Store Commit after Applied returns Indeterminate -> SummaryIndeterminate, StoreOutcomeUnknown=true).
	_, prov60, locks60 := setupFileProvider(t, "@lkal \"0.1\"\napp {\n  sibling = \"keep\"\n}\n")
	repo60 := protocol.NewMemoryStoreRepository(ownership.NewStore(), nil)
	repo60.OnSaveStore = func(stage protocol.StoreCommitStage, next *ownership.Store) *protocol.CommitResult {
		if stage == protocol.StageFinalBatchCommit {
			return &protocol.CommitResult{Status: protocol.CommitIndeterminate}
		}
		return nil
	}
	res60, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     testStoreID,
		Resource:    testResID,
		Actor:       actorA,
		Mode:        plan.ModeApply,
		BatchID:     "b-60",
		Declaration: mustDecl(t, "app {\n  k = \"v1\"\n}\n"),
		Scope:       fullScope(t),
		Provider:    prov60,
		StoreRepo:   repo60,
		Locks:       locks60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res60.Summary.Execution != protocol.SummaryIndeterminate || !res60.NormalExecution.StoreOutcomeUnknown || res60.FinalStore != nil {
		t.Fatalf("Counterexample #60 failed: indeterminate FinalBatchCommit must return SummaryIndeterminate with nil FinalStore, got %+v", res60)
	}

	// Part 4: Counterexample #42 (External modification AFTER lock release is classified as K203 ExternalDrift, NEVER K501).
	target42, prov42, locks42 := setupFileProvider(t, "@lkal \"0.1\"\napp {\n  sibling = \"keep\"\n}\n")
	repo42 := protocol.NewMemoryStoreRepository(ownership.NewStore(), nil)
	decl42 := mustDecl(t, "app {\n  k = \"v1\"\n}\n")
	res42First, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     testStoreID,
		Resource:    testResID,
		Actor:       actorA,
		Mode:        plan.ModeApply,
		BatchID:     "b-42-1",
		Declaration: decl42,
		Scope:       fullScope(t),
		Provider:    prov42,
		StoreRepo:   repo42,
		Locks:       locks42,
	})
	if err != nil || res42First.Summary.Execution != protocol.SummaryCompleted {
		t.Fatalf("expected 1st apply to complete: %v %+v", err, res42First)
	}
	// External modification after lock release:
	if err := os.WriteFile(target42, []byte("@lkal \"0.1\"\napp {\n  k = \"external-after-unlock\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res42Second, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     testStoreID,
		Resource:    testResID,
		Actor:       actorA,
		Mode:        plan.ModeApply,
		BatchID:     "b-42-2",
		Declaration: decl42,
		Scope:       fullScope(t),
		Provider:    prov42,
		StoreRepo:   repo42,
		Locks:       locks42,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res42Second.Summary.Execution != protocol.SummaryBlocked || res42Second.NormalExecution.ContractViolation {
		t.Fatalf("Counterexample #42 failed: post-unlock external edit must be SummaryBlocked (ExternalDrift), never ContractViolation: %+v", res42Second.Summary)
	}
}

// TestCounterexamples38_50_AndAdminRecoveryBoundary verifies ADR 0004 §2, §4
// and Counterexamples #38, #45, #50 (Explicit Resolution & Administrator Recovery Boundary).
func TestCounterexamples38_50_AndAdminRecoveryBoundary(t *testing.T) {
	_, prov, locks := setupFileProvider(t, "@lkal \"0.1\"\napp {\n  k = \"existing\"\n}\n")
	repo := protocol.NewMemoryStoreRepository(ownership.NewStore(), nil)
	decl := mustDecl(t, "app {\n  k = \"desired\"\n}\n")
	pK := model.MustParsePath("/app/k")

	snap, err := prov.Observe(testResID, pK)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := plan.ComputeConditionToken(testResID, pK, snap)
	if err != nil {
		t.Fatal(err)
	}

	// Counterexample #50: Explicit adopt where store Commit is Indeterminate -> SummaryIndeterminate, StoreOutcomeUnknown=true.
	repo.OnSaveStore = func(stage protocol.StoreCommitStage, next *ownership.Store) *protocol.CommitResult {
		return &protocol.CommitResult{Status: protocol.CommitIndeterminate}
	}
	resIndet, err := protocol.ExecuteExplicitResolution(protocol.ExplicitResolutionExecutionRequest{
		StoreID:          testStoreID,
		Resource:         testResID,
		Actor:            actorA,
		Action:           scope.ActionExplicitAdopt,
		Path:             pK,
		ExpectedToken:    tok,
		ExpectedRevision: 0,
		Declaration:      decl,
		Scope:            fullScope(t),
		Provider:         prov,
		StoreRepo:        repo,
		Locks:            locks,
	})
	repo.OnSaveStore = nil
	if err != nil {
		t.Fatal(err)
	}
	if resIndet.Execution != protocol.SummaryIndeterminate || !resIndet.StoreOutcomeUnknown || resIndet.Persisted {
		t.Fatalf("Counterexample #50 failed: expected SummaryIndeterminate with StoreOutcomeUnknown=true, got %+v", resIndet)
	}

	// Normal explicit adopt succeeds -> Origin=Modified, revision=1.
	resAdopt, err := protocol.ExecuteExplicitResolution(protocol.ExplicitResolutionExecutionRequest{
		StoreID:          testStoreID,
		Resource:         testResID,
		Actor:            actorA,
		Action:           scope.ActionExplicitAdopt,
		Path:             pK,
		ExpectedToken:    tok,
		ExpectedRevision: 0,
		Declaration:      decl,
		Scope:            fullScope(t),
		Provider:         prov,
		StoreRepo:        repo,
		Locks:            locks,
	})
	if err != nil || resAdopt.Execution != protocol.SummaryCompleted || !resAdopt.Persisted {
		t.Fatalf("expected explicit adopt to succeed, got %v %+v", err, resAdopt)
	}

	// ADR 0004 §4: Administrator recovery boundary tests.
	pConflict := model.MustParsePath("/app/k")
	pendingEntry, _ := ownership.NewPendingCreate(actorA, "b-1", lkal.String("existing"), 1)
	adminStore, _ := ownership.NewStore().Put(testResID, pConflict, pendingEntry)
	qRec, _ := protocol.BuildContractViolationQuarantine(testResID, "b-1", nil)
	qt, _ := protocol.NewQuarantineTable().Quarantine(qRec)
	stabOK := provider.EvaluateStabilize(snap, snap, []model.Path{pConflict}, nil)

	// 1. Rejects normal agents (CallerIsAdmin == false -> K401 PolicyDenied).
	if _, err := protocol.ResolveAdminRecovery(protocol.AdminRecoveryRequest{
		CallerIsAdmin: false,
		Resource:      testResID,
		Path:          pConflict,
		Action:        protocol.AdminActionAdoptCurrentObserved,
		Store:         adminStore,
		Quarantine:    qt,
		Snapshot:      snap,
		Stabilize:     stabOK,
	}); err == nil || err.(model.Diagnostic).Code != model.CodePolicyDenied {
		t.Fatalf("expected normal agent to be rejected with K401 from admin recovery, got %v", err)
	}

	// 2. Even when CallerIsAdmin == true and CreationAttributionVerified == true on an Origin=None entry,
	// value match alone NEVER grants Origin=Created; it downgrades to Origin=Modified with K601 and clears Quarantine!
	adminRes, err := protocol.ResolveAdminRecovery(protocol.AdminRecoveryRequest{
		CallerIsAdmin:               true,
		InFlightExecution:           false,
		Resource:                    testResID,
		Path:                        pConflict,
		Action:                      protocol.AdminActionAdoptCurrentObserved,
		CreationAttributionVerified: false,
		Store:                       adminStore,
		Quarantine:                  qt,
		Snapshot:                    observation.ObserveRecord(lkal.Record{Fields: []lkal.Field{{Key: "app", Value: lkal.Record{Fields: []lkal.Field{{Key: "k", Value: lkal.String("existing")}}}}}}, pConflict),
		Stabilize:                   stabOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	if adminRes.UpdatedEntry == nil || adminRes.UpdatedEntry.Origin != ownership.OriginModified {
		t.Fatalf("ADR 0004 §4 failed: expected conservative downgrade to Origin=Modified, got %+v", adminRes.UpdatedEntry)
	}
	if adminRes.NextQuarantine.IsQuarantined(testResID) {
		t.Fatalf("expected quarantine to be cleared after resolving the last Pending entry")
	}
}
