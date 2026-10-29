package ownership

import (
	"fmt"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
)

// BeginSetUpdate attaches a Pending Set intent to a settled OwnerEntry without altering its settled Origin or LastApplied.
func BeginSetUpdate(entry OwnerEntry, batchID string, target lkal.Value, rev uint64) (OwnerEntry, error) {
	entry, err := entry.Normalize()
	if err != nil {
		return OwnerEntry{}, err
	}
	if !entry.IsSettled() {
		return OwnerEntry{}, model.Diagnostic{
			Code:   model.CodePendingConflict,
			Reason: "cannot begin Set update on an entry with an active Pending intent",
		}
	}
	normTarget, err := model.ValidateLeafValue(target)
	if err != nil {
		return OwnerEntry{}, err
	}
	expectedObs, err := observation.PresentLeaf(entry.LastApplied)
	if err != nil {
		return OwnerEntry{}, err
	}
	prev := SettledState{
		Origin:      entry.Origin,
		LastApplied: model.CloneLeafValue(entry.LastApplied),
	}
	next := entry.Clone()
	next.Revision = rev
	next.Pending = &Pending{
		BatchID:  batchID,
		Op:       PendingOpSet,
		Expected: expectedObs,
		Target:   normTarget,
		Previous: &prev,
	}
	if err := next.Validate(); err != nil {
		return OwnerEntry{}, err
	}
	return next, nil
}

// BeginDelete attaches a Pending Delete intent to a settled Origin=Created OwnerEntry.
func BeginDelete(entry OwnerEntry, batchID string, rev uint64) (OwnerEntry, error) {
	entry, err := entry.Normalize()
	if err != nil {
		return OwnerEntry{}, err
	}
	if !entry.IsSettled() {
		return OwnerEntry{}, model.Diagnostic{
			Code:   model.CodePendingConflict,
			Reason: "cannot begin Delete on an entry with an active Pending intent",
		}
	}
	if entry.Origin != OriginCreated {
		return OwnerEntry{}, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("automatic Delete requires Origin=Created, got %s", entry.Origin.String()),
		}
	}
	expectedObs, err := observation.PresentLeaf(entry.LastApplied)
	if err != nil {
		return OwnerEntry{}, err
	}
	prev := SettledState{
		Origin:      entry.Origin,
		LastApplied: model.CloneLeafValue(entry.LastApplied),
	}
	next := entry.Clone()
	next.Revision = rev
	next.Pending = &Pending{
		BatchID:  batchID,
		Op:       PendingOpDelete,
		Expected: expectedObs,
		Previous: &prev,
	}
	if err := next.Validate(); err != nil {
		return OwnerEntry{}, err
	}
	return next, nil
}

// CommitConfirmedSet finalizes a Pending Set after in-lock verification succeeded (ADR 0002 §2, ADR 0004 §2):
//   - Origin=None transitions to Origin=Created (verified Absent -> Present creation).
//   - Existing Origin=Created or Origin=Modified is preserved across value updates (Counterexample #34).
func CommitConfirmedSet(entry OwnerEntry, rev uint64) (OwnerEntry, error) {
	entry, err := entry.Normalize()
	if err != nil {
		return OwnerEntry{}, err
	}
	if entry.Pending == nil || entry.Pending.Op != PendingOpSet {
		return OwnerEntry{}, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "CommitConfirmedSet requires an active Pending Set intent",
		}
	}
	nextOrigin := entry.Origin
	if nextOrigin == OriginNone {
		nextOrigin = OriginCreated
	}
	return OwnerEntry{
		Manager:     entry.Manager,
		Origin:      nextOrigin,
		LastApplied: model.CloneLeafValue(entry.Pending.Target),
		Revision:    rev,
	}, nil
}

// CommitRecoveredSet finalizes a Pending Set during crash recovery when Target is observed (ADR 0002 §2, ADR 0004 §3):
//   - If Origin==None, it conservatively downgrades to Origin=Modified and returns K601 (Counterexamples #6, #27).
//   - If Origin==Created or Origin==Modified, it preserves the existing Origin and updates LastApplied (Counterexample #7).
func CommitRecoveredSet(entry OwnerEntry, path model.Path, rev uint64) (OwnerEntry, *model.Diagnostic, error) {
	entry, err := entry.Normalize()
	if err != nil {
		return OwnerEntry{}, nil, err
	}
	if entry.Pending == nil || entry.Pending.Op != PendingOpSet {
		return OwnerEntry{}, nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   path.Clone(),
			Reason: "CommitRecoveredSet requires an active Pending Set intent",
		}
	}
	nextOrigin := entry.Origin
	var warning *model.Diagnostic
	if nextOrigin == OriginNone {
		nextOrigin = OriginModified
		diag := model.Diagnostic{
			Code:       model.CodeCreatedOriginUnverified,
			Path:       path.Clone(),
			Reason:     "recovered creation intent downgraded to Origin=Modified because initial creation attribution could not be verified after crash",
			NextAction: "future retirement of this leaf will unmanage the record rather than auto-deleting the environment value",
		}
		warning = &diag
	}
	settled := OwnerEntry{
		Manager:     entry.Manager,
		Origin:      nextOrigin,
		LastApplied: model.CloneLeafValue(entry.Pending.Target),
		Revision:    rev,
	}
	return settled, warning, nil
}

// RollbackPending reverts an entry with a Pending intent back to its PreviousEntry state
// (or nil if Origin==None had no prior settled ownership) (ADR 0004 §2–3, Counterexample #25).
func RollbackPending(entry OwnerEntry, rev uint64) (*OwnerEntry, error) {
	entry, err := entry.Normalize()
	if err != nil {
		return nil, err
	}
	if entry.Pending == nil {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "RollbackPending requires an active Pending intent",
		}
	}
	if entry.Origin == OriginNone {
		return nil, nil
	}
	restored := OwnerEntry{
		Manager:     entry.Manager,
		Origin:      entry.Pending.Previous.Origin,
		LastApplied: model.CloneLeafValue(entry.Pending.Previous.LastApplied),
		Revision:    rev,
	}
	return &restored, nil
}

// TransitionExplicitAdopt computes the new OwnerEntry for an explicit `adopt` command (ADR 0002 §2, ADR 0003 §3, Counterexamples #36, #38).
// It always sets Origin=Modified and emits K602 if an existing Origin=Created record was downgraded.
func TransitionExplicitAdopt(current *OwnerEntry, actor ActorID, path model.Path, val lkal.Value, rev uint64) (OwnerEntry, *model.Diagnostic, error) {
	if err := ValidateActorID(actor); err != nil {
		return OwnerEntry{}, nil, err
	}
	if err := model.ValidateLeafPath(path); err != nil {
		return OwnerEntry{}, nil, err
	}
	normVal, err := model.ValidateLeafValue(val)
	if err != nil {
		return OwnerEntry{}, nil, err.(model.Diagnostic).WithPath(path)
	}
	var notice *model.Diagnostic
	if current != nil {
		if err := current.Validate(); err != nil {
			return OwnerEntry{}, nil, err
		}
		if current.Manager != actor {
			return OwnerEntry{}, nil, model.Diagnostic{
				Code:       model.CodeOwnershipConflict,
				Path:       path.Clone(),
				Reason:     fmt.Sprintf("path %s is owned by actor %q, cannot be adopted by %q", path.String(), current.Manager, actor),
				NextAction: "coordinate with the owning actor to release ownership first",
			}
		}
		if current.HasPending() {
			return OwnerEntry{}, nil, model.Diagnostic{
				Code:       model.CodePendingConflict,
				Path:       path.Clone(),
				Reason:     fmt.Sprintf("path %s has an unresolved Pending operation and cannot be explicitly adopted", path.String()),
				NextAction: "complete crash recovery before invoking adopt",
			}
		}
		if current.Origin == OriginCreated {
			diag := model.Diagnostic{
				Code:   model.CodeExplicitDeletionRightWaived,
				Path:   path.Clone(),
				Reason: fmt.Sprintf("explicit adopt on %s downgraded Origin from Created to Modified; automatic deletion right waived", path.String()),
			}
			notice = &diag
		}
	}
	adopted, err := NewSettledModified(actor, normVal, rev)
	if err != nil {
		return OwnerEntry{}, nil, err
	}
	return adopted, notice, nil
}

// TransitionExplicitRelease validates that current can be explicitly released by actor via `release` (ADR 0002 §2, ADR 0003 §3, Counterexample #38).
// It emits K602 if the released entry had Origin=Created.
func TransitionExplicitRelease(current *OwnerEntry, actor ActorID, path model.Path) (*model.Diagnostic, error) {
	if err := ValidateActorID(actor); err != nil {
		return nil, err
	}
	if err := model.ValidateLeafPath(path); err != nil {
		return nil, err
	}
	if current == nil {
		return nil, model.Diagnostic{
			Code:       model.CodeOwnershipConflict,
			Path:       path.Clone(),
			Reason:     fmt.Sprintf("path %s is not owned by actor %q", path.String(), actor),
			NextAction: "inspect path ownership with explain before invoking release",
		}
	}
	if err := current.Validate(); err != nil {
		return nil, err
	}
	if current.Manager != actor {
		return nil, model.Diagnostic{
			Code:       model.CodeOwnershipConflict,
			Path:       path.Clone(),
			Reason:     fmt.Sprintf("path %s is owned by actor %q, cannot be released by %q", path.String(), current.Manager, actor),
			NextAction: "only the owning actor may release an ownership record",
		}
	}
	if current.HasPending() {
		return nil, model.Diagnostic{
			Code:       model.CodePendingConflict,
			Path:       path.Clone(),
			Reason:     fmt.Sprintf("path %s has an unresolved Pending operation and cannot be released", path.String()),
			NextAction: "complete crash recovery before invoking release",
		}
	}
	if current.Origin == OriginCreated {
		diag := model.Diagnostic{
			Code:   model.CodeExplicitDeletionRightWaived,
			Path:   path.Clone(),
			Reason: fmt.Sprintf("explicit release on %s waived Origin=Created automatic deletion right", path.String()),
		}
		return &diag, nil
	}
	return nil, nil
}
