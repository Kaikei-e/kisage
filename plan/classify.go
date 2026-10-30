package plan

import (
	"fmt"
	"slices"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/structure"
)

// Classify performs Stage 2 Scope-independent path classification across M_A(d, σ_virtual) = D ∪ dom(σ_virtual,A) (ADR 0003 §1, §4).
// Notice that Classify does not accept a Scope parameter: classification depends strictly on d, snap, σ_virtual, and actor.
func Classify(
	decl *projection.Declaration,
	snap *observation.Snapshot,
	virtualStore *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
) ([]ClassifiedPath, error) {
	if decl == nil || snap == nil {
		return nil, model.Diagnostic{Code: model.CodeInvalidInput, Reason: "declaration and snapshot must not be nil"}
	}
	sigmaA, err := ownership.ProjectActor(virtualStore, resID, actor)
	if err != nil {
		return nil, err
	}
	evalPaths := ownership.ComputeEvaluationScope(decl, sigmaA)
	if len(evalPaths) == 0 {
		return nil, nil
	}
	hasUnresolvedSelfPending := len(sigmaA.PendingPaths()) > 0

	out := make([]ClassifiedPath, 0, len(evalPaths))
	for _, p := range evalPaths {
		var selfPtr *ownership.OwnerEntry
		if entry, ok := sigmaA.Get(p); ok {
			selfPtr = &entry
		}
		rev := virtualStore.Revision(resID, p)
		if desired, ok := decl.Get(p); ok {
			cp := classifyDeclaredPath(p, desired, snap, virtualStore, resID, actor, sigmaA, selfPtr, rev, hasUnresolvedSelfPending)
			out = append(out, cp)
			continue
		}
		out = append(out, classifyRetiredPath(p, *selfPtr, rev, snap, decl))
	}
	return out, nil
}

func classifyDeclaredPath(
	p model.Path,
	desired lkal.Value,
	snap *observation.Snapshot,
	virtualStore *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	sigmaA *ownership.ActorView,
	selfEntry *ownership.OwnerEntry,
	rev uint64,
	hasUnresolvedSelfPending bool,
) ClassifiedPath {
	check := structure.InspectPath(snap, p)
	diags := slices.Clone(check.Diagnostics)

	// 1. Check foreign ownership/reservation overlaps (same-path, proper ancestor, or proper descendant) (ADR 0003 §1 item 2).
	foreignConflicts := ownership.FindForeignConflicts(virtualStore, resID, actor, p)
	for _, fc := range foreignConflicts {
		diags = append(diags, fc.ToDiagnostic())
	}

	// 2. Check proper ancestor/descendant overlaps with retired self-owned entries in σ_A \ D (ADR 0003 §1 item 2, §4).
	hasRetiredCreatedOverlap, retiredModifiedOverlaps, pendingSelfOverlaps := findSelfProperOverlaps(sigmaA, p)
	for _, q := range pendingSelfOverlaps {
		diags = append(diags, model.Diagnostic{
			Code:       model.CodePendingConflict,
			Path:       p.Clone(),
			Reason:     fmt.Sprintf("declared path %s overlaps unresolved self-Pending path %s", p, q),
			NextAction: "complete crash recovery before applying structural changes",
		})
	}
	if hasRetiredCreatedOverlap != nil {
		diags = append(diags, newMigrationDiagnostic(p, *hasRetiredCreatedOverlap))
		model.SortDiagnostics(diags)
		primary := model.CodeMigrationRequired
		if len(foreignConflicts) > 0 {
			primary = model.CodeOwnershipConflict
		}
		return ClassifiedPath{
			Path: p.Clone(), Membership: MemberDeclared, StateClass: StateMigrationRequired,
			Desired: desired, Observed: check.Target, SelfEntry: selfEntry, CurrentRevision: rev,
			Diagnostics: diags, PrimaryCode: primary,
		}
	}
	if len(pendingSelfOverlaps) > 0 && len(foreignConflicts) == 0 {
		model.SortDiagnostics(diags)
		return ClassifiedPath{
			Path: p.Clone(), Membership: MemberDeclared, StateClass: StateUnresolvedPending,
			Desired: desired, Observed: check.Target, SelfEntry: selfEntry, CurrentRevision: rev,
			Diagnostics: diags, PrimaryCode: selectPrimaryDiagnosticCode(diags),
		}
	}
	if len(retiredModifiedOverlaps) > 0 && len(foreignConflicts) == 0 && !hasUnresolvedSelfPending && (selfEntry == nil || !selfEntry.HasPending()) {
		// All overlapping old entries are retired Modified, with no foreign conflicts or unresolved self-Pending:
		// classify as DeferredUntilRelease (K603) and retain observation/structural diagnostics without promoting to Blocker (ADR 0003 §1, §4).
		diags = append(diags, model.Diagnostic{
			Code:       model.CodeDeferredUntilRelease,
			Path:       p.Clone(),
			Reason:     fmt.Sprintf("declared path %s overlaps retired Origin=Modified path %s; deferred until old record is unmanaged or released", p, retiredModifiedOverlaps[0]),
			NextAction: NextActionDeferredRelease,
		})
		model.SortDiagnostics(diags)
		return ClassifiedPath{
			Path: p.Clone(), Membership: MemberDeclared, StateClass: StateDeferredUntilRelease,
			Desired: desired, Observed: check.Target, SelfEntry: selfEntry, CurrentRevision: rev,
			Diagnostics: diags, PrimaryCode: model.CodeDeferredUntilRelease,
		}
	}

	// 3. Evaluate same-path self-ownership or unowned state.
	return classifyDeclaredDirect(p, desired, check, diags, foreignConflicts, selfEntry, rev)
}

func classifyDeclaredDirect(
	p model.Path,
	desired lkal.Value,
	check structure.PathCheckResult,
	diags []model.Diagnostic,
	foreignConflicts []ownership.OverlapConflict,
	selfEntry *ownership.OwnerEntry,
	rev uint64,
) ClassifiedPath {
	if selfEntry != nil {
		if selfEntry.HasPending() {
			diags = append(diags, model.Diagnostic{
				Code:       model.CodePendingConflict,
				Path:       p.Clone(),
				Reason:     fmt.Sprintf("path %s has an unresolved Pending %s operation", p, selfEntry.Pending.Op),
				NextAction: "complete crash recovery before applying changes",
			})
			model.SortDiagnostics(diags)
			return ClassifiedPath{
				Path: p.Clone(), Membership: MemberDeclared, StateClass: StateUnresolvedPending,
				Desired: desired, Observed: check.Target, SelfEntry: selfEntry, CurrentRevision: rev,
				Diagnostics: diags, PrimaryCode: selectPrimaryDiagnosticCode(diags),
			}
		}
		return classifyDeclaredSelfOwned(p, desired, check, diags, len(foreignConflicts) > 0, *selfEntry, rev)
	}
	return classifyDeclaredUnowned(p, desired, check, diags, len(foreignConflicts) > 0, rev)
}

func classifyDeclaredSelfOwned(
	p model.Path,
	desired lkal.Value,
	check structure.PathCheckResult,
	diags []model.Diagnostic,
	hasForeign bool,
	entry ownership.OwnerEntry,
	rev uint64,
) ClassifiedPath {
	if check.Target.IsAbsent() {
		diags = append(diags, model.Diagnostic{
			Code:       model.CodeExternalDrift,
			Path:       p.Clone(),
			Reason:     fmt.Sprintf("owned path %s was externally deleted (LastApplied=%v, observed Absent)", p, entry.LastApplied),
			NextAction: NextActionReleaseThenApply,
		})
	} else if v, ok := check.Target.LeafValue(); ok {
		if !model.EqualLeafValues(v, entry.LastApplied) {
			diags = append(diags, model.Diagnostic{
				Code:       model.CodeExternalDrift,
				Path:       p.Clone(),
				Reason:     fmt.Sprintf("owned path %s drifted from LastApplied=%v to observed=%v", p, entry.LastApplied, v),
				NextAction: NextActionAdoptThenApply,
			})
		} else if len(diags) == 0 && !hasForeign && check.CanSet() {
			if model.EqualLeafValues(v, desired) {
				return ClassifiedPath{
					Path: p.Clone(), Membership: MemberDeclared, StateClass: StateConverged,
					Desired: desired, Observed: check.Target, SelfEntry: &entry, CurrentRevision: rev,
				}
			}
			expectedObs, _ := observation.PresentLeaf(entry.LastApplied)
			return ClassifiedPath{
				Path: p.Clone(), Membership: MemberDeclared, StateClass: StateDeclaredSet,
				Desired: desired, Observed: check.Target, Expected: expectedObs,
				SelfEntry: &entry, CurrentRevision: rev,
			}
		}
	}
	model.SortDiagnostics(diags)
	return ClassifiedPath{
		Path: p.Clone(), Membership: MemberDeclared, StateClass: StateDeclaredConflict,
		Desired: desired, Observed: check.Target, SelfEntry: &entry, CurrentRevision: rev,
		Diagnostics: diags, PrimaryCode: selectPrimaryDiagnosticCode(diags),
	}
}

func classifyDeclaredUnowned(
	p model.Path,
	desired lkal.Value,
	check structure.PathCheckResult,
	diags []model.Diagnostic,
	hasForeign bool,
	rev uint64,
) ClassifiedPath {
	if !hasForeign {
		if v, ok := check.Target.LeafValue(); ok {
			if !model.EqualLeafValues(v, desired) {
				diags = append(diags, model.Diagnostic{
					Code:       model.CodeUnownedValueConflict,
					Path:       p.Clone(),
					Reason:     fmt.Sprintf("unowned path %s has existing value %v not equal to declared value %v", p, v, desired),
					NextAction: NextActionAdoptThenApply,
				})
			} else if len(diags) == 0 && check.CanSet() {
				return ClassifiedPath{
					Path: p.Clone(), Membership: MemberDeclared, StateClass: StateDeclaredAdoptModified,
					Desired: desired, Observed: check.Target, Expected: check.Target, CurrentRevision: rev,
				}
			}
		} else if check.Target.IsAbsent() && len(diags) == 0 && check.CanSet() {
			return ClassifiedPath{
				Path: p.Clone(), Membership: MemberDeclared, StateClass: StateDeclaredSet,
				Desired: desired, Observed: check.Target, Expected: observation.AbsentObserved(), CurrentRevision: rev,
			}
		}
	}
	model.SortDiagnostics(diags)
	return ClassifiedPath{
		Path: p.Clone(), Membership: MemberDeclared, StateClass: StateDeclaredConflict,
		Desired: desired, Observed: check.Target, CurrentRevision: rev,
		Diagnostics: diags, PrimaryCode: selectPrimaryDiagnosticCode(diags),
	}
}
