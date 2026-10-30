package protocol

import (
	"fmt"
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/provider"
)

// CommitStatus represents the 3-valued outcome of a store persistence operation (ADR 0004 §2 Step 6, ADR 0005 §2).
type CommitStatus uint8

const (
	// CommitInvalid is the zero value for an uninitialized CommitStatus.
	CommitInvalid CommitStatus = iota
	// CommitSucceeded indicates the store version was atomically persisted and durably synced.
	CommitSucceeded
	// CommitFailed indicates a definite store write failure where the prior store version remains active.
	CommitFailed
	// CommitIndeterminate indicates the store commit outcome is unknown until a reload inspects the store version.
	CommitIndeterminate
)

// String returns the canonical name of CommitStatus.
func (s CommitStatus) String() string {
	switch s {
	case CommitSucceeded:
		return "Succeeded"
	case CommitFailed:
		return "Failed"
	case CommitIndeterminate:
		return "Indeterminate"
	default:
		return "InvalidCommitStatus"
	}
}

// CommitResult holds the outcome of a store save or commit operation.
type CommitResult struct {
	Status      CommitStatus
	Diagnostics []model.Diagnostic
}

// RecoveryPhaseStatus represents the execution status of the crash-recovery phase (ADR 0004 §3, ADR 0005 §2).
type RecoveryPhaseStatus uint8

const (
	// RecoveryNoOp indicates no self-Pending entries existed for the active Actor.
	RecoveryNoOp RecoveryPhaseStatus = iota
	// RecoveryCompleted indicates all self-Pending entries were resolved, authorized, Stabilized, and committed.
	RecoveryCompleted
	// RecoveryBlocked indicates at least one self-Pending entry remained unresolved (Unknown, PendingConflict, PolicyDenied, or Stabilize Conflict).
	RecoveryBlocked
	// RecoveryFailed indicates a definite store write failure while persisting recovery candidates.
	RecoveryFailed
	// RecoveryIndeterminate indicates that Stabilize or recovery store Commit returned an Indeterminate outcome.
	RecoveryIndeterminate
)

// String returns the canonical name of RecoveryPhaseStatus.
func (s RecoveryPhaseStatus) String() string {
	switch s {
	case RecoveryNoOp:
		return "NoOp"
	case RecoveryCompleted:
		return "Completed"
	case RecoveryBlocked:
		return "Blocked"
	case RecoveryFailed:
		return "Failed"
	case RecoveryIndeterminate:
		return "Indeterminate"
	default:
		return "InvalidRecoveryPhaseStatus"
	}
}

// RecoveryPhaseResult holds the pure evaluation outcome of the crash-recovery phase (ADR 0004 §3, Counterexamples #6, #7, #24, #25, #43, #44, #59, #60).
type RecoveryPhaseResult struct {
	Status               RecoveryPhaseStatus
	Report               *plan.RecoveryReport
	StabilizeOutcome     provider.StabilizeOutcome
	CommitStatus         CommitStatus
	Store                *ownership.Store
	StoreOutcomeUnknown  bool
	PersistedCount       int
	UnresolvedCount      int
	CanProceedToPlanning bool
	Diagnostics          []model.Diagnostic
	// Snapshots retain the observation/durability evidence used before recovery
	// persistence; final observations may already include later normal changes.
	ObservedSnapshot *observation.Snapshot
	DurableSnapshot  *observation.Snapshot
}

// EvaluateRecoveryPhase purely evaluates the crash-recovery phase given a PureRecover report,
// the Provider.Stabilize result, and the store CommitResult (ADR 0004 §3, Counterexamples #6, #7, #24, #25, #43, #44, #59, #60):
//   - If report has neither Candidates nor Unresolved items, returns RecoveryNoOp and CanProceedToPlanning = true.
//   - If report has Candidates, Provider.Stabilize MUST return StabilizeDurable before any candidate
//     (CommitSet, Rollback to Expected, or ClearDelete on Absent) can be persisted (Counterexample #43).
//   - If Stabilize returns StabilizeIndeterminate or StabilizeConflict, Pending is retained in initialStore
//     and CanProceedToPlanning is false.
//   - If Stabilize succeeds and CommitSucceeded, candidates are marked Persisted and Store is updated to VirtualStore;
//     if Unresolved items also exist, PersistedCount is preserved while Status is RecoveryBlocked and CanProceedToPlanning is false.
func EvaluateRecoveryPhase(
	initialStore *ownership.Store,
	report *plan.RecoveryReport,
	stabilize provider.StabilizeResult,
	commit CommitResult,
) (RecoveryPhaseResult, error) {
	if initialStore == nil || report == nil {
		return RecoveryPhaseResult{}, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "EvaluateRecoveryPhase requires non-nil initialStore and RecoveryReport",
		}
	}

	if !report.HasCandidates() && !report.HasUnresolved() {
		return RecoveryPhaseResult{
			Status:               RecoveryNoOp,
			Report:               cloneRecoveryReport(report, false),
			Store:                initialStore.Clone(),
			CanProceedToPlanning: true,
		}, nil
	}

	if !report.HasCandidates() {
		diags := slices.Clone(report.Diagnostics)
		model.SortDiagnostics(diags)
		return RecoveryPhaseResult{
			Status:               RecoveryBlocked,
			Report:               cloneRecoveryReport(report, false),
			Store:                initialStore.Clone(),
			UnresolvedCount:      len(report.Unresolved),
			CanProceedToPlanning: false,
			Diagnostics:          diags,
		}, nil
	}

	return evaluateCandidateRecovery(initialStore, report, stabilize, commit), nil
}

func evaluateCandidateRecovery(
	initialStore *ownership.Store,
	report *plan.RecoveryReport,
	stabilize provider.StabilizeResult,
	commit CommitResult,
) RecoveryPhaseResult {
	diags := slices.Clone(report.Diagnostics)

	switch stabilize.Outcome {
	case provider.StabilizeIndeterminate:
		diags = append(diags, stabilize.Diagnostics...)
		model.SortDiagnostics(diags)
		return RecoveryPhaseResult{
			Status:               RecoveryIndeterminate,
			Report:               cloneRecoveryReport(report, false),
			StabilizeOutcome:     provider.StabilizeIndeterminate,
			Store:                initialStore.Clone(),
			UnresolvedCount:      len(report.Items),
			CanProceedToPlanning: false,
			Diagnostics:          diags,
		}
	case provider.StabilizeConflict:
		diags = append(diags, stabilize.Diagnostics...)
		model.SortDiagnostics(diags)
		return RecoveryPhaseResult{
			Status:               RecoveryBlocked,
			Report:               cloneRecoveryReport(report, false),
			StabilizeOutcome:     provider.StabilizeConflict,
			Store:                initialStore.Clone(),
			UnresolvedCount:      len(report.Items),
			CanProceedToPlanning: false,
			Diagnostics:          diags,
		}
	case provider.StabilizeDurable:
		return evaluateStabilizedRecoveryCommit(initialStore, report, commit, diags)
	default:
		diags = append(diags, model.Diagnostic{
			Code:   model.CodeIndeterminateOutcome,
			Reason: fmt.Sprintf("invalid Stabilize outcome %v during crash recovery", stabilize.Outcome),
		})
		model.SortDiagnostics(diags)
		return RecoveryPhaseResult{
			Status:               RecoveryIndeterminate,
			Report:               cloneRecoveryReport(report, false),
			StabilizeOutcome:     stabilize.Outcome,
			Store:                initialStore.Clone(),
			UnresolvedCount:      len(report.Items),
			CanProceedToPlanning: false,
			Diagnostics:          diags,
		}
	}
}

func evaluateStabilizedRecoveryCommit(
	initialStore *ownership.Store,
	report *plan.RecoveryReport,
	commit CommitResult,
	diags []model.Diagnostic,
) RecoveryPhaseResult {
	diags = append(diags, commit.Diagnostics...)
	model.SortDiagnostics(diags)

	switch commit.Status {
	case CommitSucceeded:
		persistedReport := cloneRecoveryReport(report, true)
		status := RecoveryCompleted
		canProceed := true
		if report.HasUnresolved() {
			status = RecoveryBlocked
			canProceed = false
		}
		return RecoveryPhaseResult{
			Status:               status,
			Report:               persistedReport,
			StabilizeOutcome:     provider.StabilizeDurable,
			CommitStatus:         CommitSucceeded,
			Store:                report.VirtualStore.Clone(),
			PersistedCount:       len(report.Candidates),
			UnresolvedCount:      len(report.Unresolved),
			CanProceedToPlanning: canProceed,
			Diagnostics:          diags,
		}
	case CommitFailed:
		return RecoveryPhaseResult{
			Status:               RecoveryFailed,
			Report:               cloneRecoveryReport(report, false),
			StabilizeOutcome:     provider.StabilizeDurable,
			CommitStatus:         CommitFailed,
			Store:                initialStore.Clone(),
			UnresolvedCount:      len(report.Items),
			CanProceedToPlanning: false,
			Diagnostics:          diags,
		}
	default:
		if len(commit.Diagnostics) == 0 {
			diags = append(diags, model.Diagnostic{
				Code:       model.CodeIndeterminateOutcome,
				Reason:     "recovery store commit outcome is indeterminate; reload store before assuming Pending state",
				NextAction: "reload ownership store to determine whether recovery commit succeeded",
			})
			model.SortDiagnostics(diags)
		}
		return RecoveryPhaseResult{
			Status:               RecoveryIndeterminate,
			Report:               cloneRecoveryReport(report, false),
			StabilizeOutcome:     provider.StabilizeDurable,
			CommitStatus:         CommitIndeterminate,
			Store:                nil,
			StoreOutcomeUnknown:  true,
			UnresolvedCount:      len(report.Items),
			CanProceedToPlanning: false,
			Diagnostics:          diags,
		}
	}
}

func cloneRecoveryReport(r *plan.RecoveryReport, persisted bool) *plan.RecoveryReport {
	if r == nil {
		return nil
	}
	out := &plan.RecoveryReport{
		Items:             make([]plan.RecoveryItem, len(r.Items)),
		Candidates:        make([]plan.RecoveryItem, len(r.Candidates)),
		Unresolved:        make([]plan.RecoveryItem, len(r.Unresolved)),
		VirtualStore:      r.VirtualStore.Clone(),
		RequiresStabilize: r.RequiresStabilize,
		Persisted:         persisted,
		Diagnostics:       slices.Clone(r.Diagnostics),
	}
	for i, item := range r.Items {
		c := item.Clone()
		if c.Authorized && persisted {
			c.Persisted = true
		}
		out.Items[i] = c
	}
	for i, item := range r.Candidates {
		c := item.Clone()
		c.Persisted = persisted
		out.Candidates[i] = c
	}
	for i, item := range r.Unresolved {
		out.Unresolved[i] = item.Clone()
	}
	return out
}
