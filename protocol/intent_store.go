package protocol

import (
	"fmt"
	"math"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
)

// ApplyDurableIntentToStore purely computes the Step 3 ownership store containing Pending reservations
// and PreviousEntry snapshots for all EnvironmentOps in intent (ADR 0004 §1, §2 Step 3, Counterexamples #57, #58).
// Existing settled Origin and LastApplied values are preserved while Pending is attached.
func ApplyDurableIntentToStore(
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	intent *DurableIntent,
) (*ownership.Store, error) {
	if store == nil {
		return nil, model.Diagnostic{Code: model.CodeInvalidInput, Reason: "ownership store must not be nil"}
	}
	if err := ownership.ValidateResourceID(resID); err != nil {
		return nil, err
	}
	if err := ownership.ValidateActorID(actor); err != nil {
		return nil, err
	}
	if intent == nil || len(intent.EnvironmentOps) == 0 {
		return store.Clone(), nil
	}
	if err := validateBatchID(intent.BatchID); err != nil {
		return nil, err
	}

	next := store.Clone()
	for _, op := range intent.EnvironmentOps {
		curRev := next.Revision(resID, op.Path)
		if curRev >= math.MaxUint64-1 {
			return nil, model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Path:   op.Path.Clone(),
				Reason: "ownership revision exhausted: insufficient headroom for two-phase intent",
			}
		}
		rev := curRev + 1
		pendingEntry, err := buildPendingEntryForOp(next, resID, actor, intent.BatchID, op, rev)
		if err != nil {
			return nil, err
		}
		next, err = next.Put(resID, op.Path, pendingEntry)
		if err != nil {
			return nil, err
		}
	}
	return next, nil
}

// RollbackDurableIntentInStore purely reverts all Pending reservations from intent back to their
// PreviousEntry settled states (or deletes Origin=None creation reservations) when ApplyBatch
// returns ReceiptNotApplied (ADR 0004 §2 Step 6, Counterexamples #4, #46).
func RollbackDurableIntentInStore(
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	intent *DurableIntent,
) (*ownership.Store, error) {
	if store == nil {
		return nil, model.Diagnostic{Code: model.CodeInvalidInput, Reason: "ownership store must not be nil"}
	}
	if intent == nil || len(intent.EnvironmentOps) == 0 {
		return store.Clone(), nil
	}

	next := store.Clone()
	for _, op := range intent.EnvironmentOps {
		cur, ok := next.Get(resID, op.Path)
		if !ok {
			return nil, model.Diagnostic{
				Code:   model.CodePendingConflict,
				Path:   op.Path.Clone(),
				Reason: fmt.Sprintf("cannot roll back Pending at %s: pending entry is missing", op.Path),
			}
		}
		if cur.Manager != actor || cur.Pending == nil || cur.Pending.BatchID != intent.BatchID {
			return nil, model.Diagnostic{
				Code:   model.CodePendingConflict,
				Path:   op.Path.Clone(),
				Reason: fmt.Sprintf("cannot roll back Pending at %s: active entry does not match actor %q batch %q", op.Path, actor, intent.BatchID),
			}
		}
		curRev := next.Revision(resID, op.Path)
		if curRev == math.MaxUint64 {
			return nil, model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Path:   op.Path.Clone(),
				Reason: "ownership revision exhausted: cannot advance beyond uint64 maximum",
			}
		}
		restored, err := ownership.RollbackPending(cur, curRev+1)
		if err != nil {
			return nil, err
		}
		if restored == nil {
			next, err = next.Delete(resID, op.Path)
		} else {
			next, err = next.Put(resID, op.Path, *restored)
		}
		if err != nil {
			return nil, err
		}
	}
	return next, nil
}

func buildPendingEntryForOp(
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	batchID string,
	op plan.EnvironmentOp,
	rev uint64,
) (ownership.OwnerEntry, error) {
	cur, exists := store.Get(resID, op.Path)
	if op.Previous == nil {
		if exists {
			return ownership.OwnerEntry{}, model.Diagnostic{
				Code:   model.CodeOwnershipConflict,
				Path:   op.Path.Clone(),
				Reason: fmt.Sprintf("cannot create unowned Pending reservation at %s: ownership entry already exists", op.Path),
			}
		}
		return ownership.NewPendingCreate(actor, batchID, op.Target, rev)
	}
	if !exists || cur.Manager != actor || !cur.IsSettled() {
		return ownership.OwnerEntry{}, model.Diagnostic{
			Code:   model.CodeOwnershipConflict,
			Path:   op.Path.Clone(),
			Reason: fmt.Sprintf("cannot attach Pending operation at %s: settled ownership for actor %q not found", op.Path, actor),
		}
	}
	if cur.Origin != op.Previous.Origin || !model.EqualLeafValues(cur.LastApplied, op.Previous.LastApplied) {
		return ownership.OwnerEntry{}, model.Diagnostic{
			Code:   model.CodeOwnershipConflict,
			Path:   op.Path.Clone(),
			Reason: fmt.Sprintf("settled ownership state at %s changed before Intent persistence", op.Path),
		}
	}
	if op.Op == ownership.PendingOpSet {
		return ownership.BeginSetUpdate(cur, batchID, op.Target, rev)
	}
	return ownership.BeginDelete(cur, batchID, rev)
}
