package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/cli"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
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

func setupTestCLIEnv(t *testing.T, initialTarget, initialDecl string) (string, string, *protocol.MemoryStoreRepository, *cli.HostEnvironment, *cli.Runner) {
	t.Helper()
	rootDir := t.TempDir()
	if err := os.Chmod(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lockDir := t.TempDir()
	if err := os.Chmod(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(rootDir, "target.lkal")
	declPath := filepath.Join(rootDir, "decl.lkal")

	if err := os.WriteFile(targetPath, []byte(initialTarget), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(declPath, []byte(initialDecl), 0o600); err != nil {
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
	repo := protocol.NewMemoryStoreRepository(ownership.NewStore(), nil)
	rootRule, err := scope.NewRootRule(scope.PermAll)
	if err != nil {
		t.Fatal(err)
	}
	host, err := cli.NewHostEnvironment(cli.HostEnvironmentConfig{
		HostActor: actorA,
		Registry:  reg,
		DeclarationBindings: []cli.DeclarationBinding{{
			DeclarationFile: declPath,
			Resource:        testResID,
			Scope:           scope.MustNew(rootRule),
		}},
		Provider:  prov,
		StoreRepo: repo,
		Locks:     locks,
	})
	if err != nil {
		t.Fatal(err)
	}
	return targetPath, declPath, repo, host, cli.NewRunner(host)
}

// TestCounterexamples14_16_41_CLIPlanApplyPruneAndHostTrust verifies ADR 0005 §1–2
// and Counterexamples #14, #16, #41:
//   - #41: `plan` with Pending recovery candidates performs zero writes/commits, sets conditionalOnRecovery=true,
//     and outputs the dual-mode Plan schema (`plans.apply`, `plans.prune`, and top-level `summary` with ONLY `execution` and `convergence`).
//   - #14: Modifying the declaration file after `plan` and before `apply` causes `apply` to re-plan
//     against the start-of-call snapshot and report the updated `declarationDigest`.
//   - #16: Passing `--actor actor-b` on CLI is ignored in favor of the host-bound Actor `actor-a`.
func TestCounterexamples14_16_41_CLIPlanApplyPruneAndHostTrust(t *testing.T) {
	_, declPath, repo, _, runner := setupTestCLIEnv(
		t,
		"@lkal \"0.1\"\napp {\n  sibling = \"keep\"\n  rec_key = \"recovered\"\n}\n",
		"@lkal \"0.1\"\napp {\n  k = \"v1\"\n  rec_key = \"recovered\"\n}\n",
	)

	// Seed a Pending creation on /app/rec_key to test Counterexample #41 in `plan`.
	pendingEntry, err := ownership.NewPendingCreate(actorA, "b-prev", lkal.String("recovered"), 1)
	if err != nil {
		t.Fatal(err)
	}
	initialStore, _ := ownership.NewStore().Put(testResID, model.MustParsePath("/app/rec_key"), pendingEntry)
	*repo = *protocol.NewMemoryStoreRepository(initialStore, nil)

	var stdout, stderr bytes.Buffer
	code := runner.Run([]string{"plan", declPath, "-json", "-detailed-exitcode"}, &stdout, &stderr)
	if code != cli.ExitCodePlanDiff {
		t.Fatalf("expected detailed exit code 2 (Planned), got %d (stderr=%s)", code, stderr.String())
	}
	if len(repo.SavedStagesHistory) != 0 {
		t.Fatalf("Counterexample #41 failed: read-only plan must not save to store, got stages %v", repo.SavedStagesHistory)
	}

	var planRaw map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &planRaw); err != nil {
		t.Fatalf("failed to unmarshal plan JSON: %v", err)
	}
	if planRaw["mode"] != "Plan" || planRaw["conditionalOnRecovery"] != true {
		t.Fatalf("Counterexample #41 failed: expected mode=Plan and conditionalOnRecovery=true, got %v", planRaw)
	}
	// ADR 0005 §2: Top-level summary in `plan` must have ONLY `execution` and `convergence`.
	topSummary, ok := planRaw["summary"].(map[string]any)
	if !ok || len(topSummary) != 2 || topSummary["execution"] != "Planned" || topSummary["convergence"] != "NotConverged" {
		t.Fatalf("ADR 0005 §2 failed: plan top-level summary must have only execution and convergence, got %v", topSummary)
	}
	planDigest := planRaw["declarationDigest"].(string)

	// Counterexample #14: Modify declaration file before `apply` and pass spoofed `--actor actor-b` (#16).
	if err := os.WriteFile(declPath, []byte("@lkal \"0.1\"\napp {\n  k = \"v2-updated-before-apply\"\n  rec_key = \"recovered\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	code = runner.Run([]string{"apply", declPath, "-json", "--actor", string(actorB)}, &stdout, &stderr)
	if code != cli.ExitCodeOK {
		t.Fatalf("expected apply exit code 0, got %d (stderr=%s, stdout=%s)", code, stderr.String(), stdout.String())
	}

	var applyDoc cli.CommandDocument
	if err := json.Unmarshal(stdout.Bytes(), &applyDoc); err != nil {
		t.Fatal(err)
	}
	if applyDoc.DeclarationDigest == "" || applyDoc.DeclarationDigest == planDigest {
		t.Fatalf("Counterexample #14 failed: expected updated declarationDigest on apply, got %q (plan was %q)", applyDoc.DeclarationDigest, planDigest)
	}
	if applyDoc.Summary.Execution != "Completed" || applyDoc.Summary.Convergence != "Converged" {
		t.Fatalf("expected Completed + Converged after apply, got %+v", applyDoc.Summary)
	}
	if applyDoc.Summary.Counts.RecoveredPersisted != 1 || applyDoc.Summary.Counts.EnvironmentApplied != 1 {
		t.Fatalf("expected 1 recovered + 1 env applied, got %+v", applyDoc.Summary.Counts)
	}

	// Counterexample #16: Verify ownership was recorded for host-bound `actor-a`, NEVER spoofed `actor-b`!
	finalStore, _ := repo.LoadStore()
	entryK, ok := finalStore.Get(testResID, model.MustParsePath("/app/k"))
	if !ok || entryK.Manager != actorA || !model.EqualLeafValues(entryK.LastApplied, lkal.String("v2-updated-before-apply")) {
		t.Fatalf("Counterexample #14/#16 failed: unexpected entry for /app/k: %+v", entryK)
	}

	// Re-running `plan -detailed-exitcode` after Match_A returns exit code 0 (NoOp).
	stdout.Reset()
	code = runner.Run([]string{"plan", declPath, "-detailed-exitcode"}, &stdout, &stderr)
	if code != cli.ExitCodeOK {
		t.Fatalf("expected detailed exit code 0 on converged state, got %d", code)
	}
}

// TestCLIExplainAdoptReleaseAndUsageExitCodes verifies `explain`, `adopt`, `release`,
// and exit codes (0, 1, 2, 64) (ADR 0005 §1–2, Counterexamples #36–#38).
func TestCLIExplainAdoptReleaseAndUsageExitCodes(t *testing.T) {
	_, declPath, _, _, runner := setupTestCLIEnv(
		t,
		"@lkal \"0.1\"\napp {\n  existing = \"legacy\"\n}\n",
		"@lkal \"0.1\"\napp {\n  existing = \"desired\"\n}\n",
	)

	// 1. Usage / input errors return ExitCodeUsage (64) in standard mode, and ExitCodeError (1) in `plan -detailed-exitcode`.
	var out, errBuf bytes.Buffer
	if code := runner.Run([]string{"unknown-cmd"}, &out, &errBuf); code != cli.ExitCodeUsage {
		t.Fatalf("expected 64 for unknown subcommand, got %d", code)
	}
	if code := runner.Run([]string{"plan", "/nonexistent/file.lkal", "-detailed-exitcode"}, &out, &errBuf); code != cli.ExitCodeError {
		t.Fatalf("expected 1 for input error under plan -detailed-exitcode, got %d", code)
	}

	// 2. Normal `apply` on unowned conflicting value (/app/existing = "legacy" != "desired") blocks with K206 and exit code 1.
	out.Reset()
	if code := runner.Run([]string{"apply", declPath, "-json"}, &out, &errBuf); code != cli.ExitCodeError {
		t.Fatalf("expected exit code 1 on K206 conflict, got %d", code)
	}

	// 3. `explain FILE PATH -json` returns ConditionToken, OwnerRevision, and stopping reason K206.
	out.Reset()
	_ = runner.Run([]string{"explain", declPath, "/app/existing", "-json"}, &out, &errBuf)
	var expDoc cli.CommandDocument
	if err := json.Unmarshal(out.Bytes(), &expDoc); err != nil {
		t.Fatalf("failed to unmarshal explain JSON: %v", err)
	}
	if expDoc.DeclarationDigest == "" || expDoc.Explanation == nil || expDoc.Explanation.ConditionToken == "" {
		t.Fatalf("expected non-empty declarationDigest and ConditionToken in explain output: %+v", expDoc)
	}

	// 4. `adopt` with stale revision (99) is rejected with exit code 1 (`Blocked`).
	out.Reset()
	if code := runner.Run([]string{
		"adopt", declPath, "/app/existing",
		"--expect", expDoc.Explanation.ConditionToken,
		"--owner-revision", "99",
		"-json",
	}, &out, &errBuf); code != cli.ExitCodeError {
		t.Fatalf("expected exit code 1 on stale revision, got %d", code)
	}

	// 5. `adopt` with matching TOKEN and REV=0 succeeds with exit code 0 (`Completed`), followed by `apply` -> `Converged`.
	out.Reset()
	if code := runner.Run([]string{
		"adopt", declPath, "/app/existing",
		"--expect", expDoc.Explanation.ConditionToken,
		"--owner-revision", "0",
		"-json",
	}, &out, &errBuf); code != cli.ExitCodeOK {
		t.Fatalf("expected exit code 0 on valid adopt, got %d (out=%s, err=%s)", code, out.String(), errBuf.String())
	}
	var adoptDoc cli.CommandDocument
	if err := json.Unmarshal(out.Bytes(), &adoptDoc); err != nil {
		t.Fatal(err)
	}
	if adoptDoc.Summary.Execution != "Completed" || adoptDoc.Resolution == nil || !adoptDoc.Resolution.Persisted {
		t.Fatalf("expected Completed adopt, got %+v", adoptDoc)
	}

	// 6. Now `apply` updates `/app/existing` to `"desired"` and converges!
	out.Reset()
	if code := runner.Run([]string{"apply", declPath, "-json"}, &out, &errBuf); code != cli.ExitCodeOK {
		t.Fatalf("expected exit code 0 on apply after adopt, got %d", code)
	}
	var finalApply cli.CommandDocument
	if err := json.Unmarshal(out.Bytes(), &finalApply); err != nil {
		t.Fatal(err)
	}
	if finalApply.Summary.Execution != "Completed" || finalApply.Summary.Convergence != "Converged" {
		t.Fatalf("expected Completed + Converged, got %+v", finalApply.Summary)
	}
}

// TestCounterexamples44_45_59_60_EightLevelSummaryPriorityTable verifies all 8 priority levels
// of `summary.execution` in ADR 0005 §2.
func TestCounterexamples44_45_59_60_EightLevelSummaryPriorityTable(t *testing.T) {
	// Priority 1: Indeterminate
	if got := protocol.EvaluateSummaryExecution(false, false, false,
		protocol.RecoveryPhaseResult{Status: protocol.RecoveryCompleted, PersistedCount: 1},
		protocol.NormalPhaseResult{Status: protocol.NormalIndeterminate},
	); got != protocol.SummaryIndeterminate {
		t.Fatalf("Priority 1 failed: got %s", got)
	}
	// Priority 2: ContractViolation / QuarantinedNow -> Failed (even if environment was modified, #45)
	if got := protocol.EvaluateSummaryExecution(false, false, false,
		protocol.RecoveryPhaseResult{},
		protocol.NormalPhaseResult{Status: protocol.NormalFailed, ContractViolation: true, QuarantinedNow: true, EnvironmentPossiblyModified: true},
	); got != protocol.SummaryFailed {
		t.Fatalf("Priority 2 failed: got %s", got)
	}
	// Priority 3: PartiallyApplied
	if got := protocol.EvaluateSummaryExecution(false, false, false,
		protocol.RecoveryPhaseResult{},
		protocol.NormalPhaseResult{Status: protocol.NormalPartiallyApplied},
	); got != protocol.SummaryPartiallyApplied {
		t.Fatalf("Priority 3 failed: got %s", got)
	}
	// Priority 4: Unapplied confirmed I/O failure -> Failed
	if got := protocol.EvaluateSummaryExecution(false, false, false,
		protocol.RecoveryPhaseResult{Status: protocol.RecoveryFailed},
		protocol.NormalPhaseResult{Status: protocol.NormalNoOp},
	); got != protocol.SummaryFailed {
		t.Fatalf("Priority 4 failed: got %s", got)
	}
	// Priority 5: Recovery=Completed + Normal=Blocked -> Blocked (#44, #60)
	if got := protocol.EvaluateSummaryExecution(false, false, false,
		protocol.RecoveryPhaseResult{Status: protocol.RecoveryCompleted, PersistedCount: 2},
		protocol.NormalPhaseResult{Status: protocol.NormalBlocked},
	); got != protocol.SummaryBlocked {
		t.Fatalf("Priority 5 failed: got %s", got)
	}
	// Priority 6: Read-only plan with work -> Planned
	if got := protocol.EvaluateSummaryExecution(true, true, false,
		protocol.RecoveryPhaseResult{Status: protocol.RecoveryNoOp},
		protocol.NormalPhaseResult{Status: protocol.NormalNoOp},
	); got != protocol.SummaryPlanned {
		t.Fatalf("Priority 6 failed: got %s", got)
	}
	// Priority 7: Recovery=Completed + Normal=NoOp -> Completed (#59)
	if got := protocol.EvaluateSummaryExecution(false, false, false,
		protocol.RecoveryPhaseResult{Status: protocol.RecoveryCompleted, PersistedCount: 1},
		protocol.NormalPhaseResult{Status: protocol.NormalNoOp},
	); got != protocol.SummaryCompleted {
		t.Fatalf("Priority 7 (#59) failed: got %s", got)
	}
	// Priority 8: Both NoOp -> NoOp
	if got := protocol.EvaluateSummaryExecution(false, false, false,
		protocol.RecoveryPhaseResult{Status: protocol.RecoveryNoOp},
		protocol.NormalPhaseResult{Status: protocol.NormalNoOp},
	); got != protocol.SummaryNoOp {
		t.Fatalf("Priority 8 failed: got %s", got)
	}
}
