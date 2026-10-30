package verification

import (
	"fmt"
	"reflect"

	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/scope"
)

// VerifyTheorem7Determinism checks Safety Theorem 7 (ADR 0005 §3.1):
// Identical inputs always produce identical plans, diagnostics, and ordering.
func VerifyTheorem7Determinism(in plan.Input, mode plan.Mode) error {
	r1, err1 := plan.Plan(in, mode)
	r2, err2 := plan.Plan(in, mode)
	if (err1 == nil) != (err2 == nil) {
		return fmt.Errorf("theorem 7 violated: non-deterministic error %v vs %v", err1, err2)
	}
	if err1 != nil {
		if err1.Error() != err2.Error() {
			return fmt.Errorf("theorem 7 violated: non-deterministic errors %v vs %v", err1, err2)
		}
		return nil
	}
	// Compare complete operations, values, ownership revisions, recovery,
	// diagnostics (including NextAction), classifications and ordering.
	if !reflect.DeepEqual(r1, r2) {
		return fmt.Errorf("theorem 7 violated: non-deterministic plan result")
	}
	return nil
}

// VerifyTheorem8AllOrNothingAndDeferred checks Safety Theorem 8 (ADR 0005 §3.1):
// Any in-mode Blocker, PolicyDenied, unresolved recovery, or quarantine suppresses all Changes
// and OwnershipUpdates, and Deferred items never require execution permissions.
func VerifyTheorem8AllOrNothingAndDeferred(res *plan.Result) error {
	hasBlocker := res.Quarantined || (res.Recovery != nil && res.Recovery.HasUnresolved())
	for _, ap := range res.Authorized {
		if ap.Decision == plan.DecisionBlocker || ap.Decision == plan.DecisionPolicyDenied {
			hasBlocker = true
		}
		if ap.Selected.Kind == plan.SelectDeferred && ap.Decision == plan.DecisionPolicyDenied {
			return fmt.Errorf("theorem 8 violated: Deferred path %s was converted to PolicyDenied", ap.Selected.Classified.Path)
		}
		if ap.Selected.Kind == plan.SelectNoOp && ap.Decision == plan.DecisionPolicyDenied {
			return fmt.Errorf("theorem 8 violated: NoOp path %s was converted to PolicyDenied", ap.Selected.Classified.Path)
		}
	}
	if hasBlocker {
		if !res.Gate.Blocked || len(res.Gate.Changes) != 0 || len(res.Gate.OwnershipUpdates) != 0 || len(res.Gate.MetadataCandidates) != 0 {
			return fmt.Errorf("theorem 8 violated: blocker present but normal work was not suppressed")
		}
	}
	return nil
}

// VerifyTheorem9ExplicitResolutionSafety checks Safety Theorem 9 (ADR 0005 §3.1):
// Explicit resolution (`adopt` / `release`) never modifies environment values, requires Own+Resolve
// on the single target path, never promotes to Origin=Created, and never hijacks foreign ownership.
func VerifyTheorem9ExplicitResolutionSafety(req plan.ResolutionRequest, resPlan *plan.ResolutionPlan, err error) error {
	if err != nil || resPlan == nil {
		return nil
	}
	if !req.Scope.Allows(req.Path, scope.NewPermissionSet(scope.PermOwn, scope.PermResolve)) {
		return fmt.Errorf("theorem 9 violated: explicit resolution succeeded without Own+Resolve on %s", req.Path)
	}
	if existing, ok := req.Store.Get(req.Resource, req.Path); ok && existing.Manager != req.Actor {
		return fmt.Errorf("theorem 9 violated: explicit resolution hijacked foreign owner %s on %s", existing.Manager, req.Path)
	}
	if resPlan.After != nil && resPlan.After.Origin == ownership.OriginCreated {
		return fmt.Errorf("theorem 9 violated: explicit resolution produced Origin=Created on %s", req.Path)
	}
	if resPlan.NextStore == nil || !resPlan.Path.Equal(req.Path) || resPlan.Resource != req.Resource || resPlan.Actor != req.Actor || !resPlan.RequiresStabilize {
		return fmt.Errorf("theorem 9 violated: resolution changed target context or omitted Stabilize")
	}
	var expected *ownership.Store
	var updateErr error
	if resPlan.After == nil {
		expected, updateErr = req.Store.Delete(req.Resource, req.Path)
	} else {
		if resPlan.After.Manager != req.Actor || !resPlan.After.IsSettled() {
			return fmt.Errorf("theorem 9 violated: resolution created foreign ownership or Pending")
		}
		expected, updateErr = req.Store.Put(req.Resource, req.Path, *resPlan.After)
	}
	if updateErr != nil || !reflect.DeepEqual(expected, resPlan.NextStore) {
		return fmt.Errorf("theorem 9 violated: resolution changed records or revisions outside its single target")
	}
	return nil
}

// VerifyTheorem10ModeOutAnomaliesDeferred checks Safety Theorem 10 (ADR 0005 §3.1):
// Independent out-of-mode anomalies become Deferred rather than Blocker (except MigrationRequired
// and unresolved self-Pending).
func VerifyTheorem10ModeOutAnomaliesDeferred(res *plan.Result) error {
	for _, sp := range res.Selected {
		cp := sp.Classified
		if cp.StateClass == plan.StateMigrationRequired || cp.StateClass == plan.StateUnresolvedPending {
			if sp.Kind != plan.SelectBlocker {
				return fmt.Errorf("theorem 10 violated: %s on %s is not Blocker in mode %s", cp.StateClass, cp.Path, res.Mode)
			}
			continue
		}
		switch res.Mode {
		case plan.ModeApply:
			if cp.Membership == plan.MemberRetiredCreated &&
				(cp.StateClass == plan.StateRetiredCreatedAnomaly || cp.StateClass == plan.StateRetiredCreatedPrunable) {
				if sp.Kind != plan.SelectDeferred {
					return fmt.Errorf("theorem 10 violated: independent retired Created %s (%s) was %s in Apply, want Deferred", cp.Path, cp.StateClass, sp.Kind)
				}
			}
		case plan.ModePrune:
			if (cp.Membership == plan.MemberDeclared || cp.Membership == plan.MemberRetiredModified) &&
				cp.StateClass != plan.StateConverged {
				if sp.Kind != plan.SelectDeferred {
					return fmt.Errorf("theorem 10 violated: %s path %s (%s) was %s in Prune, want Deferred", cp.Membership, cp.Path, cp.StateClass, sp.Kind)
				}
			}
		}
	}
	return nil
}

// VerifyTheorem11MetadataCandidatesSeparation checks Safety Theorem 11 (ADR 0005 §3.1):
// MetadataCandidates never generate environment Pending operations, and metadata-only paths
// never require DurableIntent persistence or Provider.ApplyBatch.
func VerifyTheorem11MetadataCandidatesSeparation(res *plan.Result) error {
	envPaths := make(map[string]bool, len(res.Gate.Changes))
	for _, op := range res.Gate.Changes {
		envPaths[op.Path.String()] = true
	}
	for _, mc := range res.Gate.MetadataCandidates {
		if envPaths[mc.Path.String()] {
			return fmt.Errorf("theorem 11 violated: MetadataCandidate %s also generated an EnvironmentOp", mc.Path)
		}
		if mc.After != nil && mc.After.HasPending() {
			return fmt.Errorf("theorem 11 violated: MetadataCandidate %s generated a Pending reservation", mc.Path)
		}
	}
	if len(res.Gate.Changes) == 0 {
		if res.Gate.RequiresDurableIntent() || res.Gate.RequiresApplyBatch() {
			return fmt.Errorf("theorem 11 violated: metadata-only or empty plan requested DurableIntent or ApplyBatch")
		}
	}
	return nil
}

// VerifyAll11PlanTheorems runs all 11 pure `Plan` safety theorem checks on `in` across both Apply and Prune modes (ADR 0005 §3.1).
func VerifyAll11PlanTheorems(in plan.Input) error {
	preview, err := plan.PlanPreview(in)
	if err != nil {
		return err
	}
	for _, r := range []*plan.Result{preview.Apply, preview.Prune} {
		if err := VerifyTheorem1ScopeAndFrame(in, r); err != nil {
			return err
		}
		if err := VerifyTheorem2NonDestructiveStructure(in, r); err != nil {
			return err
		}
		if err := VerifyTheorem3ConservativeDeletion(in, r); err != nil {
			return err
		}
		if err := VerifyTheorem4NoMutationOnUnknown(in, r); err != nil {
			return err
		}
		if err := VerifyTheorem5NoOverwriteOnForeignOrDrift(in, r); err != nil {
			return err
		}
		if err := VerifyTheorem7Determinism(in, r.Mode); err != nil {
			return err
		}
		if err := VerifyTheorem8AllOrNothingAndDeferred(r); err != nil {
			return err
		}
		if err := VerifyTheorem10ModeOutAnomaliesDeferred(r); err != nil {
			return err
		}
		if err := VerifyTheorem11MetadataCandidatesSeparation(r); err != nil {
			return err
		}
	}
	if err := VerifyTheorem6MatchFixedPoint(in, preview.Apply, preview.Prune); err != nil {
		return err
	}
	// Explicit resolution has a separate input/output shape and must actually be
	// invoked to check property 9; checking the two normal modes does not cover it.
	paths := collectVerificationDomain(in.Declaration, in.Store, in.Resource, in.Actor)
	for _, p := range paths {
		token, _ := plan.ComputeConditionToken(in.Resource, p, in.Snapshot)
		req := plan.ResolutionRequest{Resource: in.Resource, Actor: in.Actor, Path: p, ExpectedToken: token, ExpectedRevision: in.Store.Revision(in.Resource, p), Declaration: in.Declaration, Snapshot: in.Snapshot, Store: in.Store, Scope: in.Scope, Quarantined: in.Quarantined}
		for _, resolve := range []func(plan.ResolutionRequest) (*plan.ResolutionPlan, error){plan.PlanAdopt, plan.PlanRelease} {
			resolved, err := resolve(req)
			if err := VerifyTheorem9ExplicitResolutionSafety(req, resolved, err); err != nil {
				return err
			}
		}
	}
	return nil
}
