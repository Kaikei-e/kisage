package protocol

import (
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/projection"
)

// SummaryExecution represents the top-level deterministic execution status across recovery and normalExecution (ADR 0004 §2 Step 7, ADR 0005 §2).
type SummaryExecution uint8

const (
	// SummaryNoOp indicates both recovery and normalExecution were NoOp with no committed changes and no candidates (Priority 8).
	SummaryNoOp SummaryExecution = iota
	// SummaryPlanned indicates read-only `plan` has execution/recovery candidates or Deferred work without blockers (Priority 6).
	SummaryPlanned
	// SummaryCompleted indicates at least one change was committed in recovery or normalExecution without blockers/failures (Priority 7).
	SummaryCompleted
	// SummaryPartiallyApplied indicates ApplyBatch returned Applied, but ownership Commit was confirmed incomplete (Priority 3).
	SummaryPartiallyApplied
	// SummaryBlocked indicates a mode Blocker, PolicyDenied, unresolved recovery, prior Quarantine, NotApplied, or pre-apply Stabilize Conflict (Priority 5).
	SummaryBlocked
	// SummaryFailed indicates a ProviderContractViolation (K501) quarantined the Resource (Priority 2) or a confirmed I/O failure on an unapplied path (Priority 4).
	SummaryFailed
	// SummaryIndeterminate indicates environment apply, Stabilize, or store Commit returned an Indeterminate outcome (Priority 1).
	SummaryIndeterminate
)

// String returns the canonical name of SummaryExecution.
func (s SummaryExecution) String() string {
	switch s {
	case SummaryNoOp:
		return "NoOp"
	case SummaryPlanned:
		return "Planned"
	case SummaryCompleted:
		return "Completed"
	case SummaryPartiallyApplied:
		return "PartiallyApplied"
	case SummaryBlocked:
		return "Blocked"
	case SummaryFailed:
		return "Failed"
	case SummaryIndeterminate:
		return "Indeterminate"
	default:
		return "InvalidSummaryExecution"
	}
}

// PriorityRank returns the ADR 0005 §2 priority rank (lower number = higher priority).
func (s SummaryExecution) PriorityRank() int {
	switch s {
	case SummaryIndeterminate:
		return 1
	case SummaryFailed:
		return 2
	case SummaryPartiallyApplied:
		return 3
	case SummaryBlocked:
		return 5
	case SummaryPlanned:
		return 6
	case SummaryCompleted:
		return 7
	case SummaryNoOp:
		return 8
	default:
		return 0
	}
}

// CombineSummaryExecution returns the higher-priority SummaryExecution of a and b according to ADR 0005 §2.
func CombineSummaryExecution(a, b SummaryExecution) SummaryExecution {
	if a.PriorityRank() <= b.PriorityRank() {
		return a
	}
	return b
}

// ExecutionCounts summarizes the item counts across recovery and normalExecution (ADR 0005 §2, Counterexamples #44, #45, #59, #60).
type ExecutionCounts struct {
	RecoveredPersisted          int
	RecoveryUnresolved          int
	EnvironmentApplied          int
	MetadataCommitted           int
	RemainingPending            int
	EnvironmentPossiblyModified bool
	Suppressed                  int
	Deferred                    int
	PruneRequired               int
	Conflicts                   int
	ObservationUnavailable      int
	PolicyDenied                int
}

// ReconcileSummary holds the aggregated execution status, convergence status, counts, and sorted diagnostics (ADR 0004 §2 Step 7, ADR 0005 §2).
type ReconcileSummary struct {
	Execution   SummaryExecution
	Convergence plan.ConvergenceStatus
	Counts      ExecutionCounts
	Diagnostics []model.Diagnostic
}

// EvaluateSummaryExecution purely evaluates the 8-level priority aggregation table from ADR 0005 §2
// across recovery and normalExecution (Counterexamples #4, #44, #45, #46, #50, #57, #59, #60):
//
//  1. Environment apply, Stabilize, or store Commit is Indeterminate -> SummaryIndeterminate
//  2. ProviderContractViolation (K501) detected and Resource quarantined in this call -> SummaryFailed
//  3. Environment Applied, but ownership Commit confirmed incomplete -> SummaryPartiallyApplied
//  4. Confirmed I/O failure on a path where environment was NOT applied -> SummaryFailed
//  5. Mode Blocker, PolicyDenied, unresolved recovery, prior Quarantine (K503), NotApplied, or unapplied Stabilize Conflict -> SummaryBlocked
//  6. Read-only `plan` has candidates or Deferred work -> SummaryPlanned
//  7. At least 1 change committed in recovery or normalExecution -> SummaryCompleted
//  8. Both phases are NoOp -> SummaryNoOp
func EvaluateSummaryExecution(
	isReadOnlyPlan bool,
	hasPlanWork bool,
	initiallyQuarantined bool,
	recovery RecoveryPhaseResult,
	normal NormalPhaseResult,
) SummaryExecution {
	// Priority 1: Indeterminate outcome in either phase.
	if recovery.Status == RecoveryIndeterminate || normal.Status == NormalIndeterminate ||
		recovery.StoreOutcomeUnknown || normal.StoreOutcomeUnknown {
		return SummaryIndeterminate
	}
	// Priority 2: ProviderContractViolation (K501) detected and Resource quarantined in this call (#45).
	if normal.ContractViolation || normal.QuarantinedNow {
		return SummaryFailed
	}
	// Priority 3: Environment Applied, but store record commit confirmed incomplete (#60).
	if normal.Status == NormalPartiallyApplied {
		return SummaryPartiallyApplied
	}
	// Priority 4: Confirmed I/O failure on a path where environment was not applied.
	if recovery.Status == RecoveryFailed || normal.Status == NormalFailed {
		return SummaryFailed
	}
	// Priority 5: Mode Blocker, PolicyDenied, unresolved recovery, prior Quarantine, NotApplied (#4/#46), or unapplied Stabilize Conflict (#44/#60).
	if initiallyQuarantined || recovery.Status == RecoveryBlocked || normal.Status == NormalBlocked {
		return SummaryBlocked
	}
	// Priority 6: Read-only plan has candidates or Deferred work.
	if isReadOnlyPlan && hasPlanWork {
		return SummaryPlanned
	}
	// Priority 7: At least 1 change committed in recovery or normalExecution (#59).
	if recovery.PersistedCount > 0 || normal.Status == NormalCompleted ||
		normal.AppliedEnvCount > 0 || normal.CommittedMetadataCount > 0 {
		return SummaryCompleted
	}
	// Priority 8: Both phases are NoOp.
	return SummaryNoOp
}

// ComputeExecutionCounts aggregates the phase-specific and diagnostic counts without omitting
// persisted recovery counts when normalExecution is Blocked, or remaining Pending / environment modification
// flags when a contract violation occurs (ADR 0004 §2 Step 6, ADR 0005 §2, Counterexamples #44, #45, #59, #60).
func ComputeExecutionCounts(
	recovery RecoveryPhaseResult,
	planRes *plan.Result,
	normal NormalPhaseResult,
	allDiags []model.Diagnostic,
) ExecutionCounts {
	counts := ExecutionCounts{
		RecoveredPersisted:          recovery.PersistedCount,
		RecoveryUnresolved:          recovery.UnresolvedCount,
		EnvironmentApplied:          normal.AppliedEnvCount,
		MetadataCommitted:           normal.CommittedMetadataCount,
		RemainingPending:            normal.RemainingPendingCount + recovery.UnresolvedCount,
		EnvironmentPossiblyModified: normal.EnvironmentPossiblyModified,
	}
	if planRes != nil {
		counts.Suppressed = len(planRes.Gate.Suppressed)
		counts.Deferred = len(planRes.Gate.DeferredActions)
	}
	for _, d := range allDiags {
		switch d.Code {
		case model.CodePruneRequired:
			counts.PruneRequired++
		case model.CodeObservationUnavailable:
			counts.ObservationUnavailable++
		case model.CodePolicyDenied:
			counts.PolicyDenied++
		case model.CodeOwnershipConflict, model.CodeStructureConflict, model.CodeExternalDrift,
			model.CodeMigrationRequired, model.CodePendingConflict, model.CodeUnownedValueConflict:
			counts.Conflicts++
		}
	}
	return counts
}

// EvaluateFinalConfirmation purely computes the Stage 7 in-lock convergence status and aggregated
// ReconcileSummary (ADR 0004 §2 Step 7, Counterexamples #42, #44, #45, #59, #60).
//
// In accordance with ADR 0004 §2 Step 7 and Counterexample #42, post-lock external modifications are
// never diagnosed as ProviderContractViolation (K501); convergence is evaluated strictly against the
// in-lock final observation and store state.
func EvaluateFinalConfirmation(
	decl *projection.Declaration,
	finalSnap *observation.Snapshot,
	finalStore *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	initiallyQuarantined bool,
	recovery RecoveryPhaseResult,
	planRes *plan.Result,
	normal NormalPhaseResult,
) ReconcileSummary {
	exec := EvaluateSummaryExecution(false, false, initiallyQuarantined, recovery, normal)

	quarantined := initiallyQuarantined || normal.QuarantinedNow
	var conv plan.ConvergenceStatus
	if recovery.StoreOutcomeUnknown || normal.StoreOutcomeUnknown || finalStore == nil {
		conv = plan.ConvergenceUnknown
	} else {
		conv = plan.EvaluateConvergence(decl, finalSnap, finalStore, resID, actor, quarantined)
	}

	var diags []model.Diagnostic
	diags = append(diags, recovery.Diagnostics...)
	if planRes != nil {
		diags = append(diags, planRes.Diagnostics...)
	}
	diags = append(diags, normal.Diagnostics...)
	model.SortDiagnostics(diags)
	diags = compactDiagnostics(diags)

	counts := ComputeExecutionCounts(recovery, planRes, normal, diags)
	return ReconcileSummary{
		Execution:   exec,
		Convergence: conv,
		Counts:      counts,
		Diagnostics: diags,
	}
}

func compactDiagnostics(sorted []model.Diagnostic) []model.Diagnostic {
	if len(sorted) <= 1 {
		return sorted
	}
	return slices.CompactFunc(sorted, func(a, b model.Diagnostic) bool {
		return model.CompareDiagnostics(a, b) == 0 && a.NextAction == b.NextAction
	})
}
