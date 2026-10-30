package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// FormatJSON purely serializes a PlanDocument or CommandDocument as indented JSON with a trailing newline.
func FormatJSON(doc any) ([]byte, error) {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// FormatOutcomeText purely formats an ExecutionOutcome as deterministic human-readable text.
func FormatOutcomeText(outcome *ExecutionOutcome) string {
	if outcome == nil {
		return ""
	}
	if outcome.PlanDoc != nil {
		return formatPlanText(*outcome.PlanDoc)
	}
	if outcome.CommandDoc != nil {
		return formatCommandText(*outcome.CommandDoc)
	}
	return ""
}

func formatPlanText(doc PlanDocument) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mode: %s\nresource: %s\ndeclarationDigest: %s\n", doc.Mode, doc.Resource, doc.DeclarationDigest)
	if doc.ConditionalOnRecovery {
		fmt.Fprintf(&b, "note: conditional plan assuming Stabilize and recovery persistence succeed\n")
	}
	fmt.Fprintf(&b, "recovery: status=%s candidates=%d unresolved=%d\n",
		doc.Recovery.Status, len(doc.Recovery.Candidates), len(doc.Recovery.Unresolved))
	formatRecoveryDetails(&b, doc.Recovery)
	fmt.Fprintf(&b, "plans.apply: execution=%s envOps=%d metadataOps=%d deferred=%d suppressed=%d\n",
		doc.Plans.Apply.Summary.Execution,
		doc.Plans.Apply.NormalExecution.EnvironmentOpsCount,
		doc.Plans.Apply.NormalExecution.MetadataCandidatesCount,
		doc.Plans.Apply.NormalExecution.DeferredCount,
		doc.Plans.Apply.NormalExecution.SuppressedCount)
	formatPathDetails(&b, doc.Plans.Apply.PathResults)
	formatDiagnostics(&b, doc.Plans.Apply.Summary.Diagnostics)
	fmt.Fprintf(&b, "plans.prune: execution=%s envOps=%d metadataOps=%d deferred=%d suppressed=%d\n",
		doc.Plans.Prune.Summary.Execution,
		doc.Plans.Prune.NormalExecution.EnvironmentOpsCount,
		doc.Plans.Prune.NormalExecution.MetadataCandidatesCount,
		doc.Plans.Prune.NormalExecution.DeferredCount,
		doc.Plans.Prune.NormalExecution.SuppressedCount)
	formatPathDetails(&b, doc.Plans.Prune.PathResults)
	formatDiagnostics(&b, doc.Plans.Prune.Summary.Diagnostics)
	fmt.Fprintf(&b, "summary: execution=%s convergence=%s\n", doc.Summary.Execution, doc.Summary.Convergence)
	return b.String()
}

func formatCommandText(doc CommandDocument) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mode: %s\nresource: %s\ndeclarationDigest: %s\n", doc.Mode, doc.Resource, doc.DeclarationDigest)
	fmt.Fprintf(&b, "recovery: status=%s persisted=%d unresolved=%d\n",
		doc.Recovery.Status, doc.Recovery.PersistedCount, doc.Recovery.UnresolvedCount)
	formatRecoveryDetails(&b, doc.Recovery)
	fmt.Fprintf(&b, "normalExecution: status=%s appliedEnv=%d committedMetadata=%d\n",
		doc.NormalExecution.Status, doc.NormalExecution.AppliedEnvCount, doc.NormalExecution.CommittedMetadataCount)
	if doc.Explanation != nil {
		fmt.Fprintf(&b, "explain: path=%s observed=%s ownerRevision=%d token=%s apply=%s prune=%s\n",
			doc.Explanation.Path,
			doc.Explanation.Observed,
			doc.Explanation.OwnerRevision,
			doc.Explanation.ConditionToken,
			doc.Explanation.ApplyExecution,
			doc.Explanation.PruneExecution)
		fmt.Fprintf(&b, "  ownership: manager=%s origin=%s pending=%t permissions=%s\n", doc.Explanation.OwnerManager, doc.Explanation.OwnerOrigin, doc.Explanation.HasPending, doc.Explanation.GrantedPermissions)
		fmt.Fprintf(&b, "  declared=%t desired=%s\n", doc.Explanation.Declared, doc.Explanation.DesiredValue)
	}
	formatPathDetails(&b, doc.PathResults)
	if doc.Resolution != nil {
		fmt.Fprintf(&b, "resolution: action=%s path=%s persisted=%t prevRev=%d nextRev=%d\n",
			doc.Resolution.Action,
			doc.Resolution.Path,
			doc.Resolution.Persisted,
			doc.Resolution.PreviousRevision,
			doc.Resolution.NextRevision)
	}
	fmt.Fprintf(&b, "summary: execution=%s convergence=%s diagnostics=%d\n",
		doc.Summary.Execution, doc.Summary.Convergence, len(doc.Summary.Diagnostics))
	formatDiagnostics(&b, doc.Summary.Diagnostics)
	return b.String()
}

func formatRecoveryDetails(b *strings.Builder, rec RecoveryView) {
	for _, items := range [][]RecoveryItemView{rec.Candidates, rec.Unresolved} {
		for _, item := range items {
			fmt.Fprintf(b, "  %s: recovery=%s op=%s authorized=%t persisted=%t\n", item.Path, item.Kind, item.Op, item.Authorized, item.Persisted)
			fmt.Fprintf(b, "    expected=%s target=%s\n", item.Expected, item.Target)
		}
	}
	formatDiagnostics(b, rec.Diagnostics)
}

func formatPathDetails(b *strings.Builder, paths []PathResultView) {
	for _, p := range paths {
		fmt.Fprintf(b, "  %s: state=%s selection=%s decision=%s action=%s required=%s granted=%s\n", p.Path, p.StateClass, p.Selection, p.Decision, p.Action, p.RequiredPerms, p.GrantedPerms)
		if p.Observed != "" || p.DesiredValue != "" {
			fmt.Fprintf(b, "    observed=%s desired=%s\n", p.Observed, p.DesiredValue)
		}
	}
}

func formatDiagnostics(b *strings.Builder, diagnostics []DiagnosticView) {
	for _, d := range diagnostics {
		if d.Path != "" {
			fmt.Fprintf(b, "  - %s %s: %s\n", d.Code, d.Path, d.Reason)
		} else {
			fmt.Fprintf(b, "  - %s: %s\n", d.Code, d.Reason)
		}
		if d.NextAction != "" {
			fmt.Fprintf(b, "    next: %s\n", d.NextAction)
		}
	}
}

// Runner executes kisage CLI commands against a HostEnvironment (ADR 0005 §1–2).
type Runner struct {
	Host *HostEnvironment
}

// NewRunner constructs a CLI Runner bound to host.
func NewRunner(host *HostEnvironment) *Runner {
	return &Runner{Host: host}
}

// Run parses args, executes the requested subcommand, writes output to stdout (or errors to stderr),
// and returns the normative exit code (0, 1, 2, or 64) (ADR 0005 §1–2).
func (r *Runner) Run(args []string, stdout, stderr io.Writer) int {
	cmd, err := ParseArgs(args)
	if err != nil {
		return ReportError(args, stdout, stderr, err)
	}
	if r == nil || r.Host == nil {
		hostErr := usageError("CLI runner requires a configured HostEnvironment")
		return ReportError(args, stdout, stderr, hostErr)
	}

	outcome, execErr := r.Host.Execute(cmd)
	if execErr != nil {
		writeCLIError(stderr, execErr)
	}
	return writeOutcome(cmd, outcome, stdout, stderr)
}

func writeOutcome(cmd ParsedCommand, outcome *ExecutionOutcome, stdout, stderr io.Writer) int {
	if cmd.JSON {
		b, jsonErr := FormatJSON(outcome.Document())
		if jsonErr != nil {
			writeCLIError(stderr, jsonErr)
			return ExitCodeError
		}
		if stdout != nil {
			if _, err := stdout.Write(b); err != nil {
				writeCLIError(stderr, err)
				return ExitCodeError
			}
		}
	} else if stdout != nil {
		if _, err := io.WriteString(stdout, FormatOutcomeText(outcome)); err != nil {
			writeCLIError(stderr, err)
			return ExitCodeError
		}
	}
	return outcome.ExitCode
}

// ReportError renders failures that occur before host execution (usage and host
// startup included) using the same structured schema as execution failures.
func ReportError(args []string, stdout, stderr io.Writer, err error) int {
	cmd, _ := ParseArgs(args)
	if hasDetailedExitCodeFlag(args) {
		cmd.DetailedExitCode = true
	}
	if hasJSONFlag(args) {
		cmd.JSON = true
	}
	out := BuildErrorOutcome(cmd, ResolvedContext{}, nil, err)
	writeCLIError(stderr, err)
	return writeOutcome(cmd, out, stdout, stderr)
}

func hasJSONFlag(args []string) bool {
	for _, a := range args {
		if a == "-json" || a == "--json" || strings.HasPrefix(a, "-json=true") || strings.HasPrefix(a, "--json=true") {
			return true
		}
	}
	return false
}

func hasDetailedExitCodeFlag(args []string) bool {
	if len(args) == 0 || Subcommand(args[0]) != SubcommandPlan {
		return false
	}
	for _, a := range args[1:] {
		if a == "-detailed-exitcode" || a == "--detailed-exitcode" || strings.HasPrefix(a, "-detailed-exitcode=true") || strings.HasPrefix(a, "--detailed-exitcode=true") {
			return true
		}
	}
	return false
}

func writeCLIError(stderr io.Writer, err error) {
	if stderr != nil && err != nil {
		_, _ = fmt.Fprintln(stderr, err.Error())
	}
}
