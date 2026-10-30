package protocol

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
)

// ExecutionBatch represents the volatile in-memory execution plan combining environment operations
// and metadata-only candidates (ADR 0004 §1):
//
//	ExecutionBatch = { BatchID, EnvironmentOps[], MetadataCandidates[] }
type ExecutionBatch struct {
	BatchID            string
	EnvironmentOps     []plan.EnvironmentOp
	MetadataCandidates []plan.MetadataCandidate
}

// HasWork reports whether b contains at least one EnvironmentOp or MetadataCandidate.
func (b ExecutionBatch) HasWork() bool {
	return len(b.EnvironmentOps) > 0 || len(b.MetadataCandidates) > 0
}

// IsMetadataOnly reports whether b contains MetadataCandidates and zero EnvironmentOps (ADR 0004 §2, Theorem 11, Counterexample #57).
func (b ExecutionBatch) IsMetadataOnly() bool {
	return len(b.EnvironmentOps) == 0 && len(b.MetadataCandidates) > 0
}

// Clone returns a deep defensive copy of b.
func (b ExecutionBatch) Clone() ExecutionBatch {
	envOps := make([]plan.EnvironmentOp, len(b.EnvironmentOps))
	for i, op := range b.EnvironmentOps {
		envOps[i] = op.Clone()
	}
	meta := make([]plan.MetadataCandidate, len(b.MetadataCandidates))
	for i, mc := range b.MetadataCandidates {
		meta[i] = mc.Clone()
	}
	return ExecutionBatch{
		BatchID:            b.BatchID,
		EnvironmentOps:     envOps,
		MetadataCandidates: meta,
	}
}

// DurableIntent represents the persistent environment-only intent saved in Step 3 before ApplyBatch (ADR 0004 §1):
//
//	DurableIntent = { BatchID, EnvironmentOps[] }
//
// MetadataCandidates are never included in DurableIntent and never persisted as Pending reservations (Theorem 11, Counterexamples #57, #58).
type DurableIntent struct {
	BatchID        string
	EnvironmentOps []plan.EnvironmentOp
}

// Clone returns a deep defensive copy of d.
func (d *DurableIntent) Clone() *DurableIntent {
	if d == nil {
		return nil
	}
	ops := make([]plan.EnvironmentOp, len(d.EnvironmentOps))
	for i, op := range d.EnvironmentOps {
		ops[i] = op.Clone()
	}
	return &DurableIntent{
		BatchID:        d.BatchID,
		EnvironmentOps: ops,
	}
}

// NewExecutionBatch constructs and validates an ExecutionBatch from a correlation batchID and a GateOutcome (ADR 0004 §1–§2).
func NewExecutionBatch(batchID string, gate plan.GateOutcome) (ExecutionBatch, error) {
	if gate.Blocked {
		return ExecutionBatch{}, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "cannot construct an ExecutionBatch from a Blocked GateOutcome",
		}
	}
	batch := ExecutionBatch{
		BatchID:            batchID,
		EnvironmentOps:     make([]plan.EnvironmentOp, len(gate.Changes)),
		MetadataCandidates: make([]plan.MetadataCandidate, len(gate.MetadataCandidates)),
	}
	for i, op := range gate.Changes {
		batch.EnvironmentOps[i] = op.Clone()
	}
	for i, mc := range gate.MetadataCandidates {
		batch.MetadataCandidates[i] = mc.Clone()
	}
	if err := ValidateExecutionBatch(batch); err != nil {
		return ExecutionBatch{}, err
	}
	return batch, nil
}

// ValidateExecutionBatch purely validates all ADR 0004 §1–§2 structural invariants on batch:
//   - Valid BatchID when batch has work.
//   - All target paths across EnvironmentOps and MetadataCandidates are valid leaf paths and pairwise non-overlapping.
//   - Every EnvironmentOp is a valid Set (with Target != Expected) or Delete (on Origin=Created).
//   - Every MetadataCandidate is a valid metadata-only transition with no Pending reservation.
func ValidateExecutionBatch(batch ExecutionBatch) error {
	if !batch.HasWork() {
		return nil
	}
	if err := validateBatchID(batch.BatchID); err != nil {
		return err
	}

	allPaths := make([]model.Path, 0, len(batch.EnvironmentOps)+len(batch.MetadataCandidates))
	for _, op := range batch.EnvironmentOps {
		if err := validateEnvironmentOp(op); err != nil {
			return err
		}
		allPaths = append(allPaths, op.Path)
	}
	for _, mc := range batch.MetadataCandidates {
		if err := validateMetadataCandidate(mc); err != nil {
			return err
		}
		allPaths = append(allPaths, mc.Path)
	}
	return provider.ValidateBatchPaths(allPaths)
}

// ExtractDurableIntent separates the persistent DurableIntent from a volatile ExecutionBatch (ADR 0004 §1, §2 Step 3).
// When EnvironmentOps is empty (e.g., a metadata-only batch, Counterexample #57), ExtractDurableIntent
// returns (nil, false, nil) so Step 3 is skipped and no Pending reservation is ever written.
func ExtractDurableIntent(batch ExecutionBatch) (*DurableIntent, bool, error) {
	if err := ValidateExecutionBatch(batch); err != nil {
		return nil, false, err
	}
	if len(batch.EnvironmentOps) == 0 {
		return nil, false, nil
	}
	ops := make([]plan.EnvironmentOp, len(batch.EnvironmentOps))
	for i, op := range batch.EnvironmentOps {
		ops[i] = op.Clone()
	}
	slices.SortFunc(ops, func(a, b plan.EnvironmentOp) int {
		return a.Path.Compare(b.Path)
	})
	return &DurableIntent{
		BatchID:        batch.BatchID,
		EnvironmentOps: ops,
	}, true, nil
}

func validateEnvironmentOp(op plan.EnvironmentOp) error {
	if err := model.ValidateLeafPath(op.Path); err != nil {
		return err
	}
	if !op.Op.IsValid() {
		return model.Diagnostic{Code: model.CodeInvalidInput, Path: op.Path.Clone(), Reason: "invalid PendingOp"}
	}
	if op.Op == ownership.PendingOpSet {
		if _, err := model.ValidateLeafValue(op.Target); err != nil {
			return err.(model.Diagnostic).WithPath(op.Path)
		}
		if v, ok := op.Expected.LeafValue(); ok && model.EqualLeafValues(v, op.Target) {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Path:   op.Path.Clone(),
				Reason: "EnvironmentOp Set must not have Expected equal to Target",
			}
		}
		return nil
	}
	if op.Target != nil || op.Previous == nil || op.Previous.Origin != ownership.OriginCreated {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   op.Path.Clone(),
			Reason: "EnvironmentOp Delete requires Origin=Created Previous state and nil Target",
		}
	}
	return nil
}

func validateMetadataCandidate(mc plan.MetadataCandidate) error {
	if err := model.ValidateLeafPath(mc.Path); err != nil {
		return err
	}
	if mc.Action != scope.ActionAdoptModified && mc.Action != scope.ActionUnmanage && mc.Action != scope.ActionClearRecord {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   mc.Path.Clone(),
			Reason: fmt.Sprintf("invalid MetadataCandidate action %s", mc.Action),
		}
	}
	if mc.After != nil && mc.After.HasPending() {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   mc.Path.Clone(),
			Reason: "MetadataCandidate must never create a Pending reservation",
		}
	}
	return nil
}

func validateBatchID(id string) error {
	if id == "" || strings.TrimSpace(id) != id {
		return model.Diagnostic{Code: model.CodeInvalidInput, Reason: "batch ID must be non-empty without leading or trailing whitespace"}
	}
	for _, r := range id {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return model.Diagnostic{Code: model.CodeInvalidInput, Reason: fmt.Sprintf("batch ID %q contains invalid character", id)}
		}
	}
	return nil
}
