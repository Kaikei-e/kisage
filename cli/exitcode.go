package cli

import (
	"errors"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/protocol"
)

// Normative CLI exit codes (ADR 0005 §2).
const (
	// ExitCodeOK indicates operation completed (or no diff in `plan -detailed-exitcode`).
	ExitCodeOK = 0
	// ExitCodeError indicates an execution error, conflict/blocker, partial application, or indeterminate outcome.
	ExitCodeError = 1
	// ExitCodePlanDiff indicates `plan -detailed-exitcode` found executable candidates, recovery candidates, or Deferred work without blockers.
	ExitCodePlanDiff = 2
	// ExitCodeUsage indicates an input or CLI usage error (EX_USAGE = 64).
	ExitCodeUsage = 64
)

// IsUsageError reports whether err is a syntax, declaration validity (K101/K102), or CLI usage error (ADR 0005 §2).
func IsUsageError(err error) bool {
	if err == nil {
		return false
	}
	var diag model.Diagnostic
	if errors.As(err, &diag) {
		return diag.Code == model.CodeInvalidInput || diag.Code == model.CodeEmptyRecordNotSupported
	}
	return false
}

// ComputeStandardExitCode purely computes the standard CLI exit code (0, 1, or 64) for any command
// executed without `-detailed-exitcode` (ADR 0005 §2).
func ComputeStandardExitCode(exec protocol.SummaryExecution, err error) int {
	if err != nil {
		if IsUsageError(err) {
			return ExitCodeUsage
		}
		return ExitCodeError
	}
	switch exec {
	case protocol.SummaryNoOp, protocol.SummaryPlanned, protocol.SummaryCompleted:
		return ExitCodeOK
	default:
		return ExitCodeError
	}
}

// ComputePlanExitCode purely computes the CLI exit code for `kisage plan` (ADR 0005 §2):
//   - When detailed is false: uses standard exit codes (0 = NoOp/Planned, 1 = Blocked/Error, 64 = Input/Usage error).
//   - When detailed is true (`-detailed-exitcode`):
//   - 1 if err != nil (including input errors) or exec is Blocked/Failed/Indeterminate
//   - 2 if exec is Planned (environment/metadata/recovery candidates or Deferred actions exist)
//   - 0 if exec is NoOp (both Apply and Prune are NoOp)
func ComputePlanExitCode(detailed bool, exec protocol.SummaryExecution, err error) int {
	if !detailed {
		return ComputeStandardExitCode(exec, err)
	}
	if err != nil {
		return ExitCodeError
	}
	switch exec {
	case protocol.SummaryNoOp:
		return ExitCodeOK
	case protocol.SummaryPlanned:
		return ExitCodePlanDiff
	default:
		return ExitCodeError
	}
}
