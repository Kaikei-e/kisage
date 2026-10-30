import Kisage.Model
import Kisage.Recover
import Kisage.Plan
import Kisage.Theorems

namespace Kisage

/-!
# Bounded Finite-Execution Convergence and Candidate ESR Rank Function (ADR 0005 §3.3)

Formalizes `Match_A`, the candidate ESR rank function (`0`, `1`, `2`), single-step `Apply`
convergence, two-step `apply → prune` convergence for independent leaves (including retired
`Created` leaves), the `DeferredUntilRelease` structural migration lemma (Counterexamples #49, #55),
and strict rank decrease on progress steps.
-/

/-- Predicate `Match_A` (ADR 0002 §2, ADR 0005 §3.3):
    A path in `M_A` is converged when:
    - If declared with `desired`: `obs = .presentLeaf desired` and `owner = some e` with
      `e.manager == actor`, `e.pending = none`, and `e.lastApplied == desired`.
    - If undeclared: `owner = none` (or not owned by `actor`). -/
def isMatchA (actor : ActorID) (declared : Option Value) (obs : Observed) (owner : Option OwnerEntry) : Bool :=
  match declared with
  | some desired =>
    match obs, owner with
    | .presentLeaf actual, some e =>
      (e.manager == actor) && e.pending.isNone && (actual == desired) && (e.lastApplied == desired)
    | _, _ => false
  | none =>
    match owner with
    | none => true
    | some e => e.manager != actor

/-- Candidate ESR per-path rank function (`0`, `1`, `2`) from ADR 0005 §3.3:
    - `0`: converged (`isMatchA = true`)
    - `1`: environment value is already in its final state, but metadata commit/release is required
    - `2`: environment value change (`Set` or `Delete`) is required -/
def pathRank (actor : ActorID) (declared : Option Value) (obs : Observed) (owner : Option OwnerEntry) : Nat :=
  if isMatchA actor declared obs owner then
    0
  else
    match declared with
    | some desired =>
      match obs with
      | .presentLeaf actual =>
        if actual == desired then 1 else 2
      | _ => 2
    | none =>
      match owner with
      | none => 0
      | some e =>
        if e.manager != actor then 0
        else
          match e.origin with
          | .modified => 1
          | .created =>
            match obs with
            | .absent => 1
            | _ => 2

/-- Sum of `pathRank` across a finite list of paths (ADR 0005 §3.3). -/
def totalRank (inputs : List PathInput) : Nat :=
  inputs.foldl (fun acc inp => acc + pathRank inp.actor inp.declared inp.obs inp.owner) 0

/-- `pathRank` is `0` whenever `isMatchA` holds. -/
theorem rank_zero_of_matchA
    (actor : ActorID) (declared : Option Value) (obs : Observed) (owner : Option OwnerEntry)
    (hMatch : isMatchA actor declared obs owner = true) :
    pathRank actor declared obs owner = 0 := by
  unfold pathRank
  simp [hMatch]

/-- Executes a single stable reconciliation step (`PureRecover → Plan → ApplyBatch → Stabilize → Commit`)
    on a single independent path under successful Provider and Store operations (ADR 0005 §3.3). -/
def executeStableStep (inp : PathInput) (mode : Mode) : PathInput :=
  let p := planPath inp mode
  let nextObs : Observed :=
    match p.envOp with
    | some (.set _ _ target) => .presentLeaf target
    | some (.delete _ _) => .absent
    | none => inp.obs
  let nextOwner : Option OwnerEntry :=
    match p.ownershipUpdate with
    | some (.upsert _ entry _) => some entry
    | some (.remove _ _) => none
    | none => p.recovery.virtualOwner
  { inp with obs := nextObs, owner := nextOwner }

/-- Inductive characterization of a well-formed independent leaf ready for `Apply` (ADR 0005 §3.3):
    - `unownedAbsent`: declared `desired`, `obs = .absent`, `owner = none`
    - `unownedMatching`: declared `desired`, `obs = .presentLeaf desired`, `owner = none`
    - `selfOwnedDeclared`: declared `desired`, `obs = .presentLeaf actual`, self-owned settled with `lastApplied = actual`
    - `retiredModified`: undeclared, settled self-owned `Origin = .modified`, known observation
    - `retiredCreatedAbsent`: undeclared, settled self-owned `Origin = .created`, `obs = .absent` -/
inductive ApplyReadyLeaf (path : PathID) (actor : ActorID) : PathInput → Prop where
  | unownedAbsent (desired : Value) :
      ApplyReadyLeaf path actor {
        path := path, actor := actor, declared := some desired,
        obs := .absent, anc := .clear, owner := none,
        granted := permRegular, overlap := .none, hasUnresolvedSelfPending := false
      }
  | unownedMatching (desired : Value) :
      ApplyReadyLeaf path actor {
        path := path, actor := actor, declared := some desired,
        obs := .presentLeaf desired, anc := .clear, owner := none,
        granted := permRegular, overlap := .none, hasUnresolvedSelfPending := false
      }
  | selfOwnedDeclared (desired actual : Value) (orig : Origin) :
      ApplyReadyLeaf path actor {
        path := path, actor := actor, declared := some desired,
        obs := .presentLeaf actual, anc := .clear,
        owner := some { manager := actor, origin := orig, lastApplied := actual, pending := none },
        granted := permRegular, overlap := .none, hasUnresolvedSelfPending := false
      }
  | retiredModified (val : Value) (obs : Observed) (hKnown : isObservationUnknown obs .clear = false) :
      ApplyReadyLeaf path actor {
        path := path, actor := actor, declared := none,
        obs := obs, anc := .clear,
        owner := some { manager := actor, origin := .modified, lastApplied := val, pending := none },
        granted := permRegular, overlap := .none, hasUnresolvedSelfPending := false
      }
  | retiredCreatedAbsent (val : Value) :
      ApplyReadyLeaf path actor {
        path := path, actor := actor, declared := none,
        obs := .absent, anc := .clear,
        owner := some { manager := actor, origin := .created, lastApplied := val, pending := none },
        granted := permRegular, overlap := .none, hasUnresolvedSelfPending := false
      }

/-- **Single-Step `Apply` Convergence Theorem (ADR 0005 §3.3)**:
    For any independent `ApplyReadyLeaf` (no `DeferredUntilRelease` or retired `Created` leaf remaining
    in the environment), a single `Mode.apply` execution reaches `isMatchA = true` and `pathRank = 0`. -/
theorem apply_single_step_clears_apply_work
    {path : PathID} {actor : ActorID} {inp : PathInput}
    (hReady : ApplyReadyLeaf path actor inp) :
    let after := executeStableStep inp .apply
    isMatchA after.actor after.declared after.obs after.owner = true ∧
    pathRank after.actor after.declared after.obs after.owner = 0 := by
  cases hReady with
  | unownedAbsent desired =>
    simp [executeStableStep, planPath, pureRecoverPath, classifyPath, selectModeKind, selectModeAction,
          selectModePerms, requiredPermissions, authorizeSelection, buildEnvOp, buildMetadataCandidate,
          buildOwnershipUpdate, permRegular, permSetOwn, PermSet.allows, isObservationUnknown,
          isProtectedStructure, isMatchA, pathRank]
  | unownedMatching desired =>
    simp [executeStableStep, planPath, pureRecoverPath, classifyPath, selectModeKind, selectModeAction,
          selectModePerms, requiredPermissions, authorizeSelection, buildEnvOp, buildMetadataCandidate,
          buildOwnershipUpdate, permRegular, permOwn, PermSet.allows, isObservationUnknown,
          isProtectedStructure, isMatchA, pathRank]
  | selfOwnedDeclared desired actual orig =>
    by_cases hEq : (actual == desired) = true
    · have hValEq : actual = desired := beq_iff_eq.mp hEq
      subst hValEq
      simp [executeStableStep, planPath, pureRecoverPath, classifyPath, selectModeKind, selectModeAction,
            selectModePerms, authorizeSelection, buildEnvOp, buildMetadataCandidate,
            buildOwnershipUpdate, permNone, isObservationUnknown,
            isProtectedStructure, isMatchA, pathRank]
    · simp only [Bool.not_eq_true] at hEq
      simp [hEq, executeStableStep, planPath, pureRecoverPath, classifyPath, selectModeKind, selectModeAction,
            selectModePerms, requiredPermissions, authorizeSelection, buildEnvOp, buildMetadataCandidate,
            buildOwnershipUpdate, permRegular, permSetOwn, PermSet.allows, isObservationUnknown,
            isProtectedStructure, isMatchA, pathRank]
  | retiredModified val obs hKnown =>
    simp [hKnown, executeStableStep, planPath, pureRecoverPath, classifyPath, selectModeKind, selectModeAction,
          selectModePerms, requiredPermissions, authorizeSelection, buildEnvOp, buildMetadataCandidate,
          buildOwnershipUpdate, permRegular, permOwn, PermSet.allows, isMatchA, pathRank]
  | retiredCreatedAbsent val =>
    simp [executeStableStep, planPath, pureRecoverPath, classifyPath, selectModeKind, selectModeAction,
          selectModePerms, requiredPermissions, authorizeSelection, buildEnvOp, buildMetadataCandidate,
          buildOwnershipUpdate, permRegular, permOwn, PermSet.allows, isObservationUnknown,
          isProtectedStructure, isMatchA, pathRank]

/-- Inductive characterization of any independent reconcilable leaf (including retired `Created`
    leaves that require `prune` after `apply`) (ADR 0005 §3.3). -/
inductive ReconcilableLeaf (path : PathID) (actor : ActorID) : PathInput → Prop where
  | applyReady {inp : PathInput} (h : ApplyReadyLeaf path actor inp) :
      ReconcilableLeaf path actor inp
  | retiredCreatedPresent (val : Value) :
      ReconcilableLeaf path actor {
        path := path, actor := actor, declared := none,
        obs := .presentLeaf val, anc := .clear,
        owner := some { manager := actor, origin := .created, lastApplied := val, pending := none },
        granted := permRegular, overlap := .none, hasUnresolvedSelfPending := false
      }

/-- **Two-Step `apply → prune` Finite-Execution Convergence Theorem (ADR 0005 §3.3)**:
    Under fixed declaration, `permRegular` (`Set + Delete + Own`), available observations,
    no conflicts/migrations, and successful Provider/Store execution, every independent
    `ReconcilableLeaf` reaches `isMatchA = true` (`pathRank = 0`) after `apply` followed by `prune`. -/
theorem apply_then_prune_finite_convergence
    {path : PathID} {actor : ActorID} {inp : PathInput}
    (hLeaf : ReconcilableLeaf path actor inp) :
    let step1 := executeStableStep inp .apply
    let step2 := executeStableStep step1 .prune
    isMatchA step2.actor step2.declared step2.obs step2.owner = true ∧
    pathRank step2.actor step2.declared step2.obs step2.owner = 0 := by
  cases hLeaf with
  | applyReady hReady =>
    cases hReady with
    | unownedAbsent desired =>
      simp [executeStableStep, planPath, pureRecoverPath, classifyPath, selectModeKind, selectModeAction,
            selectModePerms, requiredPermissions, authorizeSelection, buildEnvOp, buildMetadataCandidate,
            buildOwnershipUpdate, permRegular, permSetOwn, permNone,
            PermSet.allows, isObservationUnknown, isProtectedStructure, isMatchA, pathRank]
    | unownedMatching desired =>
      simp [executeStableStep, planPath, pureRecoverPath, classifyPath, selectModeKind, selectModeAction,
            selectModePerms, requiredPermissions, authorizeSelection, buildEnvOp, buildMetadataCandidate,
            buildOwnershipUpdate, permRegular, permOwn, permNone,
            PermSet.allows, isObservationUnknown, isProtectedStructure, isMatchA, pathRank]
    | selfOwnedDeclared desired actual orig =>
      by_cases hEq : (actual == desired) = true
      · have hValEq : actual = desired := beq_iff_eq.mp hEq
        subst hValEq
        simp [executeStableStep, planPath, pureRecoverPath, classifyPath, selectModeKind, selectModeAction,
              selectModePerms, authorizeSelection, buildEnvOp, buildMetadataCandidate,
              buildOwnershipUpdate, permNone,
              isObservationUnknown, isProtectedStructure, isMatchA, pathRank]
      · simp only [Bool.not_eq_true] at hEq
        simp [hEq, executeStableStep, planPath, pureRecoverPath, classifyPath, selectModeKind, selectModeAction,
              selectModePerms, requiredPermissions, authorizeSelection, buildEnvOp, buildMetadataCandidate,
              buildOwnershipUpdate, permRegular, permSetOwn, permNone,
              PermSet.allows, isObservationUnknown, isProtectedStructure, isMatchA, pathRank]
    | retiredModified val obs hKnown =>
      simp [hKnown, executeStableStep, planPath, pureRecoverPath, classifyPath, selectModeKind, selectModeAction,
            selectModePerms, requiredPermissions, authorizeSelection, buildEnvOp, buildMetadataCandidate,
            buildOwnershipUpdate, permRegular, permOwn, permNone,
            PermSet.allows, isMatchA, pathRank]
    | retiredCreatedAbsent val =>
      simp [executeStableStep, planPath, pureRecoverPath, classifyPath, selectModeKind, selectModeAction,
            selectModePerms, requiredPermissions, authorizeSelection, buildEnvOp, buildMetadataCandidate,
            buildOwnershipUpdate, permRegular, permOwn, permNone,
            PermSet.allows, isObservationUnknown, isProtectedStructure, isMatchA, pathRank]
  | retiredCreatedPresent val =>
    simp [executeStableStep, planPath, pureRecoverPath, classifyPath, selectModeKind, selectModeAction,
          selectModePerms, requiredPermissions, authorizeSelection, buildEnvOp, buildMetadataCandidate,
          buildOwnershipUpdate, permRegular, permDeleteOwn, permNone,
          PermSet.allows, isObservationUnknown, isProtectedStructure, isMatchA, pathRank]

/-- **Non-Convergence of `Apply` Alone on Retired `Created` and `Prune` Alone on `Apply` Work (ADR 0005 §3.3, Counterexample #29)**:
    1. A retired `Created` leaf with `obs = .presentLeaf val` remains un-converged (`isMatchA = false`, `pathRank = 2`) after `Mode.apply` (`PruneRequired`, `K604`).
    2. An unowned declared `Absent` leaf or a retired `Modified` leaf remains un-converged (`isMatchA = false`) after `Mode.prune`. -/
theorem apply_alone_and_prune_alone_separation
    (path : PathID) (actor : ActorID) (val : Value) :
    let retCreated : PathInput := {
      path := path, actor := actor, declared := none, obs := .presentLeaf val, anc := .clear,
      owner := some { manager := actor, origin := .created, lastApplied := val, pending := none },
      granted := permRegular
    }
    let declCreate : PathInput := {
      path := path, actor := actor, declared := some val, obs := .absent, anc := .clear,
      owner := none, granted := permRegular
    }
    let retModified : PathInput := {
      path := path, actor := actor, declared := none, obs := .presentLeaf val, anc := .clear,
      owner := some { manager := actor, origin := .modified, lastApplied := val, pending := none },
      granted := permRegular
    }
    let afterApplyOnCreated := executeStableStep retCreated .apply
    let afterPruneOnCreate := executeStableStep declCreate .prune
    let afterPruneOnModified := executeStableStep retModified .prune
    (planPath retCreated .apply).selection = .deferred ∧
    isMatchA actor afterApplyOnCreated.declared afterApplyOnCreated.obs afterApplyOnCreated.owner = false ∧
    pathRank actor afterApplyOnCreated.declared afterApplyOnCreated.obs afterApplyOnCreated.owner = 2 ∧
    isMatchA actor afterPruneOnCreate.declared afterPruneOnCreate.obs afterPruneOnCreate.owner = false ∧
    isMatchA actor afterPruneOnModified.declared afterPruneOnModified.obs afterPruneOnModified.owner = false := by
  simp [executeStableStep, planPath, pureRecoverPath, classifyPath, selectModeKind, selectModeAction,
        selectModePerms, authorizeSelection, buildEnvOp, buildMetadataCandidate,
        buildOwnershipUpdate, permNone, isObservationUnknown, isProtectedStructure, isMatchA, pathRank]

/-- **`DeferredUntilRelease` Structural Migration Lemma (ADR 0005 §3.3, Counterexamples #20, #49, #55, #56)**:
    When a retired `Origin = modified` path overlaps a newly declared path:
    1. In pass 1 (`overlap = .retiredModifiedPendingRelease`), the new path is `.deferred`
       (`StateClass.deferredUntilRelease`) while the old `Modified` record is unmanaged to `none`.
    2. If the old `Modified` path has an `Unknown` observation or lacks `Own` permission (#56),
       `gateBatch` blocks the entire batch (`blocked = true`).
    3. In pass 2 (after the old `Modified` record is released so `overlap = .none`):
       - If the external environment has migrated (`obs = .presentLeaf desired`), `apply` converges (`isMatchA = true`).
       - If the external environment still has the old `Record` structure (`obs = .presentRecord`),
         `apply` halts with `StateClass.declaredConflict` and `DecisionKind.blocker`. -/
theorem retired_modified_migration_lemma
    (oldPath newPath : PathID) (actor : ActorID) (oldVal desired : Value) :
    let oldInp : PathInput := {
      path := oldPath, actor := actor, declared := none,
      obs := .absent, anc := .clear,
      owner := some { manager := actor, origin := .modified, lastApplied := oldVal, pending := none },
      granted := permRegular, overlap := .none, hasUnresolvedSelfPending := false
    }
    let oldUnknownInp : PathInput := { oldInp with obs := .unknown }
    let oldNoPermInp : PathInput := { oldInp with granted := permNone }
    let newPass1 : PathInput := {
      path := newPath, actor := actor, declared := some desired,
      obs := .presentLeaf desired, anc := .clear, owner := none,
      granted := permRegular, overlap := .retiredModifiedPendingRelease, hasUnresolvedSelfPending := false
    }
    let newPass2Migrated : PathInput := { newPass1 with overlap := .none, obs := .presentLeaf desired }
    let newPass2Unmigrated : PathInput := { newPass1 with overlap := .none, obs := .presentRecord }
    (planPath newPass1 .apply).stateClass = .deferredUntilRelease ∧
    (planPath newPass1 .apply).decision = .deferred ∧
    (executeStableStep oldInp .apply).owner = none ∧
    (gateBatch [planPath oldUnknownInp .apply, planPath newPass1 .apply] false false).blocked = true ∧
    (gateBatch [planPath oldNoPermInp .apply, planPath newPass1 .apply] false false).blocked = true ∧
    isMatchA actor (some desired) (executeStableStep newPass2Migrated .apply).obs (executeStableStep newPass2Migrated .apply).owner = true ∧
    (planPath newPass2Unmigrated .apply).stateClass = .declaredConflict ∧
    (planPath newPass2Unmigrated .apply).decision = .blocker := by
  simp [executeStableStep, gateBatch, isBlockingDecision, planPath, pureRecoverPath, classifyPath,
        selectModeKind, selectModeAction, selectModePerms, requiredPermissions, authorizeSelection,
        buildEnvOp, buildMetadataCandidate, buildOwnershipUpdate, permRegular, permOwn, permNone,
        PermSet.allows, isObservationUnknown, isProtectedStructure, isMatchA]

/-- **Progress Step Strict Rank Decrease and Converged Stutter Theorem (ADR 0005 §3.3)**:
    Every productive reconciliation step on an un-converged path strictly decreases `pathRank`:
    - Unowned creation (`2 → 0`)
    - Consistent adoption (`1 → 0`)
    - Retired `Modified` unmanagement (`1 → 0`)
    - Retired `Created` deletion in `prune` (`2 → 0`)
    While a converged path (`isMatchA = true`) stutters at `pathRank = 0`. -/
theorem progress_step_strict_rank_decrease
    (path : PathID) (actor : ActorID) (val : Value) :
    let createInp : PathInput := {
      path := path, actor := actor, declared := some val, obs := .absent, anc := .clear,
      owner := none, granted := permRegular
    }
    let adoptInp : PathInput := {
      path := path, actor := actor, declared := some val, obs := .presentLeaf val, anc := .clear,
      owner := none, granted := permRegular
    }
    let unmanageInp : PathInput := {
      path := path, actor := actor, declared := none, obs := .presentLeaf val, anc := .clear,
      owner := some { manager := actor, origin := .modified, lastApplied := val, pending := none },
      granted := permRegular
    }
    let pruneInp : PathInput := {
      path := path, actor := actor, declared := none, obs := .presentLeaf val, anc := .clear,
      owner := some { manager := actor, origin := .created, lastApplied := val, pending := none },
      granted := permRegular
    }
    let convergedInp : PathInput := {
      path := path, actor := actor, declared := some val, obs := .presentLeaf val, anc := .clear,
      owner := some { manager := actor, origin := .created, lastApplied := val, pending := none },
      granted := permRegular
    }
    pathRank actor createInp.declared (executeStableStep createInp .apply).obs (executeStableStep createInp .apply).owner <
      pathRank actor createInp.declared createInp.obs createInp.owner ∧
    pathRank actor adoptInp.declared (executeStableStep adoptInp .apply).obs (executeStableStep adoptInp .apply).owner <
      pathRank actor adoptInp.declared adoptInp.obs adoptInp.owner ∧
    pathRank actor unmanageInp.declared (executeStableStep unmanageInp .apply).obs (executeStableStep unmanageInp .apply).owner <
      pathRank actor unmanageInp.declared unmanageInp.obs unmanageInp.owner ∧
    pathRank actor pruneInp.declared (executeStableStep pruneInp .prune).obs (executeStableStep pruneInp .prune).owner <
      pathRank actor pruneInp.declared pruneInp.obs pruneInp.owner ∧
    pathRank actor convergedInp.declared (executeStableStep convergedInp .apply).obs (executeStableStep convergedInp .apply).owner =
      pathRank actor convergedInp.declared convergedInp.obs convergedInp.owner := by
  simp [executeStableStep, planPath, pureRecoverPath, classifyPath, selectModeKind, selectModeAction,
        selectModePerms, requiredPermissions, authorizeSelection, buildEnvOp, buildMetadataCandidate,
        buildOwnershipUpdate, permRegular, permSetOwn, permOwn, permDeleteOwn, permNone,
        PermSet.allows, isObservationUnknown, isProtectedStructure, isMatchA, pathRank]

end Kisage
