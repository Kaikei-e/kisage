package provider

import (
	"fmt"
	"slices"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/structure"
)

// VerifyAppliedBatchContract performs the pure in-lock post-ApplyBatch contract verification (ADR 0004 §2 Step 5, Counterexamples #18, #32, #45):
//   - For every Set(p, v) in ops: afterSnap(p) == Present(Leaf(v)) with Equal(v, Target), and all proper ancestors q ≺ p are Present(Record).
//   - For every Delete(p) in ops: afterSnap(p) == Absent, and all proper ancestors q ≺ p remain Present(Record) (empty intermediate Records are preserved, Counterexample #32).
//   - Frame Condition: every non-target path q ∉ ops retains its exact pre-apply observation (sibling leaf values, existing Records, and Opaque nodes are preserved).
//
// Any violation returns a slice of K501 (CodeProviderContractViolation) diagnostics.
func VerifyAppliedBatchContract(
	beforeSnap *observation.Snapshot,
	afterSnap *observation.Snapshot,
	ops []plan.EnvironmentOp,
) []model.Diagnostic {
	if beforeSnap == nil || afterSnap == nil {
		return []model.Diagnostic{{
			Code:       model.CodeProviderContractViolation,
			Reason:     "post-apply contract verification requires non-nil before and after observation snapshots",
			NextAction: plan.NextActionInspectQuarantine,
		}}
	}

	targetSet := make(map[string]bool, len(ops))
	setTargetPaths := make([]model.Path, 0, len(ops))
	var diags []model.Diagnostic
	for _, op := range ops {
		targetSet[op.Path.String()] = true
		if op.Op == ownership.PendingOpSet {
			setTargetPaths = append(setTargetPaths, op.Path)
		}
		diags = append(diags, verifyOpPostCondition(afterSnap, op)...)
	}
	diags = append(diags, verifySnapshotFrameContract(beforeSnap, afterSnap, targetSet, setTargetPaths)...)
	model.SortDiagnostics(diags)
	return diags
}

func verifyOpPostCondition(afterSnap *observation.Snapshot, op plan.EnvironmentOp) []model.Diagnostic {
	check := structure.InspectPath(afterSnap, op.Path)
	var diags []model.Diagnostic

	for _, anc := range check.Ancestors {
		if !anc.Observed.IsRecord() {
			diags = append(diags, model.Diagnostic{
				Code:       model.CodeProviderContractViolation,
				Path:       op.Path.Clone(),
				Reason:     fmt.Sprintf("provider contract violation on %s: proper ancestor %s must be Present(Record) after %s, observed %s", op.Path, anc.Path, op.Op, anc.Observed),
				NextAction: plan.NextActionInspectQuarantine,
			})
		}
	}

	if op.Op == ownership.PendingOpSet {
		leafVal, ok := check.Target.LeafValue()
		if !ok || !model.EqualLeafValues(leafVal, op.Target) {
			diags = append(diags, model.Diagnostic{
				Code:       model.CodeProviderContractViolation,
				Path:       op.Path.Clone(),
				Reason:     fmt.Sprintf("provider contract violation on Set(%s): expected Present(Leaf(%v)), observed %s", op.Path, op.Target, check.Target),
				NextAction: plan.NextActionInspectQuarantine,
			})
		}
		return diags
	}

	if !check.Target.IsAbsent() {
		diags = append(diags, model.Diagnostic{
			Code:       model.CodeProviderContractViolation,
			Path:       op.Path.Clone(),
			Reason:     fmt.Sprintf("provider contract violation on Delete(%s): expected Absent, observed %s", op.Path, check.Target),
			NextAction: plan.NextActionInspectQuarantine,
		})
	}
	return diags
}

func verifySnapshotFrameContract(
	beforeSnap *observation.Snapshot,
	afterSnap *observation.Snapshot,
	targetSet map[string]bool,
	setTargetPaths []model.Path,
) []model.Diagnostic {
	unionPaths := collectSnapshotUnionPaths(beforeSnap, afterSnap)
	var diags []model.Diagnostic

	for _, p := range unionPaths {
		bObs := beforeSnap.Get(p)
		aObs := afterSnap.Get(p)

		// Existing Records must remain Present(Record) even after child deletions (ADR 0001 §4, Counterexample #32).
		if bObs.IsRecord() && !aObs.IsRecord() {
			diags = append(diags, model.Diagnostic{
				Code:       model.CodeProviderContractViolation,
				Path:       p.Clone(),
				Reason:     fmt.Sprintf("provider contract violation: existing Record at %s changed to %s", p, aObs),
				NextAction: plan.NextActionInspectQuarantine,
			})
			continue
		}
		if targetSet[p.String()] {
			continue
		}
		// Implicitly created intermediate ancestor Records from Absent -> Present(Record) are permitted on proper ancestors of a Set target,
		// whereas non-target Leaf/Opaque/Absent observations must remain identical.
		if bObs.IsAbsent() && aObs.IsRecord() && isAncestorOfAnySetTarget(p, setTargetPaths) {
			continue
		}
		if !bObs.Equal(aObs) {
			diags = append(diags, model.Diagnostic{
				Code:       model.CodeProviderContractViolation,
				Path:       p.Clone(),
				Reason:     fmt.Sprintf("provider contract violation: non-target path %s changed from %s to %s", p, bObs, aObs),
				NextAction: plan.NextActionInspectQuarantine,
			})
		}
	}
	return diags
}

func isAncestorOfAnySetTarget(anc model.Path, setTargetPaths []model.Path) bool {
	for _, tPath := range setTargetPaths {
		if anc.IsProperAncestorOf(tPath) {
			return true
		}
	}
	return false
}

// EvaluateStabilize purely evaluates the outcome of a Provider.Stabilize operation by checking
// syncErr and verifying that the re-observed currentSnap matches expectedSnap on all inspected paths (ADR 0004 §2–§3, Counterexamples #43, #50, #57).
func EvaluateStabilize(
	expectedSnap *observation.Snapshot,
	currentSnap *observation.Snapshot,
	checkPaths []model.Path,
	syncErr error,
) StabilizeResult {
	if syncErr != nil {
		return StabilizeResult{
			Outcome: StabilizeIndeterminate,
			Diagnostics: []model.Diagnostic{{
				Code:       model.CodeIndeterminateOutcome,
				Reason:     fmt.Sprintf("Stabilize fsync or re-observation failed: %v", syncErr),
				NextAction: "inspect filesystem durability and re-run to recover or verify state",
				Cause:      syncErr,
			}},
		}
	}
	if expectedSnap == nil || currentSnap == nil {
		return StabilizeResult{
			Outcome: StabilizeIndeterminate,
			Diagnostics: []model.Diagnostic{{
				Code:       model.CodeIndeterminateOutcome,
				Reason:     "Stabilize requires non-nil expected and current observation snapshots",
				NextAction: "re-observe resource state before committing metadata",
			}},
		}
	}

	paths := collectStabilizePaths(expectedSnap, currentSnap, checkPaths)
	var conflictDiags []model.Diagnostic
	for _, p := range paths {
		expObs := expectedSnap.Get(p)
		curObs := currentSnap.Get(p)
		if curObs.IsUnknown() {
			return StabilizeResult{
				Outcome: StabilizeIndeterminate,
				Diagnostics: []model.Diagnostic{{
					Code:       model.CodeIndeterminateOutcome,
					Path:       p.Clone(),
					Reason:     fmt.Sprintf("Stabilize re-observation for %s is Unknown: %s", p, curObs.UnknownReason()),
					NextAction: "resolve observation failure before committing ownership records",
				}},
			}
		}
		if !curObs.Equal(expObs) {
			conflictDiags = append(conflictDiags, model.Diagnostic{
				Code:       model.CodeExternalDrift,
				Path:       p.Clone(),
				Reason:     fmt.Sprintf("Stabilize detected state change at %s: expected %s, re-observed %s", p, expObs, curObs),
				NextAction: "re-run operation to plan against the latest environment state",
			})
		}
	}
	if len(conflictDiags) > 0 {
		model.SortDiagnostics(conflictDiags)
		return StabilizeResult{
			Outcome:     StabilizeConflict,
			Diagnostics: conflictDiags,
		}
	}
	return StabilizeResult{
		Outcome:         StabilizeDurable,
		DurableSnapshot: currentSnap,
	}
}

func collectStabilizePaths(expected, current *observation.Snapshot, checkPaths []model.Path) []model.Path {
	seen := make(map[string]model.Path)
	for _, p := range collectSnapshotUnionPaths(expected, current) {
		seen[p.String()] = p
	}
	for _, p := range checkPaths {
		if len(p) == 0 {
			continue
		}
		for _, anc := range p.Ancestors() {
			seen[anc.String()] = anc
		}
		seen[p.String()] = p.Clone()
	}
	out := make([]model.Path, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	slices.SortFunc(out, model.Path.Compare)
	return out
}

func collectSnapshotUnionPaths(a, b *observation.Snapshot) []model.Path {
	seen := make(map[string]model.Path)
	for _, p := range a.Paths() {
		seen[p.String()] = p
	}
	for _, p := range b.Paths() {
		seen[p.String()] = p
	}
	out := make([]model.Path, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	slices.SortFunc(out, model.Path.Compare)
	return out
}
