package plan

import (
	"slices"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
)

// Membership identifies whether a path in M_A(d, σ) belongs to D or retired σ_A \ D (ADR 0003 §1).
type Membership uint8

const (
	// MemberInvalid is the zero value for an uninitialized Membership.
	MemberInvalid Membership = iota
	// MemberDeclared indicates p ∈ D = Paths(d).
	MemberDeclared
	// MemberRetiredModified indicates p ∈ dom(σ_A) \ D with Origin = Modified.
	MemberRetiredModified
	// MemberRetiredCreated indicates p ∈ dom(σ_A) \ D with Origin = Created.
	MemberRetiredCreated
	// MemberRetiredPending indicates p ∈ dom(σ_A) \ D with an unresolved Pending entry (Origin = None).
	MemberRetiredPending
)

// String returns the canonical name of Membership.
func (m Membership) String() string {
	switch m {
	case MemberDeclared:
		return "Declared"
	case MemberRetiredModified:
		return "RetiredModified"
	case MemberRetiredCreated:
		return "RetiredCreated"
	case MemberRetiredPending:
		return "RetiredPending"
	default:
		return "InvalidMembership"
	}
}

// StateClass represents the Scope-independent state classification of a path p ∈ M_A(d, σ_virtual) (ADR 0003 §1).
type StateClass uint8

const (
	// StateInvalid is the zero value for an uninitialized StateClass.
	StateInvalid StateClass = iota
	// StateConverged indicates p ∈ D is settled self-owned with Observed == LastApplied == d(p) and valid ancestors.
	StateConverged
	// StateDeclaredSet indicates p ∈ D requires Set (unowned Absent or settled self-owned LastApplied == Observed != d(p)).
	StateDeclaredSet
	// StateDeclaredAdoptModified indicates p ∈ D is unowned with Observed == Leaf(v) where Equal(v, d(p)).
	StateDeclaredAdoptModified
	// StateDeclaredConflict indicates p ∈ D has an observation, ownership, structural, unowned-value, or drift conflict.
	StateDeclaredConflict
	// StateDeferredUntilRelease indicates p ∈ D overlaps only retired self-Modified paths with no foreign or Pending conflicts (K603).
	StateDeferredUntilRelease
	// StateMigrationRequired indicates p belongs to an ancestor/descendant overlap between a retired Created path and D (K204).
	StateMigrationRequired
	// StateRetiredModifiedKnown indicates p ∈ dom(σ_A) \ D has Origin=Modified and known target + proper ancestors.
	StateRetiredModifiedKnown
	// StateRetiredModifiedUnknown indicates p ∈ dom(σ_A) \ D has Origin=Modified and an Unknown target or proper ancestor (K301).
	StateRetiredModifiedUnknown
	// StateRetiredCreatedAbsent indicates an independent p ∈ dom(σ_A) \ D has Origin=Created and normal Absent observation.
	StateRetiredCreatedAbsent
	// StateRetiredCreatedPrunable indicates an independent p ∈ dom(σ_A) \ D has Origin=Created and Observed == Leaf(LastApplied).
	StateRetiredCreatedPrunable
	// StateRetiredCreatedAnomaly indicates an independent p ∈ dom(σ_A) \ D has Origin=Created and drift, structure conflict, or Unknown.
	StateRetiredCreatedAnomaly
	// StateUnresolvedPending indicates p has an unresolved self-Pending reservation in σ_virtual.
	StateUnresolvedPending
)

// String returns the canonical name of StateClass.
func (s StateClass) String() string {
	switch s {
	case StateConverged:
		return "Converged"
	case StateDeclaredSet:
		return "DeclaredSet"
	case StateDeclaredAdoptModified:
		return "DeclaredAdoptModified"
	case StateDeclaredConflict:
		return "DeclaredConflict"
	case StateDeferredUntilRelease:
		return "DeferredUntilRelease"
	case StateMigrationRequired:
		return "MigrationRequired"
	case StateRetiredModifiedKnown:
		return "RetiredModifiedKnown"
	case StateRetiredModifiedUnknown:
		return "RetiredModifiedUnknown"
	case StateRetiredCreatedAbsent:
		return "RetiredCreatedAbsent"
	case StateRetiredCreatedPrunable:
		return "RetiredCreatedPrunable"
	case StateRetiredCreatedAnomaly:
		return "RetiredCreatedAnomaly"
	case StateUnresolvedPending:
		return "UnresolvedPending"
	default:
		return "InvalidStateClass"
	}
}

// ClassifiedPath holds the Scope-independent classification result for a single path p ∈ M_A(d, σ_virtual).
type ClassifiedPath struct {
	Path            model.Path
	Membership      Membership
	StateClass      StateClass
	Desired         lkal.Value
	Observed        observation.Observed
	Expected        observation.Observed
	SelfEntry       *ownership.OwnerEntry
	CurrentRevision uint64
	Diagnostics     []model.Diagnostic
	PrimaryCode     model.DiagnosticCode
}

// Clone returns a deep defensive copy of c.
func (c ClassifiedPath) Clone() ClassifiedPath {
	var self *ownership.OwnerEntry
	if c.SelfEntry != nil {
		e := c.SelfEntry.Clone()
		self = &e
	}
	return ClassifiedPath{
		Path:            c.Path.Clone(),
		Membership:      c.Membership,
		StateClass:      c.StateClass,
		Desired:         model.CloneLeafValue(c.Desired),
		Observed:        c.Observed,
		Expected:        c.Expected,
		SelfEntry:       self,
		CurrentRevision: c.CurrentRevision,
		Diagnostics:     slices.Clone(c.Diagnostics),
		PrimaryCode:     c.PrimaryCode,
	}
}

// selectPrimaryDiagnosticCode selects the primary diagnostic code according to ADR 0003 §1 item 2:
// Unknown (K301) -> OwnershipConflict (K201) -> StructureConflict / Unrepresentable (K202/K302) -> UnownedValueConflict (K206) -> Drift (K203).
func selectPrimaryDiagnosticCode(diags []model.Diagnostic) model.DiagnosticCode {
	if len(diags) == 0 {
		return ""
	}
	bestCode := diags[0].Code
	bestRank := diagnosticPriorityRank(bestCode)
	for _, d := range diags[1:] {
		if r := diagnosticPriorityRank(d.Code); r < bestRank {
			bestCode = d.Code
			bestRank = r
		}
	}
	return bestCode
}

func diagnosticPriorityRank(code model.DiagnosticCode) int {
	switch code {
	case model.CodeObservationUnavailable: // K301
		return 1
	case model.CodeOwnershipConflict: // K201
		return 2
	case model.CodeMigrationRequired: // K204
		return 3
	case model.CodeStructureConflict, model.CodeUnrepresentable: // K202, K302
		return 4
	case model.CodeUnownedValueConflict: // K206
		return 5
	case model.CodeExternalDrift: // K203
		return 6
	case model.CodePendingConflict: // K205
		return 7
	case model.CodePolicyDenied: // K401
		return 8
	default:
		return 9
	}
}
