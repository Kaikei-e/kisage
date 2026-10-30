package plan

import (
	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/structure"
)

// ConvergenceStatus represents the 3-valued convergence evaluation of Match_A(d) (ADR 0003 §2, ADR 0005 §2).
type ConvergenceStatus uint8

const (
	// ConvergenceUnknown indicates that observation was insufficient (Unknown) to confirm convergence,
	// while no definite ownership or value mismatch refuted Match_A(d).
	ConvergenceUnknown ConvergenceStatus = iota
	// ConvergenceNotConverged indicates that Match_A(d) is definitely false (e.g., missing/mismatched
	// ownership or value, unsettled Pending, Deferred actions, or PruneRequired retired paths).
	ConvergenceNotConverged
	// ConvergenceConverged indicates that Match_A(d) holds:
	//   (∀ p ∈ D. Converged_A(p)) ∧ dom(σ_A) ⊆ D.
	ConvergenceConverged
)

// String returns the canonical name of ConvergenceStatus.
func (c ConvergenceStatus) String() string {
	switch c {
	case ConvergenceConverged:
		return "Converged"
	case ConvergenceNotConverged:
		return "NotConverged"
	default:
		return "Unknown"
	}
}

// ConvergedPath evaluates Converged_A(p) for a single declared path p ∈ D with desired value d(p) (ADR 0003 §2):
//
//	Converged_A(p) ⟺ o(p) = Present(Leaf(d(p))) ∧ σ(p).Manager = A ∧ σ(p).LastApplied = d(p) ∧ σ(p).Pending = ∅
//
// In addition, all proper ancestors q ≺ p must be observed as Present(Record) without Unknown or structural conflicts.
func ConvergedPath(
	p model.Path,
	desired lkal.Value,
	snap *observation.Snapshot,
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
) bool {
	if snap == nil || store == nil {
		return false
	}
	entry, ok := store.Get(resID, p)
	if !ok || entry.Manager != actor || !entry.IsSettled() {
		return false
	}
	if !model.EqualLeafValues(entry.LastApplied, desired) {
		return false
	}
	check := structure.InspectPath(snap, p)
	if len(check.Diagnostics) > 0 || !check.CanSet() {
		return false
	}
	v, ok := check.Target.LeafValue()
	return ok && model.EqualLeafValues(v, desired)
}

// MatchA reports whether the full convergence predicate Match_A(d) holds (ADR 0003 §2, Counterexamples #12, #29):
//
//	Match_A(d) ⟺ (∀ p ∈ D. Converged_A(p)) ∧ dom(σ_A) ⊆ D
//
// Note that retired Origin=Created leaves left as PruneRequired (K604) after `apply` violate dom(σ_A) ⊆ D,
// so MatchA returns false until `prune` completes. Foreign Actors' non-overlapping entries in dom(σ) \ dom(σ_A)
// do not affect MatchA (Counterexample #21).
func MatchA(
	decl *projection.Declaration,
	snap *observation.Snapshot,
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
) bool {
	return EvaluateConvergence(decl, snap, store, resID, actor, false) == ConvergenceConverged
}

// EvaluateConvergence evaluates Match_A(d) in 3-valued logic (Converged, NotConverged, Unknown) (ADR 0003 §2, ADR 0005 §2).
func EvaluateConvergence(
	decl *projection.Declaration,
	snap *observation.Snapshot,
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	quarantined bool,
) ConvergenceStatus {
	if quarantined || decl == nil {
		return ConvergenceNotConverged
	}
	sigmaA, err := ownership.ProjectActor(store, resID, actor)
	if err != nil {
		return ConvergenceNotConverged
	}
	// Check dom(σ_A) ⊆ D first (independent of live observations).
	for _, ownedPath := range sigmaA.Domain() {
		if !decl.Has(ownedPath) {
			return ConvergenceNotConverged
		}
	}
	// Check ownership preconditions on every p ∈ D.
	for _, entry := range decl.Entries() {
		owned, ok := sigmaA.Get(entry.Path)
		if !ok || !owned.IsSettled() || !model.EqualLeafValues(owned.LastApplied, entry.Value) {
			return ConvergenceNotConverged
		}
	}
	if snap == nil {
		if decl.IsEmpty() {
			return ConvergenceConverged
		}
		return ConvergenceUnknown
	}

	hasUnknown := false
	for _, entry := range decl.Entries() {
		status := evaluatePathObservationConvergence(entry.Path, entry.Value, snap)
		if status == ConvergenceNotConverged {
			return ConvergenceNotConverged
		}
		if status == ConvergenceUnknown {
			hasUnknown = true
		}
	}
	if hasUnknown {
		return ConvergenceUnknown
	}
	return ConvergenceConverged
}

func evaluatePathObservationConvergence(p model.Path, desired lkal.Value, snap *observation.Snapshot) ConvergenceStatus {
	check := structure.InspectPath(snap, p)
	if check.HasStructureConflict() || check.HasUnrepresentable() {
		return ConvergenceNotConverged
	}
	if check.Target.IsAbsent() {
		return ConvergenceNotConverged
	}
	if v, ok := check.Target.LeafValue(); ok && !model.EqualLeafValues(v, desired) {
		return ConvergenceNotConverged
	}
	if check.HasUnknown() {
		return ConvergenceUnknown
	}
	if !check.CanSet() {
		return ConvergenceNotConverged
	}
	return ConvergenceConverged
}
