package provider

import (
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
)

// ReceiptOutcome represents the 3-valued outcome of Provider.ApplyBatch (ADR 0004 §2–§3).
type ReceiptOutcome uint8

const (
	// ReceiptInvalid is the zero value for an uninitialized ReceiptOutcome.
	ReceiptInvalid ReceiptOutcome = iota
	// ReceiptNotApplied indicates that pre-apply condition checks failed and zero environment changes occurred.
	ReceiptNotApplied
	// ReceiptApplied indicates that the file replacement and both file and parent directory fsync succeeded.
	ReceiptApplied
	// ReceiptIndeterminate indicates that the environment outcome cannot be determined (e.g., parent dir fsync failed after rename).
	ReceiptIndeterminate
)

// String returns the canonical name of ReceiptOutcome.
func (o ReceiptOutcome) String() string {
	switch o {
	case ReceiptNotApplied:
		return "NotApplied"
	case ReceiptApplied:
		return "Applied"
	case ReceiptIndeterminate:
		return "Indeterminate"
	default:
		return "InvalidReceiptOutcome"
	}
}

// IsValid reports whether o is NotApplied, Applied, or Indeterminate.
func (o ReceiptOutcome) IsValid() bool {
	return o == ReceiptNotApplied || o == ReceiptApplied || o == ReceiptIndeterminate
}

// Receipt holds the result of Provider.ApplyBatch(Resource, BatchID, Ops[]) (ADR 0004 §2–§3).
//
// Invariants:
//   - Outcome == ReceiptApplied is returned ONLY when both target file sync and parent directory sync succeeded.
//   - A sync failure after rename MUST return ReceiptIndeterminate (never ReceiptNotApplied, Counterexample #26).
type Receipt struct {
	BatchID     string
	Outcome     ReceiptOutcome
	Diagnostics []model.Diagnostic
}

// Clone returns a deep defensive copy of r.
func (r Receipt) Clone() Receipt {
	return Receipt{
		BatchID:     r.BatchID,
		Outcome:     r.Outcome,
		Diagnostics: slices.Clone(r.Diagnostics),
	}
}

// StabilizeOutcome represents the 3-valued outcome of Provider.Stabilize (ADR 0004 §2–§3).
type StabilizeOutcome uint8

const (
	// StabilizeInvalid is the zero value for an uninitialized StabilizeOutcome.
	StabilizeInvalid StabilizeOutcome = iota
	// StabilizeDurable indicates that target file and parent directory fsync succeeded and re-observation matched Snapshot.
	StabilizeDurable
	// StabilizeConflict indicates that re-observation or physical identity diverged from the expected Snapshot.
	StabilizeConflict
	// StabilizeIndeterminate indicates that file/directory fsync or re-observation failed with an indeterminate I/O error.
	StabilizeIndeterminate
)

// String returns the canonical name of StabilizeOutcome.
func (o StabilizeOutcome) String() string {
	switch o {
	case StabilizeDurable:
		return "DurableSnapshot"
	case StabilizeConflict:
		return "Conflict"
	case StabilizeIndeterminate:
		return "Indeterminate"
	default:
		return "InvalidStabilizeOutcome"
	}
}

// IsValid reports whether o is DurableSnapshot, Conflict, or Indeterminate.
func (o StabilizeOutcome) IsValid() bool {
	return o == StabilizeDurable || o == StabilizeConflict || o == StabilizeIndeterminate
}

// StabilizeResult holds the outcome of Provider.Stabilize(Resource, Snapshot) (ADR 0004 §2–§3).
type StabilizeResult struct {
	Outcome         StabilizeOutcome
	DurableSnapshot *observation.Snapshot
	Diagnostics     []model.Diagnostic
}

// Clone returns a defensive copy of s.
func (s StabilizeResult) Clone() StabilizeResult {
	return StabilizeResult{
		Outcome:         s.Outcome,
		DurableSnapshot: s.DurableSnapshot,
		Diagnostics:     slices.Clone(s.Diagnostics),
	}
}

// Observer observes the current state of paths (and their proper ancestors) on a Resource (ADR 0004 §3).
type Observer interface {
	Observe(resID ownership.ResourceID, paths ...model.Path) (*observation.Snapshot, error)
}

// BatchApplier conditionally applies a batch of environment operations and durably syncs the target resource (ADR 0004 §2–§3).
type BatchApplier interface {
	ApplyBatch(resID ownership.ResourceID, batchID string, ops []plan.EnvironmentOp) Receipt
}

// Stabilizer syncs the target regular file and parent directory and re-verifies observation conditions (ADR 0004 §2–§3).
type Stabilizer interface {
	Stabilize(resID ownership.ResourceID, expected *observation.Snapshot, paths ...model.Path) StabilizeResult
}

// Provider combines Observer, BatchApplier, and Stabilizer for a target Resource (ADR 0004 §3, §5).
type Provider interface {
	Observer
	BatchApplier
	Stabilizer
}
