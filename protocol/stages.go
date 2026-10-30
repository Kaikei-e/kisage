package protocol

import (
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/provider"
)

// NormalExecutionStatus represents the outcome of normal batch execution (Stages 2–6) (ADR 0004 §2, ADR 0005 §2).
type NormalExecutionStatus uint8

const (
	// NormalNoOp indicates there were no executable candidates and no blockers in the active mode.
	NormalNoOp NormalExecutionStatus = iota
	// NormalCompleted indicates all planned environment operations and/or metadata candidates were verified, Stabilized, and committed.
	NormalCompleted
	// NormalPartiallyApplied indicates ApplyBatch returned Applied, but ownership record Commit was confirmed incomplete (e.g., post-apply Stabilize Conflict or store Commit Failed).
	NormalPartiallyApplied
	// NormalBlocked indicates mode blockers, NotApplied, or metadata-only Stabilize Conflict prevented execution without environment changes.
	NormalBlocked
	// NormalFailed indicates a confirmed I/O failure on an unapplied path or a ProviderContractViolation (K501) that quarantined the Resource.
	NormalFailed
	// NormalIndeterminate indicates ApplyBatch, Stabilize, or store Commit returned an Indeterminate outcome.
	NormalIndeterminate
)

// String returns the canonical name of NormalExecutionStatus.
func (s NormalExecutionStatus) String() string {
	switch s {
	case NormalNoOp:
		return "NoOp"
	case NormalCompleted:
		return "Completed"
	case NormalPartiallyApplied:
		return "PartiallyApplied"
	case NormalBlocked:
		return "Blocked"
	case NormalFailed:
		return "Failed"
	case NormalIndeterminate:
		return "Indeterminate"
	default:
		return "InvalidNormalExecutionStatus"
	}
}

// NormalPhaseResult holds the pure evaluation outcome of Stages 2–6 for normal execution (ADR 0004 §2, Counterexamples #4, #18, #26, #45, #46, #57, #58, #60).
type NormalPhaseResult struct {
	Status                      NormalExecutionStatus
	Batch                       ExecutionBatch
	Intent                      *DurableIntent
	IntentSaved                 bool
	Receipt                     *provider.Receipt
	StabilizeOutcome            provider.StabilizeOutcome
	CommitStatus                CommitStatus
	ContractViolation           bool
	QuarantinedNow              bool
	QuarantineRecord            *QuarantineRecord
	HaltRuntime                 bool
	EnvironmentPossiblyModified bool
	StoreOutcomeUnknown         bool
	RemainingPendingCount       int
	AppliedEnvCount             int
	CommittedMetadataCount      int
	Store                       *ownership.Store
	Diagnostics                 []model.Diagnostic
	// Evidence of the in-lock validation and durability preceding a successful
	// environment commit, retained for Go conformance checks.
	BeforeApplySnapshot *observation.Snapshot
	VerifiedSnapshot    *observation.Snapshot
	DurableSnapshot     *observation.Snapshot
}

// EnvironmentStageInput holds the pure inputs to EvaluateEnvironmentBatchStage (Stages 3–6 when EnvironmentOps is non-empty).
type EnvironmentStageInput struct {
	Resource             ownership.ResourceID
	Actor                ownership.ActorID
	Batch                ExecutionBatch
	Intent               *DurableIntent
	PreIntentStore       *ownership.Store
	IntentStore          *ownership.Store
	IntentCommit         CommitResult
	PreApplyCheckDiags   []model.Diagnostic
	RollbackCommit       CommitResult
	BeforeSnapshot       *observation.Snapshot
	Receipt              provider.Receipt
	PostApplySnapshot    *observation.Snapshot
	PostApplyObsErr      error
	PostMetadataDiags    []model.Diagnostic
	Stabilize            provider.StabilizeResult
	FinalCommit          CommitResult
	QuarantineSaveCommit CommitResult
}

// EvaluateEnvironmentBatchStage purely evaluates Stages 3–6 for a batch with non-empty EnvironmentOps (ADR 0004 §2, Counterexamples #4, #18, #26, #45, #46, #58, #60).
func EvaluateEnvironmentBatchStage(in EnvironmentStageInput) (NormalPhaseResult, error) {
	if in.IntentCommit.Status == CommitFailed {
		return NormalPhaseResult{
			Status:       NormalFailed,
			Batch:        in.Batch.Clone(),
			Intent:       in.Intent.Clone(),
			CommitStatus: CommitFailed,
			Store:        in.PreIntentStore.Clone(),
			Diagnostics:  sortAndCloneDiags(in.IntentCommit.Diagnostics),
		}, nil
	}
	if in.IntentCommit.Status == CommitIndeterminate {
		return NormalPhaseResult{
			Status:              NormalIndeterminate,
			Batch:               in.Batch.Clone(),
			Intent:              in.Intent.Clone(),
			CommitStatus:        CommitIndeterminate,
			StoreOutcomeUnknown: true,
			Diagnostics:         ensureIndeterminateDiag(in.IntentCommit.Diagnostics, "intent persistence outcome is indeterminate"),
		}, nil
	}

	if len(in.PreApplyCheckDiags) > 0 {
		notApplied := provider.Receipt{
			BatchID:     in.Batch.BatchID,
			Outcome:     provider.ReceiptNotApplied,
			Diagnostics: slices.Clone(in.PreApplyCheckDiags),
		}
		return evaluateNotAppliedRollback(in, notApplied)
	}

	switch in.Receipt.Outcome {
	case provider.ReceiptNotApplied:
		return evaluateNotAppliedRollback(in, in.Receipt)
	case provider.ReceiptIndeterminate:
		return NormalPhaseResult{
			Status:                      NormalIndeterminate,
			Batch:                       in.Batch.Clone(),
			Intent:                      in.Intent.Clone(),
			IntentSaved:                 true,
			Receipt:                     cloneReceiptPtr(in.Receipt),
			EnvironmentPossiblyModified: true,
			RemainingPendingCount:       len(in.Batch.EnvironmentOps),
			Store:                       in.IntentStore.Clone(),
			Diagnostics:                 ensureIndeterminateDiag(in.Receipt.Diagnostics, "ApplyBatch returned Indeterminate"),
		}, nil
	case provider.ReceiptApplied:
		return evaluateAppliedVerificationAndCommit(in)
	default:
		return NormalPhaseResult{}, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "invalid ReceiptOutcome in EvaluateEnvironmentBatchStage",
		}
	}
}

func evaluateNotAppliedRollback(in EnvironmentStageInput, receipt provider.Receipt) (NormalPhaseResult, error) {
	rolledBackStore, err := RollbackDurableIntentInStore(in.IntentStore, in.Resource, in.Actor, in.Intent)
	if err != nil {
		return NormalPhaseResult{}, err
	}
	diags := slices.Clone(receipt.Diagnostics)
	diags = append(diags, in.RollbackCommit.Diagnostics...)

	if in.RollbackCommit.Status == CommitFailed {
		return NormalPhaseResult{
			Status:                NormalFailed,
			Batch:                 in.Batch.Clone(),
			Intent:                in.Intent.Clone(),
			IntentSaved:           true,
			Receipt:               cloneReceiptPtr(receipt),
			CommitStatus:          CommitFailed,
			RemainingPendingCount: len(in.Batch.EnvironmentOps),
			Store:                 in.IntentStore.Clone(),
			Diagnostics:           sortAndCloneDiags(diags),
		}, nil
	}
	if in.RollbackCommit.Status == CommitIndeterminate {
		return NormalPhaseResult{
			Status:              NormalIndeterminate,
			Batch:               in.Batch.Clone(),
			Intent:              in.Intent.Clone(),
			IntentSaved:         true,
			Receipt:             cloneReceiptPtr(receipt),
			CommitStatus:        CommitIndeterminate,
			StoreOutcomeUnknown: true,
			Diagnostics:         ensureIndeterminateDiag(diags, "rollback store commit outcome after NotApplied is indeterminate"),
		}, nil
	}
	// Counterexample #4 & #46: NotApplied rolls back Pending to PreviousEntry and saves neither EnvironmentOps nor MetadataCandidates.
	return NormalPhaseResult{
		Status:       NormalBlocked,
		Batch:        in.Batch.Clone(),
		Intent:       in.Intent.Clone(),
		IntentSaved:  true,
		Receipt:      cloneReceiptPtr(receipt),
		CommitStatus: CommitSucceeded,
		Store:        rolledBackStore,
		Diagnostics:  sortAndCloneDiags(diags),
	}, nil
}
