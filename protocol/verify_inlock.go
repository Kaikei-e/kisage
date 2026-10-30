package protocol

import (
	"fmt"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
	"github.com/Kaikei-e/kisage/structure"
)

// VerifyEnvironmentOpsInLock purely re-verifies all EnvironmentOps against snap, store, and sc
// inside the lock-protected critical section before ApplyBatch (ADR 0004 §2 Step 4, Counterexamples #4, #28, #40, #46).
//
// It checks:
//   - Provider expectations (Expected observation, ancestor structure, and CanSet/CanDelete rules).
//   - Runtime Scope permissions (sc.AuthorizeAction(op.Path, op.Action)).
//   - Shared ownership store σ (no foreign Actor overlap, and self ownership matches either the pre-intent state or the active Pending reservation for batchID).
func VerifyEnvironmentOpsInLock(
	snap *observation.Snapshot,
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	sc scope.Scope,
	batchID string,
	ops []plan.EnvironmentOp,
) []model.Diagnostic {
	diags := provider.VerifyBatchExpectations(snap, ops)
	for _, op := range ops {
		if err := sc.AuthorizeAction(op.Path, op.Action); err != nil {
			diags = append(diags, err.(model.Diagnostic))
		}
		if err := verifyOpOwnershipInLock(store, resID, actor, batchID, op); err != nil {
			diags = append(diags, err.(model.Diagnostic))
		}
	}
	model.SortDiagnostics(diags)
	return diags
}

// VerifyMetadataCandidatesInLock purely re-verifies all MetadataCandidates against snap, store, and sc
// inside the lock-protected critical section (ADR 0004 §2 Step 4 & Step 5, Counterexamples #23, #46, #57).
//
// It checks:
//   - Target and proper ancestor observations have no Unknown (K301), and ExpectedObserved still matches snap(mc.Path).
//   - For AdoptModified: target is Present(Leaf) with valid Present(Record) ancestors.
//   - For ClearRecord: target is confirmed Absent with valid Present(Record) ancestors.
//   - Runtime Scope permissions (Own permission via sc.AuthorizeAction(mc.Path, mc.Action)).
//   - Shared ownership store σ (no foreign overlap, ExpectedRevision matches, and current entry matches mc.Before).
func VerifyMetadataCandidatesInLock(
	snap *observation.Snapshot,
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	sc scope.Scope,
	candidates []plan.MetadataCandidate,
) []model.Diagnostic {
	var diags []model.Diagnostic
	for _, mc := range candidates {
		if snap == nil {
			diags = append(diags, model.Diagnostic{
				Code:   model.CodeObservationUnavailable,
				Path:   mc.Path.Clone(),
				Reason: "observation snapshot must not be nil",
			})
			continue
		}
		check := structure.InspectPath(snap, mc.Path)
		if check.HasUnknown() {
			if d, ok := check.PrimaryDiagnostic(); ok {
				diags = append(diags, d)
			}
			continue
		}
		if !check.Target.Equal(mc.ExpectedObserved) {
			diags = append(diags, model.Diagnostic{
				Code:       model.CodeExternalDrift,
				Path:       mc.Path.Clone(),
				Reason:     fmt.Sprintf("metadata candidate condition mismatch at %s: expected %s, observed %s", mc.Path, mc.ExpectedObserved, check.Target),
				NextAction: "re-run operation to plan against the latest environment state",
			})
		}
		if mc.Action == scope.ActionAdoptModified && (!check.Target.IsLeaf() || !check.CanSet()) {
			diags = append(diags, model.Diagnostic{
				Code:   model.CodeStructureConflict,
				Path:   mc.Path.Clone(),
				Reason: fmt.Sprintf("AdoptModified at %s requires Present(Leaf) with Present(Record) ancestors", mc.Path),
			})
		}
		if mc.Action == scope.ActionClearRecord && (!check.Target.IsAbsent() || len(check.Diagnostics) > 0) {
			diags = append(diags, model.Diagnostic{
				Code:   model.CodeExternalDrift,
				Path:   mc.Path.Clone(),
				Reason: fmt.Sprintf("ClearRecord at %s requires confirmed Absent with valid ancestors", mc.Path),
			})
		}
		if err := sc.AuthorizeAction(mc.Path, mc.Action); err != nil {
			diags = append(diags, err.(model.Diagnostic))
		}
		if err := verifyMetadataOwnershipInLock(store, resID, actor, mc); err != nil {
			diags = append(diags, err.(model.Diagnostic))
		}
	}
	model.SortDiagnostics(diags)
	return diags
}

func verifyOpOwnershipInLock(
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	batchID string,
	op plan.EnvironmentOp,
) error {
	if store == nil {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   op.Path.Clone(),
			Reason: "ownership store must not be nil",
		}
	}
	for _, fc := range ownership.FindForeignConflicts(store, resID, actor, op.Path) {
		return fc.ToDiagnostic()
	}
	cur, exists := store.Get(resID, op.Path)
	if !exists {
		if op.Previous == nil {
			return nil
		}
		return model.Diagnostic{
			Code:   model.CodeOwnershipConflict,
			Path:   op.Path.Clone(),
			Reason: fmt.Sprintf("expected settled ownership for actor %q at %s, found unowned", actor, op.Path),
		}
	}
	if cur.Manager != actor {
		return model.Diagnostic{
			Code:   model.CodeOwnershipConflict,
			Path:   op.Path.Clone(),
			Reason: fmt.Sprintf("path %s is owned by foreign actor %q", op.Path, cur.Manager),
		}
	}
	if cur.Pending != nil {
		if batchID != "" && cur.Pending.BatchID == batchID && cur.Pending.Op == op.Op &&
			cur.Pending.Expected.Equal(op.Expected) &&
			(op.Op != ownership.PendingOpSet || model.EqualLeafValues(cur.Pending.Target, op.Target)) {
			return nil
		}
		return model.Diagnostic{
			Code:   model.CodePendingConflict,
			Path:   op.Path.Clone(),
			Reason: fmt.Sprintf("path %s has conflicting Pending reservation (batch %q)", op.Path, cur.Pending.BatchID),
		}
	}
	if op.Previous == nil {
		return model.Diagnostic{
			Code:   model.CodeOwnershipConflict,
			Path:   op.Path.Clone(),
			Reason: fmt.Sprintf("path %s acquired unexpected ownership before ApplyBatch", op.Path),
		}
	}
	if cur.Origin != op.Previous.Origin || !model.EqualLeafValues(cur.LastApplied, op.Previous.LastApplied) {
		return model.Diagnostic{
			Code:   model.CodeOwnershipConflict,
			Path:   op.Path.Clone(),
			Reason: fmt.Sprintf("ownership state at %s changed before ApplyBatch", op.Path),
		}
	}
	return nil
}

func verifyMetadataOwnershipInLock(
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	mc plan.MetadataCandidate,
) error {
	if store == nil {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   mc.Path.Clone(),
			Reason: "ownership store must not be nil",
		}
	}
	for _, fc := range ownership.FindForeignConflicts(store, resID, actor, mc.Path) {
		return fc.ToDiagnostic()
	}
	if err := store.VerifyRevision(resID, mc.Path, mc.ExpectedRevision); err != nil {
		return err
	}
	cur, exists := store.Get(resID, mc.Path)
	if mc.Before == nil {
		if exists {
			return model.Diagnostic{
				Code:   model.CodeOwnershipConflict,
				Path:   mc.Path.Clone(),
				Reason: fmt.Sprintf("expected unowned path at %s for AdoptModified, found existing record", mc.Path),
			}
		}
		return nil
	}
	if !exists || !cur.Equal(*mc.Before) {
		return model.Diagnostic{
			Code:   model.CodeOwnershipConflict,
			Path:   mc.Path.Clone(),
			Reason: fmt.Sprintf("ownership record at %s changed since planning", mc.Path),
		}
	}
	return nil
}
