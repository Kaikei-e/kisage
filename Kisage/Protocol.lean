import Kisage.Model
import Kisage.Recover
import Kisage.Plan
import Kisage.Theorems

namespace Kisage

/-!
Single-path projection of the locked execution protocol. Other paths participate in the
batch gate; overlapping reservations and non-overlapping foreign Pending records are
explicit inputs. Atomic store persistence, conditional Provider application and locking
are the trust boundary. Initial Created history must be supplied independently.
-/

/-- A normal environment operation, including its settled pre-state and durable Intent.
    The active delete Intent is distinct from an unresolved Pending left by a prior run. -/
inductive EnvExecution where
  | create (path : PathID) (actor : ActorID) (target : Value)
  | update (path : PathID) (actor : ActorID) (origin : Origin) (previous target : Value)
  | delete (path : PathID) (actor : ActorID) (previous : Value)
  deriving DecidableEq, Repr

def EnvExecution.actor : EnvExecution → ActorID
  | .create _ a _ | .update _ a _ _ _ | .delete _ a _ => a

def EnvExecution.beforeOwner : EnvExecution → Option OwnerEntry
  | .create .. => none
  | .update _ a o v _ => some { manager := a, origin := o, lastApplied := v }
  | .delete _ a v => some { manager := a, origin := .created, lastApplied := v }

def EnvExecution.intentEntry : EnvExecution → OwnerEntry
  | .create _ a t => {
      manager := a
      origin := .modified
      lastApplied := t
      pending := some { op := .set, expected := none, target := some t, previousEntry := none }
    }
  | .update _ a o v t => {
      manager := a
      origin := o
      lastApplied := v
      pending := some { op := .set, expected := some v, target := some t, previousEntry := some (o, v) }
    }
  | .delete _ a v => {
      manager := a
      origin := .created
      lastApplied := v
      pending := some { op := .delete, expected := some v, target := none, previousEntry := some (.created, v) }
    }

def EnvExecution.afterOwner : EnvExecution → Option OwnerEntry
  | .create _ a t => some { manager := a, origin := .created, lastApplied := t }
  | .update _ a o _ t => some { manager := a, origin := o, lastApplied := t }
  | .delete .. => none

def EnvExecution.expected : EnvExecution → Observed
  | .create .. => .absent
  | .update _ _ _ v _ | .delete _ _ v => .presentLeaf v

def EnvExecution.result : EnvExecution → Observed
  | .create _ _ t | .update _ _ _ _ t => .presentLeaf t
  | .delete .. => .absent

def EnvExecution.op : EnvExecution → EnvOp
  | .create p _ t => .set p none t
  | .update p _ _ v t => .set p (some v) t
  | .delete p _ v => .delete p v

def EnvExecution.isCreate : EnvExecution → Bool
  | .create .. => true
  | _ => false

/-- A session witness can originate at a verified conditional create, or in explicitly
    supplied initial history. Merely reading a Created flag never manufactures a witness. -/
def EntryProvenance (witness : Option ActorID) (owner : Option OwnerEntry) : Prop :=
  ∀ e, owner = some e →
    (e.origin = .created → witness = some e.manager) ∧
    (∀ p v, e.pending = some p → p.previousEntry = some (.created, v) → witness = some e.manager)

structure ProtocolState where
  durableEnv : Observed
  visibleEnv : Observed
  targetStabilized : Bool
  ancestor : AncestorStatus := .clear
  storeEntry : Option OwnerEntry
  foreignReservation : Option ActorID
  unrelatedForeignPending : Option OwnerEntry
  initialForeignPending : Option OwnerEntry
  createdSessionWitness : Option ActorID
  active : Option EnvExecution := none
  applied : Bool := false
  validated : Bool := false
  plannedInput : Option PathInput := none
  plannedMode : Mode := .apply
  gate : BatchGateResult := { blocked := true, envChanges := [], metadataCandidates := [], ownershipUpdates := [] }
  deriving DecidableEq, Repr

/-- Arbitrary persisted state, with initial history checked separately by Reachable.initial. -/
def initialProtocolState (obs : Observed) (owner : Option OwnerEntry)
    (reservation : Option ActorID) (foreignPending : Option OwnerEntry)
    (history : Option ActorID) : ProtocolState :=
  {
      durableEnv := obs
      visibleEnv := obs
      targetStabilized := true
      storeEntry := owner
      foreignReservation := reservation
      unrelatedForeignPending := foreignPending
      initialForeignPending := foreignPending
      createdSessionWitness := history
    }

def initProtocolState : ProtocolState := initialProtocolState .absent none none none none

inductive StepLabel where
  | observe
  | plan (mode : Mode)
  | intent (execution : EnvExecution)
  | providerApply (execution : EnvExecution)
  | validate
  | stabilize
  | commit (indeterminate : Bool)
  | crash (powerLoss : Bool)
  | recover (actor : ActorID) (granted : PermSet)
  | adopt (actor : ActorID) (val : Value) (granted : PermSet)
  | release (actor : ActorID) (granted : PermSet)
  | externalWrite (obs : Observed)
  deriving DecidableEq, Repr

/-- State invariants supporting the nine protocol safety theorems below. The gate may
    be blocked, and foreign records may be nonempty in reachable states. -/
structure ProtocolInvariant (s : ProtocolState) : Prop where
  createdProvenance : EntryProvenance s.createdSessionWitness s.storeEntry
  actorExclusivity : s.foreignReservation ≠ none → s.storeEntry = none
  activeIntent : ∀ ex, s.active = some ex → s.storeEntry = some ex.intentEntry
  appliedIntent : s.applied = true → s.active.isSome = true
  creationAuthenticity : ∀ ex, s.active = some ex → s.applied = true →
    ex.isCreate = true → s.createdSessionWitness = some ex.actor
  validatedApplication : s.validated = true → s.applied = true ∧
    ∃ ex, s.active = some ex ∧ s.visibleEnv = ex.result
  stabilizedEnvironment : s.targetStabilized = true → s.durableEnv = s.visibleEnv
  normalBatchAllOrNothing : s.active.isSome = true → s.gate.blocked = false
  foreignNonOverlappingPendingPreserved : s.unrelatedForeignPending = s.initialForeignPending

inductive Step : ProtocolState → StepLabel → ProtocolState → Prop where
  | observe (s) : Step s .observe s
  | plan (s : ProtocolState) (inp : PathInput) (others : List PathInput) (mode : Mode)
      (unresolved quarantined : Bool)
      (hIdle : s.active = none)
      (hOwner : inp.owner = s.storeEntry) (hObs : inp.obs = s.visibleEnv)
      (hAnc : inp.anc = s.ancestor)
      (hForeign : s.foreignReservation ≠ none → inp.overlap = .foreignConflict) :
      Step s (.plan mode) { s with
          plannedInput := some inp
          plannedMode := mode
          gate := gateBatch (planPath inp mode :: others.map (fun i => planPath i mode)) unresolved quarantined
        }
  | intent (s : ProtocolState) (ex : EnvExecution) (inp : PathInput)
      (hIdle : s.active = none) (hPlanned : s.plannedInput = some inp)
      (hGate : s.gate.blocked = false) (hMember : ex.op ∈ s.gate.envChanges)
      (hActor : inp.actor = ex.actor)
      (hPlan : (planPath inp s.plannedMode).envOp = some ex.op)
      (hOwner : s.storeEntry = ex.beforeOwner) (hInputOwner : inp.owner = ex.beforeOwner)
      (hObs : s.visibleEnv = ex.expected) (hInputObs : inp.obs = ex.expected)
      (hForeign : s.foreignReservation = none) :
      Step s (.intent ex) { s with
          storeEntry := some ex.intentEntry
          active := some ex
          applied := false
          validated := false
        }
  | providerApply (s : ProtocolState) (ex : EnvExecution)
      (hActive : s.active = some ex) (hNotApplied : s.applied = false)
      (hIntent : s.storeEntry = some ex.intentEntry) (hExpected : s.visibleEnv = ex.expected)
      (hGate : s.gate.blocked = false) :
      Step s (.providerApply ex) { s with
          visibleEnv := ex.result
          targetStabilized := false
          applied := true
          validated := false
          createdSessionWitness := if ex.isCreate then some ex.actor else s.createdSessionWitness
        }
  | validate (s : ProtocolState) (ex : EnvExecution)
      (hActive : s.active = some ex) (hApplied : s.applied = true)
      (hResult : s.visibleEnv = ex.result) :
      Step s .validate { s with validated := true }
  | stabilize (s : ProtocolState) :
      Step s .stabilize { s with durableEnv := s.visibleEnv, targetStabilized := true }
  | commit (s : ProtocolState) (ex : EnvExecution)
      (hActive : s.active = some ex) (hValidated : s.validated = true)
      (hApplied : s.applied = true) (hResult : s.visibleEnv = ex.result)
      (hStab : s.targetStabilized = true) (hGate : s.gate.blocked = false) :
      Step s (.commit false) { s with
          storeEntry := ex.afterOwner
          active := none
          applied := false
          validated := false
          plannedInput := none
        }
  | commitIndeterminate (s : ProtocolState) : Step s (.commit true) s
  | crash (s : ProtocolState) (powerLoss : Bool) :
      Step s (.crash powerLoss) { s with
        visibleEnv := if powerLoss then s.durableEnv else s.visibleEnv
        targetStabilized := if powerLoss then true else s.targetStabilized
        active := none
        applied := false
        validated := false
        plannedInput := none }
  | recover (s : ProtocolState) (actor : ActorID) (anc : AncestorStatus) (granted : PermSet)
      (hIdle : s.active = none) (hAnc : anc = s.ancestor)
      (hAuth : (pureRecoverPath actor s.storeEntry s.visibleEnv anc granted).authorized = true)
      (hStab : s.targetStabilized = true) :
      Step s (.recover actor granted) { s with
        storeEntry := (pureRecoverPath actor s.storeEntry s.visibleEnv anc granted).virtualOwner
        applied := false
        validated := false
        plannedInput := none }
  | metadataAdopt (s : ProtocolState) (path : PathID) (actor : ActorID) (val : Value) (inp : PathInput)
      (hPlanned : s.plannedInput = some inp) (hInputOwner : inp.owner = s.storeEntry)
      (hInputObs : inp.obs = s.visibleEnv) (hActor : inp.actor = actor)
      (hPlan : (planPath inp s.plannedMode).metadataCandidate = some (.adoptModified path val))
      (hIdle : s.active = none) (hOwner : s.storeEntry = none)
      (hObs : s.visibleEnv = .presentLeaf val) (hStab : s.targetStabilized = true)
      (hForeign : s.foreignReservation = none) (hGate : s.gate.blocked = false)
      (hUpdate : OwnershipUpdate.upsert path {
          manager := actor
          origin := .modified
          lastApplied := val
        } false ∈ s.gate.ownershipUpdates) :
      Step s (.commit false) { s with
          storeEntry := some { manager := actor, origin := .modified, lastApplied := val }
          applied := false
          validated := false
          plannedInput := none
          createdSessionWitness := none
        }
  | metadataRemove (s : ProtocolState) (path : PathID) (inp : PathInput)
      (hPlanned : s.plannedInput = some inp) (hInputOwner : inp.owner = s.storeEntry)
      (hInputObs : inp.obs = s.visibleEnv)
      (hPlan : (planPath inp s.plannedMode).ownershipUpdate = some (.remove path false))
      (hIdle : s.active = none) (hStab : s.targetStabilized = true)
      (hSettled : ∀ e, s.storeEntry = some e → e.pending = none)
      (hGate : s.gate.blocked = false)
      (hUpdate : OwnershipUpdate.remove path false ∈ s.gate.ownershipUpdates) :
      Step s (.commit false) { s with
          storeEntry := none
          applied := false
          validated := false
          plannedInput := none
          createdSessionWitness := none
        }
  | adopt (s : ProtocolState) (actor : ActorID) (val : Value) (granted : PermSet)
      (hIdle : s.active = none) (hObs : s.visibleEnv = .presentLeaf val)
      (hAnc : s.ancestor = .clear)
      (hPerm : PermSet.allows granted permOwnResolve = true)
      (hStab : s.targetStabilized = true) (hForeign : s.foreignReservation = none)
      (hOwner : ∀ e, s.storeEntry = some e → e.manager = actor ∧ e.pending = none) :
      Step s (.adopt actor val granted) { s with
        storeEntry := some { manager := actor, origin := .modified, lastApplied := val }
        applied := false
        validated := false
        plannedInput := none
        createdSessionWitness := none }
  | release (s : ProtocolState) (actor : ActorID) (granted : PermSet) (e : OwnerEntry)
      (hIdle : s.active = none) (hOwner : s.storeEntry = some e) (hMgr : e.manager = actor)
      (hNoPend : e.pending = none) (hKnown : isObservationUnknown s.visibleEnv s.ancestor = false)
      (hPerm : PermSet.allows granted permOwnResolve = true)
      (hStab : s.targetStabilized = true) :
      Step s (.release actor granted) { s with
          storeEntry := none
          applied := false
          validated := false
          plannedInput := none
          createdSessionWitness := none
        }
  | externalWrite (s : ProtocolState) (obs : Observed) (anc : AncestorStatus) :
      Step s (.externalWrite obs) { s with
          visibleEnv := obs
          ancestor := anc
          targetStabilized := false
          active := none
          applied := false
          validated := false
          plannedInput := none
        }

/-- Authorized recovery is settled, keeps the manager, and can preserve Created only
    from an independently witnessed previous Created entry. Covers confirmation AND rollback. -/
theorem recovery_authorized_shape (actor : ActorID) (owner : Option OwnerEntry)
    (obs : Observed) (anc : AncestorStatus) (granted : PermSet)
    (hAuth : (pureRecoverPath actor owner obs anc granted).authorized = true) :
    ∀ e', (pureRecoverPath actor owner obs anc granted).virtualOwner = some e' →
      e'.manager = actor ∧ e'.pending = none ∧
      (e'.origin = .created → ∃ e p v, owner = some e ∧ e.manager = actor ∧
        e.pending = some p ∧ p.previousEntry = some (.created, v)) := by
  unfold pureRecoverPath at hAuth ⊢
  cases owner with
  | none => contradiction
  | some e =>
    by_cases hm : e.manager = actor
    · simp only [hm, bne_self_eq_false, Bool.false_eq_true, ↓reduceIte] at hAuth ⊢
      cases hp : e.pending with
      | none => simp [hp] at hAuth
      | some p =>
        simp only [hp] at hAuth ⊢
        generalize hc : classifyPendingIntent actor p obs anc = result at hAuth ⊢
        obtain ⟨kind, next⟩ := result
        cases next with
        | none => contradiction
        | some next =>
          by_cases hg : PermSet.allows granted permOwn = true
          · simp only [hg, ↓reduceIte] at hAuth ⊢
            intro e' he
            cases next with
            | none => contradiction
            | some out =>
              simp only [Option.some.injEq] at he
              subst e'
              unfold classifyPendingIntent at hc
              split at hc
              · simp at hc
              · split at hc
                · simp at hc
                · cases ho : p.op <;> cases hv : p.previousEntry <;> cases ht : p.target <;>
                    cases hob : obs <;> simp [ho, hv, ht, hob] at hc
                  all_goals repeat first | split at hc | (simp_all)
                  all_goals repeat (first | split at hc | simp_all)
                  all_goals obtain ⟨_, rfl⟩ := hc
                  all_goals simp_all
                  all_goals intro hOrig
                  all_goals exact ⟨_, Prod.ext hOrig rfl⟩
          · simp [hg] at hAuth
    · simp [hm] at hAuth

private theorem intent_provenance (ex : EnvExecution) (w : Option ActorID)
    (h : EntryProvenance w ex.beforeOwner) : EntryProvenance w (some ex.intentEntry) := by
  cases ex <;> simp_all [EntryProvenance, EnvExecution.beforeOwner, EnvExecution.intentEntry]
  all_goals intro p v hp
  all_goals cases hp
  all_goals simp_all

private theorem apply_provenance (ex : EnvExecution) (w : Option ActorID)
    (h : EntryProvenance w (some ex.intentEntry)) :
    EntryProvenance (if ex.isCreate then some ex.actor else w) (some ex.intentEntry) := by
  cases ex <;> simp_all [EntryProvenance, EnvExecution.intentEntry, EnvExecution.isCreate, EnvExecution.actor]

private theorem commit_provenance (ex : EnvExecution) (w : Option ActorID)
    (h : EntryProvenance w (some ex.intentEntry))
    (hCreate : ex.isCreate = true → w = some ex.actor) :
    EntryProvenance w ex.afterOwner := by
  cases ex <;> simp_all [EntryProvenance, EnvExecution.intentEntry,
    EnvExecution.afterOwner, EnvExecution.isCreate, EnvExecution.actor]

private theorem recovery_provenance (actor : ActorID) (owner : Option OwnerEntry)
    (obs : Observed) (anc : AncestorStatus) (granted : PermSet) (w : Option ActorID)
    (h : EntryProvenance w owner)
    (hAuth : (pureRecoverPath actor owner obs anc granted).authorized = true) :
    EntryProvenance w (pureRecoverPath actor owner obs anc granted).virtualOwner := by
  intro e' he'
  obtain ⟨hm, hp, hprev⟩ := recovery_authorized_shape actor owner obs anc granted hAuth e' he'
  refine ⟨?_, ?_⟩
  · intro hc
    obtain ⟨e, p, v, he, hMgr, hPend, hPrev⟩ := hprev hc
    exact ((h e he).2 p v hPend hPrev).trans (by rw [hMgr, hm])
  · intro p v hePend _
    rw [hp] at hePend
    contradiction

inductive Reachable : ProtocolState → Prop where
  | initial (obs : Observed) (owner : Option OwnerEntry) (reservation : Option ActorID)
      (foreignPending : Option OwnerEntry) (history : Option ActorID)
      (hHistory : EntryProvenance history owner)
      (hExclusive : reservation ≠ none → owner = none) :
      Reachable (initialProtocolState obs owner reservation foreignPending history)
  | step {s s' : ProtocolState} {lbl : StepLabel} : Reachable s → Step s lbl s' → Reachable s'

theorem protocol_initial_invariant (obs : Observed) (owner : Option OwnerEntry)
    (reservation : Option ActorID) (foreignPending : Option OwnerEntry) (history : Option ActorID)
    (hHistory : EntryProvenance history owner) (hExclusive : reservation ≠ none → owner = none) :
    ProtocolInvariant (initialProtocolState obs owner reservation foreignPending history) := by
  constructor <;> simp_all [initialProtocolState]

theorem protocol_init_invariant : ProtocolInvariant initProtocolState := by
  apply protocol_initial_invariant <;> simp [EntryProvenance]

theorem protocol_step_preserves_invariant {s s' : ProtocolState} {lbl : StepLabel}
    (hInv : ProtocolInvariant s) (hStep : Step s lbl s') : ProtocolInvariant s' := by
  obtain ⟨hProv, hExclusive, hActiveInv, hAppliedInv, hCreateInv, hValidatedInv,
    hStabilizedInv, hGateInv, hForeignInv⟩ := hInv
  cases hStep with
  | observe => exact ⟨hProv, hExclusive, hActiveInv, hAppliedInv, hCreateInv,
      hValidatedInv, hStabilizedInv, hGateInv, hForeignInv⟩
  | plan inp others mode unresolved quarantined hIdle hOwner hObs hAnc hForeign =>
    constructor <;> simp_all
  | intent ex inp hIdle hPlanned hGate hMember hActor hPlan hOwner hInputOwner hObs hInputObs hForeign =>
    refine ⟨?_, ?_, ?_, ?_, ?_, ?_, hStabilizedInv, ?_, hForeignInv⟩
    · exact intent_provenance ex _ (hOwner ▸ hProv)
    · simp [hForeign]
    · simp
    · simp
    · simp
    · simp
    · simp [hGate]
  | providerApply ex hActive hNotApplied hIntent hExpected hGate =>
    refine ⟨?_, hExclusive, hActiveInv, ?_, ?_, ?_, ?_, hGateInv, hForeignInv⟩
    · exact hIntent ▸ apply_provenance ex _ (hIntent ▸ hProv)
    · simp [hActive]
    · intro other hOther _ hIsCreate
      have hEq : other = ex := Option.some.inj (hOther.symm.trans hActive)
      subst other
      simp [hIsCreate]
    · simp
    · simp
  | validate ex hActive hApplied hResult =>
    refine ⟨hProv, hExclusive, hActiveInv, hAppliedInv, hCreateInv, ?_, hStabilizedInv, hGateInv, hForeignInv⟩
    intro _
    exact ⟨hApplied, ex, hActive, hResult⟩
  | stabilize =>
    exact ⟨hProv, hExclusive, hActiveInv, hAppliedInv, hCreateInv, hValidatedInv,
      fun _ => rfl, hGateInv, hForeignInv⟩
  | commit ex hActive hValidated hApplied hResult hStab hGate =>
    refine ⟨?_, ?_, ?_, ?_, ?_, ?_, hStabilizedInv, ?_, hForeignInv⟩
    · exact commit_provenance ex _ ((hActiveInv ex hActive) ▸ hProv)
        (hCreateInv ex hActive hApplied)
    · intro hReserved
      have hNone := hExclusive hReserved
      have hSome := hActiveInv ex hActive
      rw [hNone] at hSome
      contradiction
    all_goals simp
  | commitIndeterminate => exact ⟨hProv, hExclusive, hActiveInv, hAppliedInv, hCreateInv,
      hValidatedInv, hStabilizedInv, hGateInv, hForeignInv⟩
  | crash powerLoss =>
    constructor <;> simp_all
    cases powerLoss <;> simp_all
  | recover actor anc granted hIdle hAnc hAuth hStab =>
    refine ⟨recovery_provenance actor _ _ anc granted _ hProv hAuth, ?_, ?_, ?_, ?_, ?_, hStabilizedInv, ?_, hForeignInv⟩
    · intro hReserved
      have hNone := hExclusive hReserved
      simp [hNone, pureRecoverPath] at hAuth
    all_goals simp [hIdle]
  | metadataAdopt path actor val inp hPlanned hInputOwner hInputObs hActor hPlan hIdle hOwner hObs hStab hForeign hGate hUpdate =>
    constructor <;> simp_all [EntryProvenance]
  | metadataRemove path inp hPlanned hInputOwner hInputObs hPlan hIdle hStab hSettled hGate hUpdate =>
    constructor <;> simp_all [EntryProvenance]
  | adopt actor val granted hIdle hObs hAnc hPerm hStab hForeign hOwner =>
    constructor <;> simp_all [EntryProvenance]
  | release actor granted e hIdle hOwner hMgr hNoPend hKnown hPerm hStab =>
    constructor <;> simp_all [EntryProvenance]
  | externalWrite =>
    constructor <;> simp_all

theorem protocol_trace_invariant {s : ProtocolState} (hReach : Reachable s) : ProtocolInvariant s := by
  induction hReach with
  | initial obs owner reservation foreignPending history hHistory hExclusive =>
    exact protocol_initial_invariant obs owner reservation foreignPending history hHistory hExclusive
  | step _ hStep ih => exact protocol_step_preserves_invariant ih hStep

/-- A new creation witness is issued only by an actual conditional Absent → Leaf apply. -/
theorem creation_witness_requires_apply {s s' : ProtocolState} {lbl : StepLabel}
    (h : Step s lbl s') (actor : ActorID)
    (hNew : s'.createdSessionWitness = some actor)
    (hChanged : s.createdSessionWitness ≠ some actor) :
    ∃ path target, lbl = .providerApply (.create path actor target) ∧
      s.visibleEnv = .absent ∧ s'.visibleEnv = .presentLeaf target := by
  cases h <;> simp_all
  rename_i ex hActive hNotApplied hIntent hExpected hGate
  cases ex <;> simp_all [EnvExecution.isCreate, EnvExecution.actor, EnvExecution.expected, EnvExecution.result]

/-- Recovery uses exactly the pure model, including canceled creates, update rollback,
    delete confirmation/rollback and refusal of Unknown/third values or insufficient Own. -/
theorem protocol_recovery_matches_pure {s s' : ProtocolState} {actor : ActorID} {granted : PermSet}
    (h : Step s (.recover actor granted) s') :
    (pureRecoverPath actor s.storeEntry s.visibleEnv s.ancestor granted).authorized = true ∧
      s'.storeEntry = (pureRecoverPath actor s.storeEntry s.visibleEnv s.ancestor granted).virtualOwner := by
  cases h
  constructor
  · simpa only [← ‹_ = s.ancestor›] using ‹(pureRecoverPath _ _ _ _ _).authorized = true›
  · simp only [← ‹_ = s.ancestor›]

theorem protocol_unconfirmed_create_non_escalation
    {s s' : ProtocolState} {actor : ActorID} {granted : PermSet}
    (h : Step s (.recover actor granted) s') (e e' : OwnerEntry) (p : PendingIntent)
    (hOwner : s.storeEntry = some e) (hPend : e.pending = some p)
    (hPrev : p.previousEntry = none) (hAfter : s'.storeEntry = some e') :
    e'.origin = .modified := by
  obtain ⟨hAuth, hPure⟩ := protocol_recovery_matches_pure h
  have hv : (pureRecoverPath actor (some e) s.visibleEnv s.ancestor granted).virtualOwner = some e' := by
    simpa [hOwner] using hPure.symm.trans hAfter
  have ha : (pureRecoverPath actor (some e) s.visibleEnv s.ancestor granted).authorized = true := by
    simpa [hOwner] using hAuth
  have hp := (recovery_authorized_shape actor (some e) s.visibleEnv s.ancestor granted ha e' hv).2.1
  exact pureRecoverPath_unconfirmed_create_demoted actor e e' p _ _ _ hPend hPrev hv hp

theorem protocol_recovery_stabilize_before_persist
    {s s' : ProtocolState} {actor : ActorID} {granted : PermSet}
    (hInv : ProtocolInvariant s) (h : Step s (.recover actor granted) s') :
    s.targetStabilized = true ∧ s.durableEnv = s.visibleEnv ∧
      s'.visibleEnv = s.visibleEnv := by
  cases h
  exact ⟨‹_›, hInv.stabilizedEnvironment ‹_›, rfl⟩

/-- The saved active delete Intent is allowed to execute; an unrelated or unresolved
    Pending cannot be used as a fresh delete. Settled eligibility is checked at Intent. -/
theorem protocol_delete_requires_active_intent
    {s s' : ProtocolState} {path : PathID} {actor : ActorID} {value : Value}
    (h : Step s (.providerApply (.delete path actor value)) s') :
    s.active = some (.delete path actor value) ∧
    s.storeEntry = some (EnvExecution.delete path actor value).intentEntry ∧
    s.visibleEnv = .presentLeaf value ∧ s'.visibleEnv = .absent ∧ s.gate.blocked = false := by
  cases h
  exact ⟨‹_›, ‹_›, ‹_›, rfl, ‹_›⟩

theorem protocol_intent_requires_settled_prestate {s s' : ProtocolState} {ex : EnvExecution}
    (h : Step s (.intent ex) s') :
    ∀ e, s.storeEntry = some e → e.pending = none := by
  cases h
  have hOwner : s.storeEntry = ex.beforeOwner := by assumption
  rw [hOwner]
  cases ex <;> simp [EnvExecution.beforeOwner]

theorem protocol_indeterminate_preserves_record {s s' : ProtocolState}
    (h : Step s (.commit true) s') : s'.storeEntry = s.storeEntry := by
  cases h
  rfl

def StepLabel.isNormalExecution : StepLabel → Bool
  | .intent _ | .providerApply _ | .commit false => true
  | _ => false

/-- Actual Gate state, rather than a flag reset by every transition, controls mutation. -/
theorem protocol_normal_execution_requires_open_gate {s s' : ProtocolState} {lbl : StepLabel}
    (h : Step s lbl s') (hNormal : lbl.isNormalExecution = true) : s.gate.blocked = false := by
  cases h <;> simp_all [StepLabel.isNormalExecution]

theorem protocol_foreign_pending_preserved {s s' : ProtocolState} {lbl : StepLabel}
    (h : Step s lbl s') : s'.unrelatedForeignPending = s.unrelatedForeignPending := by
  cases h <;> rfl

theorem protocol_resolve_never_increases_delete_rights {s s' : ProtocolState} {lbl : StepLabel}
    (h : Step s lbl s')
    (hResolve : (∃ a v p, lbl = .adopt a v p) ∨ (∃ a p, lbl = .release a p)) :
    ∀ e, s'.storeEntry = some e → e.origin = .modified ∧ e.pending = none := by
  cases h <;> simp_all

/-- Prune eligibility and Delete + Own authorization are inherited from the actual Plan,
    before installing the active Pending Delete. -/
theorem protocol_delete_intent_is_prune {s s' : ProtocolState}
    {path : PathID} {actor : ActorID} {value : Value}
    (h : Step s (.intent (.delete path actor value)) s') :
    s.plannedMode = .prune ∧ ∃ inp, s.plannedInput = some inp ∧
      inp.declared = none ∧ PermSet.allows inp.granted permDeleteOwn = true := by
  cases h with
  | intent ex inp hIdle hPlanned hGate hMember hActor hPlan hOwner hInputOwner hObs hInputObs hForeign =>
    have hDel : (planPath inp s.plannedMode).envOp = some (.delete path value) := hPlan
    obtain ⟨hPrune, hUndeclared, hPerm, _⟩ := theorem3_conservative_deletion inp _ _ _ hDel
    exact ⟨hPrune, inp, hPlanned, hUndeclared, hPerm⟩

end Kisage
