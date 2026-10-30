package plan

import (
	"fmt"
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/structure"
)

func classifyRetiredPath(
	p model.Path,
	entry ownership.OwnerEntry,
	rev uint64,
	snap *observation.Snapshot,
	decl *projection.Declaration,
) ClassifiedPath {
	check := structure.InspectPath(snap, p)
	entryCopy := entry.Clone()

	if entry.HasPending() {
		diags := append(slices.Clone(check.Diagnostics), model.Diagnostic{
			Code:       model.CodePendingConflict,
			Path:       p.Clone(),
			Reason:     fmt.Sprintf("retired path %s has an unresolved Pending %s operation", p, entry.Pending.Op),
			NextAction: "complete crash recovery before proceeding",
		})
		model.SortDiagnostics(diags)
		member := MemberRetiredPending
		if entry.Origin == ownership.OriginModified {
			member = MemberRetiredModified
		} else if entry.Origin == ownership.OriginCreated {
			member = MemberRetiredCreated
		}
		return ClassifiedPath{
			Path: p.Clone(), Membership: member, StateClass: StateUnresolvedPending,
			Observed: check.Target, SelfEntry: &entryCopy, CurrentRevision: rev,
			Diagnostics: diags, PrimaryCode: selectPrimaryDiagnosticCode(diags),
		}
	}

	if entry.Origin == ownership.OriginModified {
		return classifyRetiredModified(p, entryCopy, rev, check)
	}
	return classifyRetiredCreated(p, entryCopy, rev, check, decl)
}

func classifyRetiredModified(
	p model.Path,
	entry ownership.OwnerEntry,
	rev uint64,
	check structure.PathCheckResult,
) ClassifiedPath {
	// ADR 0003 §1 table rows 6 & 7:
	// Unmanaging a retired Modified entry never mutates the target environment, so any known
	// target and ancestor state (Absent, Leaf, Record, Opaque) is eligible for Unmanage.
	// Only Unknown on target or a proper ancestor is a Blocker in Apply (and Deferred in Prune).
	if check.HasUnknown() {
		unknownDiags := filterUnknownDiagnostics(check.Diagnostics)
		model.SortDiagnostics(unknownDiags)
		return ClassifiedPath{
			Path: p.Clone(), Membership: MemberRetiredModified, StateClass: StateRetiredModifiedUnknown,
			Observed: check.Target, SelfEntry: &entry, CurrentRevision: rev,
			Diagnostics: unknownDiags, PrimaryCode: model.CodeObservationUnavailable,
		}
	}
	return ClassifiedPath{
		Path: p.Clone(), Membership: MemberRetiredModified, StateClass: StateRetiredModifiedKnown,
		Observed: check.Target, Expected: check.Target, SelfEntry: &entry, CurrentRevision: rev,
	}
}

func classifyRetiredCreated(
	p model.Path,
	entry ownership.OwnerEntry,
	rev uint64,
	check structure.PathCheckResult,
	decl *projection.Declaration,
) ClassifiedPath {
	// ADR 0003 §1 table row 11 & §4:
	// If a retired Created path overlaps any declared path pDecl ∈ D as proper ancestor or descendant,
	// it belongs to the MigrationRequired (K204) overlap set and blocks both Apply and Prune.
	if overlappingDecl := findOverlappingDeclaredPath(decl, p); overlappingDecl != nil {
		diags := collectRetiredCreatedDiagnostics(p, entry, check)
		diags = append(diags, newMigrationDiagnostic(p, *overlappingDecl))
		model.SortDiagnostics(diags)
		return ClassifiedPath{
			Path: p.Clone(), Membership: MemberRetiredCreated, StateClass: StateMigrationRequired,
			Observed: check.Target, SelfEntry: &entry, CurrentRevision: rev,
			Diagnostics: diags, PrimaryCode: model.CodeMigrationRequired,
		}
	}

	// Row 8: Normal Absent (target is Absent and proper ancestors have no Unknown or structural conflict).
	if check.Target.IsAbsent() && len(check.Diagnostics) == 0 {
		return ClassifiedPath{
			Path: p.Clone(), Membership: MemberRetiredCreated, StateClass: StateRetiredCreatedAbsent,
			Observed: check.Target, Expected: observation.AbsentObserved(), SelfEntry: &entry, CurrentRevision: rev,
		}
	}

	// Row 9: Target is Present(Leaf(LastApplied)) and all proper ancestors are Present(Record).
	if len(check.Diagnostics) == 0 && check.CanDelete() {
		if v, ok := check.Target.LeafValue(); ok && model.EqualLeafValues(v, entry.LastApplied) {
			expectedObs, _ := observation.PresentLeaf(entry.LastApplied)
			return ClassifiedPath{
				Path: p.Clone(), Membership: MemberRetiredCreated, StateClass: StateRetiredCreatedPrunable,
				Observed: check.Target, Expected: expectedObs, SelfEntry: &entry, CurrentRevision: rev,
			}
		}
	}

	// Row 10: Independent retired Created with anomaly (drift, structure conflict, or Unknown).
	diags := collectRetiredCreatedDiagnostics(p, entry, check)
	model.SortDiagnostics(diags)
	return ClassifiedPath{
		Path: p.Clone(), Membership: MemberRetiredCreated, StateClass: StateRetiredCreatedAnomaly,
		Observed: check.Target, SelfEntry: &entry, CurrentRevision: rev,
		Diagnostics: diags, PrimaryCode: selectPrimaryDiagnosticCode(diags),
	}
}

func collectRetiredCreatedDiagnostics(
	p model.Path,
	entry ownership.OwnerEntry,
	check structure.PathCheckResult,
) []model.Diagnostic {
	diags := slices.Clone(check.Diagnostics)
	if v, ok := check.Target.LeafValue(); ok && !model.EqualLeafValues(v, entry.LastApplied) {
		diags = append(diags, model.Diagnostic{
			Code:       model.CodeExternalDrift,
			Path:       p.Clone(),
			Reason:     fmt.Sprintf("retired Origin=Created path %s drifted from LastApplied=%v to observed=%v", p, entry.LastApplied, v),
			NextAction: NextActionRelease,
		})
	}
	return diags
}

func findSelfProperOverlaps(sigmaA *ownership.ActorView, target model.Path) (*model.Path, []model.Path, []model.Path) {
	if sigmaA == nil {
		return nil, nil, nil
	}
	var firstCreated *model.Path
	var modifiedPaths []model.Path
	var pendingPaths []model.Path
	for _, q := range sigmaA.Domain() {
		if q.Equal(target) || !q.Overlaps(target) {
			continue
		}
		entry, _ := sigmaA.Get(q)
		if entry.HasPending() {
			pendingPaths = append(pendingPaths, q)
			if entry.Origin == ownership.OriginCreated {
				qCopy := q.Clone()
				if firstCreated == nil {
					firstCreated = &qCopy
				}
			}
			continue
		}
		if entry.Origin == ownership.OriginCreated {
			qCopy := q.Clone()
			if firstCreated == nil {
				firstCreated = &qCopy
			}
		} else if entry.Origin == ownership.OriginModified {
			modifiedPaths = append(modifiedPaths, q)
		}
	}
	return firstCreated, modifiedPaths, pendingPaths
}

func findOverlappingDeclaredPath(decl *projection.Declaration, retiredPath model.Path) *model.Path {
	if decl == nil {
		return nil
	}
	for _, p := range decl.Paths() {
		if !p.Equal(retiredPath) && p.Overlaps(retiredPath) {
			pCopy := p.Clone()
			return &pCopy
		}
	}
	return nil
}

func newMigrationDiagnostic(path, overlapping model.Path) model.Diagnostic {
	return model.Diagnostic{
		Code:       model.CodeMigrationRequired,
		Path:       path.Clone(),
		Reason:     fmt.Sprintf("path %s overlaps %s across a Leaf/Record structural boundary involving Origin=Created ownership", path, overlapping),
		NextAction: NextActionMigration,
	}
}
