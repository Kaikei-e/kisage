package plan

import (
	"fmt"
	"math"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/scope"
	"github.com/Kaikei-e/kisage/structure"
)

// ResolutionRequest holds the inputs to the explicit metadata resolution commands `adopt` and `release` (ADR 0003 §3).
type ResolutionRequest struct {
	Resource         ownership.ResourceID
	Actor            ownership.ActorID
	Path             model.Path
	ExpectedToken    ConditionToken
	ExpectedRevision uint64
	Declaration      *projection.Declaration
	Snapshot         *observation.Snapshot
	Store            *ownership.Store
	Scope            scope.Scope
	Quarantined      bool
}

// ResolutionPlan holds the verified metadata update plan for `adopt` or `release` (ADR 0003 §3, Counterexamples #36–#38, #50).
//
// Invariants enforced by PlanAdopt and PlanRelease (ADR 0003 §3, Theorem 9):
//   - Never generates environment Set or Delete operations; the target file is never modified.
//   - Requires Own + Resolve permission on Path.
//   - Requires exact match of ExpectedToken (Resource, Path, target & ancestor observations) and ExpectedRevision.
//   - Rejects foreign ownership/reservation, unresolved self-Pending, Resource quarantine, and Unknown observations.
//   - PlanAdopt always records Origin=Modified (never promotes to Created) and rejects ancestor/descendant overlaps in σ.
type ResolutionPlan struct {
	Action              scope.ActionKind
	Resource            ownership.ResourceID
	Actor               ownership.ActorID
	Path                model.Path
	Before              *ownership.OwnerEntry
	After               *ownership.OwnerEntry
	PreviousRevision    uint64
	NextRevision        uint64
	NextStore           *ownership.Store
	RequiresStabilize   bool
	DeclaredInFile      bool
	RemanageOnNextApply bool
	Diagnostics         []model.Diagnostic
}

// PlanAdopt validates all ADR 0003 §3 preconditions for `kisage adopt` and computes the updated ownership store.
func PlanAdopt(req ResolutionRequest) (*ResolutionPlan, error) {
	if err := validateCommonResolutionPreconditions(req, scope.ActionExplicitAdopt); err != nil {
		return nil, err
	}
	if err := checkAdoptOverlaps(req.Store, req.Resource, req.Actor, req.Path); err != nil {
		return nil, err
	}
	if err := req.Store.VerifyRevision(req.Resource, req.Path, req.ExpectedRevision); err != nil {
		return nil, err
	}
	check := structure.InspectPath(req.Snapshot, req.Path)
	if err := validateAdoptObservation(req.Path, check); err != nil {
		return nil, err
	}
	if err := VerifyConditionToken(req.ExpectedToken, req.Resource, req.Path, req.Snapshot); err != nil {
		return nil, err
	}

	leafVal, _ := check.Target.LeafValue()
	var currentPtr *ownership.OwnerEntry
	if cur, ok := req.Store.Get(req.Resource, req.Path); ok {
		currentPtr = &cur
	}
	if req.ExpectedRevision == math.MaxUint64 {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   req.Path.Clone(),
			Reason: "ownership revision exhausted: cannot advance beyond uint64 maximum",
		}
	}
	nextRev := req.ExpectedRevision + 1
	adopted, notice, err := ownership.TransitionExplicitAdopt(currentPtr, req.Actor, req.Path, leafVal, nextRev)
	if err != nil {
		return nil, err
	}
	nextStore, err := req.Store.Put(req.Resource, req.Path, adopted)
	if err != nil {
		return nil, err
	}
	actualNextRev := nextStore.Revision(req.Resource, req.Path)
	adopted.Revision = actualNextRev

	var diags []model.Diagnostic
	if notice != nil {
		diags = append(diags, *notice)
	}
	return &ResolutionPlan{
		Action:            scope.ActionExplicitAdopt,
		Resource:          req.Resource,
		Actor:             req.Actor,
		Path:              req.Path.Clone(),
		Before:            cloneEntryPtr(currentPtr),
		After:             &adopted,
		PreviousRevision:  req.ExpectedRevision,
		NextRevision:      actualNextRev,
		NextStore:         nextStore,
		RequiresStabilize: true,
		DeclaredInFile:    req.Declaration != nil && req.Declaration.Has(req.Path),
		Diagnostics:       diags,
	}, nil
}

// PlanRelease validates all ADR 0003 §3 preconditions for `kisage release` and computes the updated ownership store.
func PlanRelease(req ResolutionRequest) (*ResolutionPlan, error) {
	if err := validateCommonResolutionPreconditions(req, scope.ActionExplicitRelease); err != nil {
		return nil, err
	}
	var currentPtr *ownership.OwnerEntry
	if cur, ok := req.Store.Get(req.Resource, req.Path); ok {
		currentPtr = &cur
	}
	notice, err := ownership.TransitionExplicitRelease(currentPtr, req.Actor, req.Path)
	if err != nil {
		return nil, err
	}
	if err := req.Store.VerifyRevision(req.Resource, req.Path, req.ExpectedRevision); err != nil {
		return nil, err
	}
	check := structure.InspectPath(req.Snapshot, req.Path)
	if check.HasUnknown() {
		diag, _ := check.PrimaryDiagnostic()
		return nil, diag
	}
	if err := VerifyConditionToken(req.ExpectedToken, req.Resource, req.Path, req.Snapshot); err != nil {
		return nil, err
	}

	nextStore, err := req.Store.Delete(req.Resource, req.Path)
	if err != nil {
		return nil, err
	}
	declaredInFile := req.Declaration != nil && req.Declaration.Has(req.Path)
	diags := buildReleaseDiagnostics(req.Path, notice, declaredInFile)

	return &ResolutionPlan{
		Action:              scope.ActionExplicitRelease,
		Resource:            req.Resource,
		Actor:               req.Actor,
		Path:                req.Path.Clone(),
		Before:              cloneEntryPtr(currentPtr),
		After:               nil,
		PreviousRevision:    req.ExpectedRevision,
		NextRevision:        nextStore.Revision(req.Resource, req.Path),
		NextStore:           nextStore,
		RequiresStabilize:   true,
		DeclaredInFile:      declaredInFile,
		RemanageOnNextApply: declaredInFile,
		Diagnostics:         diags,
	}, nil
}

func validateCommonResolutionPreconditions(req ResolutionRequest, action scope.ActionKind) error {
	if req.Declaration == nil {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   req.Path.Clone(),
			Reason: "declaration must not be nil; adopt and release require the declaration FILE",
		}
	}
	if err := ownership.ValidateResourceID(req.Resource); err != nil {
		return err
	}
	if err := ownership.ValidateActorID(req.Actor); err != nil {
		return err
	}
	if err := model.ValidateLeafPath(req.Path); err != nil {
		return err
	}
	if req.Snapshot == nil || req.Store == nil {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   req.Path.Clone(),
			Reason: "observation snapshot and ownership store must not be nil",
		}
	}
	if err := req.Store.Validate(); err != nil {
		return err
	}
	if err := req.Scope.Validate(); err != nil {
		return err
	}
	if req.Quarantined {
		return quarantineDiagnostic(req.Resource).WithPath(req.Path)
	}
	if err := req.Scope.AuthorizeAction(req.Path, action); err != nil {
		return err
	}
	sigmaA, err := ownership.ProjectActor(req.Store, req.Resource, req.Actor)
	if err != nil {
		return err
	}
	if pending := sigmaA.PendingPaths(); len(pending) > 0 {
		return model.Diagnostic{
			Code:       model.CodePendingConflict,
			Path:       req.Path.Clone(),
			Reason:     fmt.Sprintf("actor %q has unresolved Pending operation at %s; explicit resolution is rejected while Pending exists", req.Actor, pending[0]),
			NextAction: "complete crash recovery before invoking adopt or release",
		}
	}
	return nil
}

func checkAdoptOverlaps(store *ownership.Store, resID ownership.ResourceID, actor ownership.ActorID, p model.Path) error {
	for _, c := range ownership.FindOverlaps(store, resID, p) {
		if c.ExistingEntry.Manager != actor {
			return c.ToDiagnostic()
		}
		if c.ExistingEntry.HasPending() {
			return model.Diagnostic{
				Code:       model.CodePendingConflict,
				Path:       p.Clone(),
				Reason:     fmt.Sprintf("path %s overlaps unresolved Pending operation at %s", p, c.ExistingPath),
				NextAction: "complete crash recovery before invoking adopt",
			}
		}
		if c.Relation != ownership.OverlapSamePath {
			return model.Diagnostic{
				Code:       model.CodeOwnershipConflict,
				Path:       p.Clone(),
				Reason:     fmt.Sprintf("cannot adopt %s while overlapping self-owned %s path %s exists; release %s first", p, c.Relation, c.ExistingPath, c.ExistingPath),
				NextAction: NextActionRelease,
			}
		}
	}
	return nil
}

func validateAdoptObservation(p model.Path, check structure.PathCheckResult) error {
	if err := check.Err(); err != nil {
		return err
	}
	if !check.Target.IsLeaf() || !check.CanSet() {
		return model.Diagnostic{
			Code:       model.CodeStructureConflict,
			Path:       p.Clone(),
			Reason:     fmt.Sprintf("adopt on %s requires Present(Leaf(v)) with Present(Record) ancestors, observed %s", p, check.Target),
			NextAction: "ensure target path is a representable leaf before invoking adopt",
		}
	}
	return nil
}

func buildReleaseDiagnostics(path model.Path, notice *model.Diagnostic, declaredInFile bool) []model.Diagnostic {
	if notice != nil {
		diag := *notice
		if declaredInFile {
			diag.Reason = fmt.Sprintf("%s (note: %s remains in declaration and may be re-managed on next apply)", diag.Reason, path)
			diag.NextAction = "run apply to re-manage or remove path from declaration"
		}
		return []model.Diagnostic{diag}
	}
	if declaredInFile {
		return []model.Diagnostic{{
			Code:       model.CodeExplicitDeletionRightWaived,
			Path:       path.Clone(),
			Reason:     fmt.Sprintf("explicit release removed ownership of %s while path remains in declaration; next apply may re-manage this path", path),
			NextAction: "run apply to re-manage or remove path from declaration",
		}}
	}
	return nil
}
