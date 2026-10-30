import Kisage.Protocol

namespace Kisage.ProtocolTests

-- Positive traces ensure that tightening the guards does not eliminate real executions.
def input (ex : EnvExecution) : PathInput :=
  { path := ex.op.path, actor := ex.actor, owner := ex.beforeOwner, obs := ex.expected, anc := .clear, granted := permRegular, declared := match ex with
      | .create _ _ t | .update _ _ _ _ t => some t
      | .delete .. => none }

def initial (ex : EnvExecution) : ProtocolState :=
  initialProtocolState ex.expected ex.beforeOwner none none (some ex.actor)

def planned (ex : EnvExecution) (mode : Mode) : ProtocolState :=
  { initial ex with
      plannedInput := some (input ex)
      plannedMode := mode
      gate := gateBatch [planPath (input ex) mode] false false
    }

def pending (ex : EnvExecution) (mode : Mode) : ProtocolState :=
  { planned ex mode with storeEntry := some ex.intentEntry, active := some ex, applied := false, validated := false }

def applied (ex : EnvExecution) (mode : Mode) : ProtocolState :=
  { pending ex mode with
      visibleEnv := ex.result
      targetStabilized := false
      applied := true
      validated := false
      createdSessionWitness := if ex.isCreate then some ex.actor else some ex.actor
    }

def validated (ex : EnvExecution) (mode : Mode) : ProtocolState :=
  { applied ex mode with validated := true }

def durable (ex : EnvExecution) (mode : Mode) : ProtocolState :=
  { validated ex mode with durableEnv := ex.result, targetStabilized := true }

def committed (ex : EnvExecution) (mode : Mode) : ProtocolState :=
  { durable ex mode with
      storeEntry := ex.afterOwner
      active := none
      applied := false
      validated := false
      plannedInput := none
    }

theorem complete_execution (ex : EnvExecution) (mode : Mode)
    (hHistory : EntryProvenance (some ex.actor) ex.beforeOwner)
    (hPlan : (planPath (input ex) mode).envOp = some ex.op)
    (hGate : (planned ex mode).gate.blocked = false)
    (hMember : ex.op ∈ (planned ex mode).gate.envChanges) :
    Reachable (committed ex mode) ∧ Reachable (pending ex mode) ∧ Reachable (applied ex mode) := by
  have h0 : Reachable (initial ex) :=
    Reachable.initial _ _ _ _ _ hHistory (by simp)
  have h1 : Step (initial ex) (.plan mode) (planned ex mode) :=
    Step.plan _ (input ex) [] mode false false rfl rfl rfl rfl (by simp [initial, initialProtocolState])
  have h2 : Step (planned ex mode) (.intent ex) (pending ex mode) :=
    Step.intent _ ex (input ex) rfl rfl hGate hMember rfl hPlan rfl rfl rfl rfl rfl
  have h3 : Step (pending ex mode) (.providerApply ex) (applied ex mode) :=
    Step.providerApply _ ex rfl rfl rfl rfl hGate
  have h4 : Step (applied ex mode) .validate (validated ex mode) :=
    Step.validate _ ex rfl rfl rfl
  have h5 : Step (validated ex mode) .stabilize (durable ex mode) := Step.stabilize _
  have h6 : Step (durable ex mode) (.commit false) (committed ex mode) :=
    Step.commit _ ex rfl rfl rfl rfl rfl hGate
  have hp := Reachable.step (Reachable.step h0 h1) h2
  have ha := Reachable.step hp h3
  exact ⟨Reachable.step (Reachable.step (Reachable.step ha h4) h5) h6, hp, ha⟩

-- Create, update of both origins, and Prune delete all execute and settle normally.
example : Reachable (committed (.create 1 1 10) .apply) := by
  apply (complete_execution (.create 1 1 10) .apply (by simp [EntryProvenance, EnvExecution.beforeOwner]) (by decide) (by decide) (by decide)).1

example (origin : Origin) : Reachable (committed (.update 1 1 origin 10 20) .apply) := by
  cases origin <;>
    exact (complete_execution _ _ (by simp [EntryProvenance, EnvExecution.beforeOwner, EnvExecution.actor]) (by decide) (by decide) (by decide)).1

example : Reachable (committed (.delete 1 1 10) .prune) := by
  exact (complete_execution _ _ (by simp [EntryProvenance, EnvExecution.beforeOwner, EnvExecution.actor]) (by decide) (by decide) (by decide)).1

-- The original Intent → Validate → Commit counterexample is rejected at Validate and Commit.
example {s s' : ProtocolState} (hNotApplied : s.applied = false) : ¬ Step s .validate s' := by
  intro h
  cases h
  simp_all

example {s s' : ProtocolState} {ex : EnvExecution}
    (hActive : s.active = some ex) (hNotApplied : s.applied = false) :
    ¬ Step s (.commit false) s' := by
  intro h
  cases h <;> simp_all

-- A blocker on a DIFFERENT path suppresses the otherwise authorized create.
def createInput : PathInput := input (.create 1 1 10)
def conflictingSibling : PathInput := { createInput with path := 2, obs := .presentLeaf 99 }
def blocked : ProtocolState :=
  { initial (.create 1 1 10) with
      plannedInput := some createInput
      gate := gateBatch [planPath createInput .apply, planPath conflictingSibling .apply] false false
    }

example : Step (initial (.create 1 1 10)) (.plan .apply) blocked :=
  Step.plan _ createInput [conflictingSibling] .apply false false rfl rfl rfl rfl (by simp [initial, initialProtocolState])
example : blocked.gate.blocked = true ∧ blocked.gate.envChanges = [] := by decide
example {s' : ProtocolState} {lbl : StepLabel} (hNormal : lbl.isNormalExecution = true) :
    ¬ Step blocked lbl s' := by
  intro h
  have hg := protocol_normal_execution_requires_open_gate h hNormal
  change true = false at hg
  contradiction

-- Nonempty foreign state is reachable and protected, rather than excluded by initialization.
def foreignPending : OwnerEntry := (EnvExecution.create 2 2 20).intentEntry
def foreignInitial : ProtocolState := initialProtocolState .absent none (some 2) (some foreignPending) none
example : Reachable foreignInitial :=
  Reachable.initial _ _ _ _ _ (by simp [EntryProvenance]) (by simp)
example : foreignInitial.foreignReservation = some 2 ∧ foreignInitial.unrelatedForeignPending = some foreignPending := by decide
example {s' : ProtocolState} {ex : EnvExecution} : ¬ Step foreignInitial (.intent ex) s' := by
  intro h
  cases h
  contradiction

-- Recovery confirmation, rollback, delete cleanup, third values, Unknown and denied Own.
def recoveryState (ex : EnvExecution) (obs : Observed) : ProtocolState :=
  initialProtocolState obs (some ex.intentEntry) none (some foreignPending) (some ex.actor)
def recovered (ex : EnvExecution) (obs : Observed) : ProtocolState :=
  { recoveryState ex obs with
    storeEntry := (pureRecoverPath ex.actor (some ex.intentEntry) obs .clear permOwn).virtualOwner, applied := false, validated := false, plannedInput := none }

example : Step (recoveryState (.create 1 1 10) .absent) (.recover 1 permOwn)
    (recovered (.create 1 1 10) .absent) := Step.recover _ _ .clear _ rfl rfl (by decide) rfl
example : (recovered (.create 1 1 10) .absent).storeEntry = none := by decide
example : (recovered (.create 1 1 10) (.presentLeaf 10)).storeEntry =
    some { manager := 1, origin := .modified, lastApplied := 10 } := by decide
example : (recovered (.update 1 1 .created 10 20) (.presentLeaf 10)).storeEntry =
    some { manager := 1, origin := .created, lastApplied := 10 } := by decide
example : (recovered (.update 1 1 .created 10 20) (.presentLeaf 20)).storeEntry =
    some { manager := 1, origin := .created, lastApplied := 20 } := by decide
example : (recovered (.delete 1 1 10) .absent).storeEntry = none := by decide
example : (recovered (.delete 1 1 10) (.presentLeaf 10)).storeEntry =
    some { manager := 1, origin := .created, lastApplied := 10 } := by decide

example (obs : Observed) (hBad : obs = .unknown ∨ obs = .presentLeaf 99)
    {s' : ProtocolState} : ¬ Step (recoveryState (.create 1 1 10) obs) (.recover 1 permOwn) s' := by
  rcases hBad with rfl | rfl <;> intro h <;>
    have hAuth := (protocol_recovery_matches_pure h).1 <;> simp_all [recoveryState, initialProtocolState, pureRecoverPath, classifyPendingIntent, EnvExecution.intentEntry, isObservationUnknown, permOwn]

example {s' : ProtocolState} :
    ¬ Step (recoveryState (.create 1 1 10) (.presentLeaf 10)) (.recover 1 permNone) s' := by
  intro h
  have hAuth := (protocol_recovery_matches_pure h).1
  simp_all [recoveryState, initialProtocolState, pureRecoverPath, classifyPendingIntent, EnvExecution.intentEntry, isObservationUnknown, PermSet.allows, permOwn, permNone]

-- Recovery retains the independently initialized foreign Pending even when deleting our entry.
example : (recovered (.delete 1 1 10) .absent).unrelatedForeignPending = some foreignPending := by decide

private theorem intent_history (ex : EnvExecution) :
    EntryProvenance (some ex.actor) (some ex.intentEntry) := by
  cases ex <;> simp [EntryProvenance, EnvExecution.intentEntry, EnvExecution.actor]
  all_goals intro p v hp
  all_goals cases hp
  all_goals simp

theorem recovery_trace (ex : EnvExecution) (obs : Observed)
    (hAuth : (pureRecoverPath ex.actor (some ex.intentEntry) obs .clear permOwn).authorized = true) :
    Reachable (recovered ex obs) := by
  have h0 : Reachable (recoveryState ex obs) :=
    Reachable.initial _ _ _ _ _ (intent_history ex) (by simp)
  exact Reachable.step h0 (Step.recover _ _ .clear _ rfl rfl hAuth rfl)

example : Reachable (recovered (.create 1 1 10) .absent) := recovery_trace _ _ (by decide)
example : Reachable (recovered (.create 1 1 10) (.presentLeaf 10)) := recovery_trace _ _ (by decide)
example : Reachable (recovered (.update 1 1 .created 10 20) (.presentLeaf 10)) := recovery_trace _ _ (by decide)
example : Reachable (recovered (.update 1 1 .created 10 20) (.presentLeaf 20)) := recovery_trace _ _ (by decide)
example : Reachable (recovered (.delete 1 1 10) .absent) := recovery_trace _ _ (by decide)
example : Reachable (recovered (.delete 1 1 10) (.presentLeaf 10)) := recovery_trace _ _ (by decide)

-- A new process must stabilize visible changes before persisting recovery.
example {s' : ProtocolState} :
    ¬ Step { recoveryState (.create 1 1 10) (.presentLeaf 10) with targetStabilized := false }
      (.recover 1 permOwn) s' := by
  intro h
  cases h
  contradiction

-- A power loss between rename and sync loses the creation; recovery cancels its Intent.
def lostCreate : ProtocolState :=
  { applied (.create 1 1 10) .apply with
      visibleEnv := .absent
      targetStabilized := true
      active := none
      applied := false
      validated := false
      plannedInput := none
    }
def recoveredLostCreate : ProtocolState := { lostCreate with storeEntry := none }
example : Reachable recoveredLostCreate := by
  have ha := (complete_execution (.create 1 1 10) .apply
    (by simp [EntryProvenance, EnvExecution.beforeOwner]) (by decide) (by decide) (by decide)).2.2
  have hc : Step (applied (.create 1 1 10) .apply) (.crash true) lostCreate := Step.crash _ true
  have hr : Step lostCreate (.recover 1 permOwn) recoveredLostCreate :=
    Step.recover _ _ .clear _ rfl rfl (by decide) rfl
  exact Reachable.step (Reachable.step ha hc) hr
example : recoveredLostCreate.storeEntry = none ∧ recoveredLostCreate.visibleEnv = .absent := by decide

-- Planning with a nonempty overlapping foreign reservation is possible, but execution is blocked.
def foreignInput : PathInput := { createInput with overlap := .foreignConflict }
def foreignPlanned : ProtocolState :=
  { foreignInitial with
      plannedInput := some foreignInput
      gate := gateBatch [planPath foreignInput .apply] false false
    }
example : Step foreignInitial (.plan .apply) foreignPlanned :=
  Step.plan _ foreignInput [] .apply false false rfl rfl rfl rfl (by intros; rfl)
example : foreignPlanned.gate.blocked = true := by decide
example {s' : ProtocolState} {lbl : StepLabel} (hNormal : lbl.isNormalExecution = true) :
    ¬ Step foreignPlanned lbl s' := by
  intro h
  have hg := protocol_normal_execution_requires_open_gate h hNormal
  change true = false at hg
  contradiction

-- A caller cannot substitute a clear ancestor for an actually unknown ancestor.
example {s' : ProtocolState} :
    ¬ Step { recoveryState (.create 1 1 10) (.presentLeaf 10) with ancestor := .unknownAncestor }
      (.recover 1 permOwn) s' := by
  intro h
  have hAuth := (protocol_recovery_matches_pure h).1
  simp [recoveryState, initialProtocolState, pureRecoverPath, classifyPendingIntent,
    EnvExecution.intentEntry, isObservationUnknown] at hAuth

end Kisage.ProtocolTests
