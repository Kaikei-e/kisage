package protocol

import (
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
)

// ExplicitResolutionStageInput holds the pure inputs to EvaluateExplicitResolutionStage for `adopt` and `release` (ADR 0003 §3, ADR 0004 §2, Counterexamples #38, #50).
type ExplicitResolutionStageInput struct {
	PlanResult           *plan.ResolutionPlan
	PlanErr              error
	Stabilize            provider.StabilizeResult
	StoreCommit          CommitResult
	BeforeStore          *ownership.Store
	Declaration          *projection.Declaration
	Snapshot             *observation.Snapshot
	Resource             ownership.ResourceID
	Actor                ownership.ActorID
	InitiallyQuarantined bool
}

// ExplicitResolutionResult holds the outcome of `kisage adopt` or `kisage release` after in-lock
// verification, Provider.Stabilize, and atomic store Commit (ADR 0003 §3, ADR 0004 §2, ADR 0005 §2, Counterexamples #36–#38, #50).
type ExplicitResolutionResult struct {
	Action              scope.ActionKind
	Plan                *plan.ResolutionPlan
	StabilizeOutcome    provider.StabilizeOutcome
	CommitStatus        CommitStatus
	Execution           SummaryExecution
	Convergence         plan.ConvergenceStatus
	Store               *ownership.Store
	StoreOutcomeUnknown bool
	Persisted           bool
	Diagnostics         []model.Diagnostic
	Summary             ReconcileSummary
}

// EvaluateExplicitResolutionStage purely evaluates the outcome of an explicit `adopt` or `release`
// operation given the in-lock ResolutionPlan check, Provider.Stabilize result, and store CommitResult (Counterexamples #38, #50):
//   - If PlanAdopt / PlanRelease rejected the request (missing Resolve/Own permission, foreign owner,
//     unresolved Pending, Quarantine, Unknown observation, or stale TOKEN / REV), returns SummaryBlocked
//     with environment and store completely unchanged (#38).
//   - If Stabilize returns StabilizeConflict, returns SummaryBlocked with store unchanged (#50).
//   - If Stabilize returns StabilizeIndeterminate, returns SummaryIndeterminate with store unchanged (#50).
//   - If store Commit returns CommitFailed, returns SummaryFailed with store unchanged (#50).
//   - If store Commit returns CommitIndeterminate, returns SummaryIndeterminate and StoreOutcomeUnknown = true
//     without asserting whether the old or new store version is active until reload (#50).
//   - Only when both Stabilize and Commit succeed does it return SummaryCompleted and Persisted = true.
func EvaluateExplicitResolutionStage(in ExplicitResolutionStageInput) ExplicitResolutionResult {
	if in.PlanErr != nil || in.PlanResult == nil {
		var diags []model.Diagnostic
		exec := SummaryBlocked
		if d, ok := in.PlanErr.(model.Diagnostic); ok {
			diags = append(diags, d)
		} else if in.PlanErr != nil {
			diags = append(diags, model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: in.PlanErr.Error(),
				Cause:  in.PlanErr,
			})
		}
		var clonedStore *ownership.Store
		if in.BeforeStore != nil {
			clonedStore = in.BeforeStore.Clone()
		}
		return attachResolutionSummary(in, ExplicitResolutionResult{
			Execution:   exec,
			Store:       clonedStore,
			Diagnostics: sortAndCloneDiags(diags),
		})
	}

	diags := slices.Clone(in.PlanResult.Diagnostics)
	switch in.Stabilize.Outcome {
	case provider.StabilizeIndeterminate:
		diags = append(diags, in.Stabilize.Diagnostics...)
		return attachResolutionSummary(in, ExplicitResolutionResult{
			Action:           in.PlanResult.Action,
			Plan:             in.PlanResult,
			StabilizeOutcome: provider.StabilizeIndeterminate,
			Execution:        SummaryIndeterminate,
			Store:            in.BeforeStore.Clone(),
			Diagnostics:      ensureIndeterminateDiag(diags, "adopt/release Stabilize returned Indeterminate"),
		})
	case provider.StabilizeConflict:
		diags = append(diags, in.Stabilize.Diagnostics...)
		return attachResolutionSummary(in, ExplicitResolutionResult{
			Action:           in.PlanResult.Action,
			Plan:             in.PlanResult,
			StabilizeOutcome: provider.StabilizeConflict,
			Execution:        SummaryBlocked,
			Store:            in.BeforeStore.Clone(),
			Diagnostics:      sortAndCloneDiags(diags),
		})
	case provider.StabilizeDurable:
		res := evaluateResolutionStoreCommit(in, diags)
		return attachResolutionSummary(in, res)
	default:
		res := ExplicitResolutionResult{
			Action:           in.PlanResult.Action,
			Plan:             in.PlanResult,
			StabilizeOutcome: in.Stabilize.Outcome,
			Execution:        SummaryIndeterminate,
			Store:            in.BeforeStore.Clone(),
			Diagnostics:      ensureIndeterminateDiag(diags, "invalid StabilizeOutcome during explicit resolution"),
		}
		return attachResolutionSummary(in, res)
	}
}

func evaluateResolutionStoreCommit(in ExplicitResolutionStageInput, diags []model.Diagnostic) ExplicitResolutionResult {
	diags = append(diags, in.StoreCommit.Diagnostics...)
	switch in.StoreCommit.Status {
	case CommitSucceeded:
		return ExplicitResolutionResult{
			Action:           in.PlanResult.Action,
			Plan:             in.PlanResult,
			StabilizeOutcome: provider.StabilizeDurable,
			CommitStatus:     CommitSucceeded,
			Execution:        SummaryCompleted,
			Store:            in.PlanResult.NextStore.Clone(),
			Persisted:        true,
			Diagnostics:      sortAndCloneDiags(diags),
		}
	case CommitFailed:
		return ExplicitResolutionResult{
			Action:           in.PlanResult.Action,
			Plan:             in.PlanResult,
			StabilizeOutcome: provider.StabilizeDurable,
			CommitStatus:     CommitFailed,
			Execution:        SummaryFailed,
			Store:            in.BeforeStore.Clone(),
			Persisted:        false,
			Diagnostics:      sortAndCloneDiags(diags),
		}
	default:
		// Counterexample #50: When store Commit outcome is indeterminate, never assert success or unchanged state until reload.
		return ExplicitResolutionResult{
			Action:              in.PlanResult.Action,
			Plan:                in.PlanResult,
			StabilizeOutcome:    provider.StabilizeDurable,
			CommitStatus:        CommitIndeterminate,
			Execution:           SummaryIndeterminate,
			Store:               nil,
			StoreOutcomeUnknown: true,
			Persisted:           false,
			Diagnostics:         ensureIndeterminateDiag(diags, "explicit resolution store commit outcome is indeterminate"),
		}
	}
}

func attachResolutionSummary(in ExplicitResolutionStageInput, res ExplicitResolutionResult) ExplicitResolutionResult {
	conv := plan.ConvergenceUnknown
	if !res.StoreOutcomeUnknown && res.Store != nil && in.Declaration != nil && in.Snapshot != nil &&
		in.Resource != "" && in.Actor != "" {
		conv = plan.EvaluateConvergence(in.Declaration, in.Snapshot, res.Store, in.Resource, in.Actor, in.InitiallyQuarantined)
	}
	normal := NormalPhaseResult{
		StoreOutcomeUnknown: res.StoreOutcomeUnknown,
	}
	if res.Persisted {
		normal.CommittedMetadataCount = 1
	}
	if res.Store != nil && in.Resource != "" && in.Actor != "" {
		if sigmaA, err := ownership.ProjectActor(res.Store, in.Resource, in.Actor); err == nil {
			normal.RemainingPendingCount = len(sigmaA.PendingPaths())
		}
	}
	diags := compactDiagnostics(sortAndCloneDiags(res.Diagnostics))
	counts := ComputeExecutionCounts(RecoveryPhaseResult{}, nil, normal, diags)
	res.Convergence = conv
	res.Diagnostics = diags
	res.Summary = ReconcileSummary{
		Execution:   res.Execution,
		Convergence: conv,
		Counts:      counts,
		Diagnostics: diags,
	}
	return res
}
