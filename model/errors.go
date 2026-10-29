package model

import (
	"fmt"
	"slices"
)

// DiagnosticCode identifies a normative kisage diagnostic or error code (ADR 0001–0005).
type DiagnosticCode string

const (
	// CodeInvalidInput represents a syntax, path, or LKAL document validity error.
	CodeInvalidInput DiagnosticCode = "K101"
	// CodeEmptyRecordNotSupported rejects non-root empty Records in declarations (ADR 0001 §2).
	CodeEmptyRecordNotSupported DiagnosticCode = "K102"

	// CodeOwnershipConflict indicates another Actor owns or reserves an overlapping path (ADR 0002 §1).
	CodeOwnershipConflict DiagnosticCode = "K201"
	// CodeStructureConflict indicates a Leaf vs. Record structural mismatch on target or ancestor (ADR 0001 §4).
	CodeStructureConflict DiagnosticCode = "K202"
	// CodeExternalDrift indicates an owned path drifted from LastApplied (ADR 0003 §1).
	CodeExternalDrift DiagnosticCode = "K203"
	// CodeMigrationRequired indicates an overlapping retired Created path and declared path (ADR 0003 §4).
	CodeMigrationRequired DiagnosticCode = "K204"
	// CodePendingConflict indicates crash recovery observed a third state incompatible with Pending (ADR 0004 §3).
	CodePendingConflict DiagnosticCode = "K205"
	// CodeUnownedValueConflict indicates an unowned path has a value not equal to the desired value (ADR 0003 §1).
	CodeUnownedValueConflict DiagnosticCode = "K206"

	// CodeObservationUnavailable indicates target or required ancestor observation is Unknown (ADR 0001 §3–4).
	CodeObservationUnavailable DiagnosticCode = "K301"
	// CodeUnrepresentable indicates target or required ancestor is Present(Opaque) (ADR 0001 §3–4).
	CodeUnrepresentable DiagnosticCode = "K302"

	// CodePolicyDenied indicates required Scope permissions are missing for a candidate action (ADR 0002 §3, ADR 0003 §1).
	CodePolicyDenied DiagnosticCode = "K401"

	// CodeProviderContractViolation indicates post-apply re-observation violated the Provider contract (ADR 0004 §2).
	CodeProviderContractViolation DiagnosticCode = "K501"
	// CodeIndeterminateOutcome indicates ApplyBatch, Stabilize, or Commit outcome is indeterminate (ADR 0004 §2).
	CodeIndeterminateOutcome DiagnosticCode = "K502"
	// CodeResourceQuarantined indicates the Resource is quarantined due to a contract violation (ADR 0004 §2).
	CodeResourceQuarantined DiagnosticCode = "K503"

	// CodeCreatedOriginUnverified warns that a recovered creation was conservatively downgraded to Modified (ADR 0004 §3).
	CodeCreatedOriginUnverified DiagnosticCode = "K601"
	// CodeExplicitDeletionRightWaived notes that adopt/release relinquished Created deletion rights (ADR 0003 §3).
	CodeExplicitDeletionRightWaived DiagnosticCode = "K602"
	// CodeDeferredUntilRelease defers a declared path overlapping only retired Modified paths (ADR 0003 §1, §4).
	CodeDeferredUntilRelease DiagnosticCode = "K603"
	// CodePruneRequired notes a retired Created leaf remains for prune (ADR 0003 §1–2).
	CodePruneRequired DiagnosticCode = "K604"
)

// Diagnostic represents a structured diagnostic associated with a path and a code.
type Diagnostic struct {
	Code       DiagnosticCode `json:"code"`
	Path       Path           `json:"path,omitempty"`
	Reason     string         `json:"reason"`
	NextAction string         `json:"nextAction,omitempty"`
	Cause      error          `json:"-"`
}

// Error implements the error interface for Diagnostic.
func (d Diagnostic) Error() string {
	if len(d.Path) > 0 {
		return fmt.Sprintf("%s %s: %s", d.Code, d.Path.String(), d.Reason)
	}
	return fmt.Sprintf("%s: %s", d.Code, d.Reason)
}

// Unwrap returns the underlying cause of the diagnostic, if any.
func (d Diagnostic) Unwrap() error {
	return d.Cause
}

// WithPath returns a copy of d with Path set to p if d.Path is currently empty.
func (d Diagnostic) WithPath(p Path) Diagnostic {
	if len(d.Path) == 0 && len(p) > 0 {
		d.Path = p.Clone()
	}
	return d
}

// CompareDiagnostics orders diagnostics deterministically by path (segment order) and then by code (ADR 0005 §2).
func CompareDiagnostics(a, b Diagnostic) int {
	if c := a.Path.Compare(b.Path); c != 0 {
		return c
	}
	if a.Code < b.Code {
		return -1
	}
	if a.Code > b.Code {
		return 1
	}
	if a.Reason < b.Reason {
		return -1
	}
	if a.Reason > b.Reason {
		return 1
	}
	return 0
}

// SortDiagnostics sorts a slice of diagnostics in place by path and code order.
func SortDiagnostics(diags []Diagnostic) {
	slices.SortFunc(diags, CompareDiagnostics)
}
