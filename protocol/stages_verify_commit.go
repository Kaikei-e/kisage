package protocol

import (
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/provider"
)

func evaluateAppliedVerificationAndCommit(in EnvironmentStageInput) (NormalPhaseResult, error) {
	if in.PostApplyObsErr != nil || in.PostApplySnapshot == nil {
		var diags []model.Diagnostic
		if in.PostApplyObsErr != nil {
			if d, ok := in.PostApplyObsErr.(model.Diagnostic); ok {
				diags = append(diags, d)
			}
		}
		return NormalPhaseResult{
			Status:                      NormalIndeterminate,
			Batch:                       in.Batch.Clone(),
			Intent:                      in.Intent.Clone(),
			IntentSaved:                 true,
			Receipt:                     cloneReceiptPtr(in.Receipt),
			EnvironmentPossiblyModified: true,
			RemainingPendingCount:       len(in.Batch.EnvironmentOps),
			AppliedEnvCount:             len(in.Batch.EnvironmentOps),
			Store:                       in.IntentStore.Clone(),
			Diagnostics:                 ensureIndeterminateDiag(diags, "in-lock re-observation after Applied failed"),
		}, nil
	}

	// Step 5: In-lock contract verification (Set(v)->Leaf(v), Delete->Absent, Frame Condition, Counterexamples #18, #45).
	if contractDiags := provider.VerifyAppliedBatchContract(in.BeforeSnapshot, in.PostApplySnapshot, in.Batch.EnvironmentOps); len(contractDiags) > 0 {
		qRec, qDiags := BuildContractViolationQuarantine(in.Resource, in.Batch.BatchID, contractDiags)
		qDiags = append(qDiags, in.QuarantineSaveCommit.Diagnostics...)
		halt := in.QuarantineSaveCommit.Status != CommitSucceeded
		return NormalPhaseResult{
			Status:                      NormalFailed,
			Batch:                       in.Batch.Clone(),
			Intent:                      in.Intent.Clone(),
			IntentSaved:                 true,
			Receipt:                     cloneReceiptPtr(in.Receipt),
			ContractViolation:           true,
			QuarantinedNow:              true,
			QuarantineRecord:            &qRec,
			HaltRuntime:                 halt,
			EnvironmentPossiblyModified: true,
			RemainingPendingCount:       len(in.Batch.EnvironmentOps),
			Store:                       in.IntentStore.Clone(),
			Diagnostics:                 sortAndCloneDiags(qDiags),
		}, nil
	}

	if len(in.PostMetadataDiags) > 0 {
		return NormalPhaseResult{
			Status:                      NormalPartiallyApplied,
			Batch:                       in.Batch.Clone(),
			Intent:                      in.Intent.Clone(),
			IntentSaved:                 true,
			Receipt:                     cloneReceiptPtr(in.Receipt),
			EnvironmentPossiblyModified: true,
			RemainingPendingCount:       len(in.Batch.EnvironmentOps),
			AppliedEnvCount:             len(in.Batch.EnvironmentOps),
			Store:                       in.IntentStore.Clone(),
			Diagnostics:                 sortAndCloneDiags(in.PostMetadataDiags),
		}, nil
	}

	return evaluatePostApplyStabilizeAndCommit(in)
}

func evaluatePostApplyStabilizeAndCommit(in EnvironmentStageInput) (NormalPhaseResult, error) {
	if in.Stabilize.Outcome == provider.StabilizeIndeterminate {
		return NormalPhaseResult{
			Status:                      NormalIndeterminate,
			Batch:                       in.Batch.Clone(),
			Intent:                      in.Intent.Clone(),
			IntentSaved:                 true,
			Receipt:                     cloneReceiptPtr(in.Receipt),
			StabilizeOutcome:            provider.StabilizeIndeterminate,
			EnvironmentPossiblyModified: true,
			RemainingPendingCount:       len(in.Batch.EnvironmentOps),
			AppliedEnvCount:             len(in.Batch.EnvironmentOps),
			Store:                       in.IntentStore.Clone(),
			Diagnostics:                 ensureIndeterminateDiag(in.Stabilize.Diagnostics, "post-apply Stabilize returned Indeterminate"),
		}, nil
	}
	if in.Stabilize.Outcome == provider.StabilizeConflict {
		return NormalPhaseResult{
			Status:                      NormalPartiallyApplied,
			Batch:                       in.Batch.Clone(),
			Intent:                      in.Intent.Clone(),
			IntentSaved:                 true,
			Receipt:                     cloneReceiptPtr(in.Receipt),
			StabilizeOutcome:            provider.StabilizeConflict,
			EnvironmentPossiblyModified: true,
			RemainingPendingCount:       len(in.Batch.EnvironmentOps),
			AppliedEnvCount:             len(in.Batch.EnvironmentOps),
			Store:                       in.IntentStore.Clone(),
			Diagnostics:                 sortAndCloneDiags(in.Stabilize.Diagnostics),
		}, nil
	}

	committedStore, err := CommitExecutionBatchToStore(in.IntentStore, in.Resource, in.Actor, in.Batch)
	if err != nil {
		return NormalPhaseResult{}, err
	}
	switch in.FinalCommit.Status {
	case CommitSucceeded:
		return NormalPhaseResult{
			Status:                 NormalCompleted,
			Batch:                  in.Batch.Clone(),
			Intent:                 in.Intent.Clone(),
			IntentSaved:            true,
			Receipt:                cloneReceiptPtr(in.Receipt),
			StabilizeOutcome:       provider.StabilizeDurable,
			CommitStatus:           CommitSucceeded,
			AppliedEnvCount:        len(in.Batch.EnvironmentOps),
			CommittedMetadataCount: len(in.Batch.MetadataCandidates),
			Store:                  committedStore,
			BeforeApplySnapshot:    in.BeforeSnapshot,
			VerifiedSnapshot:       in.PostApplySnapshot,
			DurableSnapshot:        in.Stabilize.DurableSnapshot,
		}, nil
	case CommitFailed:
		return NormalPhaseResult{
			Status:                      NormalPartiallyApplied,
			Batch:                       in.Batch.Clone(),
			Intent:                      in.Intent.Clone(),
			IntentSaved:                 true,
			Receipt:                     cloneReceiptPtr(in.Receipt),
			StabilizeOutcome:            provider.StabilizeDurable,
			CommitStatus:                CommitFailed,
			EnvironmentPossiblyModified: true,
			RemainingPendingCount:       len(in.Batch.EnvironmentOps),
			AppliedEnvCount:             len(in.Batch.EnvironmentOps),
			Store:                       in.IntentStore.Clone(),
			Diagnostics:                 sortAndCloneDiags(in.FinalCommit.Diagnostics),
		}, nil
	default:
		// Counterexample #60: When store Commit outcome after Applied is indeterminate, never assert Pending presence or absence; report Indeterminate.
		return NormalPhaseResult{
			Status:                      NormalIndeterminate,
			Batch:                       in.Batch.Clone(),
			Intent:                      in.Intent.Clone(),
			IntentSaved:                 true,
			Receipt:                     cloneReceiptPtr(in.Receipt),
			StabilizeOutcome:            provider.StabilizeDurable,
			CommitStatus:                CommitIndeterminate,
			EnvironmentPossiblyModified: true,
			StoreOutcomeUnknown:         true,
			AppliedEnvCount:             len(in.Batch.EnvironmentOps),
			Diagnostics:                 ensureIndeterminateDiag(in.FinalCommit.Diagnostics, "ownership store commit outcome after Applied is indeterminate"),
		}, nil
	}
}
