package protocol

import (
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/provider"
)

// MetadataStageInput holds the pure inputs to EvaluateMetadataOnlyBatchStage (Stages 4–6 when EnvironmentOps is empty, ADR 0004 §2, Theorem 11, Counterexample #57).
type MetadataStageInput struct {
	Resource      ownership.ResourceID
	Actor         ownership.ActorID
	Batch         ExecutionBatch
	InitialStore  *ownership.Store
	PreCheckDiags []model.Diagnostic
	Stabilize     provider.StabilizeResult
	FinalCommit   CommitResult
}

// EvaluateMetadataOnlyBatchStage purely evaluates a metadata-only batch (EnvironmentOps is empty, MetadataCandidates is non-empty) (ADR 0004 §2 Step 3–6, Theorem 11, Counterexample #57):
//   - Never creates a DurableIntent or Pending reservation (`Intent == nil`, `IntentSaved == false`).
//   - Never calls ApplyBatch or synthesizes an Applied Receipt (`Receipt == nil`).
//   - If pre-Stabilize condition verification or Stabilize fails, no store Commit occurs and no Pending is created.
//   - Only after pre-check and Stabilize succeed is a single atomic store Commit performed.
func EvaluateMetadataOnlyBatchStage(in MetadataStageInput) (NormalPhaseResult, error) {
	if !in.Batch.IsMetadataOnly() {
		return NormalPhaseResult{}, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "EvaluateMetadataOnlyBatchStage requires a metadata-only ExecutionBatch (zero EnvironmentOps and non-empty MetadataCandidates)",
		}
	}

	if len(in.PreCheckDiags) > 0 {
		return NormalPhaseResult{
			Status:      NormalBlocked,
			Batch:       in.Batch.Clone(),
			Store:       in.InitialStore.Clone(),
			Diagnostics: sortAndCloneDiags(in.PreCheckDiags),
		}, nil
	}

	switch in.Stabilize.Outcome {
	case provider.StabilizeIndeterminate:
		return NormalPhaseResult{
			Status:           NormalIndeterminate,
			Batch:            in.Batch.Clone(),
			StabilizeOutcome: provider.StabilizeIndeterminate,
			Store:            in.InitialStore.Clone(),
			Diagnostics:      ensureIndeterminateDiag(in.Stabilize.Diagnostics, "metadata-only Stabilize returned Indeterminate"),
		}, nil
	case provider.StabilizeConflict:
		return NormalPhaseResult{
			Status:           NormalBlocked,
			Batch:            in.Batch.Clone(),
			StabilizeOutcome: provider.StabilizeConflict,
			Store:            in.InitialStore.Clone(),
			Diagnostics:      sortAndCloneDiags(in.Stabilize.Diagnostics),
		}, nil
	case provider.StabilizeDurable:
		return evaluateMetadataOnlyCommit(in)
	default:
		return NormalPhaseResult{}, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "invalid StabilizeOutcome in EvaluateMetadataOnlyBatchStage",
		}
	}
}

func evaluateMetadataOnlyCommit(in MetadataStageInput) (NormalPhaseResult, error) {
	committedStore, err := CommitExecutionBatchToStore(in.InitialStore, in.Resource, in.Actor, in.Batch)
	if err != nil {
		return NormalPhaseResult{}, err
	}

	switch in.FinalCommit.Status {
	case CommitSucceeded:
		return NormalPhaseResult{
			Status:                 NormalCompleted,
			Batch:                  in.Batch.Clone(),
			StabilizeOutcome:       provider.StabilizeDurable,
			CommitStatus:           CommitSucceeded,
			CommittedMetadataCount: len(in.Batch.MetadataCandidates),
			Store:                  committedStore,
		}, nil
	case CommitFailed:
		return NormalPhaseResult{
			Status:           NormalFailed,
			Batch:            in.Batch.Clone(),
			StabilizeOutcome: provider.StabilizeDurable,
			CommitStatus:     CommitFailed,
			Store:            in.InitialStore.Clone(),
			Diagnostics:      sortAndCloneDiags(in.FinalCommit.Diagnostics),
		}, nil
	default:
		return NormalPhaseResult{
			Status:              NormalIndeterminate,
			Batch:               in.Batch.Clone(),
			StabilizeOutcome:    provider.StabilizeDurable,
			CommitStatus:        CommitIndeterminate,
			StoreOutcomeUnknown: true,
			Diagnostics:         ensureIndeterminateDiag(in.FinalCommit.Diagnostics, "metadata-only store commit outcome is indeterminate"),
		}, nil
	}
}

func cloneReceiptPtr(r provider.Receipt) *provider.Receipt {
	c := r.Clone()
	return &c
}

func sortAndCloneDiags(diags []model.Diagnostic) []model.Diagnostic {
	out := slices.Clone(diags)
	model.SortDiagnostics(out)
	return out
}

func ensureIndeterminateDiag(diags []model.Diagnostic, fallbackReason string) []model.Diagnostic {
	out := slices.Clone(diags)
	hasK502 := false
	for _, d := range out {
		if d.Code == model.CodeIndeterminateOutcome {
			hasK502 = true
			break
		}
	}
	if !hasK502 {
		out = append(out, model.Diagnostic{
			Code:       model.CodeIndeterminateOutcome,
			Reason:     fallbackReason,
			NextAction: "reload ownership store and re-observe target resource before retrying",
		})
	}
	model.SortDiagnostics(out)
	return out
}
