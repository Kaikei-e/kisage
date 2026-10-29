package ownership

import (
	"fmt"
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/projection"
)

// OverlapRelation describes how an existing ownership path q in σ overlaps a target path p.
type OverlapRelation uint8

const (
	// OverlapSamePath indicates q == p.
	OverlapSamePath OverlapRelation = iota + 1
	// OverlapAncestor indicates q ≺ p (existing entry is a proper ancestor of target path p).
	OverlapAncestor
	// OverlapDescendant indicates p ≺ q (existing entry is a proper descendant of target path p).
	OverlapDescendant
)

// String returns a human-readable description of the OverlapRelation.
func (r OverlapRelation) String() string {
	switch r {
	case OverlapSamePath:
		return "same-path"
	case OverlapAncestor:
		return "ancestor"
	case OverlapDescendant:
		return "descendant"
	default:
		return "unknown"
	}
}

// OverlapConflict records an overlapping ownership entry in σ for a target path p.
type OverlapConflict struct {
	TargetPath    model.Path
	ExistingPath  model.Path
	Relation      OverlapRelation
	ExistingEntry OwnerEntry
}

// ToDiagnostic converts an OverlapConflict into a normative K201 OwnershipConflict Diagnostic.
func (c OverlapConflict) ToDiagnostic() model.Diagnostic {
	stateDesc := fmt.Sprintf("Origin=%s", c.ExistingEntry.Origin.String())
	if c.ExistingEntry.HasPending() {
		stateDesc = fmt.Sprintf("Pending(%s, BatchID=%s)", c.ExistingEntry.Pending.Op.String(), c.ExistingEntry.Pending.BatchID)
	}
	var reason string
	switch c.Relation {
	case OverlapSamePath:
		reason = fmt.Sprintf("path %s is exclusively owned or reserved by actor %q (%s)", c.TargetPath.String(), c.ExistingEntry.Manager, stateDesc)
	case OverlapAncestor:
		reason = fmt.Sprintf("proper ancestor %s of %s is owned or reserved by actor %q (%s)", c.ExistingPath.String(), c.TargetPath.String(), c.ExistingEntry.Manager, stateDesc)
	case OverlapDescendant:
		reason = fmt.Sprintf("proper descendant %s of %s is owned or reserved by actor %q (%s)", c.ExistingPath.String(), c.TargetPath.String(), c.ExistingEntry.Manager, stateDesc)
	}
	return model.Diagnostic{
		Code:       model.CodeOwnershipConflict,
		Path:       c.TargetPath.Clone(),
		Reason:     reason,
		NextAction: "remove conflicting path from declaration or have the owning actor release ownership",
	}
}

// ActorView represents σ_A, the projection of the shared store σ restricted to Manager == A
// for a single ResourceID (ADR 0002 §1).
type ActorView struct {
	resource ResourceID
	actor    ActorID
	entries  map[string]storedRecord
	paths    []model.Path
}

// ProjectActor extracts σ_A = { (p, e) ∈ σ(resID) | e.Manager == actor } (ADR 0002 §1).
// Foreign entries (Manager != actor) are excluded from σ_A so that unrelated foreign paths
// never enter M_A(d, σ) or trigger spurious conflicts (Counterexamples #21, #40).
func ProjectActor(store *Store, resID ResourceID, actor ActorID) (*ActorView, error) {
	if err := ValidateResourceID(resID); err != nil {
		return nil, err
	}
	if err := ValidateActorID(actor); err != nil {
		return nil, err
	}
	view := &ActorView{
		resource: resID,
		actor:    actor,
		entries:  make(map[string]storedRecord),
	}
	if store == nil {
		return view, nil
	}
	for _, p := range store.Paths(resID) {
		entry, ok := store.Get(resID, p)
		if !ok || entry.Manager != actor {
			continue
		}
		view.entries[p.String()] = storedRecord{
			path:  p.Clone(),
			entry: entry,
		}
		view.paths = append(view.paths, p.Clone())
	}
	return view, nil
}

// Resource returns the ResourceID of the ActorView.
func (v *ActorView) Resource() ResourceID {
	if v == nil {
		return ""
	}
	return v.resource
}

// Actor returns the ActorID A of σ_A.
func (v *ActorView) Actor() ActorID {
	if v == nil {
		return ""
	}
	return v.actor
}

// Len returns the number of paths in dom(σ_A).
func (v *ActorView) Len() int {
	if v == nil {
		return 0
	}
	return len(v.paths)
}

// Domain returns dom(σ_A) sorted in canonical segment order.
func (v *ActorView) Domain() []model.Path {
	if v == nil || len(v.paths) == 0 {
		return nil
	}
	out := make([]model.Path, len(v.paths))
	for i, p := range v.paths {
		out[i] = p.Clone()
	}
	return out
}

// Has reports whether p ∈ dom(σ_A).
func (v *ActorView) Has(p model.Path) bool {
	if v == nil || len(p) == 0 {
		return false
	}
	_, ok := v.entries[p.String()]
	return ok
}

// Get returns the Actor's OwnerEntry at p if p ∈ dom(σ_A).
func (v *ActorView) Get(p model.Path) (OwnerEntry, bool) {
	if v == nil || len(p) == 0 {
		return OwnerEntry{}, false
	}
	rec, ok := v.entries[p.String()]
	if !ok {
		return OwnerEntry{}, false
	}
	return rec.entry.Clone(), true
}

// PendingPaths returns all paths in dom(σ_A) that currently have an unsettled Pending reservation.
// Note that only σ_A is inspected for recovery (ADR 0004 §3, Counterexample #40).
func (v *ActorView) PendingPaths() []model.Path {
	if v == nil || len(v.paths) == 0 {
		return nil
	}
	var out []model.Path
	for _, p := range v.paths {
		if v.entries[p.String()].entry.HasPending() {
			out = append(out, p.Clone())
		}
	}
	return out
}

// ComputeEvaluationScope computes M_A(d, σ) = D ∪ dom(σ_A) in canonical segment order (ADR 0002 §1).
// Undeclared paths owned by other Actors (dom(σ) \ dom(σ_A) outside D) are never included.
func ComputeEvaluationScope(decl *projection.Declaration, sigmaA *ActorView) []model.Path {
	seen := make(map[string]model.Path)
	if decl != nil {
		for _, p := range decl.Paths() {
			seen[p.String()] = p
		}
	}
	if sigmaA != nil {
		for _, p := range sigmaA.Domain() {
			if _, exists := seen[p.String()]; !exists {
				seen[p.String()] = p
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]model.Path, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	slices.SortFunc(out, model.Path.Compare)
	return out
}

// FindOverlaps returns all entries in σ(resID) whose path q overlaps p (q == p, q ≺ p, or p ≺ q),
// sorted by ExistingPath in canonical segment order.
func FindOverlaps(store *Store, resID ResourceID, p model.Path) []OverlapConflict {
	if store == nil || len(p) == 0 {
		return nil
	}
	var out []OverlapConflict
	for _, q := range store.Paths(resID) {
		rel, overlaps := classifyOverlap(p, q)
		if !overlaps {
			continue
		}
		entry, _ := store.Get(resID, q)
		out = append(out, OverlapConflict{
			TargetPath:    p.Clone(),
			ExistingPath:  q,
			Relation:      rel,
			ExistingEntry: entry,
		})
	}
	return out
}

// FindForeignConflicts returns all entries in σ(resID) owned or reserved by an Actor != actor
// that overlap p (same path, proper ancestor, or proper descendant) (ADR 0002 §1, Counterexamples #8, #30, #40).
func FindForeignConflicts(store *Store, resID ResourceID, actor ActorID, p model.Path) []OverlapConflict {
	var out []OverlapConflict
	for _, c := range FindOverlaps(store, resID, p) {
		if c.ExistingEntry.Manager != actor {
			out = append(out, c)
		}
	}
	return out
}

// DiagnoseForeignConflict checks whether p overlaps any foreign ownership or Pending reservation in σ(resID).
// This check depends only on σ, actor, and p—it rejects foreign ownership even when the live
// observation at p is Absent or equal to the desired value (ADR 0002 §1, Counterexamples #8, #30).
func DiagnoseForeignConflict(store *Store, resID ResourceID, actor ActorID, p model.Path) (model.Diagnostic, bool) {
	conflicts := FindForeignConflicts(store, resID, actor, p)
	if len(conflicts) == 0 {
		return model.Diagnostic{}, false
	}
	return conflicts[0].ToDiagnostic(), true
}

// InspectDeclaredConflicts evaluates all declared paths p ∈ D against foreign entries in σ(resID)
// and returns sorted K201 OwnershipConflict diagnostics. Foreign entries that do not overlap D
// produce no diagnostics (ADR 0002 §1, Counterexamples #21, #40).
func InspectDeclaredConflicts(store *Store, resID ResourceID, actor ActorID, decl *projection.Declaration) []model.Diagnostic {
	if decl == nil {
		return nil
	}
	var diags []model.Diagnostic
	for _, p := range decl.Paths() {
		for _, conflict := range FindForeignConflicts(store, resID, actor, p) {
			diags = append(diags, conflict.ToDiagnostic())
		}
	}
	model.SortDiagnostics(diags)
	return diags
}

func classifyOverlap(target, existing model.Path) (OverlapRelation, bool) {
	switch {
	case target.Equal(existing):
		return OverlapSamePath, true
	case existing.IsProperAncestorOf(target):
		return OverlapAncestor, true
	case target.IsProperAncestorOf(existing):
		return OverlapDescendant, true
	default:
		return 0, false
	}
}

