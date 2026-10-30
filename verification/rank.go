package verification

import (
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/structure"
)

// ComputePathRank purely evaluates the candidate ESR ranking function for a single path p ∈ M_A(d, σ) (ADR 0005 §3.3):
//   - 2 if an environment value change is required (declared path whose observed value != d(p),
//     or retired Created path whose environment value is still present).
//   - 1 if the environment value is already in its final state, but an ownership record commit,
//     Pending resolution, retired Modified unmanage, or retired Created Absent record cleanup is required.
//   - 0 if p is fully converged (or outside M_A(d, σ)).
func ComputePathRank(
	decl *projection.Declaration,
	snap *observation.Snapshot,
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	p model.Path,
) int {
	declared := decl != nil && decl.Has(p)
	desired, _ := decl.Get(p)

	var selfEntry *ownership.OwnerEntry
	if store != nil {
		if e, ok := store.Get(resID, p); ok && e.Manager == actor {
			selfEntry = &e
		}
	}
	if !declared && selfEntry == nil {
		return 0
	}

	var obs observation.Observed
	if snap != nil {
		obs = snap.Get(p)
	}

	if declared {
		if snap == nil || !structure.InspectPath(snap, p).CanSet() {
			return 2
		}
		leafVal, isLeaf := obs.LeafValue()
		if !isLeaf || !model.EqualLeafValues(leafVal, desired) {
			return 2
		}
		// Environment value matches desired d(p); check if ownership record is settled at d(p).
		if selfEntry != nil && !selfEntry.HasPending() && model.EqualLeafValues(selfEntry.LastApplied, desired) {
			return 0
		}
		return 1
	}

	// Retired self-owned path p ∈ dom(σ_A) \ D:
	if selfEntry.Origin == ownership.OriginModified {
		// Retired Modified only needs metadata unmanage (environment value is left untouched).
		return 1
	}
	if selfEntry.Origin == ownership.OriginCreated {
		if obs.Presence() == observation.PresenceAbsent {
			// Already Absent; only ownership record cleanup remains.
			return 1
		}
		// Environment value still present; requires Prune Delete + record cleanup.
		return 2
	}
	// Origin == None with Pending on a retired path requires recovery resolution.
	return 1
}

// ComputeTotalRank purely computes the sum of ComputePathRank across all paths in M_A(d, σ) = D ∪ dom(σ_A) (ADR 0005 §3.3).
func ComputeTotalRank(
	decl *projection.Declaration,
	snap *observation.Snapshot,
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
) int {
	paths := collectVerificationDomain(decl, store, resID, actor)
	total := 0
	for _, p := range paths {
		total += ComputePathRank(decl, snap, store, resID, actor, p)
	}
	return total
}

func collectVerificationDomain(
	decl *projection.Declaration,
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
) []model.Path {
	seen := make(map[string]model.Path)
	if decl != nil {
		for _, p := range decl.Paths() {
			seen[p.String()] = p
		}
	}
	if store != nil {
		if sigmaA, err := ownership.ProjectActor(store, resID, actor); err == nil {
			for _, p := range sigmaA.Domain() {
				seen[p.String()] = p
			}
		}
	}
	out := make([]model.Path, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	slices.SortFunc(out, model.Path.Compare)
	return out
}
