package plan

import (
	"fmt"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/scope"
)

// Mode discriminates the reconciliation execution mode (ADR 0003 §1):
//
//	Mode = Apply | Prune
//
// Apply handles leaf creation, updates, matching adoption, retired Modified unmanagement,
// and clearing confirmed-Absent retired Created records. Prune handles conditional deletion
// of retired Created leaves (plus clearing confirmed-Absent retired Created records).
type Mode uint8

const (
	// ModeInvalid is the zero value for an uninitialized Mode.
	ModeInvalid Mode = iota
	// ModeApply creates/updates declared leaves and unmanages retired Modified records without deleting environment leaves.
	ModeApply
	// ModePrune deletes retired Created leaves matching LastApplied without creating, updating, or adopting leaves.
	ModePrune
)

// IsValid reports whether m is ModeApply or ModePrune.
func (m Mode) IsValid() bool {
	return m == ModeApply || m == ModePrune
}

// String returns the canonical name of Mode.
func (m Mode) String() string {
	switch m {
	case ModeApply:
		return "Apply"
	case ModePrune:
		return "Prune"
	default:
		return "InvalidMode"
	}
}

// Normative NextAction guidance strings for diagnostics (ADR 0003 §1, §3, §4; ADR 0005 §2).
const (
	// NextActionAdoptThenApply is the canonical resolution path for K206 (UnownedValueConflict)
	// and K203 (DriftChanged on a declared leaf) (ADR 0003 §1, §3; ADR 0005 §2; Counterexamples #36, #37).
	NextActionAdoptThenApply = "adopt -> apply"

	// NextActionReleaseThenApply is the canonical resolution path for K203 (DriftDeleted on a declared leaf)
	// when re-creating an externally deleted leaf (ADR 0003 §3; Counterexamples #22, #37).
	NextActionReleaseThenApply = "release -> apply"

	// NextActionRelease is the canonical resolution path for relinquishing a drifted retired Created leaf (ADR 0003 §3).
	NextActionRelease = "release"

	// NextActionPrune is the canonical next step for K604 (PruneRequired) after apply (ADR 0003 §1–2; ADR 0005 §2).
	NextActionPrune = "prune"

	// NextActionMigration is the canonical 3-step manual procedure for K204 (MigrationRequired)
	// when a retired Created leaf overlaps a declared leaf (ADR 0003 §4; ADR 0005 §2).
	NextActionMigration = "release -> migrate environment structure -> apply"

	// NextActionDeferredRelease indicates that a declared path overlapping only retired Modified records
	// will be evaluated on the next apply after old records are unmanaged or released (ADR 0003 §4).
	NextActionDeferredRelease = "re-run apply after retired Origin=Modified record is unmanaged, or run release"

	// NextActionInspectQuarantine indicates that administrator recovery is required for a quarantined Resource (ADR 0004 §2, §4).
	NextActionInspectQuarantine = "inspect quarantined resource and complete administrator recovery before resuming"
)

// Input holds the immutable snapshot inputs to the pure planning pipeline (ADR 0003 §1).
type Input struct {
	Resource    ownership.ResourceID
	Actor       ownership.ActorID
	Declaration *projection.Declaration
	Snapshot    *observation.Snapshot
	Store       *ownership.Store
	Scope       scope.Scope
	Quarantined bool
}

// ValidateInput checks all structural and boundary invariants on Input before planning (ADR 0003 §1 item 1).
func ValidateInput(in Input) error {
	if err := ownership.ValidateResourceID(in.Resource); err != nil {
		return err
	}
	if err := ownership.ValidateActorID(in.Actor); err != nil {
		return err
	}
	if in.Declaration == nil {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "declaration must not be nil",
		}
	}
	if in.Snapshot == nil {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "observation snapshot must not be nil",
		}
	}
	if in.Store != nil {
		if err := in.Store.Validate(); err != nil {
			return err
		}
	}
	return in.Scope.Validate()
}

func validateMode(m Mode) error {
	if !m.IsValid() {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("invalid plan mode %d", m),
		}
	}
	return nil
}

func quarantineDiagnostic(resID ownership.ResourceID) model.Diagnostic {
	return model.Diagnostic{
		Code:       model.CodeResourceQuarantined,
		Reason:     fmt.Sprintf("resource %q is quarantined due to a prior Provider contract violation; all operations are halted", resID),
		NextAction: NextActionInspectQuarantine,
	}
}
