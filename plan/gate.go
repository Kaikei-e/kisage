package plan

import (
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/scope"
)

// GateOutcome holds the Stage 5 (Gate) all-or-nothing evaluation result (ADR 0003 §1 item 5, Counterexamples #31, #48).
type GateOutcome struct {
	Blocked            bool
	Changes            []EnvironmentOp
	MetadataCandidates []MetadataCandidate
	OwnershipUpdates   []OwnershipUpdate
	Suppressed         []PathResult
	DeferredActions    []DeferredAction
	PathResults        []PathResult
	Diagnostics        []model.Diagnostic
}

// HasWork reports whether the gate produced at least one executable environment change or metadata update.
func (g GateOutcome) HasWork() bool {
	return !g.Blocked && (len(g.Changes) > 0 || len(g.MetadataCandidates) > 0)
}

// RequiresDurableIntent reports whether DurableIntent / Pending persistence is needed (true only when Changes is non-empty, ADR 0004 §1, Theorem 11).
func (g GateOutcome) RequiresDurableIntent() bool {
	return !g.Blocked && len(g.Changes) > 0
}

// RequiresApplyBatch reports whether Provider.ApplyBatch is needed (true only when Changes is non-empty, ADR 0004 §2, Theorem 11).
func (g GateOutcome) RequiresApplyBatch() bool {
	return !g.Blocked && len(g.Changes) > 0
}

// RequiresStabilize reports whether Provider.Stabilize is needed before committing ownership updates (ADR 0004 §2).
func (g GateOutcome) RequiresStabilize() bool {
	return g.HasWork()
}

// Gate executes Stage 5 of the pure planning pipeline (ADR 0003 §1 item 5).
// If any Blocker or PolicyDenied exists in authorized (or if unresolvedRecovery or quarantined is true),
// all Changes, MetadataCandidates, and OwnershipUpdates are suppressed and Authorized candidates become DecisionSuppressed.
func Gate(
	authorized []AuthorizedPath,
	actor ownership.ActorID,
	unresolvedRecovery bool,
	quarantined bool,
) GateOutcome {
	blocked := quarantined || unresolvedRecovery || hasModeBlocker(authorized)
	var out GateOutcome
	out.Blocked = blocked

	for _, ap := range authorized {
		cp := ap.Selected.Classified
		pr := PathResult{
			Path:          cp.Path.Clone(),
			Membership:    cp.Membership,
			StateClass:    cp.StateClass,
			Selection:     ap.Selected.Kind,
			Decision:      ap.Decision,
			Action:        ap.Selected.Action,
			RequiredPerms: ap.Selected.RequiredPerms,
			GrantedPerms:  ap.GrantedPerms,
			PrimaryCode:   ap.PrimaryCode,
			Diagnostics:   slices.Clone(ap.Diagnostics),
		}
		out.Diagnostics = append(out.Diagnostics, ap.Diagnostics...)

		if ap.Decision == DecisionDeferred {
			out.DeferredActions = append(out.DeferredActions, DeferredAction{
				Path:        cp.Path.Clone(),
				Membership:  cp.Membership,
				StateClass:  cp.StateClass,
				Action:      ap.Selected.Action,
				ReasonCode:  ap.PrimaryCode,
				Diagnostics: slices.Clone(ap.Diagnostics),
			})
		} else if ap.Decision == DecisionAuthorized {
			if blocked {
				pr.Decision = DecisionSuppressed
				out.Suppressed = append(out.Suppressed, pr)
			} else {
				appendAuthorizedCandidate(&out, ap, actor)
			}
		}
		out.PathResults = append(out.PathResults, pr)
	}
	model.SortDiagnostics(out.Diagnostics)
	return out
}

func hasModeBlocker(authorized []AuthorizedPath) bool {
	for _, ap := range authorized {
		if ap.Decision == DecisionBlocker || ap.Decision == DecisionPolicyDenied {
			return true
		}
	}
	return false
}

func appendAuthorizedCandidate(out *GateOutcome, ap AuthorizedPath, actor ownership.ActorID) {
	sp := ap.Selected
	cp := sp.Classified
	nextRev := cp.CurrentRevision + 1

	switch sp.Action {
	case scope.ActionSet:
		appendSetCandidate(out, sp, cp, actor, nextRev)
	case scope.ActionDelete:
		appendDeleteCandidate(out, sp, cp)
	case scope.ActionAdoptModified:
		after, _ := ownership.NewSettledModified(actor, cp.Desired, nextRev)
		appendMetadataCandidate(out, sp, cp, UpdateAdoptModified, &after)
	case scope.ActionUnmanage:
		appendMetadataCandidate(out, sp, cp, UpdateUnmanage, nil)
	case scope.ActionClearRecord:
		appendMetadataCandidate(out, sp, cp, UpdateClearAbsentRecord, nil)
	}
}

func appendSetCandidate(out *GateOutcome, sp SelectedPath, cp ClassifiedPath, actor ownership.ActorID, nextRev uint64) {
	// Never generate a Set where Expected equals Present(Leaf(Target)) (ADR 0003 §1).
	if v, ok := cp.Expected.LeafValue(); ok && model.EqualLeafValues(v, cp.Desired) {
		return
	}
	var prev *ownership.SettledState
	var after ownership.OwnerEntry
	kind := UpdateCreate
	if cp.SelfEntry != nil {
		prev = &ownership.SettledState{
			Origin:      cp.SelfEntry.Origin,
			LastApplied: model.CloneLeafValue(cp.SelfEntry.LastApplied),
		}
		kind = UpdateModifyValue
		after = ownership.OwnerEntry{
			Manager:     actor,
			Origin:      cp.SelfEntry.Origin,
			LastApplied: model.CloneLeafValue(cp.Desired),
			Revision:    nextRev,
		}
	} else {
		after, _ = ownership.NewSettledCreated(actor, cp.Desired, nextRev)
	}
	out.Changes = append(out.Changes, EnvironmentOp{
		Path:          cp.Path.Clone(),
		Op:            ownership.PendingOpSet,
		Action:        scope.ActionSet,
		RequiredPerms: sp.RequiredPerms,
		Expected:      cp.Expected,
		Target:        model.CloneLeafValue(cp.Desired),
		Previous:      prev,
	})
	out.OwnershipUpdates = append(out.OwnershipUpdates, OwnershipUpdate{
		Path:             cp.Path.Clone(),
		Kind:             kind,
		Action:           scope.ActionSet,
		IsMetadataOnly:   false,
		ExpectedObserved: cp.Expected,
		ExpectedRevision: cp.CurrentRevision,
		Before:           cloneEntryPtr(cp.SelfEntry),
		After:            &after,
	})
}

func appendDeleteCandidate(out *GateOutcome, sp SelectedPath, cp ClassifiedPath) {
	prev := &ownership.SettledState{
		Origin:      cp.SelfEntry.Origin,
		LastApplied: model.CloneLeafValue(cp.SelfEntry.LastApplied),
	}
	out.Changes = append(out.Changes, EnvironmentOp{
		Path:          cp.Path.Clone(),
		Op:            ownership.PendingOpDelete,
		Action:        scope.ActionDelete,
		RequiredPerms: sp.RequiredPerms,
		Expected:      cp.Expected,
		Previous:      prev,
	})
	out.OwnershipUpdates = append(out.OwnershipUpdates, OwnershipUpdate{
		Path:             cp.Path.Clone(),
		Kind:             UpdateDeleteCreated,
		Action:           scope.ActionDelete,
		IsMetadataOnly:   false,
		ExpectedObserved: cp.Expected,
		ExpectedRevision: cp.CurrentRevision,
		Before:           cloneEntryPtr(cp.SelfEntry),
		After:            nil,
	})
}

func appendMetadataCandidate(
	out *GateOutcome,
	sp SelectedPath,
	cp ClassifiedPath,
	kind OwnershipUpdateKind,
	after *ownership.OwnerEntry,
) {
	out.MetadataCandidates = append(out.MetadataCandidates, MetadataCandidate{
		Path:             cp.Path.Clone(),
		Action:           sp.Action,
		RequiredPerms:    sp.RequiredPerms,
		ExpectedObserved: cp.Expected,
		ExpectedRevision: cp.CurrentRevision,
		Before:           cloneEntryPtr(cp.SelfEntry),
		After:            cloneEntryPtr(after),
	})
	out.OwnershipUpdates = append(out.OwnershipUpdates, OwnershipUpdate{
		Path:             cp.Path.Clone(),
		Kind:             kind,
		Action:           sp.Action,
		IsMetadataOnly:   true,
		ExpectedObserved: cp.Expected,
		ExpectedRevision: cp.CurrentRevision,
		Before:           cloneEntryPtr(cp.SelfEntry),
		After:            cloneEntryPtr(after),
	})
}
