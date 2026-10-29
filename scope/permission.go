package scope

import (
	"fmt"
	"strings"
)

// Permission represents one of the 4 operation permissions in kisage (ADR 0002 §3):
// Set, Delete, Own, and Resolve.
type Permission uint8

const (
	// PermSet authorizes creating or updating leaf values in the target environment (requires PermOwn).
	PermSet Permission = 1 << iota
	// PermDelete authorizes deleting retired Created leaf values in the target environment (requires PermOwn).
	PermDelete
	// PermOwn authorizes acquiring, updating, or releasing ownership records in σ.
	PermOwn
	// PermResolve authorizes explicit conflict/drift resolution commands (adopt and release).
	// PermResolve is never derived from PermOwn, PermSet, or PermDelete (ADR 0002 §3).
	PermResolve

	permAllMask Permission = PermSet | PermDelete | PermOwn | PermResolve
)

// PermissionSet represents a bitmask set of Permission values.
type PermissionSet uint8

const (
	// PermNone is the empty permission set.
	PermNone PermissionSet = 0
	// PermRegular combines the normal reconciliation permissions (Set, Delete, Own) without Resolve.
	PermRegular PermissionSet = PermissionSet(PermSet | PermDelete | PermOwn)
	// PermAll combines all 4 permissions including Resolve.
	PermAll PermissionSet = PermissionSet(permAllMask)
)

// NewPermissionSet constructs a PermissionSet from zero or more individual Permission values.
func NewPermissionSet(perms ...Permission) PermissionSet {
	var set PermissionSet
	for _, p := range perms {
		set |= PermissionSet(p)
	}
	return set
}

// String returns the canonical name of a single Permission.
func (p Permission) String() string {
	switch p {
	case PermSet:
		return "Set"
	case PermDelete:
		return "Delete"
	case PermOwn:
		return "Own"
	case PermResolve:
		return "Resolve"
	default:
		return "InvalidPermission"
	}
}

// IsValid reports whether p is one of the 4 defined single permissions.
func (p Permission) IsValid() bool {
	return p == PermSet || p == PermDelete || p == PermOwn || p == PermResolve
}

// IsValid reports whether s contains only defined permission bits.
func (s PermissionSet) IsValid() bool {
	return s&^PermAll == 0
}

// IsEmpty reports whether s contains no permissions.
func (s PermissionSet) IsEmpty() bool {
	return s == PermNone
}

// Has reports whether s contains every permission bit in required.
func (s PermissionSet) Has(required PermissionSet) bool {
	if !s.IsValid() || !required.IsValid() {
		return false
	}
	return (s & required) == required
}

// Contains reports whether s includes the single permission p.
func (s PermissionSet) Contains(p Permission) bool {
	return p.IsValid() && s.Has(PermissionSet(p))
}

// Union returns the union of s and other.
func (s PermissionSet) Union(other PermissionSet) PermissionSet {
	return (s | other) & PermAll
}

// Missing returns the permissions in required that are not present in s.
func (s PermissionSet) Missing(required PermissionSet) PermissionSet {
	return (required & PermAll) &^ (s & PermAll)
}

// List returns the individual permissions in s in canonical order (Set, Delete, Own, Resolve).
func (s PermissionSet) List() []Permission {
	var out []Permission
	for _, p := range []Permission{PermSet, PermDelete, PermOwn, PermResolve} {
		if s.Contains(p) {
			out = append(out, p)
		}
	}
	return out
}

// String formats s as "+"-joined canonical permission names (e.g., "Set+Own", "Own+Resolve", or "None").
func (s PermissionSet) String() string {
	perms := s.List()
	if len(perms) == 0 {
		return "None"
	}
	names := make([]string, len(perms))
	for i, p := range perms {
		names[i] = p.String()
	}
	return strings.Join(names, "+")
}

// ActionKind enumerates the candidate and explicit resolution operations whose required
// permissions are governed by ADR 0002 §3.
type ActionKind uint8

const (
	// ActionInvalid is the zero value for an uninitialized ActionKind.
	ActionInvalid ActionKind = iota
	// ActionSet creates an Absent leaf or updates an owned leaf value (requires Set+Own).
	ActionSet
	// ActionAdoptModified adopts an existing unowned leaf whose value equals the declared value (requires Own).
	ActionAdoptModified
	// ActionUnmanage removes ownership of a retired Modified leaf without mutating the environment (requires Own).
	ActionUnmanage
	// ActionClearRecord removes ownership of a retired Created leaf confirmed to be Absent (requires Own).
	ActionClearRecord
	// ActionRecover persists a resolved crash-recovery update for the active Actor's Pending entry (requires Own).
	ActionRecover
	// ActionDelete deletes a retired Created leaf matching LastApplied during prune (requires Delete+Own).
	ActionDelete
	// ActionExplicitAdopt explicitly adopts an existing leaf as Modified via `kisage adopt` (requires Own+Resolve).
	ActionExplicitAdopt
	// ActionExplicitRelease explicitly releases a self-owned entry via `kisage release` (requires Own+Resolve).
	ActionExplicitRelease
)

// String returns the canonical name of the ActionKind.
func (a ActionKind) String() string {
	switch a {
	case ActionSet:
		return "Set"
	case ActionAdoptModified:
		return "AdoptModified"
	case ActionUnmanage:
		return "Unmanage"
	case ActionClearRecord:
		return "ClearRecord"
	case ActionRecover:
		return "Recover"
	case ActionDelete:
		return "Delete"
	case ActionExplicitAdopt:
		return "ExplicitAdopt"
	case ActionExplicitRelease:
		return "ExplicitRelease"
	default:
		return "InvalidAction"
	}
}

// RequiredPermissions returns the normative permission set required for action (ADR 0002 §3).
func RequiredPermissions(action ActionKind) (PermissionSet, error) {
	switch action {
	case ActionSet:
		return NewPermissionSet(PermSet, PermOwn), nil
	case ActionAdoptModified, ActionUnmanage, ActionClearRecord, ActionRecover:
		return NewPermissionSet(PermOwn), nil
	case ActionDelete:
		return NewPermissionSet(PermDelete, PermOwn), nil
	case ActionExplicitAdopt, ActionExplicitRelease:
		return NewPermissionSet(PermOwn, PermResolve), nil
	default:
		return PermNone, fmt.Errorf("unknown action kind: %d", action)
	}
}
