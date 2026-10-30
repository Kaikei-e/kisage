package plan

import (
	"slices"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/scope"
	"github.com/Kaikei-e/kisage/structure"
)

// Explanation holds the read-only `explain FILE PATH` inspection result (ADR 0003 §3, ADR 0005 §1).
type Explanation struct {
	Resource       ownership.ResourceID
	Actor          ownership.ActorID
	Path           model.Path
	Declared       bool
	DesiredValue   lkal.Value
	Observed       observation.Observed
	Ancestors      []structure.AncestorObservation
	Owner          *ownership.OwnerEntry
	OwnerRevision  uint64
	ConditionToken ConditionToken
	GrantedPerms   scope.PermissionSet
	Apply          PathResult
	Prune          PathResult
	ApplyExecution ExecutionStatus
	PruneExecution ExecutionStatus
	Diagnostics    []model.Diagnostic
}

// ExplainPath computes the read-only inspection for path under in (ADR 0003 §3, ADR 0005 §1).
func ExplainPath(in Input, path model.Path) (*Explanation, error) {
	if err := ValidateInput(in); err != nil {
		return nil, err
	}
	if err := model.ValidateLeafPath(path); err != nil {
		return nil, err
	}
	preview, err := PlanPreview(in)
	if err != nil {
		return nil, err
	}

	check := structure.InspectPath(in.Snapshot, path)
	var ownerPtr *ownership.OwnerEntry
	if entry, ok := in.Store.Get(in.Resource, path); ok {
		ownerPtr = &entry
	}
	rev := in.Store.Revision(in.Resource, path)
	tok, _ := ComputeConditionToken(in.Resource, path, in.Snapshot)
	desired, declared := in.Declaration.Get(path)
	granted := in.Scope.PermissionsFor(path)

	applyPR, foundApply := findPathResult(preview.Apply.Gate.PathResults, path)
	prunePR, foundPrune := findPathResult(preview.Prune.Gate.PathResults, path)
	if !foundApply || !foundPrune {
		applyPR, prunePR = buildExternalPathResults(in, path, check)
	}

	diags := slices.Clone(applyPR.Diagnostics)
	diags = append(diags, prunePR.Diagnostics...)
	// A path's local decision does not describe resource-wide or recovery gates.
	diags = append(diags, preview.Apply.Diagnostics...)
	diags = append(diags, preview.Prune.Diagnostics...)
	model.SortDiagnostics(diags)
	diags = deduplicateDiagnostics(diags)

	return &Explanation{
		Resource:       in.Resource,
		Actor:          in.Actor,
		Path:           path.Clone(),
		Declared:       declared,
		DesiredValue:   desired,
		Observed:       check.Target,
		Ancestors:      check.Ancestors,
		Owner:          ownerPtr,
		OwnerRevision:  rev,
		ConditionToken: tok,
		GrantedPerms:   granted,
		Apply:          applyPR,
		Prune:          prunePR,
		ApplyExecution: preview.Apply.Execution,
		PruneExecution: preview.Prune.Execution,
		Diagnostics:    diags,
	}, nil
}

func findPathResult(results []PathResult, target model.Path) (PathResult, bool) {
	for _, pr := range results {
		if pr.Path.Equal(target) {
			return pr, true
		}
	}
	return PathResult{}, false
}

func buildExternalPathResults(in Input, p model.Path, check structure.PathCheckResult) (PathResult, PathResult) {
	diags := slices.Clone(check.Diagnostics)
	for _, fc := range ownership.FindForeignConflicts(in.Store, in.Resource, in.Actor, p) {
		diags = append(diags, fc.ToDiagnostic())
	}
	model.SortDiagnostics(diags)
	pr := PathResult{
		Path:         p.Clone(),
		Selection:    SelectNoOp,
		Decision:     DecisionNoOp,
		GrantedPerms: in.Scope.PermissionsFor(p),
		PrimaryCode:  selectPrimaryDiagnosticCode(diags),
		Diagnostics:  diags,
	}
	return pr, pr
}
