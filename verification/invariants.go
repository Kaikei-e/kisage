package verification

import (
	"fmt"
	"reflect"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/protocol"
	"github.com/Kaikei-e/kisage/provider"
)

// ProtocolStepKind identifies one of the 12 protocol transitions in ADR 0005 §3.2.
type ProtocolStepKind string

const (
	StepObserve       ProtocolStepKind = "Observe"
	StepPlan          ProtocolStepKind = "Plan"
	StepIntent        ProtocolStepKind = "Intent"
	StepProviderApply ProtocolStepKind = "ProviderApply"
	StepValidate      ProtocolStepKind = "Validate"
	StepStabilize     ProtocolStepKind = "Stabilize"
	StepCommit        ProtocolStepKind = "Commit"
	StepCrash         ProtocolStepKind = "Crash"
	StepRecover       ProtocolStepKind = "Recover"
	StepAdopt         ProtocolStepKind = "Adopt"
	StepRelease       ProtocolStepKind = "Release"
	StepExternalWrite ProtocolStepKind = "ExternalWrite"
)

// VerifiedCreationRecord tracks the historical ground truth of whether the current ownership session
// for a path began with a confirmed `Absent -> Present` creation by VerifiedCreator (ADR 0002 §2, ADR 0005 §3.2).
type VerifiedCreationRecord struct {
	Path            model.Path
	VerifiedCreator ownership.ActorID
}

// ProtocolInvariantContext holds the state inspected by VerifyProtocolInductiveInvariants (ADR 0005 §3.2).
type ProtocolInvariantContext struct {
	Resource          ownership.ResourceID
	BeforeStore       *ownership.Store
	AfterStore        *ownership.Store
	VisibleEnv        *observation.Snapshot
	DurableEnv        *observation.Snapshot
	EnvStabilized     bool
	RecoveryPersisted bool
	IsExplicitResolve bool
	ActiveActor       ownership.ActorID
	VerifiedCreations map[string]ownership.ActorID
	RecoveryPaths     []model.Path
}

// VerifyProtocolInductiveInvariants purely checks the inductive invariants of the protocol state machine (ADR 0005 §3.2):
//  1. Historical basis of `Origin = Created`: every entry with `Origin = Created` must have a verified
//     `Absent -> Present` creation by the same Manager in VerifiedCreations.
//  2. Non-promotion of unverified creation: crash recovery of an `Origin = None` Pending entry never produces `Origin = Created`.
//  3. Inter-Actor mutual exclusion: no two distinct Actors ever own or reserve the same or ancestor/descendant overlapping paths.
//  4. Stabilization before recovery commit: if RecoveryPersisted is true, EnvStabilized must be true and VisibleEnv must match DurableEnv.
//  5. Preservation of non-overlapping foreign Pending reservations across ActiveActor transitions.
//  6. Explicit resolution (`Adopt` / `Release`) never increases deletion rights (`Origin = Created`).
func VerifyProtocolInductiveInvariants(ctx ProtocolInvariantContext) error {
	if ctx.AfterStore == nil {
		return fmt.Errorf("cannot check invariants without an after-store")
	}
	if err := ctx.AfterStore.Validate(); err != nil {
		return fmt.Errorf("store structural invariant failed: %w", err)
	}
	if err := verifyCreatedHistoricalBasis(ctx); err != nil {
		return err
	}
	if err := verifyRecoveryStabilizeInvariant(ctx); err != nil {
		return err
	}
	if err := verifyForeignPendingPreservation(ctx); err != nil {
		return err
	}
	if ctx.IsExplicitResolve {
		if err := verifyResolveDoesNotIncreaseCreatedRights(ctx); err != nil {
			return err
		}
	}
	return nil
}

func verifyCreatedHistoricalBasis(ctx ProtocolInvariantContext) error {
	for _, p := range ctx.AfterStore.Paths(ctx.Resource) {
		afterEntry, ok := ctx.AfterStore.Get(ctx.Resource, p)
		if !ok {
			continue
		}
		if afterEntry.Origin == ownership.OriginCreated {
			creator, verified := ctx.VerifiedCreations[p.String()]
			if !verified || creator != afterEntry.Manager {
				return fmt.Errorf("invariant violated: %s has Origin=Created for %s without verified Absent->Present creation history", p, afterEntry.Manager)
			}
		}
		if ctx.BeforeStore != nil && ctx.RecoveryPersisted && afterEntry.Origin == ownership.OriginCreated {
			beforeEntry, hadBefore := ctx.BeforeStore.Get(ctx.Resource, p)
			if !hadBefore || beforeEntry.Origin != ownership.OriginCreated || beforeEntry.Manager != afterEntry.Manager {
				return fmt.Errorf("invariant violated: crash recovery granted new Created ownership on %s", p)
			}
		}
	}
	return nil
}

func verifyRecoveryStabilizeInvariant(ctx ProtocolInvariantContext) error {
	if !ctx.RecoveryPersisted {
		return nil
	}
	if !ctx.EnvStabilized {
		return fmt.Errorf("invariant violated: recovery persisted without Provider.Stabilize durability confirmation")
	}
	if ctx.VisibleEnv == nil || ctx.DurableEnv == nil || len(ctx.RecoveryPaths) == 0 {
		return fmt.Errorf("recovery persistence requires visible and durable snapshots of its candidates")
	}
	for _, p := range ctx.RecoveryPaths {
		for _, q := range append(p.Ancestors(), p) {
			visible, durable := ctx.VisibleEnv.Get(q), ctx.DurableEnv.Get(q)
			if visible.IsUnknown() || durable.IsUnknown() || !visible.Equal(durable) {
				return fmt.Errorf("invariant violated: recovery persisted on %s without matching known durable observation at %s", p, q)
			}
		}
	}
	return nil
}

func verifyForeignPendingPreservation(ctx ProtocolInvariantContext) error {
	if ctx.BeforeStore == nil || ctx.ActiveActor == "" {
		return nil
	}
	for _, p := range ctx.BeforeStore.Paths(ctx.Resource) {
		beforeEntry, ok := ctx.BeforeStore.Get(ctx.Resource, p)
		if !ok || beforeEntry.Manager == ctx.ActiveActor {
			continue
		}
		afterEntry, stillExists := ctx.AfterStore.Get(ctx.Resource, p)
		if !stillExists || !beforeEntry.Equal(afterEntry) {
			return fmt.Errorf("invariant violated: active actor %s modified foreign actor %s entry at %s", ctx.ActiveActor, beforeEntry.Manager, p)
		}
	}
	for _, p := range ctx.AfterStore.Paths(ctx.Resource) {
		afterEntry, ok := ctx.AfterStore.Get(ctx.Resource, p)
		if !ok || afterEntry.Manager == ctx.ActiveActor {
			continue
		}
		beforeEntry, existed := ctx.BeforeStore.Get(ctx.Resource, p)
		if !existed || !beforeEntry.Equal(afterEntry) {
			return fmt.Errorf("invariant violated: active actor %s fabricated or modified foreign actor %s entry at %s", ctx.ActiveActor, afterEntry.Manager, p)
		}
	}
	if ctx.Resource != "" {
		seenRes := make(map[ownership.ResourceID]struct{})
		for _, r := range ctx.BeforeStore.Resources() {
			seenRes[r] = struct{}{}
		}
		for _, r := range ctx.AfterStore.Resources() {
			seenRes[r] = struct{}{}
		}
		for r := range seenRes {
			if r == ctx.Resource {
				continue
			}
			beforePaths := ctx.BeforeStore.Paths(r)
			afterPaths := ctx.AfterStore.Paths(r)
			if !reflect.DeepEqual(beforePaths, afterPaths) {
				return fmt.Errorf("invariant violated: transition on resource %s modified path domain of unrelated resource %s", ctx.Resource, r)
			}
			for _, p := range beforePaths {
				be, _ := ctx.BeforeStore.Get(r, p)
				ae, _ := ctx.AfterStore.Get(r, p)
				if !be.Equal(ae) || ctx.BeforeStore.Revision(r, p) != ctx.AfterStore.Revision(r, p) {
					return fmt.Errorf("invariant violated: transition on resource %s modified entry at %s in unrelated resource %s", ctx.Resource, p, r)
				}
			}
		}
	}
	return nil
}

func verifyResolveDoesNotIncreaseCreatedRights(ctx ProtocolInvariantContext) error {
	if ctx.BeforeStore == nil {
		return nil
	}
	for _, p := range ctx.AfterStore.Paths(ctx.Resource) {
		afterEntry, ok := ctx.AfterStore.Get(ctx.Resource, p)
		if !ok || afterEntry.Origin != ownership.OriginCreated {
			continue
		}
		beforeEntry, hadBefore := ctx.BeforeStore.Get(ctx.Resource, p)
		if !hadBefore || beforeEntry.Origin != ownership.OriginCreated || beforeEntry.Manager != afterEntry.Manager {
			return fmt.Errorf("invariant violated: explicit Resolve operation granted new Origin=Created deletion right on %s", p)
		}
	}
	return nil
}

// VerifyReconcileResultInvariants checks protocol invariants on a completed or halted ReconcileResult (ADR 0005 §3.2).
func VerifyReconcileResultInvariants(
	beforeStore *ownership.Store,
	res *protocol.ReconcileResult,
) error {
	_, err := VerifyReconcileResultWithHistory(beforeStore, res, nil)
	return err
}

// VerifyReconcileResultWithHistory checks a transition and returns the creation
// witnesses to carry to the next call. Initial Created ownership needs explicit
// fixture/trace evidence; it is never inferred from an arbitrary loaded store.
// New witnesses require observed Absent -> Leaf, validated Applied, Stabilize,
// and successful ownership Commit. Released sessions are removed from history.
// This is an executable conformance check, not a formal inductive proof.
func VerifyReconcileResultWithHistory(beforeStore *ownership.Store, res *protocol.ReconcileResult, history map[string]ownership.ActorID) (map[string]ownership.ActorID, error) {
	if res == nil || beforeStore == nil {
		return nil, fmt.Errorf("reconcile result and before-store must not be nil")
	}
	initial := ProtocolInvariantContext{Resource: res.Resource, AfterStore: beforeStore, VerifiedCreations: history}
	if err := VerifyProtocolInductiveInvariants(initial); err != nil {
		return nil, err
	}
	if res.Recovery.PersistedCount > 0 {
		if res.Recovery.StabilizeOutcome != provider.StabilizeDurable || res.Recovery.CommitStatus != protocol.CommitSucceeded || res.Recovery.Report == nil {
			return nil, fmt.Errorf("recovery persisted without successful Stabilize and Commit evidence")
		}
		paths := make([]model.Path, 0, len(res.Recovery.Report.Candidates))
		for _, item := range res.Recovery.Report.Candidates {
			if !item.Persisted || !item.Authorized {
				return nil, fmt.Errorf("recovery persistence contains an unpersisted or unauthorized candidate")
			}
			paths = append(paths, item.Path)
		}
		if len(paths) != res.Recovery.PersistedCount {
			return nil, fmt.Errorf("recovery persistence count differs from candidates")
		}
		if err := VerifyProtocolInductiveInvariants(ProtocolInvariantContext{Resource: res.Resource, BeforeStore: beforeStore, AfterStore: res.Recovery.Store, ActiveActor: res.Actor, VerifiedCreations: history, RecoveryPersisted: true, EnvStabilized: true, RecoveryPaths: paths, VisibleEnv: res.Recovery.ObservedSnapshot, DurableEnv: res.Recovery.DurableSnapshot}); err != nil {
			return nil, err
		}
	}
	if res.Plan != nil && len(res.Plan.Gate.Changes) == 0 {
		if res.NormalExecution.IntentSaved || res.NormalExecution.Receipt != nil {
			return nil, fmt.Errorf("metadata-only or empty plan saved DurableIntent or called ApplyBatch")
		}
	}
	if res.NormalExecution.ContractViolation {
		if res.Summary.Execution != protocol.SummaryFailed && res.Summary.Execution != protocol.SummaryIndeterminate {
			return nil, fmt.Errorf("contract violation must aggregate to Failed or higher-priority Indeterminate, got %s", res.Summary.Execution)
		}
	}
	if res.Recovery.Status == protocol.RecoveryCompleted && res.NormalExecution.Status == protocol.NormalNoOp {
		if res.Summary.Execution != protocol.SummaryCompleted {
			return nil, fmt.Errorf("recovery=Completed + normal=NoOp must aggregate to SummaryCompleted, got %s", res.Summary.Execution)
		}
	}
	if res.Recovery.PersistedCount > 0 && res.NormalExecution.Status == protocol.NormalBlocked {
		if res.Summary.Execution != protocol.SummaryBlocked || res.Summary.Counts.RecoveredPersisted != res.Recovery.PersistedCount {
			return nil, fmt.Errorf("recovery=Completed + normal=Blocked must aggregate to SummaryBlocked while retaining RecoveredPersisted")
		}
	}
	if res.FinalStore == nil {
		if res.Recovery.StoreOutcomeUnknown || res.NormalExecution.StoreOutcomeUnknown {
			if res.Summary.Execution != protocol.SummaryIndeterminate {
				return nil, fmt.Errorf("unknown store outcome must aggregate to SummaryIndeterminate, got %s", res.Summary.Execution)
			}
			return cloneHistory(history), nil // Preserve prior creation history across unknown store commit outcome.
		}
		return nil, fmt.Errorf("missing final store without an indeterminate store outcome")
	}
	base := beforeStore
	if res.Recovery.Store != nil {
		if res.Recovery.PersistedCount == 0 && !reflect.DeepEqual(beforeStore, res.Recovery.Store) {
			return nil, fmt.Errorf("recovery changed the known store without persisted candidates")
		}
		base = res.Recovery.Store
	}
	nextHistory := make(map[string]ownership.ActorID)
	for _, p := range res.FinalStore.Paths(res.Resource) {
		after, _ := res.FinalStore.Get(res.Resource, p)
		if after.Origin != ownership.OriginCreated {
			continue
		}
		before, exists := base.Get(res.Resource, p)
		if exists && before.Origin == ownership.OriginCreated && before.Manager == after.Manager && history[p.String()] == after.Manager {
			if !model.EqualLeafValues(before.LastApplied, after.LastApplied) && !hasConfirmedSet(res, p, after, &before) {
				return nil, fmt.Errorf("Created ownership on %s changed without a verified Set", p)
			}
			nextHistory[p.String()] = after.Manager
			continue
		}
		if exists || !hasConfirmedSet(res, p, after, nil) {
			return nil, fmt.Errorf("Created ownership at %s has no verified creation in the current session", p)
		}
		nextHistory[p.String()] = after.Manager
	}
	err := VerifyProtocolInductiveInvariants(ProtocolInvariantContext{
		Resource:          res.Resource,
		BeforeStore:       beforeStore,
		AfterStore:        res.FinalStore,
		ActiveActor:       res.Actor,
		VerifiedCreations: nextHistory,
	})
	return nextHistory, err
}

func hasConfirmedSet(res *protocol.ReconcileResult, path model.Path, after ownership.OwnerEntry, previous *ownership.OwnerEntry) bool {
	normal := res.NormalExecution
	if normal.Status != protocol.NormalCompleted || normal.CommitStatus != protocol.CommitSucceeded || normal.StabilizeOutcome != provider.StabilizeDurable || normal.ContractViolation || !normal.IntentSaved || normal.Intent == nil || normal.Receipt == nil || normal.Receipt.Outcome != provider.ReceiptApplied || normal.Receipt.BatchID != normal.Batch.BatchID || after.Manager != res.Actor || after.HasPending() {
		return false
	}
	if normal.BeforeApplySnapshot == nil || normal.VerifiedSnapshot == nil || normal.DurableSnapshot == nil {
		return false
	}
	intent, _, err := protocol.ExtractDurableIntent(normal.Batch)
	if err != nil || !reflect.DeepEqual(intent, normal.Intent) {
		return false
	}
	if len(provider.VerifyAppliedBatchContract(normal.BeforeApplySnapshot, normal.VerifiedSnapshot, normal.Batch.EnvironmentOps)) != 0 {
		return false
	}
	for _, op := range normal.Batch.EnvironmentOps {
		if !op.Path.Equal(path) || op.Op != ownership.PendingOpSet || !model.EqualLeafValues(after.LastApplied, op.Target) {
			continue
		}
		if previous == nil {
			if op.Previous != nil || op.Expected.Presence() != observation.PresenceAbsent || normal.BeforeApplySnapshot.Get(path).Presence() != observation.PresenceAbsent {
				return false
			}
		} else if op.Previous == nil || op.Previous.Origin != previous.Origin || !model.EqualLeafValues(op.Previous.LastApplied, previous.LastApplied) {
			return false
		}
		if normal.BeforeApplySnapshot.Get(path).Equal(op.Expected) {
			v, leaf := normal.DurableSnapshot.Get(path).LeafValue()
			return leaf && model.EqualLeafValues(v, op.Target)
		}
	}
	return false
}

func cloneHistory(history map[string]ownership.ActorID) map[string]ownership.ActorID {
	if history == nil {
		return nil
	}
	out := make(map[string]ownership.ActorID, len(history))
	for k, v := range history {
		out[k] = v
	}
	return out
}
