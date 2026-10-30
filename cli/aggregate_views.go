package cli

import (
	"slices"

	lkal "github.com/Kaikei-e/LKAL"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/protocol"
)

func buildPreviewRecoveryView(rec *plan.RecoveryReport, quarantined bool) RecoveryView {
	if rec == nil {
		return RecoveryView{
			Status:      protocol.RecoveryNoOp.String(),
			Candidates:  []RecoveryItemView{},
			Unresolved:  []RecoveryItemView{},
			Diagnostics: []DiagnosticView{},
		}
	}
	status := protocol.RecoveryNoOp.String()
	if quarantined || rec.HasUnresolved() {
		status = protocol.RecoveryBlocked.String()
	} else if rec.HasCandidates() {
		status = protocol.SummaryPlanned.String()
	}
	return RecoveryView{
		Status:                status,
		ConditionalAssumption: rec.HasCandidates(),
		RequiresStabilize:     rec.RequiresStabilize,
		Persisted:             false,
		PersistedCount:        0,
		UnresolvedCount:       len(rec.Unresolved),
		Candidates:            buildRecoveryItemViews(rec.Candidates),
		Unresolved:            buildRecoveryItemViews(rec.Unresolved),
		Diagnostics:           buildDiagnosticViews(rec.Diagnostics),
	}
}

func buildExecutedRecoveryView(rec protocol.RecoveryPhaseResult) RecoveryView {
	view := RecoveryView{
		Status:              rec.Status.String(),
		StabilizeOutcome:    stabilizeString(rec.StabilizeOutcome),
		CommitStatus:        commitString(rec.CommitStatus),
		StoreOutcomeUnknown: rec.StoreOutcomeUnknown,
		Persisted:           rec.PersistedCount > 0,
		PersistedCount:      rec.PersistedCount,
		UnresolvedCount:     rec.UnresolvedCount,
		Candidates:          []RecoveryItemView{},
		Unresolved:          []RecoveryItemView{},
		Diagnostics:         buildDiagnosticViews(rec.Diagnostics),
	}
	if rec.Report != nil {
		view.RequiresStabilize = rec.Report.RequiresStabilize
		view.Candidates = buildRecoveryItemViews(rec.Report.Candidates)
		view.Unresolved = buildRecoveryItemViews(rec.Report.Unresolved)
	}
	return view
}

func buildRecoveryItemViews(items []plan.RecoveryItem) []RecoveryItemView {
	out := make([]RecoveryItemView, len(items))
	for i, item := range items {
		var batchID, opStr string
		if item.Before.Pending != nil {
			batchID = item.Before.Pending.BatchID
			opStr = item.Before.Pending.Op.String()
		}
		out[i] = RecoveryItemView{
			Path:        item.Path.String(),
			BatchID:     batchID,
			Op:          opStr,
			Kind:        item.Kind.String(),
			Authorized:  item.Authorized,
			Persisted:   item.Persisted,
			Diagnostics: buildDiagnosticViews(item.Diagnostics),
		}
		if pending := item.Before.Pending; pending != nil {
			out[i].Expected = pending.Expected.String()
			if pending.Target != nil {
				out[i].Target = string(lkal.FormatValue(pending.Target))
			}
		}
	}
	return out
}

func buildExecutedNormalView(planRes *plan.Result, normal protocol.NormalPhaseResult) NormalExecutionView {
	view := NormalExecutionView{
		Status:                      normal.Status.String(),
		EnvironmentOpsCount:         len(normal.Batch.EnvironmentOps),
		MetadataCandidatesCount:     len(normal.Batch.MetadataCandidates),
		IntentSaved:                 normal.IntentSaved,
		StabilizeOutcome:            stabilizeString(normal.StabilizeOutcome),
		CommitStatus:                commitString(normal.CommitStatus),
		ContractViolation:           normal.ContractViolation,
		Quarantined:                 normal.QuarantinedNow,
		EnvironmentPossiblyModified: normal.EnvironmentPossiblyModified,
		StoreOutcomeUnknown:         normal.StoreOutcomeUnknown,
		AppliedEnvCount:             normal.AppliedEnvCount,
		CommittedMetadataCount:      normal.CommittedMetadataCount,
		RemainingPendingCount:       normal.RemainingPendingCount,
		Diagnostics:                 buildDiagnosticViews(normal.Diagnostics),
	}
	if normal.Receipt != nil {
		view.ReceiptOutcome = normal.Receipt.Outcome.String()
	}
	if planRes != nil {
		view.AllOrNothingSuppressed = planRes.Gate.Blocked && len(planRes.Gate.Suppressed) > 0
		view.SuppressedCount = len(planRes.Gate.Suppressed)
		view.DeferredCount = len(planRes.Gate.DeferredActions)
		if view.EnvironmentOpsCount == 0 {
			view.EnvironmentOpsCount = len(planRes.Gate.Changes)
		}
		if view.MetadataCandidatesCount == 0 {
			view.MetadataCandidatesCount = len(planRes.Gate.MetadataCandidates)
		}
	}
	return view
}

func buildPathResultViews(results []plan.PathResult, classified []plan.ClassifiedPath) []PathResultView {
	byPath := make(map[string]plan.ClassifiedPath, len(classified))
	for _, cp := range classified {
		byPath[cp.Path.String()] = cp
	}
	out := make([]PathResultView, len(results))
	for i, pr := range results {
		out[i] = buildSinglePathResultView(pr)
		if cp, ok := byPath[pr.Path.String()]; ok {
			out[i].Observed = cp.Observed.String()
			if cp.Desired != nil {
				out[i].DesiredValue = string(lkal.FormatValue(cp.Desired))
			}
		}
	}
	return out
}

func buildSinglePathResultView(pr plan.PathResult) PathResultView {
	var actionStr, reqPermsStr string
	if pr.Action != 0 {
		actionStr = pr.Action.String()
	}
	if pr.RequiredPerms != 0 {
		reqPermsStr = pr.RequiredPerms.String()
	}
	return PathResultView{
		Path:          pr.Path.String(),
		Membership:    pr.Membership.String(),
		StateClass:    pr.StateClass.String(),
		Selection:     pr.Selection.String(),
		Decision:      pr.Decision.String(),
		Action:        actionStr,
		RequiredPerms: reqPermsStr,
		GrantedPerms:  pr.GrantedPerms.String(),
		PrimaryCode:   string(pr.PrimaryCode),
		Diagnostics:   buildDiagnosticViews(pr.Diagnostics),
	}
}

func buildSummaryView(sum protocol.ReconcileSummary) SummaryView {
	return SummaryView{
		Execution:   sum.Execution.String(),
		Convergence: sum.Convergence.String(),
		Counts:      buildCountsView(sum.Counts),
		Diagnostics: buildDiagnosticViews(sum.Diagnostics),
	}
}

func buildCountsView(c protocol.ExecutionCounts) ExecutionCountsView {
	return ExecutionCountsView{
		RecoveredPersisted:          c.RecoveredPersisted,
		RecoveryUnresolved:          c.RecoveryUnresolved,
		EnvironmentApplied:          c.EnvironmentApplied,
		MetadataCommitted:           c.MetadataCommitted,
		RemainingPending:            c.RemainingPending,
		EnvironmentPossiblyModified: c.EnvironmentPossiblyModified,
		Suppressed:                  c.Suppressed,
		Deferred:                    c.Deferred,
		PruneRequired:               c.PruneRequired,
		Conflicts:                   c.Conflicts,
		ObservationUnavailable:      c.ObservationUnavailable,
		PolicyDenied:                c.PolicyDenied,
	}
}

func buildDiagnosticViews(diags []model.Diagnostic) []DiagnosticView {
	if len(diags) == 0 {
		return []DiagnosticView{}
	}
	sorted := slices.Clone(diags)
	model.SortDiagnostics(sorted)
	sorted = slices.CompactFunc(sorted, func(a, b model.Diagnostic) bool {
		return model.CompareDiagnostics(a, b) == 0 && a.NextAction == b.NextAction
	})
	out := make([]DiagnosticView, len(sorted))
	for i, d := range sorted {
		out[i] = buildSingleDiagnosticView(d)
	}
	return out
}

func buildSingleDiagnosticView(d model.Diagnostic) DiagnosticView {
	var pathStr string
	if len(d.Path) > 0 {
		pathStr = d.Path.String()
	}
	return DiagnosticView{
		Code:       string(d.Code),
		Path:       pathStr,
		Reason:     d.Reason,
		NextAction: d.NextAction,
	}
}
