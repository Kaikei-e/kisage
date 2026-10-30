package protocol_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/protocol"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
)

const (
	testStoreID ownership.StoreID    = "store-main"
	testResID   ownership.ResourceID = "res-app"
	actorA      ownership.ActorID    = "actor-a"
	actorB      ownership.ActorID    = "actor-b"
)

func fullScope(t *testing.T) scope.Scope {
	t.Helper()
	r, err := scope.NewRootRule(scope.PermAll)
	if err != nil {
		t.Fatal(err)
	}
	return scope.MustNew(r)
}

func setupFileProvider(t *testing.T, initialLKAL string) (string, *provider.LKALFileProvider, *provider.FileLockManager) {
	t.Helper()
	rootDir := t.TempDir()
	if err := os.Chmod(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lockDir := t.TempDir()
	if err := os.Chmod(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(rootDir, "app.lkal")
	if err := os.WriteFile(targetPath, []byte(initialLKAL), 0o600); err != nil {
		t.Fatal(err)
	}
	phys, err := provider.InspectPhysicalIdentity(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := ownership.NewResourceRegistry(ownership.ResourceBinding{
		ID:             testResID,
		Provider:       ownership.ProviderLKALFile,
		ManagementRoot: rootDir,
		TargetPath:     targetPath,
		StoreID:        testStoreID,
		Physical:       phys,
	})
	if err != nil {
		t.Fatal(err)
	}
	prov, err := provider.NewLKALFileProvider(reg)
	if err != nil {
		t.Fatal(err)
	}
	locks, err := provider.NewFileLockManager(lockDir)
	if err != nil {
		t.Fatal(err)
	}
	return targetPath, prov, locks
}

func mustDecl(t *testing.T, src string) *projection.Declaration {
	t.Helper()
	d, err := projection.ParseDeclaration("decl.lkal", []byte("@lkal \"0.1\"\n"+src))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestCounterexamples57_58_ExecutionBatchVsDurableIntentSeparation verifies ADR 0004 §1, §2 Step 3–5,
// Theorem 11, and Counterexamples #57 and #58:
//   - #57: Metadata-only batch (AdoptModified, Unmanage, ClearRecord) produces no DurableIntent,
//     no Pending reservation, no ApplyBatch call, no synthesized Applied Receipt, and commits once after Stabilize.
//   - #58: Mixed batch (EnvironmentOps + MetadataCandidates) crashing after StageIntentSave persists
//     ONLY EnvironmentOps as Pending; MetadataCandidates are never persisted as Pending and are re-planned from the latest state on recovery.
func TestCounterexamples57_58_ExecutionBatchVsDurableIntentSeparation(t *testing.T) {
	// Part 1: Counterexample #57 (Metadata-only batch).
	_, prov, locks := setupFileProvider(t, `@lkal "0.1"
app {
  existing_match = "same"
}
`)
	store := ownership.NewStore()
	retMod, _ := ownership.NewSettledModified(actorA, lkal.Int(1), 1)
	retCreAbsent, _ := ownership.NewSettledCreated(actorA, lkal.Int(2), 1)
	store, _ = store.Put(testResID, model.MustParsePath("/app/ret_mod"), retMod)
	store, _ = store.Put(testResID, model.MustParsePath("/app/ret_cre_absent"), retCreAbsent)

	repo := protocol.NewMemoryStoreRepository(store, nil)
	decl := mustDecl(t, `
app {
  existing_match = "same"
}
`)

	res, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     testStoreID,
		Resource:    testResID,
		Actor:       actorA,
		Mode:        plan.ModeApply,
		BatchID:     "batch-meta-only",
		Declaration: decl,
		Scope:       fullScope(t),
		Provider:    prov,
		StoreRepo:   repo,
		Locks:       locks,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.NormalExecution.Intent != nil || res.NormalExecution.IntentSaved || res.NormalExecution.Receipt != nil {
		t.Fatalf("Counterexample #57 failed: metadata-only batch must not create DurableIntent or Receipt: %+v", res.NormalExecution)
	}
	if len(repo.SavedStagesHistory) != 1 || repo.SavedStagesHistory[0] != protocol.StageFinalBatchCommit {
		t.Fatalf("Counterexample #57 failed: expected exactly 1 store save (StageFinalBatchCommit), got %v", repo.SavedStagesHistory)
	}
	if res.Summary.Execution != protocol.SummaryCompleted || res.Summary.Convergence != plan.ConvergenceConverged {
		t.Fatalf("expected SummaryCompleted & Converged, got %+v", res.Summary)
	}

	// Part 2: Counterexample #58 (Mixed batch crashes right after StageIntentSave).
	_, prov2, locks2 := setupFileProvider(t, `@lkal "0.1"
app {
  adopt_me = "existing"
}
`)
	repo2 := protocol.NewMemoryStoreRepository(ownership.NewStore(), nil)
	declMixed := mustDecl(t, `
app {
  adopt_me = "existing"
  create_me = "new-val"
}
`)

	// Simulate crash after StageIntentSave by inspecting the store saved at StageIntentSave.
	var storeAfterIntent *ownership.Store
	repo2.OnSaveStore = func(stage protocol.StoreCommitStage, next *ownership.Store) *protocol.CommitResult {
		if stage == protocol.StageIntentSave {
			storeAfterIntent = next.Clone()
			return &protocol.CommitResult{
				Status: protocol.CommitFailed,
				Diagnostics: []model.Diagnostic{{
					Code:   model.CodeIndeterminateOutcome,
					Reason: "simulated stop after intent inspection",
				}},
			}
		}
		return nil
	}
	_, _ = protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     testStoreID,
		Resource:    testResID,
		Actor:       actorA,
		Mode:        plan.ModeApply,
		BatchID:     "batch-mixed",
		Declaration: declMixed,
		Scope:       fullScope(t),
		Provider:    prov2,
		StoreRepo:   repo2,
		Locks:       locks2,
	})
	if storeAfterIntent == nil {
		t.Fatal("expected StageIntentSave to be invoked for mixed batch")
	}
	// Verify ONLY /app/create_me has a Pending entry in storeAfterIntent; /app/adopt_me must NOT be in storeAfterIntent!
	if !storeAfterIntent.Has(testResID, model.MustParsePath("/app/create_me")) {
		t.Fatalf("Counterexample #58 failed: expected /app/create_me Pending in storeAfterIntent")
	}
	if storeAfterIntent.Has(testResID, model.MustParsePath("/app/adopt_me")) {
		t.Fatalf("Counterexample #58 failed: MetadataCandidate /app/adopt_me must NEVER be persisted in DurableIntent")
	}
}

// TestCounterexamples06_07_27_40_43_44_59_60_RecoveryWithStabilizeAndSiblings verifies ADR 0004 §3
// and Counterexamples #6, #7, #27, #40, #43, #44, #59, #60.
func TestCounterexamples06_07_27_40_43_44_59_60_RecoveryWithStabilizeAndSiblings(t *testing.T) {
	targetPath, prov, locks := setupFileProvider(t, `@lkal "0.1"
app {
  new_created = "target-v"
  existing_updated = 200
  undeclared_sibling = "edited-by-human-after-crash"
}
`)

	store := ownership.NewStore()
	// #6 & #27: Origin=None Pending Set where Target ("target-v") is observed on disk, and undeclared sibling changed.
	pNew, _ := ownership.NewPendingCreate(actorA, "b-crash", lkal.String("target-v"), 1)
	store, _ = store.Put(testResID, model.MustParsePath("/app/new_created"), pNew)

	// #7: Origin=Created Pending Set update where Target (200) is observed on disk -> preserves Origin=Created.
	settledCre, _ := ownership.NewSettledCreated(actorA, lkal.Int(100), 1)
	pUpd, _ := ownership.BeginSetUpdate(settledCre, "b-crash", lkal.Int(200), 2)
	store, _ = store.Put(testResID, model.MustParsePath("/app/existing_updated"), pUpd)

	// #40: Foreign Actor B has an unrelated Pending reservation at /foreign/b -> ignored by A's recovery and preserved.
	pForeignB, _ := ownership.NewPendingCreate(actorB, "b-foreign", lkal.String("b-val"), 1)
	store, _ = store.Put(testResID, model.MustParsePath("/foreign/b"), pForeignB)

	repo := protocol.NewMemoryStoreRepository(store, nil)
	decl := mustDecl(t, `
app {
  new_created = "target-v"
  existing_updated = 200
}
`)

	// Counterexample #43: If Stabilize fails during recovery, Pending must be retained and not committed!
	prov.Hooks.OnStabilizeSync = func(resID ownership.ResourceID) error {
		return errors.New("simulated Stabilize fsync failure during recovery")
	}
	resStabFail, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     testStoreID,
		Resource:    testResID,
		Actor:       actorA,
		Mode:        plan.ModeApply,
		BatchID:     "b-rec-1",
		Declaration: decl,
		Scope:       fullScope(t),
		Provider:    prov,
		StoreRepo:   repo,
		Locks:       locks,
	})
	prov.Hooks.OnStabilizeSync = nil
	if err != nil {
		t.Fatal(err)
	}
	if resStabFail.Summary.Execution != protocol.SummaryIndeterminate || resStabFail.Recovery.PersistedCount != 0 {
		t.Fatalf("Counterexample #43 failed: Stabilize failure during recovery must return Indeterminate with 0 persisted, got %+v", resStabFail.Summary)
	}
	loadedAfterFail, _ := repo.LoadStore()
	if e, _ := loadedAfterFail.Get(testResID, model.MustParsePath("/app/new_created")); !e.HasPending() {
		t.Fatalf("Counterexample #43 failed: Pending was cleared despite Stabilize failure")
	}

	// Counterexamples #6, #7, #27, #40, #59: Stabilize succeeds -> recovery commits, normalExecution is NoOp -> SummaryCompleted!
	// Even if undeclared_sibling in the file was modified externally (#27), recovery succeeds without whole-file digest.
	_ = targetPath
	resOK, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     testStoreID,
		Resource:    testResID,
		Actor:       actorA,
		Mode:        plan.ModeApply,
		BatchID:     "b-rec-2",
		Declaration: decl,
		Scope:       fullScope(t),
		Provider:    prov,
		StoreRepo:   repo,
		Locks:       locks,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Counterexample #59: recovery=Completed and normalExecution=NoOp -> SummaryCompleted.
	if resOK.Recovery.Status != protocol.RecoveryCompleted || resOK.NormalExecution.Status != protocol.NormalNoOp || resOK.Summary.Execution != protocol.SummaryCompleted {
		t.Fatalf("Counterexample #59 failed: expected RecoveryCompleted + NormalNoOp -> SummaryCompleted, got rec=%v norm=%v sum=%v", resOK.Recovery.Status, resOK.NormalExecution.Status, resOK.Summary.Execution)
	}
	// Counterexample #6 & #27: /app/new_created downgraded to Origin=Modified with K601.
	eNew, _ := resOK.FinalStore.Get(testResID, model.MustParsePath("/app/new_created"))
	if eNew.Origin != ownership.OriginModified || !eNew.IsSettled() {
		t.Fatalf("Counterexample #6/#27 failed: expected Origin=Modified on recovered creation, got %+v", eNew)
	}
	// Counterexample #7: /app/existing_updated preserved Origin=Created.
	eUpd, _ := resOK.FinalStore.Get(testResID, model.MustParsePath("/app/existing_updated"))
	if eUpd.Origin != ownership.OriginCreated || !eUpd.IsSettled() {
		t.Fatalf("Counterexample #7 failed: expected Origin=Created preserved on recovered update, got %+v", eUpd)
	}
	// Counterexample #40: Foreign Actor B's Pending at /foreign/b is untouched.
	eForeign, ok := resOK.FinalStore.Get(testResID, model.MustParsePath("/foreign/b"))
	if !ok || !eForeign.HasPending() || eForeign.Manager != actorB {
		t.Fatalf("Counterexample #40 failed: foreign Actor B's Pending was altered: %+v", eForeign)
	}

	// Counterexample #44 & #60: Recovery succeeds (recovery=Completed), then normal plan is Blocked ->
	// SummaryBlocked, while recovered records remain persisted in store and Counts.RecoveredPersisted == 1!
	store44 := ownership.NewStore()
	pRec44, _ := ownership.NewPendingCreate(actorA, "b-44", lkal.String("target-v"), 1)
	store44, _ = store44.Put(testResID, model.MustParsePath("/app/new_created"), pRec44)
	store44, _ = store44.Put(testResID, model.MustParsePath("/foreign/b"), pForeignB) // overlap if declared
	repo44 := protocol.NewMemoryStoreRepository(store44, nil)
	decl44Blocked := mustDecl(t, `
app {
  new_created = "target-v"
}
foreign {
  b = "conflict-with-actor-b"
}
`)
	res44, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     testStoreID,
		Resource:    testResID,
		Actor:       actorA,
		Mode:        plan.ModeApply,
		BatchID:     "b-44-run",
		Declaration: decl44Blocked,
		Scope:       fullScope(t),
		Provider:    prov,
		StoreRepo:   repo44,
		Locks:       locks,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res44.Recovery.Status != protocol.RecoveryCompleted || res44.NormalExecution.Status != protocol.NormalBlocked || res44.Summary.Execution != protocol.SummaryBlocked {
		t.Fatalf("Counterexample #44/#60 failed: expected RecoveryCompleted + NormalBlocked -> SummaryBlocked, got %+v", res44.Summary)
	}
	if res44.Summary.Counts.RecoveredPersisted != 1 {
		t.Fatalf("Counterexample #44/#60 failed: expected Counts.RecoveredPersisted=1 to be preserved, got %d", res44.Summary.Counts.RecoveredPersisted)
	}
	persisted44, _ := repo44.LoadStore()
	if e, _ := persisted44.Get(testResID, model.MustParsePath("/app/new_created")); !e.IsSettled() {
		t.Fatalf("Counterexample #44 failed: persisted recovery record was rolled back when normal plan blocked")
	}
}
