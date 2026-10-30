package verification

import (
	"fmt"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/scope"
	"github.com/Kaikei-e/kisage/structure"
)

// VerifyTheorem1ScopeAndFrame checks Safety Theorem 1 (ADR 0005 §3.1):
// Every normal plan and recovery candidate modifies only paths in M_A(d, σ) = D ∪ dom(σ_A)
// and satisfies its required Scope permissions (Set+Own, Delete+Own, or Own).
func VerifyTheorem1ScopeAndFrame(in plan.Input, res *plan.Result) error {
	sigmaA, err := ownership.ProjectActor(in.Store, in.Resource, in.Actor)
	if err != nil {
		return err
	}
	inMA := func(p model.Path) bool {
		return in.Declaration.Has(p) || sigmaA.Has(p) ||
			(res.Recovery != nil && hasPathInVirtualActorDomain(res.Recovery.VirtualStore, in.Resource, in.Actor, p))
	}

	if res.Recovery != nil {
		for _, cand := range res.Recovery.Candidates {
			if !sigmaA.Has(cand.Path) {
				return fmt.Errorf("theorem 1 violated: recovery candidate %s outside dom(σ_A)", cand.Path)
			}
			if !in.Scope.Allows(cand.Path, scope.NewPermissionSet(scope.PermOwn)) {
				return fmt.Errorf("theorem 1 violated: recovery candidate %s lacks Own permission", cand.Path)
			}
		}
	}
	for _, op := range res.Gate.Changes {
		if !inMA(op.Path) {
			return fmt.Errorf("theorem 1 violated: environment op %s outside M_A(d, σ)", op.Path)
		}
		if !in.Scope.Allows(op.Path, op.RequiredPerms) {
			return fmt.Errorf("theorem 1 violated: environment op %s lacks required perms %v", op.Path, op.RequiredPerms)
		}
	}
	for _, mc := range res.Gate.MetadataCandidates {
		if !inMA(mc.Path) {
			return fmt.Errorf("theorem 1 violated: metadata candidate %s outside M_A(d, σ)", mc.Path)
		}
		if !in.Scope.Allows(mc.Path, mc.RequiredPerms) {
			return fmt.Errorf("theorem 1 violated: metadata candidate %s lacks required perms %v", mc.Path, mc.RequiredPerms)
		}
	}
	for _, u := range res.Gate.OwnershipUpdates {
		if !inMA(u.Path) {
			return fmt.Errorf("theorem 1 violated: ownership update %s outside M_A(d, σ)", u.Path)
		}
	}
	return nil
}

// VerifyTheorem2NonDestructiveStructure checks Safety Theorem 2 (ADR 0005 §3.1):
// No planned environment change overwrites an existing Record (including empty Record) or Opaque node,
// or traverses a Leaf or Opaque proper ancestor.
func VerifyTheorem2NonDestructiveStructure(in plan.Input, res *plan.Result) error {
	for _, op := range res.Gate.Changes {
		check := structure.InspectPath(in.Snapshot, op.Path)
		if check.Target.IsRecord() || check.Target.IsOpaque() {
			return fmt.Errorf("theorem 2 violated: environment op on %s targets %s", op.Path, check.Target)
		}
		for _, anc := range check.Ancestors {
			if anc.Observed.IsLeaf() || anc.Observed.IsOpaque() {
				return fmt.Errorf("theorem 2 violated: environment op on %s traverses non-record ancestor %s (%s)", op.Path, anc.Path, anc.Observed)
			}
		}
	}
	return nil
}

// VerifyTheorem3ConservativeDeletion checks Safety Theorem 3 (ADR 0005 §3.1):
// Environment Delete is generated ONLY in ModePrune, for paths outside D, owned by self Actor
// with Origin == Created, Pending == nil, and Observed == Present(Leaf(LastApplied)).
func VerifyTheorem3ConservativeDeletion(in plan.Input, res *plan.Result) error {
	vStore := in.Store
	if res.Recovery != nil && res.Recovery.VirtualStore != nil {
		vStore = res.Recovery.VirtualStore
	}
	for _, op := range res.Gate.Changes {
		if op.Op != ownership.PendingOpDelete {
			continue
		}
		if res.Mode != plan.ModePrune {
			return fmt.Errorf("theorem 3 violated: Delete generated in mode %s for %s", res.Mode, op.Path)
		}
		if in.Declaration.Has(op.Path) {
			return fmt.Errorf("theorem 3 violated: Delete generated for declared path %s", op.Path)
		}
		entry, ok := vStore.Get(in.Resource, op.Path)
		if !ok || entry.Manager != in.Actor || entry.Origin != ownership.OriginCreated || entry.HasPending() {
			return fmt.Errorf("theorem 3 violated: Delete on %s has invalid ownership %+v", op.Path, entry)
		}
		obs := in.Snapshot.Get(op.Path)
		leafVal, isLeaf := obs.LeafValue()
		if !isLeaf || !model.EqualLeafValues(leafVal, entry.LastApplied) {
			return fmt.Errorf("theorem 3 violated: Delete on %s observed %s != LastApplied %v", op.Path, obs, entry.LastApplied)
		}
	}
	return nil
}

// VerifyTheorem4NoMutationOnUnknown checks Safety Theorem 4 (ADR 0005 §3.1):
// If target or any proper ancestor observation is Unknown, neither environment changes nor
// ownership updates are generated for that path.
func VerifyTheorem4NoMutationOnUnknown(in plan.Input, res *plan.Result) error {
	for _, op := range res.Gate.Changes {
		check := structure.InspectPath(in.Snapshot, op.Path)
		if check.HasUnknown() {
			return fmt.Errorf("theorem 4 violated: environment op generated on path %s with Unknown observation", op.Path)
		}
	}
	for _, u := range res.Gate.OwnershipUpdates {
		check := structure.InspectPath(in.Snapshot, u.Path)
		if check.HasUnknown() {
			return fmt.Errorf("theorem 4 violated: ownership update generated on path %s with Unknown observation", u.Path)
		}
	}
	return nil
}

// VerifyTheorem5NoOverwriteOnForeignOrDrift checks Safety Theorem 5 (ADR 0005 §3.1):
// Never overwrites a foreign Actor's ownership/reservation, an unowned conflicting value,
// or a drifted self-owned value (including DriftDeleted / Absent).
func VerifyTheorem5NoOverwriteOnForeignOrDrift(in plan.Input, res *plan.Result) error {
	vStore := in.Store
	if res.Recovery != nil && res.Recovery.VirtualStore != nil {
		vStore = res.Recovery.VirtualStore
	}
	for _, u := range res.Gate.OwnershipUpdates {
		if conflicts := ownership.FindForeignConflicts(vStore, in.Resource, in.Actor, u.Path); len(conflicts) > 0 {
			return fmt.Errorf("theorem 5 violated: update on %s overlaps foreign ownership %+v", u.Path, conflicts[0])
		}
		obs := in.Snapshot.Get(u.Path)
		entry, owned := vStore.Get(in.Resource, u.Path)
		if owned && entry.Manager == in.Actor {
			if obs.Presence() == observation.PresenceAbsent {
				if in.Declaration.Has(u.Path) {
					return fmt.Errorf("theorem 5 violated: declared path %s with DriftDeleted (Absent) was updated", u.Path)
				}
			} else if leafVal, ok := obs.LeafValue(); ok {
				if !model.EqualLeafValues(leafVal, entry.LastApplied) && u.Kind != plan.UpdateUnmanage {
					return fmt.Errorf("theorem 5 violated: drifted path %s (obs=%v, last=%v) was overwritten", u.Path, leafVal, entry.LastApplied)
				}
			}
		} else if !owned && in.Declaration.Has(u.Path) {
			if leafVal, ok := obs.LeafValue(); ok {
				desired, _ := in.Declaration.Get(u.Path)
				if !model.EqualLeafValues(leafVal, desired) {
					return fmt.Errorf("theorem 5 violated: unowned conflicting value on %s was overwritten", u.Path)
				}
			}
		}
	}
	return nil
}

// VerifyTheorem6MatchFixedPoint checks Safety Theorem 6 (ADR 0005 §3.1):
// When Match_A(d) holds, both Apply and Prune produce empty Changes and empty OwnershipUpdates.
func VerifyTheorem6MatchFixedPoint(in plan.Input, applyRes, pruneRes *plan.Result) error {
	vStore := in.Store
	if applyRes.Recovery != nil && applyRes.Recovery.VirtualStore != nil {
		vStore = applyRes.Recovery.VirtualStore
	}
	conv := plan.EvaluateConvergence(in.Declaration, in.Snapshot, vStore, in.Resource, in.Actor, in.Quarantined)
	if conv != plan.ConvergenceConverged {
		return nil
	}
	if applyRes.Recovery != nil && applyRes.Recovery.HasCandidates() {
		return nil
	}
	for _, r := range []*plan.Result{applyRes, pruneRes} {
		if len(r.Gate.Changes) > 0 || len(r.Gate.OwnershipUpdates) > 0 || r.Execution != plan.ExecNoOp {
			return fmt.Errorf("theorem 6 violated: Match_A holds but mode %s produced work (exec=%s)", r.Mode, r.Execution)
		}
	}
	return nil
}

func hasPathInVirtualActorDomain(store *ownership.Store, resID ownership.ResourceID, actor ownership.ActorID, p model.Path) bool {
	if store == nil {
		return false
	}
	entry, ok := store.Get(resID, p)
	return ok && entry.Manager == actor
}
