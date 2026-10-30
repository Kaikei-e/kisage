package plan

import (
	"fmt"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/structure"
)

func classifyPendingEntry(
	p model.Path,
	entry ownership.OwnerEntry,
	snap *observation.Snapshot,
	nextRev uint64,
) (RecoveryItem, error) {
	check := structure.InspectPath(snap, p)
	// Rule 1: Target or required proper ancestor is Unknown -> retain Pending (ADR 0004 §3 rule 1).
	if check.HasUnknown() {
		return RecoveryItem{
			Path:        p.Clone(),
			Kind:        RecoveryRetainedUnknown,
			Before:      entry.Clone(),
			Diagnostics: filterUnknownDiagnostics(check.Diagnostics),
		}, nil
	}
	// Rule 6 (ancestor check): Any proper ancestor structural conflict (Leaf, Opaque, or non-monotonic Present under Absent)
	// is a PendingConflict (K205) (ADR 0004 §3 rule 6, Counterexample #24).
	if hasAncestorStructuralConflict(check) {
		return newPendingConflictItem(p, entry, fmt.Sprintf("ancestor structural conflict while recovering Pending %s on %s", entry.Pending.Op, p)), nil
	}

	if entry.Pending.Op == ownership.PendingOpSet {
		return classifyPendingSet(p, entry, check.Target, nextRev)
	}
	return classifyPendingDelete(p, entry, check.Target, nextRev)
}

func classifyPendingSet(
	p model.Path,
	entry ownership.OwnerEntry,
	targetObs observation.Observed,
	nextRev uint64,
) (RecoveryItem, error) {
	// Rule 2: Set and observed matches Target leaf value -> CommitRecoveredSet (with K601 if Origin==None).
	if leafVal, ok := targetObs.LeafValue(); ok && model.EqualLeafValues(leafVal, entry.Pending.Target) {
		settled, warn, err := ownership.CommitRecoveredSet(entry, p, nextRev)
		if err != nil {
			return RecoveryItem{}, err
		}
		var diags []model.Diagnostic
		if warn != nil {
			diags = append(diags, *warn)
		}
		return RecoveryItem{
			Path:        p.Clone(),
			Kind:        RecoveryCommitSet,
			Resolved:    true,
			Before:      entry.Clone(),
			After:       &settled,
			Diagnostics: diags,
		}, nil
	}
	// Rule 3: Set and observed matches Expected -> RollbackPending.
	if targetObs.Equal(entry.Pending.Expected) {
		restored, err := ownership.RollbackPending(entry, nextRev)
		if err != nil {
			return RecoveryItem{}, err
		}
		return RecoveryItem{
			Path:     p.Clone(),
			Kind:     RecoveryRollback,
			Resolved: true,
			Before:   entry.Clone(),
			After:    restored,
		}, nil
	}
	// Rule 6: Third leaf value, Record, or Opaque -> PendingConflict (K205).
	return newPendingConflictItem(
		p,
		entry,
		fmt.Sprintf("observed %s at %s matches neither Pending Set Target (%v) nor Expected (%s)", targetObs, p, entry.Pending.Target, entry.Pending.Expected),
	), nil
}

func classifyPendingDelete(
	p model.Path,
	entry ownership.OwnerEntry,
	targetObs observation.Observed,
	nextRev uint64,
) (RecoveryItem, error) {
	// Rule 4: Delete and observed is Absent -> Clear ownership record.
	if targetObs.IsAbsent() {
		return RecoveryItem{
			Path:     p.Clone(),
			Kind:     RecoveryClearDelete,
			Resolved: true,
			Before:   entry.Clone(),
			After:    nil,
		}, nil
	}
	// Rule 5: Delete and observed matches Expected (Present(Leaf(LastApplied))) -> RollbackPending.
	if targetObs.Equal(entry.Pending.Expected) {
		restored, err := ownership.RollbackPending(entry, nextRev)
		if err != nil {
			return RecoveryItem{}, err
		}
		return RecoveryItem{
			Path:     p.Clone(),
			Kind:     RecoveryRollback,
			Resolved: true,
			Before:   entry.Clone(),
			After:    restored,
		}, nil
	}
	// Rule 6: Third leaf value, Record, or Opaque -> PendingConflict (K205).
	return newPendingConflictItem(
		p,
		entry,
		fmt.Sprintf("observed %s at %s matches neither Absent nor Pending Delete Expected (%s)", targetObs, p, entry.Pending.Expected),
	), nil
}

func hasAncestorStructuralConflict(check structure.PathCheckResult) bool {
	seenAbsent := false
	for _, anc := range check.Ancestors {
		if anc.Observed.IsAbsent() {
			seenAbsent = true
			continue
		}
		if !anc.Observed.IsRecord() || seenAbsent {
			return true
		}
	}
	return seenAbsent && check.Target.IsPresent()
}

func filterUnknownDiagnostics(diags []model.Diagnostic) []model.Diagnostic {
	var out []model.Diagnostic
	for _, d := range diags {
		if d.Code == model.CodeObservationUnavailable {
			out = append(out, d)
		}
	}
	return out
}

func newPendingConflictItem(p model.Path, entry ownership.OwnerEntry, reason string) RecoveryItem {
	return RecoveryItem{
		Path:   p.Clone(),
		Kind:   RecoveryConflict,
		Before: entry.Clone(),
		Diagnostics: []model.Diagnostic{{
			Code:       model.CodePendingConflict,
			Path:       p.Clone(),
			Reason:     reason,
			NextAction: "inspect target environment state and complete administrator PendingConflict recovery",
		}},
	}
}
