package plan

import (
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/scope"
)

// DecisionKind represents the per-path outcome after Stage 4 (Authorize) and Stage 5 (Gate) (ADR 0003 §1).
type DecisionKind uint8

const (
	// DecisionInvalid is the zero value for an uninitialized DecisionKind.
	DecisionInvalid DecisionKind = iota
	// DecisionNoOp indicates the path is converged and requires no action.
	DecisionNoOp
	// DecisionDeferred indicates the path's action or out-of-mode anomaly is deferred without checking permissions.
	DecisionDeferred
	// DecisionAuthorized indicates the candidate satisfied Scope permissions.
	DecisionAuthorized
	// DecisionPolicyDenied indicates the candidate lacked required Scope permissions (K401).
	DecisionPolicyDenied
	// DecisionBlocker indicates the path is an in-mode or global blocker.
	DecisionBlocker
	// DecisionSuppressed indicates an Authorized candidate was suppressed by Stage 5 (Gate) due to another Blocker or PolicyDenied.
	DecisionSuppressed
)

// String returns the canonical name of DecisionKind.
func (d DecisionKind) String() string {
	switch d {
	case DecisionNoOp:
		return "NoOp"
	case DecisionDeferred:
		return "Deferred"
	case DecisionAuthorized:
		return "Authorized"
	case DecisionPolicyDenied:
		return "PolicyDenied"
	case DecisionBlocker:
		return "Blocker"
	case DecisionSuppressed:
		return "Suppressed"
	default:
		return "InvalidDecision"
	}
}

// AuthorizedPath holds the Stage 4 (Authorize) result for a single path.
type AuthorizedPath struct {
	Selected     SelectedPath
	Decision     DecisionKind
	GrantedPerms scope.PermissionSet
	Diagnostics  []model.Diagnostic
	PrimaryCode  model.DiagnosticCode
}

// Clone returns a deep defensive copy of a.
func (a AuthorizedPath) Clone() AuthorizedPath {
	return AuthorizedPath{
		Selected:     a.Selected.Clone(),
		Decision:     a.Decision,
		GrantedPerms: a.GrantedPerms,
		Diagnostics:  slices.Clone(a.Diagnostics),
		PrimaryCode:  a.PrimaryCode,
	}
}

// Authorize executes Stage 4 of the pure planning pipeline across all selected paths (ADR 0003 §1 item 4, Counterexample #39).
//
// Invariants enforced by Authorize:
//   - Only SelectCandidate paths are checked against Scope(p).
//   - SelectNoOp, SelectDeferred, and SelectBlocker never demand execution permissions and never emit spurious K401 PolicyDenied.
//   - Existing Blocker diagnostics (K201–K302) are never overwritten by K401 PolicyDenied.
func Authorize(selected []SelectedPath, sc scope.Scope) ([]AuthorizedPath, error) {
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, nil
	}
	out := make([]AuthorizedPath, len(selected))
	for i, sp := range selected {
		out[i] = AuthorizePath(sp, sc)
	}
	return out, nil
}

// AuthorizePath evaluates Scope authorization for a single SelectedPath.
func AuthorizePath(sp SelectedPath, sc scope.Scope) AuthorizedPath {
	granted := sc.PermissionsFor(sp.Classified.Path)
	out := AuthorizedPath{
		Selected:     sp.Clone(),
		GrantedPerms: granted,
		Diagnostics:  adjustDiagnosticsForScope(sp.Diagnostics, granted),
		PrimaryCode:  sp.PrimaryCode,
	}

	switch sp.Kind {
	case SelectNoOp:
		out.Decision = DecisionNoOp
		return out
	case SelectDeferred:
		out.Decision = DecisionDeferred
		return out
	case SelectBlocker:
		out.Decision = DecisionBlocker
		return out
	case SelectCandidate:
		if err := sc.AuthorizeAction(sp.Classified.Path, sp.Action); err != nil {
			diag := err.(model.Diagnostic)
			out.Decision = DecisionPolicyDenied
			out.Diagnostics = append(out.Diagnostics, diag)
			model.SortDiagnostics(out.Diagnostics)
			out.PrimaryCode = model.CodePolicyDenied
			return out
		}
		out.Decision = DecisionAuthorized
		return out
	default:
		out.Decision = DecisionBlocker
		return out
	}
}

func adjustDiagnosticsForScope(diags []model.Diagnostic, granted scope.PermissionSet) []model.Diagnostic {
	if len(diags) == 0 {
		return nil
	}
	out := slices.Clone(diags)
	permSetOwn := scope.NewPermissionSet(scope.PermSet, scope.PermOwn)
	permOwnResolve := scope.NewPermissionSet(scope.PermOwn, scope.PermResolve)
	for i := range out {
		switch out[i].NextAction {
		case NextActionAdoptThenApply:
			if !granted.Has(permSetOwn) {
				out[i].NextAction = NextActionOutsideScope
			} else if !granted.Has(permOwnResolve) {
				out[i].NextAction = NextActionAdoptRequiresResolve
			}
		case NextActionReleaseThenApply:
			if !granted.Has(permSetOwn) {
				out[i].NextAction = NextActionOutsideScope
			} else if !granted.Has(permOwnResolve) {
				out[i].NextAction = NextActionReleaseRequiresResolve
			}
		case NextActionRelease:
			if !granted.Has(permOwnResolve) {
				out[i].NextAction = NextActionRetiredReleaseRequiresResolve
			}
		}
	}
	return out
}
