package protocol

import (
	"errors"
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
)

// StoreCommitStage identifies the persistence boundary for store commits and fault injection (ADR 0004 §2–§3, Counterexample #35).
type StoreCommitStage uint8

const (
	// StageRecoveryCommit persists resolved crash-recovery candidates after Stabilize.
	StageRecoveryCommit StoreCommitStage = iota + 1
	// StageIntentSave persists DurableIntent and Pending reservations before ApplyBatch.
	StageIntentSave
	// StageRollbackNotApplied rolls back Pending reservations to PreviousEntry after NotApplied.
	StageRollbackNotApplied
	// StageFinalBatchCommit atomically commits confirmed EnvironmentOps and MetadataCandidates.
	StageFinalBatchCommit
	// StageExplicitResolutionCommit atomically commits an explicit `adopt` or `release` update.
	StageExplicitResolutionCommit
	// StageQuarantineSave persists a Provider contract violation before returning.
	StageQuarantineSave
)

// StoreRepository abstracts atomic persistence of the shared ownership store σ and Resource quarantine state (ADR 0004 §2).
type StoreRepository interface {
	LoadStore() (*ownership.Store, error)
	SaveStore(stage StoreCommitStage, next *ownership.Store) CommitResult
	LoadQuarantine() (*QuarantineTable, error)
	SaveQuarantine(next *QuarantineTable) CommitResult
	// HaltRuntime permanently disables this repository instance after quarantine
	// persistence failed. Hosts must stop the process and require administrator
	// intervention before restarting it (ADR 0004 §2 Step 6).
	HaltRuntime(cause error)
}

var ErrRuntimeHalted = errors.New("runtime halted: administrator recovery required")

// ReconcileRequest holds the inputs to the 7-stage batch persistence procedure (ADR 0004 §2).
type ReconcileRequest struct {
	StoreID     ownership.StoreID
	Resource    ownership.ResourceID
	Actor       ownership.ActorID
	Mode        plan.Mode
	BatchID     string
	Declaration *projection.Declaration
	Scope       scope.Scope
	Provider    provider.Provider
	StoreRepo   StoreRepository
	Locks       provider.LockManager
}

// ReconcileResult holds the complete phase-separated output of the 7-stage batch persistence procedure (ADR 0004 §2, ADR 0005 §2).
type ReconcileResult struct {
	Mode            plan.Mode
	Resource        ownership.ResourceID
	Actor           ownership.ActorID
	BatchID         string
	Recovery        RecoveryPhaseResult
	Plan            *plan.Result
	NormalExecution NormalPhaseResult
	FinalSnapshot   *observation.Snapshot
	FinalStore      *ownership.Store
	FinalPlan       *plan.Result
	Summary         ReconcileSummary
}

// ExecuteReconcile executes the 7-stage batch persistence procedure under the canonical
// SharedStore (σ) -> Resource lock order (ADR 0004 §2, Counterexamples #4, #6, #7, #18, #24–#28, #35, #40, #42–#46, #57–#60).
func ExecuteReconcile(req ReconcileRequest) (*ReconcileResult, error) {
	if err := validateReconcileRequest(req); err != nil {
		return nil, err
	}
	lease, err := req.Locks.Acquire(req.StoreID, req.Resource)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lease.Unlock() }()

	initialStore, quarantineTbl, err := loadStateInLock(req.StoreRepo)
	if err != nil {
		return nil, err
	}
	initiallyQuarantined := quarantineTbl.IsQuarantined(req.Resource)

	// Stage 1: Observe, Recover (with Stabilize), and Plan (ADR 0004 §2 Step 1, §3).
	queryPaths := collectQueryPaths(req.Declaration, initialStore, req.Resource, req.Actor)
	initialSnap, err := req.Provider.Observe(req.Resource, queryPaths...)
	if err != nil {
		return nil, err
	}

	recoveryRes, err := executeRecoveryInLock(req, initialStore, initialSnap, initiallyQuarantined)
	if err != nil {
		return nil, err
	}
	if !recoveryRes.CanProceedToPlanning {
		return finalizeEarlyAfterRecovery(req, initialSnap, initiallyQuarantined, recoveryRes)
	}

	planRes, err := plan.Plan(plan.Input{
		Resource:    req.Resource,
		Actor:       req.Actor,
		Declaration: req.Declaration,
		Snapshot:    initialSnap,
		Store:       recoveryRes.Store,
		Scope:       req.Scope,
		Quarantined: initiallyQuarantined,
	}, req.Mode)
	if err != nil {
		return nil, err
	}

	// Stage 2: All-or-Nothing Mode Gate (ADR 0004 §2 Step 2, Counterexamples #31, #44, #48, #59, #60).
	normalRes, err := executeNormalStagesInLock(req, initialSnap, recoveryRes.Store, quarantineTbl, planRes)
	if err != nil {
		return nil, err
	}

	// Stage 7: Final Confirmation in the same lock (ADR 0004 §2 Step 7, Counterexample #42).
	var finalSnap *observation.Snapshot
	var finalObsErr error
	if normalRes.HaltRuntime {
		req.StoreRepo.HaltRuntime(ErrRuntimeHalted)
		finalSnap = observation.NewSnapshot()
	} else {
		finalSnap, finalObsErr = selectFinalSnapshot(req, normalRes)
	}
	var finalPlan *plan.Result
	if normalRes.Store != nil && !normalRes.StoreOutcomeUnknown {
		finalPlan, _ = plan.Plan(plan.Input{
			Resource:    req.Resource,
			Actor:       req.Actor,
			Declaration: req.Declaration,
			Snapshot:    finalSnap,
			Store:       normalRes.Store,
			Scope:       req.Scope,
			Quarantined: initiallyQuarantined || normalRes.QuarantinedNow,
		}, req.Mode)
	}
	summary := EvaluateFinalConfirmation(
		req.Declaration,
		finalSnap,
		normalRes.Store,
		req.Resource,
		req.Actor,
		initiallyQuarantined,
		recoveryRes,
		planRes,
		normalRes,
	)
	recordFinalObservationFailure(&summary, finalObsErr)

	return &ReconcileResult{
		Mode:            req.Mode,
		Resource:        req.Resource,
		Actor:           req.Actor,
		BatchID:         req.BatchID,
		Recovery:        recoveryRes,
		Plan:            planRes,
		NormalExecution: normalRes,
		FinalSnapshot:   finalSnap,
		FinalStore:      normalRes.Store,
		FinalPlan:       finalPlan,
		Summary:         summary,
	}, nil
}

func executeRecoveryInLock(
	req ReconcileRequest,
	initialStore *ownership.Store,
	initialSnap *observation.Snapshot,
	quarantined bool,
) (RecoveryPhaseResult, error) {
	if quarantined {
		return RecoveryPhaseResult{
			Status:               RecoveryBlocked,
			Report:               &plan.RecoveryReport{VirtualStore: initialStore.Clone()},
			Store:                initialStore.Clone(),
			CanProceedToPlanning: true,
		}, nil
	}
	report, err := plan.PureRecover(initialStore, req.Resource, req.Actor, initialSnap, req.Scope)
	if err != nil {
		return RecoveryPhaseResult{}, err
	}
	if !report.HasCandidates() {
		return EvaluateRecoveryPhase(initialStore, report, provider.StabilizeResult{}, CommitResult{})
	}
	candPaths := make([]model.Path, len(report.Candidates))
	for i, c := range report.Candidates {
		candPaths[i] = c.Path
	}
	stab := req.Provider.Stabilize(req.Resource, initialSnap, candPaths...)
	var commit CommitResult
	if stab.Outcome == provider.StabilizeDurable {
		commit = req.StoreRepo.SaveStore(StageRecoveryCommit, report.VirtualStore)
	}
	out, err := EvaluateRecoveryPhase(initialStore, report, stab, commit)
	out.ObservedSnapshot = initialSnap
	out.DurableSnapshot = stab.DurableSnapshot
	return out, err
}

func finalizeEarlyAfterRecovery(
	req ReconcileRequest,
	snap *observation.Snapshot,
	initiallyQuarantined bool,
	recoveryRes RecoveryPhaseResult,
) (*ReconcileResult, error) {
	var planRes *plan.Result
	if recoveryRes.Store != nil {
		planRes, _ = plan.Plan(plan.Input{
			Resource:    req.Resource,
			Actor:       req.Actor,
			Declaration: req.Declaration,
			Snapshot:    snap,
			Store:       recoveryRes.Store,
			Scope:       req.Scope,
			Quarantined: initiallyQuarantined,
		}, req.Mode)
	}
	normalRes := NormalPhaseResult{
		Status: NormalNoOp,
		Store:  recoveryRes.Store,
	}
	if planRes != nil && planRes.Gate.Blocked {
		normalRes.Status = NormalBlocked
		normalRes.Diagnostics = slices.Clone(planRes.Gate.Diagnostics)
	}
	finalSnap, finalObsErr := selectFinalSnapshot(req, normalRes)
	var finalPlan *plan.Result
	if recoveryRes.Store != nil && !recoveryRes.StoreOutcomeUnknown {
		finalPlan, _ = plan.Plan(plan.Input{Resource: req.Resource, Actor: req.Actor, Declaration: req.Declaration, Snapshot: finalSnap, Store: recoveryRes.Store, Scope: req.Scope, Quarantined: initiallyQuarantined}, req.Mode)
	}
	summary := EvaluateFinalConfirmation(
		req.Declaration,
		finalSnap,
		recoveryRes.Store,
		req.Resource,
		req.Actor,
		initiallyQuarantined,
		recoveryRes,
		planRes,
		normalRes,
	)
	recordFinalObservationFailure(&summary, finalObsErr)
	return &ReconcileResult{
		Mode:            req.Mode,
		Resource:        req.Resource,
		Actor:           req.Actor,
		BatchID:         req.BatchID,
		Recovery:        recoveryRes,
		Plan:            planRes,
		NormalExecution: normalRes,
		FinalSnapshot:   finalSnap,
		FinalStore:      recoveryRes.Store,
		FinalPlan:       finalPlan,
		Summary:         summary,
	}, nil
}

func recordFinalObservationFailure(summary *ReconcileSummary, err error) {
	if err == nil {
		return
	}
	summary.Convergence = plan.ConvergenceUnknown
	summary.Diagnostics = append(summary.Diagnostics, model.Diagnostic{Code: model.CodeObservationUnavailable, Reason: "final in-lock observation failed", Cause: err})
	model.SortDiagnostics(summary.Diagnostics)
	summary.Counts.ObservationUnavailable++
}
