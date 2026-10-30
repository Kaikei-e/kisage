package plan

import (
	"math"
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/scope"
)

// RecoveryKind discriminates the classification of an active Actor's Pending entry during PureRecover (ADR 0003 §1, ADR 0004 §3).
type RecoveryKind uint8

const (
	// RecoveryInvalid is the zero value for an uninitialized RecoveryKind.
	RecoveryInvalid RecoveryKind = iota
	// RecoveryCommitSet commits a Pending Set because the observed value equals Target (downgrading Origin=None to Modified with K601).
	RecoveryCommitSet
	// RecoveryRollback reverts a Pending Set or Delete back to PreviousEntry (or deletes Origin=None) because Observed equals Expected.
	RecoveryRollback
	// RecoveryClearDelete removes the ownership record of a Pending Delete because Observed is confirmed Absent.
	RecoveryClearDelete
	// RecoveryRetainedUnknown retains Pending because target or a proper ancestor observation is Unknown (K301).
	RecoveryRetainedUnknown
	// RecoveryConflict retains Pending with K205 because a third leaf value, Record, Opaque, or ancestor conflict was observed.
	RecoveryConflict
	// RecoveryPolicyDenied retains Pending with K401 because the resolved recovery candidate lacked Own permission in Scope.
	RecoveryPolicyDenied
)

// String returns the canonical name of RecoveryKind.
func (k RecoveryKind) String() string {
	switch k {
	case RecoveryCommitSet:
		return "CommitSet"
	case RecoveryRollback:
		return "Rollback"
	case RecoveryClearDelete:
		return "ClearDelete"
	case RecoveryRetainedUnknown:
		return "RetainedUnknown"
	case RecoveryConflict:
		return "Conflict"
	case RecoveryPolicyDenied:
		return "PolicyDenied"
	default:
		return "InvalidRecovery"
	}
}

// RecoveryItem represents the pure recovery evaluation outcome for a single path p ∈ dom(σ_A) with Pending != nil.
type RecoveryItem struct {
	Path              model.Path
	Kind              RecoveryKind
	Resolved          bool
	Authorized        bool
	RequiresStabilize bool
	Persisted         bool
	Before            ownership.OwnerEntry
	After             *ownership.OwnerEntry
	Diagnostics       []model.Diagnostic
}

// Clone returns a deep defensive copy of r.
func (r RecoveryItem) Clone() RecoveryItem {
	return RecoveryItem{
		Path:              r.Path.Clone(),
		Kind:              r.Kind,
		Resolved:          r.Resolved,
		Authorized:        r.Authorized,
		RequiresStabilize: r.RequiresStabilize,
		Persisted:         r.Persisted,
		Before:            r.Before.Clone(),
		After:             cloneEntryPtr(r.After),
		Diagnostics:       slices.Clone(r.Diagnostics),
	}
}

// RecoveryReport holds the pure crash-recovery outcome for σ_A (ADR 0003 §1, ADR 0004 §3, Counterexamples #6, #7, #23–#25, #40, #41).
//
// PureRecover never performs disk I/O or fsync. Only Own-authorized recovery candidates are
// reflected in VirtualStore; rejected or unresolved Pending entries remain in VirtualStore.
type RecoveryReport struct {
	Items             []RecoveryItem
	Candidates        []RecoveryItem
	Unresolved        []RecoveryItem
	VirtualStore      *ownership.Store
	RequiresStabilize bool
	Persisted         bool
	Diagnostics       []model.Diagnostic
}

// HasCandidates reports whether at least one authorized recovery update is proposed.
func (r *RecoveryReport) HasCandidates() bool {
	return r != nil && len(r.Candidates) > 0
}

// HasUnresolved reports whether at least one self-Pending entry could not be resolved and authorized.
func (r *RecoveryReport) HasUnresolved() bool {
	return r != nil && len(r.Unresolved) > 0
}

// PureRecover evaluates all Pending entries belonging to actor in store(resID) against snap and sc (ADR 0003 §1, ADR 0004 §3).
// Foreign Pending entries (Manager != actor) are never inspected or modified here (Counterexample #40).
func PureRecover(
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	snap *observation.Snapshot,
	sc scope.Scope,
) (*RecoveryReport, error) {
	if err := validateRecoverInputs(store, resID, actor, snap, sc); err != nil {
		return nil, err
	}
	sigmaA, err := ownership.ProjectActor(store, resID, actor)
	if err != nil {
		return nil, err
	}
	virtual := store.Clone()
	pendingPaths := sigmaA.PendingPaths()
	if len(pendingPaths) == 0 {
		return &RecoveryReport{VirtualStore: virtual}, nil
	}

	report := &RecoveryReport{VirtualStore: virtual}
	for _, p := range pendingPaths {
		entry, _ := sigmaA.Get(p)
		curRev := virtual.Revision(resID, p)
		if curRev == math.MaxUint64 {
			return nil, model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Path:   p.Clone(),
				Reason: "ownership revision exhausted: cannot advance beyond uint64 maximum",
			}
		}
		nextRev := curRev + 1
		item, err := classifyPendingEntry(p, entry, snap, nextRev)
		if err != nil {
			return nil, err
		}
		item = authorizeRecoveryCandidate(item, sc)
		report.Items = append(report.Items, item)
		report.Diagnostics = append(report.Diagnostics, item.Diagnostics...)
		if item.Authorized {
			virtual, err = applyRecoveryItemToStore(virtual, resID, item)
			if err != nil {
				return nil, err
			}
			report.Candidates = append(report.Candidates, item)
		} else {
			report.Unresolved = append(report.Unresolved, item)
		}
	}
	report.VirtualStore = virtual
	report.RequiresStabilize = len(report.Candidates) > 0
	model.SortDiagnostics(report.Diagnostics)
	return report, nil
}

func validateRecoverInputs(
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	snap *observation.Snapshot,
	sc scope.Scope,
) error {
	if err := ownership.ValidateResourceID(resID); err != nil {
		return err
	}
	if err := ownership.ValidateActorID(actor); err != nil {
		return err
	}
	if snap == nil {
		return model.Diagnostic{Code: model.CodeInvalidInput, Reason: "observation snapshot must not be nil"}
	}
	if store != nil {
		if err := store.Validate(); err != nil {
			return err
		}
	}
	return sc.Validate()
}

func authorizeRecoveryCandidate(item RecoveryItem, sc scope.Scope) RecoveryItem {
	if !item.Resolved {
		return item
	}
	if err := sc.AuthorizeAction(item.Path, scope.ActionRecover); err != nil {
		diag := err.(model.Diagnostic)
		return RecoveryItem{
			Path:        item.Path.Clone(),
			Kind:        RecoveryPolicyDenied,
			Resolved:    false,
			Authorized:  false,
			Before:      item.Before.Clone(),
			Diagnostics: []model.Diagnostic{diag},
		}
	}
	item.Authorized = true
	item.RequiresStabilize = true
	return item
}

func applyRecoveryItemToStore(store *ownership.Store, resID ownership.ResourceID, item RecoveryItem) (*ownership.Store, error) {
	if item.After == nil {
		return store.Delete(resID, item.Path)
	}
	return store.Put(resID, item.Path, *item.After)
}
