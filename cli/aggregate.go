package cli

import (
	"slices"

	lkal "github.com/Kaikei-e/LKAL"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/protocol"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
)

// BuildPlanDocument purely constructs the dual-mode `PlanDocument` from a read-only `plan.PreviewResult`
// and declaration digest (ADR 0005 §1–2, Counterexamples #14, #31, #41).
func BuildPlanDocument(digest string, preview *plan.PreviewResult) PlanDocument {
	recView := buildPreviewRecoveryView(preview.Recovery, preview.Quarantined)
	applyView, applyExec := buildModePlanView(preview.Quarantined, preview.Recovery, preview.Apply)
	pruneView, pruneExec := buildModePlanView(preview.Quarantined, preview.Recovery, preview.Prune)

	topExec := protocol.CombineSummaryExecution(applyExec, pruneExec)
	return PlanDocument{
		SchemaVersion:         SchemaVersion,
		Mode:                  ModeNamePlan,
		Resource:              string(preview.Resource),
		DeclarationDigest:     digest,
		ConditionalOnRecovery: preview.Recovery != nil && preview.Recovery.HasCandidates(),
		Recovery:              recView,
		Plans: DualModePlansView{
			Apply: applyView,
			Prune: pruneView,
		},
		Summary: PlanTopSummaryView{
			Execution:   topExec.String(),
			Convergence: preview.Convergence.String(),
		},
	}
}

// BuildReconcileDocument purely constructs a singular `CommandDocument` for `apply` or `prune`
// from a `protocol.ReconcileResult` and declaration digest (ADR 0005 §2, Counterexamples #4, #14, #44, #45, #59, #60).
func BuildReconcileDocument(digest string, res *protocol.ReconcileResult) CommandDocument {
	modeName := ModeNameApply
	if res.Mode == plan.ModePrune {
		modeName = ModeNamePrune
	}
	recView := buildExecutedRecoveryView(res.Recovery)
	normView := buildExecutedNormalView(res.Plan, res.NormalExecution)
	var pathViews []PathResultView
	if res.Plan != nil {
		pathViews = buildPathResultViews(res.Plan.Gate.PathResults, res.Plan.Classified)
	} else {
		pathViews = []PathResultView{}
	}
	return CommandDocument{
		SchemaVersion:     SchemaVersion,
		Mode:              modeName,
		Resource:          string(res.Resource),
		DeclarationDigest: digest,
		Recovery:          recView,
		NormalExecution:   normView,
		PathResults:       pathViews,
		Summary:           buildSummaryView(res.Summary),
	}
}

// BuildExplainDocument purely constructs a singular `CommandDocument` for `explain FILE PATH`
// from the read-only preview, path explanation, and declaration digest (ADR 0005 §1–2, Counterexamples #36–#38, #41).
func BuildExplainDocument(digest string, preview *plan.PreviewResult, exp *plan.Explanation) CommandDocument {
	recView := buildPreviewRecoveryView(preview.Recovery, preview.Quarantined)
	_, applyExec := buildModePlanView(preview.Quarantined, preview.Recovery, preview.Apply)
	_, pruneExec := buildModePlanView(preview.Quarantined, preview.Recovery, preview.Prune)
	exec := protocol.CombineSummaryExecution(applyExec, pruneExec)
	if isPathBlocked(exp.Apply) || isPathBlocked(exp.Prune) || exp.Observed.IsUnknown() {
		exec = protocol.SummaryBlocked
	}

	normView := NormalExecutionView{
		Status:                exec.String(),
		Diagnostics:           buildDiagnosticViews(exp.Diagnostics),
		RemainingPendingCount: len(recView.Unresolved),
	}
	applyPR := buildSinglePathResultView(exp.Apply)
	prunePR := buildSinglePathResultView(exp.Prune)
	expView := buildExplanationView(exp, applyPR, prunePR)

	counts := protocol.ComputeExecutionCounts(
		protocol.RecoveryPhaseResult{UnresolvedCount: len(recView.Unresolved)},
		nil,
		protocol.NormalPhaseResult{},
		exp.Diagnostics,
	)
	return CommandDocument{
		SchemaVersion:     SchemaVersion,
		Mode:              ModeNameExplain,
		Resource:          string(exp.Resource),
		DeclarationDigest: digest,
		Recovery:          recView,
		NormalExecution:   normView,
		PathResults:       []PathResultView{applyPR, prunePR},
		Explanation:       &expView,
		Summary: SummaryView{
			Execution:   exec.String(),
			Convergence: preview.Convergence.String(),
			Counts:      buildCountsView(counts),
			Diagnostics: buildDiagnosticViews(exp.Diagnostics),
		},
	}
}

// BuildResolutionDocument purely constructs a singular `CommandDocument` for `adopt` or `release`
// from a `protocol.ExplicitResolutionResult` and declaration digest (ADR 0003 §3, ADR 0005 §1–2, Counterexamples #36–#38, #50).
func BuildResolutionDocument(
	digest string,
	resID ownership.ResourceID,
	targetPath model.Path,
	action scope.ActionKind,
	res *protocol.ExplicitResolutionResult,
) CommandDocument {
	modeName := ModeNameAdopt
	if action == scope.ActionExplicitRelease {
		modeName = ModeNameRelease
	}
	recView := RecoveryView{
		Status:      protocol.RecoveryNoOp.String(),
		Candidates:  []RecoveryItemView{},
		Unresolved:  []RecoveryItemView{},
		Diagnostics: []DiagnosticView{},
	}
	normStatus := mapSummaryToNormalStatus(res.Execution)
	normView := NormalExecutionView{
		Status:              normStatus,
		StabilizeOutcome:    stabilizeString(res.StabilizeOutcome),
		CommitStatus:        commitString(res.CommitStatus),
		StoreOutcomeUnknown: res.StoreOutcomeUnknown,
		Diagnostics:         buildDiagnosticViews(res.Diagnostics),
	}
	if res.Persisted {
		normView.CommittedMetadataCount = 1
	}

	var resView ResolutionView
	resView.Action = action.String()
	resView.Path = targetPath.String()
	resView.Persisted = res.Persisted
	if res.Plan != nil {
		resView.DeclaredInFile = res.Plan.DeclaredInFile
		resView.RemanageOnNextApply = res.Plan.RemanageOnNextApply
		resView.PreviousRevision = res.Plan.PreviousRevision
		resView.NextRevision = res.Plan.NextRevision
	}

	prView := buildResolutionPathResultView(targetPath, action, res)
	return CommandDocument{
		SchemaVersion:     SchemaVersion,
		Mode:              modeName,
		Resource:          string(resID),
		DeclarationDigest: digest,
		Recovery:          recView,
		NormalExecution:   normView,
		PathResults:       []PathResultView{prView},
		Resolution:        &resView,
		Summary:           buildSummaryView(res.Summary),
	}
}

func buildModePlanView(
	quarantined bool,
	rec *plan.RecoveryReport,
	modeRes *plan.Result,
) (ModePlanView, protocol.SummaryExecution) {
	recPhase := protocol.RecoveryPhaseResult{}
	if rec != nil {
		recPhase.UnresolvedCount = len(rec.Unresolved)
		if rec.HasUnresolved() {
			recPhase.Status = protocol.RecoveryBlocked
		}
	}
	normPhase := protocol.NormalPhaseResult{}
	hasWork := false
	if rec != nil && rec.HasCandidates() {
		hasWork = true
	}
	var pathViews []PathResultView
	var modeDiags []model.Diagnostic
	if modeRes != nil {
		if modeRes.Gate.Blocked {
			normPhase.Status = protocol.NormalBlocked
		}
		if modeRes.Gate.HasWork() || len(modeRes.Gate.DeferredActions) > 0 {
			hasWork = true
		}
		pathViews = buildPathResultViews(modeRes.Gate.PathResults, modeRes.Classified)
		modeDiags = slices.Clone(modeRes.Diagnostics)
	} else {
		pathViews = []PathResultView{}
	}

	exec := protocol.EvaluateSummaryExecution(true, hasWork, quarantined, recPhase, normPhase)
	counts := protocol.ComputeExecutionCounts(recPhase, modeRes, normPhase, modeDiags)

	normView := NormalExecutionView{
		Status:                exec.String(),
		Diagnostics:           buildDiagnosticViews(modeDiags),
		RemainingPendingCount: recPhase.UnresolvedCount,
	}
	if modeRes != nil {
		normView.AllOrNothingSuppressed = modeRes.Gate.Blocked && len(modeRes.Gate.Suppressed) > 0
		normView.SuppressedCount = len(modeRes.Gate.Suppressed)
		normView.EnvironmentOpsCount = len(modeRes.Gate.Changes)
		normView.MetadataCandidatesCount = len(modeRes.Gate.MetadataCandidates)
		normView.DeferredCount = len(modeRes.Gate.DeferredActions)
	}

	return ModePlanView{
		NormalExecution: normView,
		PathResults:     pathViews,
		Summary: ModePlanSummaryView{
			Execution:   exec.String(),
			Counts:      buildCountsView(counts),
			Diagnostics: buildDiagnosticViews(modeDiags),
		},
	}, exec
}

func isPathBlocked(pr plan.PathResult) bool {
	return pr.Decision == plan.DecisionBlocker || pr.Decision == plan.DecisionPolicyDenied
}

func buildExplanationView(exp *plan.Explanation, applyPR, prunePR PathResultView) ExplanationView {
	view := ExplanationView{
		Path:               exp.Path.String(),
		Declared:           exp.Declared,
		Observed:           exp.Observed.String(),
		OwnerRevision:      exp.OwnerRevision,
		ConditionToken:     exp.ConditionToken.String(),
		GrantedPermissions: exp.GrantedPerms.String(),
		Apply:              applyPR,
		Prune:              prunePR,
		ApplyExecution:     exp.ApplyExecution.String(),
		PruneExecution:     exp.PruneExecution.String(),
	}
	if exp.Declared && exp.DesiredValue != nil {
		view.DesiredValue = string(lkal.FormatValue(exp.DesiredValue))
	}
	if exp.Owner != nil {
		view.OwnerManager = string(exp.Owner.Manager)
		view.OwnerOrigin = exp.Owner.Origin.String()
		view.HasPending = exp.Owner.HasPending()
	}
	return view
}

func buildResolutionPathResultView(
	targetPath model.Path,
	action scope.ActionKind,
	res *protocol.ExplicitResolutionResult,
) PathResultView {
	decision := "Authorized"
	selection := "Candidate"
	if res.Execution == protocol.SummaryBlocked || res.Execution == protocol.SummaryFailed || res.Execution == protocol.SummaryIndeterminate {
		decision = "Blocker"
		selection = "Blocker"
	}
	var primary string
	if len(res.Diagnostics) > 0 {
		primary = string(res.Diagnostics[0].Code)
	}
	return PathResultView{
		Path:        targetPath.String(),
		Membership:  "ExplicitTarget",
		StateClass:  action.String(),
		Selection:   selection,
		Decision:    decision,
		Action:      action.String(),
		PrimaryCode: primary,
		Diagnostics: buildDiagnosticViews(res.Diagnostics),
	}
}

func mapSummaryToNormalStatus(s protocol.SummaryExecution) string {
	switch s {
	case protocol.SummaryCompleted:
		return protocol.NormalCompleted.String()
	case protocol.SummaryBlocked:
		return protocol.NormalBlocked.String()
	case protocol.SummaryFailed:
		return protocol.NormalFailed.String()
	case protocol.SummaryIndeterminate:
		return protocol.NormalIndeterminate.String()
	default:
		return s.String()
	}
}

func stabilizeString(o provider.StabilizeOutcome) string {
	if o == 0 {
		return ""
	}
	return o.String()
}

func commitString(c protocol.CommitStatus) string {
	if c == protocol.CommitInvalid {
		return ""
	}
	return c.String()
}
