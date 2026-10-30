import Kisage.Model

namespace Kisage

/-- Classification of a pending intent during `PureRecover` (ADR 0003 §1, ADR 0004 §3).
    Note that an in-flight creation (`previousEntry = none`) is ALWAYS conservatively demoted
    to `Origin.modified` (`createDemotedModified`, `K601`) upon crash recovery (Counterexamples #6, #27). -/
inductive RecoveryKind where
  | none
  | createDemotedModified
  | createCanceled
  | updateConfirmed
  | updateCanceled
  | deleteConfirmed
  | deleteCanceled
  | pendingUnknown
  | pendingConflict
  deriving DecidableEq, Repr, Inhabited

/-- Result of running `PureRecover` on a single path (ADR 0003 §1, Counterexamples #6, #7, #23–#25, #27). -/
structure RecoveryResult where
  kind : RecoveryKind
  isCandidate : Bool
  authorized : Bool
  unresolved : Bool
  virtualOwner : Option OwnerEntry
  deriving DecidableEq, Repr, Inhabited

/-- Purely classifies a self-owned `PendingIntent` against current observation (ADR 0004 §3). -/
def classifyPendingIntent
    (actor : ActorID)
    (pend : PendingIntent)
    (obs : Observed)
    (anc : AncestorStatus) : RecoveryKind × Option (Option OwnerEntry) :=
  if isObservationUnknown obs anc then
    (.pendingUnknown, none)
  else if anc != .clear then
    (.pendingConflict, none)
  else
    match pend.op, pend.previousEntry, pend.target with
    | .set, none, some t =>
      match obs with
      | .presentLeaf v =>
        if v == t then
          (.createDemotedModified, some (some { manager := actor, origin := .modified, lastApplied := t, pending := none }))
        else
          (.pendingConflict, none)
      | .absent =>
        (.createCanceled, some none)
      | _ =>
        (.pendingConflict, none)
    | .set, some (prevOrigin, prevVal), some t =>
      match obs with
      | .presentLeaf v =>
        if v == t then
          (.updateConfirmed, some (some { manager := actor, origin := prevOrigin, lastApplied := t, pending := none }))
        else if v == prevVal then
          (.updateCanceled, some (some { manager := actor, origin := prevOrigin, lastApplied := prevVal, pending := none }))
        else
          (.pendingConflict, none)
      | _ =>
        (.pendingConflict, none)
    | .delete, some (prevOrigin, prevVal), none =>
      match obs with
      | .absent =>
        (.deleteConfirmed, some none)
      | .presentLeaf v =>
        if v == prevVal then
          (.deleteCanceled, some (some { manager := actor, origin := prevOrigin, lastApplied := prevVal, pending := none }))
        else
          (.pendingConflict, none)
      | _ =>
        (.pendingConflict, none)
    | _, _, _ =>
      (.pendingConflict, none)

/-- Stage 1 (`PureRecover`) for a single path (ADR 0003 §1, Counterexamples #6, #23–#25, #27).
    Only self-owned entries (`e.manager == actor`) with `e.pending = some pend` are candidates,
    and virtual store application requires `Own` permission (`permOwn`). -/
def pureRecoverPath
    (actor : ActorID)
    (owner : Option OwnerEntry)
    (obs : Observed)
    (anc : AncestorStatus)
    (granted : PermSet) : RecoveryResult :=
  match owner with
  | none =>
    { kind := .none, isCandidate := false, authorized := false, unresolved := false, virtualOwner := none }
  | some e =>
    if e.manager != actor then
      { kind := .none, isCandidate := false, authorized := false, unresolved := false, virtualOwner := some e }
    else
      match e.pending with
      | none =>
        { kind := .none, isCandidate := false, authorized := false, unresolved := false, virtualOwner := some e }
      | some pend =>
        let (kind, candidateAfter) := classifyPendingIntent actor pend obs anc
        match candidateAfter with
        | none =>
          { kind := kind, isCandidate := false, authorized := false, unresolved := true, virtualOwner := some e }
        | some nextOwner =>
          if PermSet.allows granted permOwn then
            { kind := kind, isCandidate := true, authorized := true, unresolved := false, virtualOwner := nextOwner }
          else
            { kind := kind, isCandidate := true, authorized := false, unresolved := true, virtualOwner := some e }

/-- **Unverified Creation Non-Promotion in `PureRecover` (ADR 0004 §3 rule 2, Counterexamples #6, #27)**:
    When recovering an in-flight creation (`pend.previousEntry = none`), any settled `virtualOwner`
    produced by `pureRecoverPath` is conservatively demoted to `Origin.modified` (never `Origin.created`). -/
theorem pureRecoverPath_unconfirmed_create_demoted
    (actor : ActorID) (e e' : OwnerEntry) (pend : PendingIntent)
    (obs : Observed) (anc : AncestorStatus) (granted : PermSet)
    (hPend : e.pending = some pend)
    (hPrev : pend.previousEntry = none)
    (hVirt : (pureRecoverPath actor (some e) obs anc granted).virtualOwner = some e')
    (hSettled : e'.pending = none) :
    e'.origin = .modified := by
  unfold pureRecoverPath at hVirt
  by_cases hMgr : (e.manager != actor) = true
  · simp [hMgr] at hVirt
    subst hVirt
    rw [hPend] at hSettled
    contradiction
  · simp only [Bool.not_eq_true] at hMgr
    simp only [hMgr, Bool.false_eq_true, ↓reduceIte, hPend] at hVirt
    unfold classifyPendingIntent at hVirt
    rw [hPrev] at hVirt
    by_cases hUnk : isObservationUnknown obs anc = true
    · simp [hUnk] at hVirt
      subst hVirt
      rw [hPend] at hSettled
      contradiction
    · simp only [Bool.not_eq_true] at hUnk
      simp only [hUnk, Bool.false_eq_true, ↓reduceIte] at hVirt
      by_cases hAnc : (anc != .clear) = true
      · simp [hAnc] at hVirt
        subst hVirt
        rw [hPend] at hSettled
        contradiction
      · simp only [Bool.not_eq_true] at hAnc
        simp only [hAnc, Bool.false_eq_true, ↓reduceIte] at hVirt
        cases hOp : pend.op <;> cases hTgt : pend.target <;> cases hObs : obs <;>
          simp [hOp, hTgt, hObs] at hVirt
        all_goals (
          try (subst hVirt; rw [hPend] at hSettled; contradiction)
        )
        · by_cases hPerm : PermSet.allows granted permOwn = true
          · simp [hPerm] at hVirt
          · simp [hPerm] at hVirt
            subst hVirt
            rw [hPend] at hSettled
            contradiction
        · rename_i t v
          by_cases hEq : v = t
          · simp only [hEq, ↓reduceIte] at hVirt
            by_cases hPerm : PermSet.allows granted permOwn = true
            · simp [hPerm] at hVirt
              subst hVirt
              rfl
            · simp [hPerm] at hVirt
              subst hVirt
              rw [hPend] at hSettled
              contradiction
          · simp [hEq] at hVirt
            subst hVirt
            rw [hPend] at hSettled
            contradiction

end Kisage
