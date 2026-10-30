package ownership

import (
	"fmt"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
)

// Origin represents the provenance of an ownership entry in σ (ADR 0002 §2):
//
//	Origin = None | Created | Modified
type Origin uint8

const (
	// OriginNone is valid only while a new ownership reservation (Pending Set from Absent)
	// is in flight. It never exists as settled ownership (ADR 0002 §2).
	OriginNone Origin = iota
	// OriginCreated indicates that at the start of the current ownership session, the active
	// Actor's transition from Absent to Present was confirmed applied, and all subsequent
	// transitions were valid updates by the same Actor. Only OriginCreated grants automatic
	// deletion rights during prune (ADR 0002 §2).
	OriginCreated
	// OriginModified indicates that ownership was acquired by adopting an existing matching
	// value, conservatively downgraded during crash recovery (K601), or explicitly adopted (K602).
	// OriginModified never grants automatic deletion rights (ADR 0002 §2).
	OriginModified
)

// String returns the canonical name of Origin.
func (o Origin) String() string {
	switch o {
	case OriginNone:
		return "None"
	case OriginCreated:
		return "Created"
	case OriginModified:
		return "Modified"
	default:
		return "InvalidOrigin"
	}
}

// IsValid reports whether o is None, Created, or Modified.
func (o Origin) IsValid() bool {
	return o == OriginNone || o == OriginCreated || o == OriginModified
}

// IsSettled reports whether o is a settled ownership origin (Created or Modified).
func (o Origin) IsSettled() bool {
	return o == OriginCreated || o == OriginModified
}

// HasAutoDeleteRight reports whether o grants automatic deletion rights on retirement (prune).
func (o Origin) HasAutoDeleteRight() bool {
	return o == OriginCreated
}

// PendingOp discriminates the environment operation of a Pending intent (ADR 0004 §1).
type PendingOp uint8

const (
	// PendingOpInvalid is the zero value for an uninitialized PendingOp.
	PendingOpInvalid PendingOp = iota
	// PendingOpSet represents an in-flight Set(p, Target) environment operation.
	PendingOpSet
	// PendingOpDelete represents an in-flight Delete(p) environment operation on a Created leaf.
	PendingOpDelete
)

// String returns the canonical name of PendingOp.
func (op PendingOp) String() string {
	switch op {
	case PendingOpSet:
		return "Set"
	case PendingOpDelete:
		return "Delete"
	default:
		return "InvalidPendingOp"
	}
}

// IsValid reports whether op is Set or Delete.
func (op PendingOp) IsValid() bool {
	return op == PendingOpSet || op == PendingOpDelete
}

// SettledState records the settled (Origin, LastApplied) state prior to an in-flight Pending operation.
type SettledState struct {
	Origin      Origin
	LastApplied lkal.Value
}

// Validate checks that s holds a settled Origin (Created or Modified) and a valid LKAL leaf value.
func (s SettledState) Validate() error {
	if !s.Origin.IsSettled() {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("settled state origin must be Created or Modified, got %s", s.Origin.String()),
		}
	}
	if _, err := model.ValidateLeafValue(s.LastApplied); err != nil {
		return err
	}
	return nil
}

// Clone returns a defensive copy of s.
func (s SettledState) Clone() SettledState {
	return SettledState{
		Origin:      s.Origin,
		LastApplied: model.CloneLeafValue(s.LastApplied),
	}
}

// Equal reports whether s and other have the same Origin and equal LastApplied leaf values.
func (s SettledState) Equal(other SettledState) bool {
	return s.Origin == other.Origin && model.EqualLeafValues(s.LastApplied, other.LastApplied)
}

// Pending represents a durable in-flight environment operation intent on a path (ADR 0002 §1–2, ADR 0004 §1):
//
//	Pending = { BatchID, Op: Set|Delete, Expected, Target?, PreviousEntry? }
type Pending struct {
	BatchID  string
	Op       PendingOp
	Expected observation.Observed
	Target   lkal.Value
	Previous *SettledState
}

// Clone returns a defensive copy of p.
func (p *Pending) Clone() *Pending {
	if p == nil {
		return nil
	}
	var prev *SettledState
	if p.Previous != nil {
		c := p.Previous.Clone()
		prev = &c
	}
	return &Pending{
		BatchID:  p.BatchID,
		Op:       p.Op,
		Expected: p.Expected,
		Target:   model.CloneLeafValue(p.Target),
		Previous: prev,
	}
}

// Equal reports whether p and other represent the same Pending reservation.
func (p *Pending) Equal(other *Pending) bool {
	if p == nil || other == nil {
		return p == other
	}
	if p.BatchID != other.BatchID || p.Op != other.Op || !p.Expected.Equal(other.Expected) {
		return false
	}
	if (p.Target == nil) != (other.Target == nil) {
		return false
	}
	if p.Target != nil && !model.EqualLeafValues(p.Target, other.Target) {
		return false
	}
	if (p.Previous == nil) != (other.Previous == nil) {
		return false
	}
	if p.Previous != nil && !p.Previous.Equal(*other.Previous) {
		return false
	}
	return true
}

func validatePendingForOrigin(p *Pending, entryOrigin Origin, lastApplied lkal.Value) error {
	if p == nil {
		return model.Diagnostic{Code: model.CodeInvalidInput, Reason: "pending intent must not be nil"}
	}
	if err := validateIdentifier("batch ID", p.BatchID); err != nil {
		return err
	}
	if !p.Op.IsValid() {
		return model.Diagnostic{Code: model.CodeInvalidInput, Reason: fmt.Sprintf("invalid pending op %d", p.Op)}
	}
	if entryOrigin == OriginNone {
		return validateNewCreatePending(p)
	}
	return validateExistingEntryPending(p, entryOrigin, lastApplied)
}

func validateNewCreatePending(p *Pending) error {
	if p.Op != PendingOpSet {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "Origin=None pending intent must have Op=Set",
		}
	}
	if !p.Expected.IsAbsent() {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "Origin=None pending intent must have Expected=Absent",
		}
	}
	if _, err := model.ValidateLeafValue(p.Target); err != nil {
		return err
	}
	if p.Previous != nil {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "Origin=None pending intent must not have a PreviousEntry",
		}
	}
	return nil
}

func validateExistingEntryPending(p *Pending, entryOrigin Origin, lastApplied lkal.Value) error {
	if p.Previous == nil {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "pending intent on settled entry requires PreviousEntry",
		}
	}
	if err := p.Previous.Validate(); err != nil {
		return err
	}
	if p.Previous.Origin != entryOrigin || !model.EqualLeafValues(p.Previous.LastApplied, lastApplied) {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "pending PreviousEntry must match entry's current settled Origin and LastApplied",
		}
	}
	expectedLeaf, err := observation.PresentLeaf(lastApplied)
	if err != nil || !p.Expected.Equal(expectedLeaf) {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "pending Expected on existing entry must equal Present(Leaf(LastApplied))",
		}
	}
	if p.Op == PendingOpDelete {
		if entryOrigin != OriginCreated {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: "PendingOpDelete is permitted only for Origin=Created entries",
			}
		}
		if p.Target != nil {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: "PendingOpDelete must not specify a Target value",
			}
		}
		return nil
	}
	normTarget, err := model.ValidateLeafValue(p.Target)
	if err != nil {
		return err
	}
	if model.EqualLeafValues(lastApplied, normTarget) {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "PendingOpSet Target must differ from Expected LastApplied value",
		}
	}
	return nil
}
