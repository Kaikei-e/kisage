package cli

import (
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/protocol"
	"github.com/Kaikei-e/kisage/scope"
)

// ExecutionOutcome holds the structured result and exit code of a single CLI or MCP command invocation.
type ExecutionOutcome struct {
	Subcommand      Subcommand
	Actor           ownership.ActorID
	Resource        ownership.ResourceID
	Digest          string
	ExecutionStatus protocol.SummaryExecution
	ExitCode        int
	PlanDoc         *PlanDocument
	CommandDoc      *CommandDocument
	PlanPreview     *plan.PreviewResult
	ReconcileResult *protocol.ReconcileResult
	Explanation     *plan.Explanation
	Resolution      *protocol.ExplicitResolutionResult
}

// Execute runs a validated ParsedCommand against the host environment (ADR 0005 §1–2).
func (h *HostEnvironment) Execute(cmd ParsedCommand) (outcome *ExecutionOutcome, execErr error) {
	var ctx ResolvedContext
	var declSnap *DeclarationSnapshot
	defer func() {
		if execErr != nil {
			outcome = BuildErrorOutcome(cmd, ctx, declSnap, execErr)
		}
	}()
	ctx, err := h.ResolveExecutionContext(cmd.DeclarationFile, cmd.UntrustedActorClaim)
	if err != nil {
		return nil, err
	}
	declSnap, err = h.loadSnapshotAndVerifyDistinct(ctx)
	if err != nil {
		return nil, err
	}

	switch cmd.Subcommand {
	case SubcommandPlan:
		return h.executePlan(ctx, declSnap, cmd.DetailedExitCode)
	case SubcommandApply:
		return h.executeReconcile(ctx, declSnap, plan.ModeApply)
	case SubcommandPrune:
		return h.executeReconcile(ctx, declSnap, plan.ModePrune)
	case SubcommandExplain:
		return h.executeExplain(ctx, declSnap, cmd.TargetPath)
	case SubcommandAdopt:
		return h.executeResolution(ctx, declSnap, SubcommandAdopt, scope.ActionExplicitAdopt, cmd.TargetPath, cmd.ExpectToken, cmd.OwnerRevision)
	case SubcommandRelease:
		return h.executeResolution(ctx, declSnap, SubcommandRelease, scope.ActionExplicitRelease, cmd.TargetPath, cmd.ExpectToken, cmd.OwnerRevision)
	default:
		return nil, usageError("unsupported subcommand %q", cmd.Subcommand)
	}
}

func (h *HostEnvironment) executePlan(
	ctx ResolvedContext,
	declSnap *DeclarationSnapshot,
	detailedExitCode bool,
) (*ExecutionOutcome, error) {
	store, qt, err := protocol.LoadReadOnlyState(h.storeRepo)
	if err != nil {
		return nil, err
	}
	resID := ctx.ResourceBinding.ID
	queryPaths := collectPreviewQueryPaths(declSnap.Declaration, store, resID, ctx.Actor)
	obsSnap, err := h.provider.Observe(resID, queryPaths...)
	if err != nil {
		return nil, err
	}
	preview, err := plan.PlanPreview(plan.Input{
		Resource:    resID,
		Actor:       ctx.Actor,
		Declaration: declSnap.Declaration,
		Snapshot:    obsSnap,
		Store:       store,
		Scope:       ctx.Scope,
		Quarantined: qt.IsQuarantined(resID),
	})
	if err != nil {
		return nil, err
	}
	doc := BuildPlanDocument(declSnap.Digest, preview)
	execStatus := mapPlanExecToSummary(preview.Execution)
	exitCode := ComputePlanExitCode(detailedExitCode, execStatus, nil)
	return &ExecutionOutcome{
		Subcommand:      SubcommandPlan,
		Actor:           ctx.Actor,
		Resource:        resID,
		Digest:          declSnap.Digest,
		ExecutionStatus: execStatus,
		ExitCode:        exitCode,
		PlanDoc:         &doc,
		PlanPreview:     preview,
	}, nil
}

func (h *HostEnvironment) executeReconcile(
	ctx ResolvedContext,
	declSnap *DeclarationSnapshot,
	mode plan.Mode,
) (*ExecutionOutcome, error) {
	sub := SubcommandApply
	if mode == plan.ModePrune {
		sub = SubcommandPrune
	}
	res, err := protocol.ExecuteReconcile(protocol.ReconcileRequest{
		StoreID:     ctx.ResourceBinding.StoreID,
		Resource:    ctx.ResourceBinding.ID,
		Actor:       ctx.Actor,
		Mode:        mode,
		BatchID:     h.batchIDGenerator(),
		Declaration: declSnap.Declaration,
		Scope:       ctx.Scope,
		Provider:    h.provider,
		StoreRepo:   h.storeRepo,
		Locks:       h.locks,
	})
	if err != nil {
		return nil, err
	}
	doc := BuildReconcileDocument(declSnap.Digest, res)
	exitCode := ComputeStandardExitCode(res.Summary.Execution, nil)
	return &ExecutionOutcome{
		Subcommand:      sub,
		Actor:           ctx.Actor,
		Resource:        ctx.ResourceBinding.ID,
		Digest:          declSnap.Digest,
		ExecutionStatus: res.Summary.Execution,
		ExitCode:        exitCode,
		CommandDoc:      &doc,
		ReconcileResult: res,
	}, nil
}

func (h *HostEnvironment) executeExplain(
	ctx ResolvedContext,
	declSnap *DeclarationSnapshot,
	targetPath model.Path,
) (*ExecutionOutcome, error) {
	store, qt, err := protocol.LoadReadOnlyState(h.storeRepo)
	if err != nil {
		return nil, err
	}
	resID := ctx.ResourceBinding.ID
	queryPaths := collectPreviewQueryPaths(declSnap.Declaration, store, resID, ctx.Actor, targetPath)
	obsSnap, err := h.provider.Observe(resID, queryPaths...)
	if err != nil {
		return nil, err
	}
	in := plan.Input{
		Resource:    resID,
		Actor:       ctx.Actor,
		Declaration: declSnap.Declaration,
		Snapshot:    obsSnap,
		Store:       store,
		Scope:       ctx.Scope,
		Quarantined: qt.IsQuarantined(resID),
	}
	preview, err := plan.PlanPreview(in)
	if err != nil {
		return nil, err
	}
	exp, err := plan.ExplainPath(in, targetPath)
	if err != nil {
		return nil, err
	}
	doc := BuildExplainDocument(declSnap.Digest, preview, exp)
	execStatus := parseSummaryExecutionString(doc.Summary.Execution)
	exitCode := ComputeStandardExitCode(execStatus, nil)
	return &ExecutionOutcome{
		Subcommand:      SubcommandExplain,
		Actor:           ctx.Actor,
		Resource:        resID,
		Digest:          declSnap.Digest,
		ExecutionStatus: execStatus,
		ExitCode:        exitCode,
		CommandDoc:      &doc,
		PlanPreview:     preview,
		Explanation:     exp,
	}, nil
}

func (h *HostEnvironment) executeResolution(
	ctx ResolvedContext,
	declSnap *DeclarationSnapshot,
	sub Subcommand,
	action scope.ActionKind,
	targetPath model.Path,
	token plan.ConditionToken,
	rev uint64,
) (*ExecutionOutcome, error) {
	resID := ctx.ResourceBinding.ID
	res, err := protocol.ExecuteExplicitResolution(protocol.ExplicitResolutionExecutionRequest{
		StoreID:          ctx.ResourceBinding.StoreID,
		Resource:         resID,
		Actor:            ctx.Actor,
		Action:           action,
		Path:             targetPath,
		ExpectedToken:    token,
		ExpectedRevision: rev,
		Declaration:      declSnap.Declaration,
		Scope:            ctx.Scope,
		Provider:         h.provider,
		StoreRepo:        h.storeRepo,
		Locks:            h.locks,
	})
	if err != nil {
		return nil, err
	}
	doc := BuildResolutionDocument(declSnap.Digest, resID, targetPath, action, res)
	var diagErr error
	for _, d := range res.Diagnostics {
		if d.Code == model.CodeInvalidInput {
			diagErr = d
			break
		}
	}
	exitCode := ComputeStandardExitCode(res.Execution, diagErr)
	return &ExecutionOutcome{
		Subcommand:      sub,
		Actor:           ctx.Actor,
		Resource:        resID,
		Digest:          declSnap.Digest,
		ExecutionStatus: res.Execution,
		ExitCode:        exitCode,
		CommandDoc:      &doc,
		Resolution:      res,
	}, nil
}

func collectPreviewQueryPaths(
	decl *projection.Declaration,
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	extra ...model.Path,
) []model.Path {
	seen := make(map[string]model.Path)
	if decl != nil {
		for _, p := range decl.Paths() {
			seen[p.String()] = p
		}
	}
	if store != nil {
		if sigmaA, err := ownership.ProjectActor(store, resID, actor); err == nil {
			for _, p := range sigmaA.Domain() {
				seen[p.String()] = p
			}
		}
	}
	for _, p := range extra {
		if len(p) > 0 {
			seen[p.String()] = p.Clone()
		}
	}
	out := make([]model.Path, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	slices.SortFunc(out, model.Path.Compare)
	return out
}

func mapPlanExecToSummary(exec plan.ExecutionStatus) protocol.SummaryExecution {
	switch exec {
	case plan.ExecBlocked:
		return protocol.SummaryBlocked
	case plan.ExecPlanned:
		return protocol.SummaryPlanned
	default:
		return protocol.SummaryNoOp
	}
}

func parseSummaryExecutionString(s string) protocol.SummaryExecution {
	switch s {
	case "Indeterminate":
		return protocol.SummaryIndeterminate
	case "Failed":
		return protocol.SummaryFailed
	case "PartiallyApplied":
		return protocol.SummaryPartiallyApplied
	case "Blocked":
		return protocol.SummaryBlocked
	case "Planned":
		return protocol.SummaryPlanned
	case "Completed":
		return protocol.SummaryCompleted
	default:
		return protocol.SummaryNoOp
	}
}
