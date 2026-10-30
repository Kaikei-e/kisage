package verification

import (
	"fmt"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/protocol"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
)

// ConvergenceVerifyEnv bundles the dependencies for bounded finite-execution convergence verification (ADR 0005 §3.3).
type ConvergenceVerifyEnv struct {
	StoreID     ownership.StoreID
	Resource    ownership.ResourceID
	Actor       ownership.ActorID
	Declaration *projection.Declaration
	Scope       scope.Scope
	Provider    provider.Provider
	StoreRepo   protocol.StoreRepository
	Locks       provider.LockManager
	// CreationHistory contains independently established witnesses for initial
	// Created entries. Empty stores need no initial witnesses.
	CreationHistory map[string]ownership.ActorID
}

// VerifyApplyThenPruneConvergence executes `apply` followed by `prune` under the ADR 0005 §3.3
// preconditions and verifies that:
//  1. If retired Created leaves exist, `apply` completes all Apply-eligible work while retaining
//     retired Created leaves (`PruneRequired`, K604) and reporting `NotConverged`.
//  2. The subsequent `prune` deletes the retired Created leaves and reaches `Match_A(d)`
//     (`Summary.Convergence == Converged` and `ComputeTotalRank == 0`).
//  3. Each non-NoOp step strictly decreases `ComputeTotalRank`.
func VerifyApplyThenPruneConvergence(env ConvergenceVerifyEnv) (*protocol.ReconcileResult, *protocol.ReconcileResult, error) {
	initialStore, err := env.StoreRepo.LoadStore()
	if err != nil {
		return nil, nil, err
	}
	initialPaths := collectVerificationDomain(env.Declaration, initialStore, env.Resource, env.Actor)
	initialSnap, err := env.Provider.Observe(env.Resource, initialPaths...)
	if err != nil {
		return nil, nil, err
	}
	rank0 := ComputeTotalRank(env.Declaration, initialSnap, initialStore, env.Resource, env.Actor)

	applyRes, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     env.StoreID,
		Resource:    env.Resource,
		Actor:       env.Actor,
		Mode:        plan.ModeApply,
		BatchID:     "conv-apply",
		Declaration: env.Declaration,
		Scope:       env.Scope,
		Provider:    env.Provider,
		StoreRepo:   env.StoreRepo,
		Locks:       env.Locks,
	})
	if err != nil {
		return nil, nil, err
	}
	history, err := VerifyReconcileResultWithHistory(initialStore, applyRes, env.CreationHistory)
	if err != nil {
		return applyRes, nil, err
	}
	rank1 := ComputeTotalRank(env.Declaration, applyRes.FinalSnapshot, applyRes.FinalStore, env.Resource, env.Actor)
	if applyRes.Summary.Execution == protocol.SummaryCompleted && rank1 >= rank0 {
		return applyRes, nil, fmt.Errorf("ESR rank did not strictly decrease on Completed apply: rank0=%d rank1=%d", rank0, rank1)
	}

	pruneRes, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     env.StoreID,
		Resource:    env.Resource,
		Actor:       env.Actor,
		Mode:        plan.ModePrune,
		BatchID:     "conv-prune",
		Declaration: env.Declaration,
		Scope:       env.Scope,
		Provider:    env.Provider,
		StoreRepo:   env.StoreRepo,
		Locks:       env.Locks,
	})
	if err != nil {
		return applyRes, nil, err
	}
	if _, err := VerifyReconcileResultWithHistory(applyRes.FinalStore, pruneRes, history); err != nil {
		return applyRes, pruneRes, err
	}
	rank2 := ComputeTotalRank(env.Declaration, pruneRes.FinalSnapshot, pruneRes.FinalStore, env.Resource, env.Actor)
	if pruneRes.Summary.Execution == protocol.SummaryCompleted && rank2 >= rank1 {
		return applyRes, pruneRes, fmt.Errorf("ESR rank did not strictly decrease on Completed prune: rank1=%d rank2=%d", rank1, rank2)
	}
	if pruneRes.Summary.Convergence != plan.ConvergenceConverged || rank2 != 0 {
		return applyRes, pruneRes, fmt.Errorf("expected Match_A convergence and rank 0 after apply->prune, got conv=%s rank=%d", pruneRes.Summary.Convergence, rank2)
	}

	// Verify Theorem 6 (Fixed Point): re-running apply or prune after Match_A is a NoOp.
	if err := VerifyAll11PlanTheorems(plan.Input{
		Resource:    env.Resource,
		Actor:       env.Actor,
		Declaration: env.Declaration,
		Snapshot:    pruneRes.FinalSnapshot,
		Store:       pruneRes.FinalStore,
		Scope:       env.Scope,
	}); err != nil {
		return applyRes, pruneRes, err
	}
	return applyRes, pruneRes, nil
}

// VerifyDeferredModifiedReleaseTwoStep verifies the ADR 0005 §3.3 separate lemma for overlapping
// retired `Modified` leaf and new declared descendant (Counterexamples #20, #49, #55):
//   - Step 1 (`apply`): unmanages the retired `Modified` path while deferring the new path (`DeferredUntilRelease`, K603);
//     reports `NotConverged` (never inferring convergence from unmanage alone).
//   - Step 2 (`apply`): if the environment has been externally migrated to the new structure and value,
//     adopts the new path as `Modified` and reaches `Converged`; if the environment is still in the old
//     structure, halts with `StructureConflict` (K202) and `SummaryBlocked`.
func VerifyDeferredModifiedReleaseTwoStep(
	env ConvergenceVerifyEnv,
	externallyMigrated bool,
) (*protocol.ReconcileResult, *protocol.ReconcileResult, error) {
	step1, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     env.StoreID,
		Resource:    env.Resource,
		Actor:       env.Actor,
		Mode:        plan.ModeApply,
		BatchID:     "step1-unmanage-modified",
		Declaration: env.Declaration,
		Scope:       env.Scope,
		Provider:    env.Provider,
		StoreRepo:   env.StoreRepo,
		Locks:       env.Locks,
	})
	if err != nil {
		return nil, nil, err
	}
	if step1.Summary.Execution != protocol.SummaryCompleted || step1.Summary.Convergence != plan.ConvergenceNotConverged {
		return step1, nil, fmt.Errorf("step 1 expected SummaryCompleted + NotConverged, got exec=%s conv=%s", step1.Summary.Execution, step1.Summary.Convergence)
	}

	step2, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     env.StoreID,
		Resource:    env.Resource,
		Actor:       env.Actor,
		Mode:        plan.ModeApply,
		BatchID:     "step2-classify-new",
		Declaration: env.Declaration,
		Scope:       env.Scope,
		Provider:    env.Provider,
		StoreRepo:   env.StoreRepo,
		Locks:       env.Locks,
	})
	if err != nil {
		return step1, nil, err
	}
	if externallyMigrated {
		if step2.Summary.Execution != protocol.SummaryCompleted || step2.Summary.Convergence != plan.ConvergenceConverged {
			return step1, step2, fmt.Errorf("step 2 (migrated) expected SummaryCompleted + Converged, got exec=%s conv=%s", step2.Summary.Execution, step2.Summary.Convergence)
		}
	} else {
		if step2.Summary.Execution != protocol.SummaryBlocked || !hasDiagCode(step2.Summary.Diagnostics, model.CodeStructureConflict) {
			return step1, step2, fmt.Errorf("step 2 (unmigrated) expected SummaryBlocked with K202 StructureConflict, got exec=%s diags=%+v", step2.Summary.Execution, step2.Summary.Diagnostics)
		}
	}
	return step1, step2, nil
}

func hasDiagCode(diags []model.Diagnostic, code model.DiagnosticCode) bool {
	for _, d := range diags {
		if d.Code == code {
			return true
		}
	}
	return false
}
