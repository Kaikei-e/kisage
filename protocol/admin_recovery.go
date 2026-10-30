package protocol

import (
	"fmt"
	"math"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/structure"
)

// AdminRecoveryAction identifies how an administrator resolves a PendingConflict or quarantined entry (ADR 0004 §4).
type AdminRecoveryAction uint8

const (
	// AdminActionInvalid is the zero value for an uninitialized AdminRecoveryAction.
	AdminActionInvalid AdminRecoveryAction = iota
	// AdminActionAdoptCurrentObserved resolves a Pending entry by adopting the current observed leaf value.
	AdminActionAdoptCurrentObserved
	// AdminActionRemoveRecord deletes the ownership record so the path can be re-evaluated cleanly.
	AdminActionRemoveRecord
)

// AdminRecoveryRequest holds the inputs to the pure administrator recovery boundary (ADR 0004 §4, Counterexample #45).
//
// Invariants enforced by ADR 0004 §4:
//   - Administrator recovery for PendingConflict and Resource quarantine is NEVER exposed to normal agents (`CallerIsAdmin` must be true).
//   - No active execution may be in flight (`InFlightExecution` must be false).
//   - The target resource observation must be durably stabilized (`Stabilize.Outcome == StabilizeDurable`).
//   - Creation attribution must be re-inspected: value equality alone NEVER re-grants Origin=Created;
//     whenever creation attribution is unverified (`!CreationAttributionVerified` or `entry.Origin == OriginNone`),
//     the recovered record is conservatively downgraded to Origin=Modified with K601.
type AdminRecoveryRequest struct {
	CallerIsAdmin               bool
	InFlightExecution           bool
	Resource                    ownership.ResourceID
	Path                        model.Path
	Action                      AdminRecoveryAction
	CreationAttributionVerified bool
	Store                       *ownership.Store
	Quarantine                  *QuarantineTable
	Snapshot                    *observation.Snapshot
	Stabilize                   provider.StabilizeResult
}

// AdminRecoveryResult holds the pure output of administrator recovery on a PendingConflict or quarantined Resource.
type AdminRecoveryResult struct {
	NextStore      *ownership.Store
	NextQuarantine *QuarantineTable
	UpdatedEntry   *ownership.OwnerEntry
	Diagnostics    []model.Diagnostic
}

// ValidateAdminRecoveryPreconditions purely checks the ADR 0004 §4 boundary preconditions:
//   - CallerIsAdmin is true (rejects normal agents with K401 PolicyDenied).
//   - InFlightExecution is false (rejects concurrent execution with K205 PendingConflict).
//   - Stabilize.Outcome is StabilizeDurable (rejects unstabilized states with K502/K203).
func ValidateAdminRecoveryPreconditions(req AdminRecoveryRequest) error {
	if !req.CallerIsAdmin {
		return model.Diagnostic{
			Code:       model.CodePolicyDenied,
			Path:       req.Path.Clone(),
			Reason:     "administrator recovery for PendingConflict and Resource quarantine is not exposed to normal agents",
			NextAction: "escalate to a human or privileged administrator recovery workflow",
		}
	}
	if req.InFlightExecution {
		return model.Diagnostic{
			Code:       model.CodePendingConflict,
			Path:       req.Path.Clone(),
			Reason:     "cannot run administrator recovery while an execution is still in flight on the target resource",
			NextAction: "ensure all running processes on the resource have stopped before resuming",
		}
	}
	if err := ownership.ValidateResourceID(req.Resource); err != nil {
		return err
	}
	if req.Store == nil || req.Snapshot == nil {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   req.Path.Clone(),
			Reason: "administrator recovery requires non-nil ownership store and observation snapshot",
		}
	}
	if err := req.Store.Validate(); err != nil {
		return err
	}
	if req.Stabilize.Outcome != provider.StabilizeDurable {
		if len(req.Stabilize.Diagnostics) > 0 {
			return req.Stabilize.Diagnostics[0]
		}
		return model.Diagnostic{
			Code:   model.CodeIndeterminateOutcome,
			Path:   req.Path.Clone(),
			Reason: fmt.Sprintf("administrator recovery requires a durable Stabilized snapshot, got %s", req.Stabilize.Outcome),
		}
	}
	return nil
}

// ResolveAdminRecovery purely executes administrator recovery on (Resource, Path) and optionally
// clears Resource quarantine once all Pending entries on the Resource are resolved (ADR 0004 §4, Counterexample #45).
func ResolveAdminRecovery(req AdminRecoveryRequest) (*AdminRecoveryResult, error) {
	if err := ValidateAdminRecoveryPreconditions(req); err != nil {
		return nil, err
	}
	if err := model.ValidateLeafPath(req.Path); err != nil {
		return nil, err
	}
	cur, exists := req.Store.Get(req.Resource, req.Path)
	if !exists {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   req.Path.Clone(),
			Reason: fmt.Sprintf("no ownership record exists at %s for administrator recovery", req.Path),
		}
	}
	if !cur.HasPending() && (req.Quarantine == nil || !req.Quarantine.IsQuarantined(req.Resource)) {
		return nil, model.Diagnostic{
			Code:   model.CodePolicyDenied,
			Path:   req.Path.Clone(),
			Reason: fmt.Sprintf("administrator recovery on %s requires an unsettled Pending record or quarantined resource", req.Path),
		}
	}

	switch req.Action {
	case AdminActionRemoveRecord:
		return resolveAdminRemoveRecord(req)
	case AdminActionAdoptCurrentObserved:
		return resolveAdminAdoptObserved(req, cur)
	default:
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   req.Path.Clone(),
			Reason: fmt.Sprintf("invalid AdminRecoveryAction %d", req.Action),
		}
	}
}

func resolveAdminRemoveRecord(req AdminRecoveryRequest) (*AdminRecoveryResult, error) {
	check := structure.InspectPath(req.Snapshot, req.Path)
	if check.HasUnknown() {
		diag, _ := check.PrimaryDiagnostic()
		return nil, diag
	}
	nextStore, err := req.Store.Delete(req.Resource, req.Path)
	if err != nil {
		return nil, err
	}
	nextQuarantine := clearQuarantineIfNoPending(nextStore, req.Quarantine, req.Resource)
	return &AdminRecoveryResult{
		NextStore:      nextStore,
		NextQuarantine: nextQuarantine,
	}, nil
}

func resolveAdminAdoptObserved(req AdminRecoveryRequest, cur ownership.OwnerEntry) (*AdminRecoveryResult, error) {
	check := structure.InspectPath(req.Snapshot, req.Path)
	if err := check.Err(); err != nil {
		return nil, err
	}
	leafVal, ok := check.Target.LeafValue()
	if !ok || !check.CanSet() {
		return nil, model.Diagnostic{
			Code:   model.CodeStructureConflict,
			Path:   req.Path.Clone(),
			Reason: fmt.Sprintf("administrator adoption at %s requires Present(Leaf(v)) with Present(Record) ancestors, observed %s", req.Path, check.Target),
		}
	}

	nextOrigin := ownership.OriginModified
	var diags []model.Diagnostic
	// ADR 0004 §4: Value equality alone NEVER re-grants Origin=Created.
	// Origin=Created is preserved only if the prior entry already had settled Origin=Created,
	// creation attribution was explicitly verified, AND the observed value matches either the
	// prior LastApplied value or the Pending Set target (never an arbitrary third-party value).
	matchesKnownActorValue := model.EqualLeafValues(leafVal, cur.LastApplied) ||
		(cur.Pending != nil && cur.Pending.Op == ownership.PendingOpSet && model.EqualLeafValues(leafVal, cur.Pending.Target))
	if cur.Origin == ownership.OriginCreated && req.CreationAttributionVerified && matchesKnownActorValue {
		nextOrigin = ownership.OriginCreated
	} else {
		diags = append(diags, model.Diagnostic{
			Code:       model.CodeCreatedOriginUnverified,
			Path:       req.Path.Clone(),
			Reason:     "administrator recovery downgraded ownership to Origin=Modified because initial creation attribution could not be verified from value equality alone",
			NextAction: "future retirement of this leaf will unmanage the record rather than auto-deleting the environment value",
		})
	}

	curRev := req.Store.Revision(req.Resource, req.Path)
	if curRev == math.MaxUint64 {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Path:   req.Path.Clone(),
			Reason: "ownership revision exhausted: cannot advance beyond uint64 maximum",
		}
	}
	rev := curRev + 1
	updated := ownership.OwnerEntry{
		Manager:     cur.Manager,
		Origin:      nextOrigin,
		LastApplied: model.CloneLeafValue(leafVal),
		Revision:    rev,
	}
	nextStore, err := req.Store.Put(req.Resource, req.Path, updated)
	if err != nil {
		return nil, err
	}
	updated.Revision = nextStore.Revision(req.Resource, req.Path)
	nextQuarantine := clearQuarantineIfNoPending(nextStore, req.Quarantine, req.Resource)

	return &AdminRecoveryResult{
		NextStore:      nextStore,
		NextQuarantine: nextQuarantine,
		UpdatedEntry:   &updated,
		Diagnostics:    diags,
	}, nil
}

func clearQuarantineIfNoPending(store *ownership.Store, qt *QuarantineTable, resID ownership.ResourceID) *QuarantineTable {
	if qt == nil {
		return NewQuarantineTable()
	}
	for _, p := range store.Paths(resID) {
		if e, ok := store.Get(resID, p); ok && e.HasPending() {
			return qt.Clone()
		}
	}
	return qt.Clear(resID)
}
