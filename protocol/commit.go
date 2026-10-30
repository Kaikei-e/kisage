package protocol

import (
	"fmt"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
)

// CommitExecutionBatchToStore purely computes the single atomic Step 5 store Commit combining:
//   - Finalized EnvironmentOps:
//   - Set on Origin=None (unowned creation) -> Origin=Created, LastApplied=Target, Pending=nil (Counterexample #2).
//   - Set on settled Origin=Created|Modified -> preserves Origin, updates LastApplied=Target, Pending=nil (Counterexample #34).
//   - Delete on Origin=Created -> deletes ownership record (Counterexample #29).
//   - Finalized MetadataCandidates:
//   - AdoptModified -> Origin=Modified, LastApplied=Target, Pending=nil (Counterexample #11).
//   - Unmanage / ClearRecord -> deletes ownership record (Counterexamples #5, #53).
//
// In accordance with ADR 0004 §2 Step 5, all environment Pending reservations for batch.BatchID
// are resolved in the same atomic store transition.
func CommitExecutionBatchToStore(
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	batch ExecutionBatch,
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
	if err := ValidateExecutionBatch(batch); err != nil {
		return nil, err
	}

	next := store.Clone()
	var err error
	for _, op := range batch.EnvironmentOps {
		next, err = commitEnvironmentOpToStore(next, resID, actor, batch.BatchID, op)
		if err != nil {
			return nil, err
		}
	}
	for _, mc := range batch.MetadataCandidates {
		next, err = commitMetadataCandidateToStore(next, resID, actor, mc)
		if err != nil {
			return nil, err
		}
	}
	return next, nil
}

func commitEnvironmentOpToStore(
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	batchID string,
	op plan.EnvironmentOp,
) (*ownership.Store, error) {
	cur, exists := store.Get(resID, op.Path)
	if !exists || cur.Manager != actor || cur.Pending == nil || cur.Pending.BatchID != batchID || cur.Pending.Op != op.Op {
		return nil, model.Diagnostic{
			Code:   model.CodePendingConflict,
			Path:   op.Path.Clone(),
			Reason: fmt.Sprintf("cannot commit EnvironmentOp at %s: active Pending intent for actor %q batch %q not found", op.Path, actor, batchID),
		}
	}
	if op.Op == ownership.PendingOpDelete {
		return store.Delete(resID, op.Path)
	}
	rev := store.Revision(resID, op.Path) + 1
	settled, err := ownership.CommitConfirmedSet(cur, rev)
	if err != nil {
		return nil, err
	}
	return store.Put(resID, op.Path, settled)
}

func commitMetadataCandidateToStore(
	store *ownership.Store,
	resID ownership.ResourceID,
	actor ownership.ActorID,
	mc plan.MetadataCandidate,
) (*ownership.Store, error) {
	if err := store.VerifyRevision(resID, mc.Path, mc.ExpectedRevision); err != nil {
		return nil, err
	}
	if mc.After == nil {
		cur, exists := store.Get(resID, mc.Path)
		if !exists || cur.Manager != actor || !cur.IsSettled() {
			return nil, model.Diagnostic{
				Code:   model.CodeOwnershipConflict,
				Path:   mc.Path.Clone(),
				Reason: fmt.Sprintf("cannot commit metadata deletion at %s: settled ownership for actor %q not found", mc.Path, actor),
			}
		}
		return store.Delete(resID, mc.Path)
	}
	after := mc.After.Clone()
	after.Manager = actor
	after.Revision = store.Revision(resID, mc.Path) + 1
	return store.Put(resID, mc.Path, after)
}
