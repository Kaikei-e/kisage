package provider

import (
	"fmt"
	"slices"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/structure"
)

// ValidateBatchPaths checks that all paths are valid leaf paths and pairwise non-overlapping
// (neither duplicate nor proper ancestor/descendant) as required by ADR 0004 §2.
func ValidateBatchPaths(paths []model.Path) error {
	for i, p := range paths {
		if err := model.ValidateLeafPath(p); err != nil {
			return err
		}
		for j := i + 1; j < len(paths); j++ {
			q := paths[j]
			if p.Overlaps(q) {
				return model.Diagnostic{
					Code:   model.CodeStructureConflict,
					Path:   p.Clone(),
					Reason: fmt.Sprintf("batch contains overlapping target paths %s and %s; a batch may only contain mutually non-overlapping leaf paths", p, q),
				}
			}
		}
	}
	return nil
}

// VerifyOpExpectation checks that a single EnvironmentOp's structural preconditions and Expected
// observation match snap (ADR 0004 §2 Step 4, Counterexamples #4, #10, #13, #28).
func VerifyOpExpectation(snap *observation.Snapshot, op plan.EnvironmentOp) error {
	if snap == nil {
		return model.Diagnostic{
			Code:   model.CodeObservationUnavailable,
			Path:   op.Path.Clone(),
			Reason: "observation snapshot must not be nil",
		}
	}
	if err := model.ValidateLeafPath(op.Path); err != nil {
		return err
	}
	if !op.Op.IsValid() {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   op.Path.Clone(),
			Reason: fmt.Sprintf("invalid environment operation %v", op.Op),
		}
	}

	check := structure.InspectPath(snap, op.Path)
	if err := check.Err(); err != nil {
		return err
	}
	if op.Op == ownership.PendingOpSet {
		if err := structure.ValidateSet(snap, op.Path, op.Target); err != nil {
			return err
		}
		if v, ok := op.Expected.LeafValue(); ok && model.EqualLeafValues(v, op.Target) {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Path:   op.Path.Clone(),
				Reason: fmt.Sprintf("Set(%s) Expected equals Target; no-op Set operations are forbidden", op.Path),
			}
		}
	} else {
		if err := structure.ValidateDelete(snap, op.Path); err != nil {
			return err
		}
	}

	if !check.Target.Equal(op.Expected) {
		return model.Diagnostic{
			Code:       model.CodeExternalDrift,
			Path:       op.Path.Clone(),
			Reason:     fmt.Sprintf("precondition mismatch at %s: expected %s, observed %s", op.Path, op.Expected, check.Target),
			NextAction: "re-run plan or apply to observe the latest environment state",
		}
	}
	return nil
}

// VerifyBatchExpectations checks all EnvironmentOps against snap in deterministic path order.
// If any single operation fails structural or Expected checks, all Diagnostics are collected
// and returned so the entire batch is NotApplied with zero environment changes (Counterexamples #4, #13, #28).
func VerifyBatchExpectations(snap *observation.Snapshot, ops []plan.EnvironmentOp) []model.Diagnostic {
	if len(ops) == 0 {
		return []model.Diagnostic{{
			Code:   model.CodeInvalidInput,
			Reason: "ApplyBatch requires at least one EnvironmentOp",
		}}
	}
	paths := make([]model.Path, len(ops))
	for i, op := range ops {
		paths[i] = op.Path
	}
	if err := ValidateBatchPaths(paths); err != nil {
		if d, ok := err.(model.Diagnostic); ok {
			return []model.Diagnostic{d}
		}
		return []model.Diagnostic{{Code: model.CodeInvalidInput, Reason: err.Error(), Cause: err}}
	}

	sortedOps := slices.Clone(ops)
	slices.SortFunc(sortedOps, func(a, b plan.EnvironmentOp) int {
		return a.Path.Compare(b.Path)
	})

	var diags []model.Diagnostic
	for _, op := range sortedOps {
		if err := VerifyOpExpectation(snap, op); err != nil {
			if d, ok := err.(model.Diagnostic); ok {
				diags = append(diags, d)
			} else {
				diags = append(diags, model.Diagnostic{
					Code:   model.CodeInvalidInput,
					Path:   op.Path.Clone(),
					Reason: err.Error(),
					Cause:  err,
				})
			}
		}
	}
	model.SortDiagnostics(diags)
	return diags
}

// ApplyBatchToBytes purely parses src as an LKAL document, verifies all Expected and structural
// preconditions for ops, applies all changes in memory while preserving comments and sibling
// values, verifies the Frame Condition, and returns the patched bytes (ADR 0004 §2, §5; Counterexamples #1, #4, #13, #28, #32).
// If any check fails, ApplyBatchToBytes returns ReceiptNotApplied and nil bytes.
func ApplyBatchToBytes(name string, src []byte, batchID string, ops []plan.EnvironmentOp) ([]byte, *lkal.File, Receipt) {
	if batchID == "" {
		return nil, nil, Receipt{
			Outcome: ReceiptNotApplied,
			Diagnostics: []model.Diagnostic{{
				Code:   model.CodeInvalidInput,
				Reason: "batch ID must not be empty",
			}},
		}
	}
	f, err := lkal.Parse(name, src)
	if err != nil {
		return nil, nil, Receipt{
			BatchID: batchID,
			Outcome: ReceiptNotApplied,
			Diagnostics: []model.Diagnostic{{
				Code:   model.CodeObservationUnavailable,
				Reason: fmt.Sprintf("failed to parse LKAL target file %q: %v", name, err),
				Cause:  err,
			}},
		}
	}

	targetPaths := extractOpPaths(ops)
	beforeRecord := f.Data()
	snap := observation.ObserveRecord(beforeRecord, targetPaths...)
	if diags := VerifyBatchExpectations(snap, ops); len(diags) > 0 {
		return nil, nil, Receipt{
			BatchID:     batchID,
			Outcome:     ReceiptNotApplied,
			Diagnostics: diags,
		}
	}

	sortedOps := slices.Clone(ops)
	slices.SortFunc(sortedOps, func(a, b plan.EnvironmentOp) int {
		return a.Path.Compare(b.Path)
	})

	for _, op := range sortedOps {
		var applyErr error
		if op.Op == ownership.PendingOpSet {
			applyErr = structure.ApplyLeafSet(f, op.Path, op.Target)
		} else {
			applyErr = structure.ApplyLeafDelete(f, op.Path)
		}
		if applyErr != nil {
			return nil, nil, receiptFromError(batchID, op.Path, applyErr)
		}
	}

	if err := structure.VerifyFrameCondition(beforeRecord, f.Data(), targetPaths...); err != nil {
		return nil, nil, receiptFromError(batchID, nil, err)
	}
	return f.Format(), f, Receipt{
		BatchID: batchID,
		Outcome: ReceiptApplied,
	}
}

func extractOpPaths(ops []plan.EnvironmentOp) []model.Path {
	out := make([]model.Path, len(ops))
	for i, op := range ops {
		out[i] = op.Path.Clone()
	}
	return out
}

func receiptFromError(batchID string, p model.Path, err error) Receipt {
	if d, ok := err.(model.Diagnostic); ok {
		return Receipt{
			BatchID:     batchID,
			Outcome:     ReceiptNotApplied,
			Diagnostics: []model.Diagnostic{d.WithPath(p)},
		}
	}
	return Receipt{
		BatchID: batchID,
		Outcome: ReceiptNotApplied,
		Diagnostics: []model.Diagnostic{{
			Code:   model.CodeInvalidInput,
			Path:   p.Clone(),
			Reason: err.Error(),
			Cause:  err,
		}},
	}
}
