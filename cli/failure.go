package cli

import (
	"errors"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/protocol"
)

// BuildErrorOutcome preserves the context available before an invocation failed.
// An empty declarationDigest means the declaration bytes could not be read; it
// is never replaced with a fabricated digest or a second read of the file.
func BuildErrorOutcome(cmd ParsedCommand, ctx ResolvedContext, snap *DeclarationSnapshot, err error) *ExecutionOutcome {
	diag := model.Diagnostic{Code: model.CodeInvalidInput, Reason: err.Error()}
	var original model.Diagnostic
	if errors.As(err, &original) {
		diag = original
	}
	exec := protocol.SummaryFailed
	if IsUsageError(err) || diag.Code == model.CodePolicyDenied || diag.Code == model.CodeResourceQuarantined {
		exec = protocol.SummaryBlocked
	}
	code := ComputeStandardExitCode(exec, err)
	if cmd.Subcommand == SubcommandPlan {
		code = ComputePlanExitCode(cmd.DetailedExitCode, exec, err)
	}
	out := &ExecutionOutcome{Subcommand: cmd.Subcommand, Actor: ctx.Actor, Resource: ctx.ResourceBinding.ID, ExecutionStatus: exec, ExitCode: code}
	if snap != nil {
		out.Digest = snap.Digest
	}
	diags := buildDiagnosticViews([]model.Diagnostic{diag})
	rec := RecoveryView{Status: "NoOp", Candidates: []RecoveryItemView{}, Unresolved: []RecoveryItemView{}, Diagnostics: []DiagnosticView{}}
	normal := NormalExecutionView{Status: exec.String(), Diagnostics: diags}
	counts := buildCountsView(protocol.ComputeExecutionCounts(protocol.RecoveryPhaseResult{}, nil, protocol.NormalPhaseResult{}, []model.Diagnostic{diag}))
	if cmd.Subcommand == SubcommandPlan {
		branch := ModePlanView{NormalExecution: normal, PathResults: []PathResultView{}, Summary: ModePlanSummaryView{Execution: exec.String(), Counts: counts, Diagnostics: diags}}
		out.PlanDoc = &PlanDocument{SchemaVersion: SchemaVersion, Mode: ModeNamePlan, Resource: string(out.Resource), DeclarationDigest: out.Digest, Recovery: rec, Plans: DualModePlansView{Apply: branch, Prune: branch}, Summary: PlanTopSummaryView{Execution: exec.String(), Convergence: plan.ConvergenceUnknown.String()}}
	} else {
		modes := map[Subcommand]string{SubcommandApply: ModeNameApply, SubcommandPrune: ModeNamePrune, SubcommandExplain: ModeNameExplain, SubcommandAdopt: ModeNameAdopt, SubcommandRelease: ModeNameRelease}
		out.CommandDoc = &CommandDocument{SchemaVersion: SchemaVersion, Mode: modes[cmd.Subcommand], Resource: string(out.Resource), DeclarationDigest: out.Digest, Recovery: rec, NormalExecution: normal, PathResults: []PathResultView{}, Summary: SummaryView{Execution: exec.String(), Convergence: plan.ConvergenceUnknown.String(), Counts: counts, Diagnostics: diags}}
	}
	return out
}

// Document returns the common JSON payload shared by CLI and MCP responses.
func (o *ExecutionOutcome) Document() any {
	if o.PlanDoc != nil {
		return o.PlanDoc
	}
	return o.CommandDoc
}
