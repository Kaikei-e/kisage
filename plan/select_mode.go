package plan

import (
	"fmt"
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/scope"
)

// SelectionKind represents the mode-specific classification produced by Stage 3 (SelectMode) (ADR 0003 §1):
//
//	SelectMode(StateClass, Mode) → NoOp | Deferred | Candidate(action, perms) | Blocker
type SelectionKind uint8

const (
	// SelectInvalid is the zero value for an uninitialized SelectionKind.
	SelectInvalid SelectionKind = iota
	// SelectNoOp indicates the path is already converged and requires no action or permissions.
	SelectNoOp
	// SelectDeferred indicates the path's action or independent out-of-mode anomaly is deferred in the current Mode.
	SelectDeferred
	// SelectCandidate indicates the path is an active execution candidate in the current Mode and requires Scope authorization.
	SelectCandidate
	// SelectBlocker indicates the path is an in-mode blocker (or global MigrationRequired / Pending blocker) that halts normal execution.
	SelectBlocker
)

// String returns the canonical name of SelectionKind.
func (k SelectionKind) String() string {
	switch k {
	case SelectNoOp:
		return "NoOp"
	case SelectDeferred:
		return "Deferred"
	case SelectCandidate:
		return "Candidate"
	case SelectBlocker:
		return "Blocker"
	default:
		return "InvalidSelection"
	}
}

// SelectedPath holds the Stage 3 (SelectMode) result for a single path in a given Mode.
type SelectedPath struct {
	Classified    ClassifiedPath
	Mode          Mode
	Kind          SelectionKind
	Action        scope.ActionKind
	RequiredPerms scope.PermissionSet
	Diagnostics   []model.Diagnostic
	PrimaryCode   model.DiagnosticCode
}

// Clone returns a deep defensive copy of s.
func (s SelectedPath) Clone() SelectedPath {
	return SelectedPath{
		Classified:    s.Classified.Clone(),
		Mode:          s.Mode,
		Kind:          s.Kind,
		Action:        s.Action,
		RequiredPerms: s.RequiredPerms,
		Diagnostics:   slices.Clone(s.Diagnostics),
		PrimaryCode:   s.PrimaryCode,
	}
}

// SelectMode executes Stage 3 of the pure planning pipeline across all classified paths for mode (ADR 0003 §1 item 3).
func SelectMode(classified []ClassifiedPath, mode Mode) ([]SelectedPath, error) {
	if err := validateMode(mode); err != nil {
		return nil, err
	}
	if len(classified) == 0 {
		return nil, nil
	}
	out := make([]SelectedPath, len(classified))
	for i, cp := range classified {
		out[i] = SelectModeForPath(cp, mode)
	}
	return out, nil
}

// SelectModeForPath implements the 11-row normative SelectMode table in ADR 0003 §1 item 3.
func SelectModeForPath(cp ClassifiedPath, mode Mode) SelectedPath {
	base := SelectedPath{
		Classified:  cp.Clone(),
		Mode:        mode,
		Diagnostics: slices.Clone(cp.Diagnostics),
		PrimaryCode: cp.PrimaryCode,
	}

	switch cp.StateClass {
	case StateConverged:
		// Row 1: Declared, fully converged -> NoOp in both Apply and Prune.
		base.Kind = SelectNoOp
		return base

	case StateDeclaredSet:
		// Row 2: Declared, Set -> Candidate(Set, Set+Own) in Apply; Deferred in Prune.
		return selectForCandidateOrDeferred(base, mode, ModeApply, scope.ActionSet)

	case StateDeclaredAdoptModified:
		// Row 3: Declared, matching unowned value -> Candidate(AdoptModified, Own) in Apply; Deferred in Prune.
		return selectForCandidateOrDeferred(base, mode, ModeApply, scope.ActionAdoptModified)

	case StateDeclaredConflict:
		// Row 4: Declared, anomaly/conflict -> Blocker in Apply; Deferred in Prune (MigrationRequired handled in Row 11).
		if mode == ModeApply {
			base.Kind = SelectBlocker
		} else {
			base.Kind = SelectDeferred
		}
		return base

	case StateDeferredUntilRelease:
		// Row 5: Declared, waiting for retired Modified release -> DeferredUntilRelease in both modes.
		base.Kind = SelectDeferred
		return base

	case StateRetiredModifiedKnown:
		// Row 6: Retired Modified, known target & ancestors -> Candidate(Unmanage, Own) in Apply; Deferred in Prune.
		return selectForCandidateOrDeferred(base, mode, ModeApply, scope.ActionUnmanage)

	case StateRetiredModifiedUnknown:
		// Row 7: Retired Modified, Unknown target or ancestor -> Blocker in Apply; Deferred in Prune.
		if mode == ModeApply {
			base.Kind = SelectBlocker
		} else {
			base.Kind = SelectDeferred
		}
		return base

	case StateRetiredCreatedAbsent:
		// Row 8: Retired Created, normal Absent -> Candidate(ClearRecord, Own) in both Apply and Prune.
		perms, _ := scope.RequiredPermissions(scope.ActionClearRecord)
		base.Kind = SelectCandidate
		base.Action = scope.ActionClearRecord
		base.RequiredPerms = perms
		return base

	case StateRetiredCreatedPrunable:
		// Row 9: Retired Created, Leaf(LastApplied) -> Deferred(PruneRequired, K604) in Apply; Candidate(Delete, Delete+Own) in Prune.
		if mode == ModeApply {
			pruneDiag := model.Diagnostic{
				Code:       model.CodePruneRequired,
				Path:       cp.Path.Clone(),
				Reason:     fmt.Sprintf("retired Origin=Created leaf %s remains in environment and requires prune for deletion", cp.Path),
				NextAction: NextActionPrune,
			}
			base.Kind = SelectDeferred
			base.Action = scope.ActionDelete
			base.Diagnostics = append(base.Diagnostics, pruneDiag)
			model.SortDiagnostics(base.Diagnostics)
			base.PrimaryCode = model.CodePruneRequired
			return base
		}
		perms, _ := scope.RequiredPermissions(scope.ActionDelete)
		base.Kind = SelectCandidate
		base.Action = scope.ActionDelete
		base.RequiredPerms = perms
		return base

	case StateRetiredCreatedAnomaly:
		// Row 10: Independent retired Created, anomaly (drift, structure conflict, Unknown) -> Deferred in Apply; Blocker in Prune.
		if mode == ModeApply {
			base.Kind = SelectDeferred
		} else {
			base.Kind = SelectBlocker
		}
		return base

	case StateMigrationRequired, StateUnresolvedPending:
		// Row 11: Retired Created + declared overlap (MigrationRequired) or unresolved self-Pending -> Blocker in both modes.
		base.Kind = SelectBlocker
		return base

	default:
		base.Kind = SelectBlocker
		return base
	}
}

func selectForCandidateOrDeferred(base SelectedPath, currentMode, candidateMode Mode, action scope.ActionKind) SelectedPath {
	base.Action = action
	if currentMode == candidateMode {
		perms, _ := scope.RequiredPermissions(action)
		base.Kind = SelectCandidate
		base.RequiredPerms = perms
		return base
	}
	base.Kind = SelectDeferred
	base.RequiredPerms = scope.PermNone
	return base
}
