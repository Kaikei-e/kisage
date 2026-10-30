package plan

import (
	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/scope"
)

// EnvironmentOp represents a conditional environment change (Set or Delete) in Changes / EnvironmentOps (ADR 0003 §1, ADR 0004 §1).
//
// Invariants:
//   - Op == PendingOpSet has Expected == Absent (for unowned creation) or Expected == Present(Leaf(LastApplied))
//     with Target != Expected (no no-op Set is ever generated).
//   - Op == PendingOpDelete is generated only in ModePrune for retired Origin=Created leaves with Expected == Present(Leaf(LastApplied)).
type EnvironmentOp struct {
	Path          model.Path
	Op            ownership.PendingOp
	Action        scope.ActionKind
	RequiredPerms scope.PermissionSet
	Expected      observation.Observed
	Target        lkal.Value
	Previous      *ownership.SettledState
}

// Clone returns a deep defensive copy of op.
func (op EnvironmentOp) Clone() EnvironmentOp {
	var prev *ownership.SettledState
	if op.Previous != nil {
		c := op.Previous.Clone()
		prev = &c
	}
	return EnvironmentOp{
		Path:          op.Path.Clone(),
		Op:            op.Op,
		Action:        op.Action,
		RequiredPerms: op.RequiredPerms,
		Expected:      op.Expected,
		Target:        model.CloneLeafValue(op.Target),
		Previous:      prev,
	}
}

// MetadataCandidate represents a metadata-only ownership update (AdoptModified, Unmanage, or ClearRecord)
// that never mutates the target environment and never creates a Pending intent (ADR 0003 §1, ADR 0004 §1, Theorem 11).
type MetadataCandidate struct {
	Path             model.Path
	Action           scope.ActionKind
	RequiredPerms    scope.PermissionSet
	ExpectedObserved observation.Observed
	ExpectedRevision uint64
	Before           *ownership.OwnerEntry
	After            *ownership.OwnerEntry
}

// Clone returns a deep defensive copy of m.
func (m MetadataCandidate) Clone() MetadataCandidate {
	return MetadataCandidate{
		Path:             m.Path.Clone(),
		Action:           m.Action,
		RequiredPerms:    m.RequiredPerms,
		ExpectedObserved: m.ExpectedObserved,
		ExpectedRevision: m.ExpectedRevision,
		Before:           cloneEntryPtr(m.Before),
		After:            cloneEntryPtr(m.After),
	}
}

// OwnershipUpdateKind discriminates the planned ownership transition for a path in σ.
type OwnershipUpdateKind uint8

const (
	// UpdateInvalid is the zero value for an uninitialized OwnershipUpdateKind.
	UpdateInvalid OwnershipUpdateKind = iota
	// UpdateCreate creates a new Origin=Created record after confirmed Absent -> Present Set.
	UpdateCreate
	// UpdateModifyValue updates LastApplied while preserving existing settled Origin (Created or Modified).
	UpdateModifyValue
	// UpdateAdoptModified adopts an existing matching unowned value as Origin=Modified (metadata-only).
	UpdateAdoptModified
	// UpdateUnmanage removes a retired Origin=Modified record without touching the environment (metadata-only).
	UpdateUnmanage
	// UpdateClearAbsentRecord removes a retired Origin=Created record confirmed Absent (metadata-only).
	UpdateClearAbsentRecord
	// UpdateDeleteCreated removes a retired Origin=Created record after confirmed Prune Delete.
	UpdateDeleteCreated
)

// String returns the canonical name of OwnershipUpdateKind.
func (k OwnershipUpdateKind) String() string {
	switch k {
	case UpdateCreate:
		return "Create"
	case UpdateModifyValue:
		return "ModifyValue"
	case UpdateAdoptModified:
		return "AdoptModified"
	case UpdateUnmanage:
		return "Unmanage"
	case UpdateClearAbsentRecord:
		return "ClearAbsentRecord"
	case UpdateDeleteCreated:
		return "DeleteCreated"
	default:
		return "InvalidOwnershipUpdate"
	}
}

// OwnershipUpdate describes a planned update to σ (either paired with an EnvironmentOp or a MetadataCandidate).
type OwnershipUpdate struct {
	Path             model.Path
	Kind             OwnershipUpdateKind
	Action           scope.ActionKind
	IsMetadataOnly   bool
	ExpectedObserved observation.Observed
	ExpectedRevision uint64
	Before           *ownership.OwnerEntry
	After            *ownership.OwnerEntry
}

// Clone returns a deep defensive copy of u.
func (u OwnershipUpdate) Clone() OwnershipUpdate {
	return OwnershipUpdate{
		Path:             u.Path.Clone(),
		Kind:             u.Kind,
		Action:           u.Action,
		IsMetadataOnly:   u.IsMetadataOnly,
		ExpectedObserved: u.ExpectedObserved,
		ExpectedRevision: u.ExpectedRevision,
		Before:           cloneEntryPtr(u.Before),
		After:            cloneEntryPtr(u.After),
	}
}

// DeferredAction records an out-of-mode candidate, out-of-mode anomaly, PruneRequired, or DeferredUntilRelease item.
type DeferredAction struct {
	Path        model.Path
	Membership  Membership
	StateClass  StateClass
	Action      scope.ActionKind
	ReasonCode  model.DiagnosticCode
	Diagnostics []model.Diagnostic
}

// PathResult summarizes the 5-stage pipeline outcome for a single path p ∈ M_A(d, σ_virtual).
type PathResult struct {
	Path          model.Path
	Membership    Membership
	StateClass    StateClass
	Selection     SelectionKind
	Decision      DecisionKind
	Action        scope.ActionKind
	RequiredPerms scope.PermissionSet
	GrantedPerms  scope.PermissionSet
	PrimaryCode   model.DiagnosticCode
	Diagnostics   []model.Diagnostic
}

func cloneEntryPtr(e *ownership.OwnerEntry) *ownership.OwnerEntry {
	if e == nil {
		return nil
	}
	c := e.Clone()
	return &c
}
