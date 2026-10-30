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
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/protocol"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
	"github.com/Kaikei-e/kisage/verification"
)

const (
	testStoreID ownership.StoreID    = "store-verif"
	testResID   ownership.ResourceID = "res-verif"
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

func mustDecl(t *testing.T, src string) *projection.Declaration {
	t.Helper()
	d, err := projection.ParseDeclaration("test.lkal", []byte("@lkal \"0.1\"\n"+src))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestPlanElevenSafetyTheorems verifies all 11 pure `Plan` safety theorems from ADR 0005 §3.1
// across normal, conflict, unknown, retired, migration, and metadata-only inputs.
func TestPlanElevenSafetyTheorems(t *testing.T) {
	pCreate := model.MustParsePath("/app/create")
	pUpdate := model.MustParsePath("/app/update")
	pAdopt := model.MustParsePath("/app/adopt")
	pRetMod := model.MustParsePath("/app/ret_mod")
	pRetCre := model.MustParsePath("/app/ret_cre")
	pForeign := model.MustParsePath("/other/foreign")

	store := ownership.NewStore()
	eUpdate, _ := ownership.NewSettledCreated(actorA, lkal.String("old"), 1)
	eRetMod, _ := ownership.NewSettledModified(actorA, lkal.String("m"), 1)
	eRetCre, _ := ownership.NewSettledCreated(actorA, lkal.String("c"), 1)
	eForeign, _ := ownership.NewSettledCreated(actorB, lkal.String("f"), 1)

	store, _ = store.Put(testResID, pUpdate, eUpdate)
	store, _ = store.Put(testResID, pRetMod, eRetMod)
	store, _ = store.Put(testResID, pRetCre, eRetCre)
	store, _ = store.Put(testResID, pForeign, eForeign)

	rec := lkal.Record{Fields: []lkal.Field{
		{Key: "app", Value: lkal.Record{Fields: []lkal.Field{
			{Key: "update", Value: lkal.String("old")},
			{Key: "adopt", Value: lkal.String("same")},
			{Key: "ret_mod", Value: lkal.String("m")},
			{Key: "ret_cre", Value: lkal.String("c")},
		}}},
	}}
	snap := observation.ObserveRecord(rec, pCreate, pUpdate, pAdopt, pRetMod, pRetCre, pForeign)
	decl := mustDecl(t, "app {\n  create = \"new\"\n  update = \"updated\"\n  adopt = \"same\"\n}\n")

	in := plan.Input{
		Resource:    testResID,
		Actor:       actorA,
		Declaration: decl,
		Snapshot:    snap,
		Store:       store,
		Scope:       fullScope(t),
	}
	if err := verification.VerifyAll11PlanTheorems(in); err != nil {
		t.Fatalf("VerifyAll11PlanTheorems failed on normal multi-path scenario: %v", err)
	}

	// Also verify Theorem 9 on explicit resolution (`PlanAdopt` and `PlanRelease`).
	tok, err := plan.ComputeConditionToken(testResID, pAdopt, snap)
	if err != nil {
		t.Fatal(err)
	}
	adoptReq := plan.ResolutionRequest{
		Resource:         testResID,
		Actor:            actorA,
		Path:             pAdopt,
		ExpectedToken:    tok,
		ExpectedRevision: 0,
		Declaration:      decl,
		Snapshot:         snap,
		Store:            store,
		Scope:            fullScope(t),
	}
	resPlan, resErr := plan.PlanAdopt(adoptReq)
	if err := verification.VerifyTheorem9ExplicitResolutionSafety(adoptReq, resPlan, resErr); err != nil {
		t.Fatalf("VerifyTheorem9ExplicitResolutionSafety failed: %v", err)
	}
}

// TestProtocolInductiveInvariantsAndFiniteConvergence verifies ADR 0005 §3.2 and §3.3:
//   - Bounded convergence of `apply -> prune` with strict ESR rank decrease to 0.
//   - Two-step convergence of retired `Modified` overlap (`DeferredUntilRelease`) when externally migrated (#49)
//     vs halting with `StructureConflict` (K202) when unmigrated (#55).
func TestProtocolInductiveInvariantsAndFiniteConvergence(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.Chmod(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lockDir := t.TempDir()
	if err := os.Chmod(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(rootDir, "app.lkal")
	if err := os.WriteFile(targetPath, []byte("@lkal \"0.1\"\napp {\n  retired_cre = \"old\"\n  keep = \"stay\"\n}\n"), 0o600); err != nil {
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

	eRetCre, _ := ownership.NewSettledCreated(actorA, lkal.String("old"), 1)
	initialStore, _ := ownership.NewStore().Put(testResID, model.MustParsePath("/app/retired_cre"), eRetCre)
	repo := protocol.NewMemoryStoreRepository(initialStore, nil)

	decl := mustDecl(t, "app {\n  fresh = \"created\"\n  keep = \"stay\"\n}\n")
	applyRes, pruneRes, err := verification.VerifyApplyThenPruneConvergence(verification.ConvergenceVerifyEnv{
		StoreID:         testStoreID,
		Resource:        testResID,
		Actor:           actorA,
		Declaration:     decl,
		Scope:           fullScope(t),
		Provider:        prov,
		StoreRepo:       repo,
		Locks:           locks,
		CreationHistory: map[string]ownership.ActorID{"/app/retired_cre": actorA},
	})
	if err != nil {
		t.Fatalf("VerifyApplyThenPruneConvergence failed: %v (apply=%+v, prune=%+v)", err, applyRes, pruneRes)
	}

	// Part 2: Verify retired Modified overlap (`DeferredUntilRelease`) 2-step lemma (#49 vs #55).
	for _, migrated := range []bool{true, false} {
		targetMod := filepath.Join(rootDir, "mod_"+boolName(migrated)+".lkal")
		fileContent := "@lkal \"0.1\"\na {\n  b = \"leaf-still-here\"\n}\n"
		if migrated {
			fileContent = "@lkal \"0.1\"\na {\n  b {\n    c = \"migrated-val\"\n  }\n}\n"
		}
		if err := os.WriteFile(targetMod, []byte(fileContent), 0o600); err != nil {
			t.Fatal(err)
		}
		physMod, err := provider.InspectPhysicalIdentity(targetMod)
		if err != nil {
			t.Fatal(err)
		}
		resModID := ownership.ResourceID("res-" + boolName(migrated))
		regMod, err := ownership.NewResourceRegistry(ownership.ResourceBinding{
			ID:             resModID,
			Provider:       ownership.ProviderLKALFile,
			ManagementRoot: rootDir,
			TargetPath:     targetMod,
			StoreID:        testStoreID,
			Physical:       physMod,
		})
		if err != nil {
			t.Fatal(err)
		}
		provMod, err := provider.NewLKALFileProvider(regMod)
		if err != nil {
			t.Fatal(err)
		}
		eOldMod, _ := ownership.NewSettledModified(actorA, lkal.String("old-b"), 1)
		storeMod, _ := ownership.NewStore().Put(resModID, model.MustParsePath("/a/b"), eOldMod)
		repoMod := protocol.NewMemoryStoreRepository(storeMod, nil)

		_, _, err = verification.VerifyDeferredModifiedReleaseTwoStep(verification.ConvergenceVerifyEnv{
			StoreID:     testStoreID,
			Resource:    resModID,
			Actor:       actorA,
			Declaration: mustDecl(t, "a {\n  b {\n    c = \"migrated-val\"\n  }\n}\n"),
			Scope:       fullScope(t),
			Provider:    provMod,
			StoreRepo:   repoMod,
			Locks:       locks,
		}, migrated)
		if err != nil {
			t.Fatalf("VerifyDeferredModifiedReleaseTwoStep(migrated=%t) failed: %v", migrated, err)
		}
	}
}

func boolName(b bool) string {
	if b {
		return "migrated"
	}
	return "unmigrated"
}
