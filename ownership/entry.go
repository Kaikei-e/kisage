package ownership

import (
	"fmt"
	"math"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
)

// OwnerEntry represents an ownership record in σ for a (ResourceID, Path) key (ADR 0002 §2):
//
//	OwnerEntry = { Manager, Origin, LastApplied?, Pending?, Revision }
type OwnerEntry struct {
	Manager     ActorID
	Origin      Origin
	LastApplied lkal.Value
	Pending     *Pending
	Revision    uint64
}

// Validate checks the ADR 0002 §2 structural invariants on a normalized copy of e:
//   - Manager is a valid non-empty ActorID.
//   - Origin is None, Created, or Modified.
//   - Origin=None is permitted only when Pending != nil (new creation intent) and LastApplied == nil.
//   - Settled origins (Created, Modified) require a valid LKAL leaf value in LastApplied.
//   - If Pending != nil, it satisfies all Pending invariants for e.Origin and e.LastApplied.
func (e OwnerEntry) Validate() error {
	_, err := e.Normalize()
	return err
}

// Normalize returns a validated, independent copy with canonical LKAL values in
// LastApplied, Pending.Target, and Pending.Previous.LastApplied. Validation and
// transitions use the same normalization so accepted programmatic values remain
// usable, without changing the caller's values or LKAL's Equal semantics.
func (e OwnerEntry) Normalize() (OwnerEntry, error) {
	next := e
	var err error
	if e.LastApplied != nil {
		next.LastApplied, err = model.ValidateLeafValue(e.LastApplied)
		if err != nil {
			return OwnerEntry{}, err
		}
	}
	if e.Pending != nil {
		pending := *e.Pending
		if pending.Target != nil {
			pending.Target, err = model.ValidateLeafValue(pending.Target)
			if err != nil {
				return OwnerEntry{}, err
			}
		}
		if pending.Previous != nil {
			prev := *pending.Previous
			prev.LastApplied, err = model.ValidateLeafValue(prev.LastApplied)
			if err != nil {
				return OwnerEntry{}, err
			}
			pending.Previous = &prev
		}
		next.Pending = &pending
	}
	if err := next.validateNormalized(); err != nil {
		return OwnerEntry{}, err
	}
	return next, nil
}

func (e OwnerEntry) validateNormalized() error {
	if err := ValidateActorID(e.Manager); err != nil {
		return err
	}
	if !e.Origin.IsValid() {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("invalid ownership origin %d", e.Origin),
		}
	}
	if e.Pending != nil && e.Revision == math.MaxUint64 {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "ownership revision exhausted: pending entry requires headroom for settlement",
		}
	}
	if e.Origin == OriginNone {
		if e.LastApplied != nil {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: "Origin=None entry must not have LastApplied",
			}
		}
		if e.Pending == nil {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: "Origin=None is valid only while Pending is present",
			}
		}
		return validatePendingForOrigin(e.Pending, e.Origin, nil)
	}
	normVal, err := model.ValidateLeafValue(e.LastApplied)
	if err != nil {
		return err
	}
	if e.Pending != nil {
		return validatePendingForOrigin(e.Pending, e.Origin, normVal)
	}
	return nil
}

// Clone returns a deep defensive copy of e.
func (e OwnerEntry) Clone() OwnerEntry {
	return OwnerEntry{
		Manager:     e.Manager,
		Origin:      e.Origin,
		LastApplied: model.CloneLeafValue(e.LastApplied),
		Pending:     e.Pending.Clone(),
		Revision:    e.Revision,
	}
}

// Equal reports whether e and other have identical Manager, Origin, LastApplied, Pending, and Revision.
func (e OwnerEntry) Equal(other OwnerEntry) bool {
	if e.Manager != other.Manager || e.Origin != other.Origin || e.Revision != other.Revision {
		return false
	}
	if (e.LastApplied == nil) != (other.LastApplied == nil) {
		return false
	}
	if e.LastApplied != nil && !model.EqualLeafValues(e.LastApplied, other.LastApplied) {
		return false
	}
	return e.Pending.Equal(other.Pending)
}

// HasPending reports whether e has an unsettled Pending reservation.
func (e OwnerEntry) HasPending() bool {
	return e.Pending != nil
}

// IsSettled reports whether e is a settled ownership record with no Pending operation.
func (e OwnerEntry) IsSettled() bool {
	return e.Origin.IsSettled() && e.Pending == nil
}

// CanAutoDelete reports whether e grants automatic deletion rights for prune (Origin=Created and no Pending).
func (e OwnerEntry) CanAutoDelete() bool {
	return e.Origin == OriginCreated && e.Pending == nil
}

// NewPendingCreate constructs an Origin=None reservation for creating an Absent leaf (ADR 0002 §2, ADR 0004 §1).
func NewPendingCreate(manager ActorID, batchID string, target lkal.Value, rev uint64) (OwnerEntry, error) {
	normTarget, err := model.ValidateLeafValue(target)
	if err != nil {
		return OwnerEntry{}, err
	}
	entry := OwnerEntry{
		Manager:  manager,
		Origin:   OriginNone,
		Revision: rev,
		Pending: &Pending{
			BatchID:  batchID,
			Op:       PendingOpSet,
			Expected: observation.AbsentObserved(),
			Target:   normTarget,
		},
	}
	if err := entry.Validate(); err != nil {
		return OwnerEntry{}, err
	}
	return entry, nil
}

// NewSettledCreated constructs a settled Origin=Created ownership record (confirmed Absent -> Present creation).
func NewSettledCreated(manager ActorID, val lkal.Value, rev uint64) (OwnerEntry, error) {
	normVal, err := model.ValidateLeafValue(val)
	if err != nil {
		return OwnerEntry{}, err
	}
	entry := OwnerEntry{
		Manager:     manager,
		Origin:      OriginCreated,
		LastApplied: normVal,
		Revision:    rev,
	}
	if err := entry.Validate(); err != nil {
		return OwnerEntry{}, err
	}
	return entry, nil
}

// NewSettledModified constructs a settled Origin=Modified ownership record (adopted matching value).
func NewSettledModified(manager ActorID, val lkal.Value, rev uint64) (OwnerEntry, error) {
	normVal, err := model.ValidateLeafValue(val)
	if err != nil {
		return OwnerEntry{}, err
	}
	entry := OwnerEntry{
		Manager:     manager,
		Origin:      OriginModified,
		LastApplied: normVal,
		Revision:    rev,
	}
	if err := entry.Validate(); err != nil {
		return OwnerEntry{}, err
	}
	return entry, nil
}
