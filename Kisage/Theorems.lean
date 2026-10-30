import Kisage.Model
import Kisage.Recover
import Kisage.Plan

namespace Kisage

/-!
# Safety Theorems for Pure `Plan` (ADR 0005 §3.1)

Proves all 11 pure planning safety theorems with zero `sorryAx`.
-/

/-- Helper lemma: when `dec ≠ .authorized`, `buildEnvOp` is `none`. -/
theorem buildEnvOp_not_authorized
    (path : PathID) (dec : DecisionKind) (act : ActionKind)
    (declared : Option Value) (virtualOwner : Option OwnerEntry) (obs : Observed)
    (hNotAuth : dec ≠ .authorized) :
    buildEnvOp path dec act declared virtualOwner obs = none := by
  cases dec <;> simp_all [buildEnvOp]

/-- Helper lemma: when `dec ≠ .authorized`, `buildMetadataCandidate` is `none`. -/
theorem buildMetadataCandidate_not_authorized
    (path : PathID) (dec : DecisionKind) (act : ActionKind)
    (declared : Option Value) (virtualOwner : Option OwnerEntry) (obs : Observed)
    (hNotAuth : dec ≠ .authorized) :
    buildMetadataCandidate path dec act declared virtualOwner obs = none := by
  cases dec <;> simp_all [buildMetadataCandidate]

/-- Helper lemma: when `dec ≠ .authorized`, `buildOwnershipUpdate` is `none`. -/
theorem buildOwnershipUpdate_not_authorized
    (path : PathID) (actor : ActorID) (dec : DecisionKind) (act : ActionKind)
    (declared : Option Value) (virtualOwner : Option OwnerEntry) (obs : Observed)
    (hNotAuth : dec ≠ .authorized) :
    buildOwnershipUpdate path actor dec act declared virtualOwner obs = none := by
  cases dec <;> simp_all [buildOwnershipUpdate]

/-- Helper lemma: if any of `buildEnvOp`, `buildMetadataCandidate`, or `buildOwnershipUpdate` is `isSome`, `dec = .authorized`. -/
theorem buildOps_some_implies_authorized
    (path : PathID) (actor : ActorID) (dec : DecisionKind) (act : ActionKind)
    (declared : Option Value) (virtualOwner : Option OwnerEntry) (obs : Observed)
    (hSome : (buildEnvOp path dec act declared virtualOwner obs).isSome = true ∨
             (buildMetadataCandidate path dec act declared virtualOwner obs).isSome = true ∨
             (buildOwnershipUpdate path actor dec act declared virtualOwner obs).isSome = true) :
    dec = .authorized := by
  cases dec <;> simp_all [buildEnvOp, buildMetadataCandidate, buildOwnershipUpdate]

/-- Helper lemma: when `act ≠ .setLeaf` and `act ≠ .deleteLeaf`, `buildEnvOp` is `none`. -/
theorem buildEnvOp_none_of_not_set_or_delete
    (path : PathID) (dec : DecisionKind) (act : ActionKind)
    (declared : Option Value) (virtualOwner : Option OwnerEntry) (obs : Observed)
    (hNotSet : act ≠ .setLeaf) (hNotDel : act ≠ .deleteLeaf) :
    buildEnvOp path dec act declared virtualOwner obs = none := by
  cases dec <;> cases act <;> cases declared <;> cases virtualOwner <;> cases obs <;> simp_all [buildEnvOp]

/-- Helper lemma: when `declared = none` and `act ≠ .deleteLeaf`, `buildEnvOp` is `none`. -/
theorem buildEnvOp_none_of_undeclared_not_delete
    (path : PathID) (dec : DecisionKind) (act : ActionKind)
    (virtualOwner : Option OwnerEntry) (obs : Observed)
    (hNotDel : act ≠ .deleteLeaf) :
    buildEnvOp path dec act none virtualOwner obs = none := by
  cases dec <;> cases act <;> cases virtualOwner <;> cases obs <;> simp_all [buildEnvOp]

/-- Helper lemma: `buildEnvOp = some (.delete p v)` implies `dec = .authorized`, `act = .deleteLeaf`, and `declared = none`. -/
theorem buildEnvOp_eq_delete_implies
    (path : PathID) (dec : DecisionKind) (act : ActionKind)
    (declared : Option Value) (virtualOwner : Option OwnerEntry) (obs : Observed)
    (p : PathID) (v : Value)
    (hDel : buildEnvOp path dec act declared virtualOwner obs = some (.delete p v)) :
    dec = .authorized ∧ act = .deleteLeaf ∧ declared = none := by
  cases dec <;> cases act <;> cases declared <;> cases virtualOwner <;> cases obs <;> simp_all [buildEnvOp]

/-- Helper lemma: when `authorizeSelection sel reqPerms granted = .authorized`, `sel = .candidate` and `PermSet.allows granted reqPerms = true`. -/
theorem authorizeSelection_eq_authorized
    (sel : SelectionKind) (reqPerms granted : PermSet)
    (hAuth : authorizeSelection sel reqPerms granted = .authorized) :
    sel = .candidate ∧ PermSet.allows granted reqPerms = true := by
  unfold authorizeSelection at hAuth
  cases sel with
  | noOp | deferred | blocker => contradiction
  | candidate =>
    by_cases hPerm : PermSet.allows granted reqPerms = true
    · exact ⟨rfl, hPerm⟩
    · simp [hPerm] at hAuth

/-- **Theorem 1a (Recovery Scope & Domain Bound, ADR 0005 §3.1 #1)**:
    `PureRecover` authorizes a candidate only when `PermSet.allows granted permOwn = true`
    and the entry belongs to `dom(σ_A)` (`owner = some e`, `(e.manager != actor) = false`, `e.pending.isSome`). -/
theorem theorem1_recovery_scope_and_frame
    (actor : ActorID) (owner : Option OwnerEntry) (obs : Observed) (anc : AncestorStatus) (granted : PermSet)
    (hAuth : (pureRecoverPath actor owner obs anc granted).authorized = true) :
    PermSet.allows granted permOwn = true ∧
    ∃ e, owner = some e ∧ (e.manager != actor) = false ∧ e.pending.isSome = true := by
  unfold pureRecoverPath at hAuth
  cases owner with
  | none => contradiction
  | some e =>
    by_cases hMgr : (e.manager != actor) = true
    · simp [hMgr] at hAuth
    · simp only [Bool.not_eq_true] at hMgr
      simp only [hMgr, Bool.false_eq_true, ↓reduceIte] at hAuth
      cases hPend : e.pending with
      | none => simp [hPend] at hAuth
      | some pend =>
        simp only [hPend] at hAuth
        cases hClass : classifyPendingIntent actor pend obs anc with
        | mk kind candAfter =>
          simp only [hClass] at hAuth
          cases candAfter with
          | none => contradiction
          | some nextOwner =>
            by_cases hPerm : PermSet.allows granted permOwn = true
            · exact ⟨hPerm, e, rfl, hMgr, by rw [hPend]; rfl⟩
            · simp [hPerm] at hAuth

/-- **Theorem 1b (Normal Plan Scope & `M_A` Frame Bound, ADR 0005 §3.1 #1)**:
    Whenever `planPath` emits an `envOp`, `metadataCandidate`, or `ownershipUpdate`,
    its `decision` is `.authorized`, `PermSet.allows inp.granted (planPath inp mode).requiredPerms = true`,
    and the path lies within `M_A(d, σ)` (`(planPath inp mode).inMA = true`). -/
theorem theorem1_scope_and_frame
    (inp : PathInput) (mode : Mode)
    (hWork : (planPath inp mode).envOp.isSome = true ∨
             (planPath inp mode).metadataCandidate.isSome = true ∨
             (planPath inp mode).ownershipUpdate.isSome = true) :
    (planPath inp mode).decision = .authorized ∧
    PermSet.allows inp.granted (planPath inp mode).requiredPerms = true ∧
    (planPath inp mode).inMA = true := by
  have hDec : (planPath inp mode).decision = .authorized :=
    buildOps_some_implies_authorized inp.path inp.actor (planPath inp mode).decision
      (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs hWork
  have hUnres : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved = false := by
    by_cases hU : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved = true
    · unfold planPath at hDec
      simp [hU] at hDec
    · exact Bool.eq_false_iff.mpr hU
  have hAuthSel : authorizeSelection (planPath inp mode).selection (planPath inp mode).requiredPerms inp.granted = .authorized := by
    unfold planPath at hDec ⊢
    simpa [hUnres] using hDec
  have ⟨hSelCand, hPerm⟩ := authorizeSelection_eq_authorized (planPath inp mode).selection (planPath inp mode).requiredPerms inp.granted hAuthSel
  have hInMA : (planPath inp mode).inMA = true := by
    have hNotOutside : (planPath inp mode).stateClass ≠ .outsideDomain := by
      intro hOut
      unfold planPath at hSelCand hOut
      dsimp only at hSelCand hOut
      rw [hOut] at hSelCand
      cases mode <;> contradiction
    unfold planPath at hNotOutside ⊢
    dsimp only at hNotOutside ⊢
    unfold classifyPath at hNotOutside
    cases hDecl : inp.declared with
    | some _ =>
      unfold inActiveDomain
      simp
    | none =>
      rw [hDecl] at hNotOutside
      cases hVirt : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).virtualOwner with
      | none =>
        rw [hVirt] at hNotOutside
        exact False.elim (hNotOutside rfl)
      | some ve =>
        rw [hVirt] at hNotOutside
        by_cases hMgr : (ve.manager != inp.actor) = true
        · simp [hMgr] at hNotOutside
        · simp only [Bool.not_eq_true, bne_eq_false_iff_eq] at hMgr
          unfold inActiveDomain
          simp [hMgr]
  exact ⟨hDec, hPerm, hInMA⟩

/-- Helper lemma: whenever `isProtectedStructure obs anc = true`, `selectModeAction (classifyPath ...)`
    is neither `.setLeaf` nor `.deleteLeaf`. -/
theorem classifyPath_protected_not_env_action
    (actor : ActorID) (declared : Option Value) (obs : Observed) (anc : AncestorStatus)
    (virtualOwner : Option OwnerEntry) (overlap : OverlapKind) (hasUnres : Bool)
    (hProt : isProtectedStructure obs anc = true) :
    let act := selectModeAction (classifyPath actor declared obs anc virtualOwner overlap hasUnres)
    act ≠ .setLeaf ∧ act ≠ .deleteLeaf := by
  unfold classifyPath
  cases declared with
  | some _ =>
    cases overlap with
    | retiredCreatedOverlap | foreignConflict => simp [selectModeAction]
    | retiredModifiedPendingRelease =>
      cases virtualOwner with
      | none =>
        by_cases hU : hasUnres = true <;> simp [hU, selectModeAction]
      | some e =>
        by_cases hM : (e.manager != actor) = true
        · simp [hM, selectModeAction]
        · by_cases hP : (e.pending.isSome || hasUnres) = true <;> simp [hM, hP, selectModeAction]
    | none =>
      cases virtualOwner with
      | none => simp [hProt, selectModeAction]
      | some e =>
        by_cases hM : (e.manager != actor) = true
        · simp [hM, selectModeAction]
        · by_cases hP : e.pending.isSome = true <;> simp [hM, hP, hProt, selectModeAction]
  | none =>
    cases virtualOwner with
    | none => simp [selectModeAction]
    | some e =>
      by_cases hM : (e.manager != actor) = true
      · simp [hM, selectModeAction]
      · by_cases hP : e.pending.isSome = true
        · simp [hM, hP, selectModeAction]
        · cases hO : e.origin with
          | modified =>
            by_cases hU : isObservationUnknown obs anc = true <;> simp [hM, hP, hO, hU, selectModeAction]
          | created =>
            by_cases hOv : (overlap == .retiredCreatedOverlap) = true <;> simp [hM, hP, hO, hOv, hProt, selectModeAction]

/-- **Theorem 2 (Structural Non-Destruction, ADR 0005 §3.1 #2)**:
    When the target or any ancestor is a protected structure (`presentRecord`, `presentOpaque`,
    or `protectedAncestor`), `planPath` never generates an environment mutation (`envOp = none`). -/
theorem theorem2_structure_non_destruction
    (inp : PathInput) (mode : Mode)
    (hProt : isProtectedStructure inp.obs inp.anc = true) :
    (planPath inp mode).envOp = none := by
  have ⟨hNotSet, hNotDel⟩ := classifyPath_protected_not_env_action
    inp.actor inp.declared inp.obs inp.anc
    (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).virtualOwner
    inp.overlap
    (inp.hasUnresolvedSelfPending || (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved)
    hProt
  exact buildEnvOp_none_of_not_set_or_delete
    inp.path (planPath inp mode).decision (planPath inp mode).action
    inp.declared (planPath inp mode).recovery.virtualOwner inp.obs hNotSet hNotDel

/-- **Theorem 3 (Conservative Deletion, ADR 0005 §3.1 #3)**:
    An environment deletion `EnvOp.delete` is generated ONLY when:
    1. `mode = .prune`,
    2. the path is undeclared (`inp.declared = none`),
    3. the virtual owner is self-owned (`(e.manager != inp.actor) = false`),
    4. `e.origin = .created`,
    5. `e.pending = none`,
    6. the observation is a leaf matching `e.lastApplied`, and
    7. `Delete + Own` permissions are granted. -/
theorem theorem3_conservative_deletion
    (inp : PathInput) (mode : Mode) (p : PathID) (v : Value)
    (hDel : (planPath inp mode).envOp = some (.delete p v)) :
    mode = .prune ∧
    inp.declared = none ∧
    PermSet.allows inp.granted permDeleteOwn = true ∧
    ∃ e actual,
      (planPath inp mode).recovery.virtualOwner = some e ∧
      (e.manager != inp.actor) = false ∧
      e.origin = .created ∧
      e.pending = none ∧
      inp.obs = .presentLeaf actual ∧
      (actual == e.lastApplied) = true ∧
      v = e.lastApplied := by
  have ⟨hDec, hActDel, hDecl⟩ := buildEnvOp_eq_delete_implies
    inp.path (planPath inp mode).decision (planPath inp mode).action
    inp.declared (planPath inp mode).recovery.virtualOwner inp.obs p v hDel
  have hUnres : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved = false := by
    by_cases hU : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved = true
    · unfold planPath at hDec
      simp [hU] at hDec
    · exact Bool.eq_false_iff.mpr hU
  have hAuthSel : authorizeSelection (planPath inp mode).selection (planPath inp mode).requiredPerms inp.granted = .authorized := by
    unfold planPath at hDec ⊢
    simpa [hUnres] using hDec
  have ⟨hSelCand, hPermAllow⟩ := authorizeSelection_eq_authorized (planPath inp mode).selection (planPath inp mode).requiredPerms inp.granted hAuthSel
  have hScEq : (planPath inp mode).stateClass = .retiredCreatedPrunable := by
    unfold planPath at hActDel ⊢
    dsimp only at hActDel ⊢
    cases hSc : classifyPath inp.actor inp.declared inp.obs inp.anc (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).virtualOwner inp.overlap (inp.hasUnresolvedSelfPending || (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved) <;>
      simp_all [selectModeAction]
  have hModePrune : mode = .prune := by
    unfold planPath at hSelCand hScEq
    dsimp only at hSelCand hScEq
    rw [hScEq] at hSelCand
    cases mode <;> simp_all [selectModeKind]
  subst hModePrune
  have hReqPermEq : (planPath inp .prune).requiredPerms = permDeleteOwn := by
    unfold planPath at hScEq ⊢
    dsimp only at hScEq ⊢
    rw [hScEq]
    rfl
  rw [hReqPermEq] at hPermAllow
  change buildEnvOp inp.path (planPath inp .prune).decision (planPath inp .prune).action inp.declared (planPath inp .prune).recovery.virtualOwner inp.obs = some (.delete p v) at hDel
  rw [hDec, hActDel, hDecl] at hDel
  unfold planPath at hScEq
  dsimp only at hScEq
  unfold classifyPath at hScEq
  rw [hDecl] at hScEq
  cases hVirt : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).virtualOwner with
  | none =>
    rw [hVirt] at hScEq
    contradiction
  | some e =>
    change (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).virtualOwner = some e at hVirt
    rw [hVirt] at hScEq
    by_cases hMgr : (e.manager != inp.actor) = true
    · simp [hMgr] at hScEq
    · simp only [Bool.not_eq_true] at hMgr
      simp only [hMgr, Bool.false_eq_true, ↓reduceIte] at hScEq
      cases hPend : e.pending with
      | some _ => simp [hPend] at hScEq
      | none =>
        simp only [hPend, Option.isSome_none, Bool.false_eq_true, ↓reduceIte] at hScEq
        cases hOrig : e.origin with
        | modified =>
          rw [hOrig] at hScEq
          by_cases hU : isObservationUnknown inp.obs inp.anc = true <;> simp [hU] at hScEq
        | created =>
          rw [hOrig] at hScEq
          by_cases hOv : (inp.overlap == .retiredCreatedOverlap) = true
          · simp [hOv] at hScEq
          · simp only [Bool.not_eq_true] at hOv
            simp only [hOv, Bool.false_eq_true, ↓reduceIte] at hScEq
            by_cases hAnom : (isObservationUnknown inp.obs inp.anc || isProtectedStructure inp.obs inp.anc || (inp.overlap == .foreignConflict)) = true
            · simp [hAnom] at hScEq
            · simp only [Bool.not_eq_true] at hAnom
              simp only [hAnom, Bool.false_eq_true, ↓reduceIte] at hScEq
              cases hObs : inp.obs with
              | absent | presentRecord | presentOpaque | unknown =>
                rw [hObs] at hScEq
                contradiction
              | presentLeaf actual =>
                rw [hObs] at hScEq hDel
                by_cases hMatch : (actual == e.lastApplied) = true
                · change buildEnvOp inp.path .authorized .deleteLeaf none (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).virtualOwner (.presentLeaf actual) = some (.delete p v) at hDel
                  rw [hVirt] at hDel
                  unfold buildEnvOp at hDel
                  injection hDel with hDelEq
                  injection hDelEq with _ hValEq
                  exact ⟨rfl, hDecl, hPermAllow, e, actual, hVirt, hMgr, hOrig, hPend, rfl, hMatch, hValEq.symm⟩
                · simp [hMatch] at hScEq

/-- Helper lemma: whenever `isObservationUnknown obs anc = true`, `selectModeKind (classifyPath ...) mode ≠ .candidate`. -/
theorem classifyPath_unknown_not_candidate
    (actor : ActorID) (declared : Option Value) (obs : Observed) (anc : AncestorStatus)
    (virtualOwner : Option OwnerEntry) (overlap : OverlapKind) (hasUnres : Bool) (mode : Mode)
    (hUnk : isObservationUnknown obs anc = true) :
    selectModeKind (classifyPath actor declared obs anc virtualOwner overlap hasUnres) mode ≠ .candidate := by
  unfold classifyPath
  cases declared with
  | some _ =>
    cases overlap with
    | retiredCreatedOverlap | foreignConflict => cases mode <;> simp [selectModeKind]
    | retiredModifiedPendingRelease =>
      cases virtualOwner with
      | none =>
        by_cases hU : hasUnres = true <;> cases mode <;> simp [hU, selectModeKind]
      | some e =>
        by_cases hM : (e.manager != actor) = true
        · cases mode <;> simp [hM, selectModeKind]
        · by_cases hP : (e.pending.isSome || hasUnres) = true <;> cases mode <;> simp [hM, hP, selectModeKind]
    | none =>
      cases virtualOwner with
      | none => cases mode <;> simp [hUnk, selectModeKind]
      | some e =>
        by_cases hM : (e.manager != actor) = true
        · cases mode <;> simp [hM, selectModeKind]
        · by_cases hP : e.pending.isSome = true <;> cases mode <;> simp [hM, hP, hUnk, selectModeKind]
  | none =>
    cases virtualOwner with
    | none => cases mode <;> simp [selectModeKind]
    | some e =>
      by_cases hM : (e.manager != actor) = true
      · cases mode <;> simp [hM, selectModeKind]
      · by_cases hP : e.pending.isSome = true
        · cases mode <;> simp [hM, hP, selectModeKind]
        · cases hO : e.origin with
          | modified => cases mode <;> simp [hM, hP, hO, hUnk, selectModeKind]
          | created =>
            by_cases hOv : (overlap == .retiredCreatedOverlap) = true <;>
              cases mode <;> simp [hM, hP, hO, hOv, hUnk, selectModeKind]

/-- **Theorem 4 (Unknown Target/Ancestor Safety, ADR 0005 §3.1 #4)**:
    When the target or any ancestor is `Unknown` (`isObservationUnknown inp.obs inp.anc = true`),
    neither `PureRecover` nor `planPath` emits any environment operation, metadata candidate,
    or ownership update. -/
theorem theorem4_unknown_safety
    (inp : PathInput) (mode : Mode)
    (hUnk : isObservationUnknown inp.obs inp.anc = true) :
    (planPath inp mode).recovery.authorized = false ∧
    (planPath inp mode).recovery.virtualOwner = inp.owner ∧
    (planPath inp mode).envOp = none ∧
    (planPath inp mode).metadataCandidate = none ∧
    (planPath inp mode).ownershipUpdate = none := by
  have hRec : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).authorized = false ∧
              (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).virtualOwner = inp.owner := by
    unfold pureRecoverPath classifyPendingIntent
    cases inp.owner with
    | none => simp
    | some e =>
      by_cases hM : (e.manager != inp.actor) = true
      · simp [hM]
      · simp only [Bool.not_eq_true] at hM
        cases hP : e.pending <;> simp [hM, hP, hUnk]
  have hNotAuth : (planPath inp mode).decision ≠ .authorized := by
    intro hDec
    have hUnres : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved = false := by
      by_cases hU : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved = true
      · unfold planPath at hDec
        simp [hU] at hDec
      · exact Bool.eq_false_iff.mpr hU
    have hAuthSel : authorizeSelection (planPath inp mode).selection (planPath inp mode).requiredPerms inp.granted = .authorized := by
      unfold planPath at hDec ⊢
      simpa [hUnres] using hDec
    have ⟨hCand, _⟩ := authorizeSelection_eq_authorized (planPath inp mode).selection (planPath inp mode).requiredPerms inp.granted hAuthSel
    exact classifyPath_unknown_not_candidate
      inp.actor inp.declared inp.obs inp.anc
      (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).virtualOwner
      inp.overlap
      (inp.hasUnresolvedSelfPending || (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved)
      mode hUnk hCand
  exact ⟨hRec.1, hRec.2,
    buildEnvOp_not_authorized inp.path (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs hNotAuth,
    buildMetadataCandidate_not_authorized inp.path (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs hNotAuth,
    buildOwnershipUpdate_not_authorized inp.path inp.actor (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs hNotAuth⟩

/-- **Theorem 5a (No Silent Overwrite of Foreign Ownership, ADR 0005 §3.1 #5)**:
    If a path is owned by a foreign actor (`(e.manager != inp.actor) = true`), `planPath` emits no `envOp` or `ownershipUpdate`. -/
theorem theorem5_no_silent_overwrite_foreign
    (inp : PathInput) (mode : Mode) (e : OwnerEntry)
    (hOwner : (planPath inp mode).recovery.virtualOwner = some e)
    (hForeign : (e.manager != inp.actor) = true) :
    (planPath inp mode).envOp = none ∧ (planPath inp mode).ownershipUpdate = none := by
  have hNotCand : (planPath inp mode).selection ≠ .candidate := by
    change selectModeKind (planPath inp mode).stateClass mode ≠ .candidate
    unfold planPath at hOwner ⊢
    dsimp only at hOwner ⊢
    rw [hOwner]
    unfold classifyPath
    cases inp.declared with
    | some _ => cases inp.overlap <;> cases mode <;> simp [hForeign, selectModeKind]
    | none => cases mode <;> simp [hForeign, selectModeKind]
  have hNotAuth : (planPath inp mode).decision ≠ .authorized := by
    intro hDec
    have hUnres : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved = false := by
      by_cases hU : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved = true
      · unfold planPath at hDec
        simp [hU] at hDec
      · exact Bool.eq_false_iff.mpr hU
    have hAuthSel : authorizeSelection (planPath inp mode).selection (planPath inp mode).requiredPerms inp.granted = .authorized := by
      unfold planPath at hDec ⊢
      simpa [hUnres] using hDec
    have ⟨hCand, _⟩ := authorizeSelection_eq_authorized (planPath inp mode).selection (planPath inp mode).requiredPerms inp.granted hAuthSel
    exact hNotCand hCand
  exact ⟨
    buildEnvOp_not_authorized inp.path (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs hNotAuth,
    buildOwnershipUpdate_not_authorized inp.path inp.actor (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs hNotAuth⟩

/-- **Theorem 5b (No Silent Overwrite of Drifted Self-Owned Leaf, ADR 0005 §3.1 #5)**:
    If a self-owned path (`e.pending = none`) has drifted (`obs = .presentLeaf actual` with `(actual != e.lastApplied) = true`),
    `planPath` never emits an environment operation (`envOp = none`), and for declared or `Created` paths emits no `ownershipUpdate`. -/
theorem theorem5_no_silent_overwrite_drift
    (inp : PathInput) (mode : Mode) (e : OwnerEntry) (actual : Value)
    (hOwner : (planPath inp mode).recovery.virtualOwner = some e)
    (hSelf : (e.manager != inp.actor) = false)
    (hNoPend : e.pending = none)
    (hObs : inp.obs = .presentLeaf actual)
    (hDrift : (actual != e.lastApplied) = true) :
    (planPath inp mode).envOp = none ∧
    ((inp.declared.isSome = true ∨ e.origin = .created) → (planPath inp mode).ownershipUpdate = none) := by
  have hNotEq : (actual == e.lastApplied) = false := by
    rw [bne_iff_ne] at hDrift
    exact beq_eq_false_iff_ne.mpr hDrift
  have hCase : (inp.declared.isSome = true ∨ e.origin = .created) →
      (planPath inp mode).selection ≠ .candidate := by
    intro hCond
    change selectModeKind (planPath inp mode).stateClass mode ≠ .candidate
    unfold planPath at hOwner ⊢
    dsimp only at hOwner ⊢
    rw [hOwner, hObs]
    unfold classifyPath
    cases hDecl : inp.declared with
    | some _ =>
      cases inp.overlap with
      | retiredCreatedOverlap | foreignConflict => cases mode <;> simp [selectModeKind]
      | retiredModifiedPendingRelease =>
        by_cases hU : (inp.hasUnresolvedSelfPending || (pureRecoverPath inp.actor inp.owner (Observed.presentLeaf actual) inp.anc inp.granted).unresolved) = true <;>
          cases mode <;> simp [hSelf, hNoPend, hU, selectModeKind]
      | none =>
        by_cases hUnkProt : (isObservationUnknown (.presentLeaf actual) inp.anc || isProtectedStructure (.presentLeaf actual) inp.anc) = true <;>
          cases mode <;> simp [hSelf, hNoPend, hUnkProt, hDrift, selectModeKind]
    | none =>
      simp [hDecl] at hCond
      by_cases hOv : (inp.overlap == .retiredCreatedOverlap) = true
      · cases mode <;> simp [hSelf, hNoPend, hCond, hOv, selectModeKind]
      · by_cases hAnom : (isObservationUnknown (.presentLeaf actual) inp.anc || isProtectedStructure (.presentLeaf actual) inp.anc || (inp.overlap == .foreignConflict)) = true <;>
          cases mode <;> simp [hSelf, hNoPend, hCond, hOv, hAnom, hNotEq, selectModeKind]
  have hNotAuthOfCond : (inp.declared.isSome = true ∨ e.origin = .created) → (planPath inp mode).decision ≠ .authorized := by
    intro hCond hDec
    have hNotCand := hCase hCond
    have hUnres : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved = false := by
      by_cases hU : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved = true
      · unfold planPath at hDec
        simp [hU] at hDec
      · exact Bool.eq_false_iff.mpr hU
    have hAuthSel : authorizeSelection (planPath inp mode).selection (planPath inp mode).requiredPerms inp.granted = .authorized := by
      unfold planPath at hDec ⊢
      simpa [hUnres] using hDec
    have ⟨hCand, _⟩ := authorizeSelection_eq_authorized (planPath inp mode).selection (planPath inp mode).requiredPerms inp.granted hAuthSel
    exact hNotCand hCand
  refine ⟨?_, ?_⟩
  · by_cases hCond : inp.declared.isSome = true ∨ e.origin = .created
    · exact buildEnvOp_not_authorized inp.path (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs (hNotAuthOfCond hCond)
    · have hDeclNone : inp.declared = none := by
        cases hD : inp.declared with
        | none => rfl
        | some _ => exact False.elim (hCond (Or.inl (by simp [hD])))
      have hOrigMod : e.origin = .modified := by
        cases hO : e.origin with
        | modified => rfl
        | created => exact False.elim (hCond (Or.inr hO))
      have hActNotDel : (planPath inp mode).action ≠ .deleteLeaf := by
        change selectModeAction (planPath inp mode).stateClass ≠ .deleteLeaf
        unfold planPath at hOwner ⊢
        dsimp only at hOwner ⊢
        rw [hDeclNone, hOwner, hObs]
        unfold classifyPath
        by_cases hU : isObservationUnknown (.presentLeaf actual) inp.anc = true <;>
          simp [hSelf, hNoPend, hOrigMod, hU, selectModeAction]
      change buildEnvOp inp.path (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs = none
      rw [hDeclNone]
      exact buildEnvOp_none_of_undeclared_not_delete inp.path (planPath inp mode).decision (planPath inp mode).action (planPath inp mode).recovery.virtualOwner inp.obs hActNotDel
  · intro hCond
    exact buildOwnershipUpdate_not_authorized inp.path inp.actor (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs (hNotAuthOfCond hCond)

/-- **Theorem 5c (No Silent Recreation on `DriftDeleted`, ADR 0005 §3.1 #5, Counterexample #22)**:
    If a declared path (`inp.declared = some desired`) is self-owned (`virtualOwner = some e`)
    and observed as `Absent` (`inp.obs = .absent`), `planPath` never recreates the value or modifies ownership. -/
theorem theorem5_no_overwrite_drift_deleted
    (inp : PathInput) (mode : Mode) (desired : Value) (e : OwnerEntry)
    (hDecl : inp.declared = some desired)
    (hOwner : (planPath inp mode).recovery.virtualOwner = some e)
    (hObs : inp.obs = .absent) :
    (planPath inp mode).envOp = none ∧
    (planPath inp mode).metadataCandidate = none ∧
    (planPath inp mode).ownershipUpdate = none := by
  have hNotCand : (planPath inp mode).selection ≠ .candidate := by
    change selectModeKind (planPath inp mode).stateClass mode ≠ .candidate
    unfold planPath at hOwner ⊢
    dsimp only at hOwner ⊢
    rw [hDecl, hOwner, hObs]
    unfold classifyPath
    cases inp.overlap with
    | retiredCreatedOverlap | foreignConflict => cases mode <;> simp [selectModeKind]
    | retiredModifiedPendingRelease =>
      by_cases hM : (e.manager != inp.actor) = true
      · cases mode <;> simp [hM, selectModeKind]
      · by_cases hP : (e.pending.isSome || (inp.hasUnresolvedSelfPending || (pureRecoverPath inp.actor inp.owner .absent inp.anc inp.granted).unresolved)) = true <;>
          cases mode <;> simp [hM, hP, selectModeKind]
    | none =>
      by_cases hM : (e.manager != inp.actor) = true
      · cases mode <;> simp [hM, selectModeKind]
      · by_cases hP : e.pending.isSome = true
        · cases mode <;> simp [hM, hP, selectModeKind]
        · by_cases hUnkProt : (isObservationUnknown .absent inp.anc || isProtectedStructure .absent inp.anc) = true <;>
            cases mode <;> simp [hM, hP, hUnkProt, selectModeKind]
  have hNotAuth : (planPath inp mode).decision ≠ .authorized := by
    intro hDec
    have hUnres : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved = false := by
      by_cases hU : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved = true
      · unfold planPath at hDec
        simp [hU] at hDec
      · exact Bool.eq_false_iff.mpr hU
    have hAuthSel : authorizeSelection (planPath inp mode).selection (planPath inp mode).requiredPerms inp.granted = .authorized := by
      unfold planPath at hDec ⊢
      simpa [hUnres] using hDec
    have ⟨hCand, _⟩ := authorizeSelection_eq_authorized (planPath inp mode).selection (planPath inp mode).requiredPerms inp.granted hAuthSel
    exact hNotCand hCand
  exact ⟨
    buildEnvOp_not_authorized inp.path (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs hNotAuth,
    buildMetadataCandidate_not_authorized inp.path (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs hNotAuth,
    buildOwnershipUpdate_not_authorized inp.path inp.actor (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs hNotAuth⟩

/-- **Theorem 5d (No Silent Overwrite of Unowned Conflicting Leaf `K206`, ADR 0005 §3.1 #5, Counterexample #37)**:
    If an unowned declared path (`virtualOwner = none`, `inp.declared = some desired`) has an existing
    leaf value `actual` with `(actual == desired) = false`, `planPath` emits no `envOp`, `metadataCandidate`, or `ownershipUpdate`. -/
theorem theorem5_no_overwrite_unowned_conflict
    (inp : PathInput) (mode : Mode) (desired actual : Value)
    (hDecl : inp.declared = some desired)
    (hOwner : (planPath inp mode).recovery.virtualOwner = none)
    (hObs : inp.obs = .presentLeaf actual)
    (hMismatch : (actual == desired) = false) :
    (planPath inp mode).envOp = none ∧
    (planPath inp mode).metadataCandidate = none ∧
    (planPath inp mode).ownershipUpdate = none := by
  have hNotCand : (planPath inp mode).selection ≠ .candidate := by
    change selectModeKind (planPath inp mode).stateClass mode ≠ .candidate
    unfold planPath at hOwner ⊢
    dsimp only at hOwner ⊢
    rw [hDecl, hOwner, hObs]
    unfold classifyPath
    cases inp.overlap with
    | retiredCreatedOverlap | foreignConflict => cases mode <;> simp [selectModeKind]
    | retiredModifiedPendingRelease =>
      by_cases hU : (inp.hasUnresolvedSelfPending || (pureRecoverPath inp.actor inp.owner (.presentLeaf actual) inp.anc inp.granted).unresolved) = true <;>
        cases mode <;> simp [hU, selectModeKind]
    | none =>
      by_cases hUnkProt : (isObservationUnknown (.presentLeaf actual) inp.anc || isProtectedStructure (.presentLeaf actual) inp.anc) = true <;>
        cases mode <;> simp [hUnkProt, hMismatch, selectModeKind]
  have hNotAuth : (planPath inp mode).decision ≠ .authorized := by
    intro hDec
    have hUnres : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved = false := by
      by_cases hU : (pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted).unresolved = true
      · unfold planPath at hDec
        simp [hU] at hDec
      · exact Bool.eq_false_iff.mpr hU
    have hAuthSel : authorizeSelection (planPath inp mode).selection (planPath inp mode).requiredPerms inp.granted = .authorized := by
      unfold planPath at hDec ⊢
      simpa [hUnres] using hDec
    have ⟨hCand, _⟩ := authorizeSelection_eq_authorized (planPath inp mode).selection (planPath inp mode).requiredPerms inp.granted hAuthSel
    exact hNotCand hCand
  exact ⟨
    buildEnvOp_not_authorized inp.path (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs hNotAuth,
    buildMetadataCandidate_not_authorized inp.path (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs hNotAuth,
    buildOwnershipUpdate_not_authorized inp.path inp.actor (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs hNotAuth⟩

/-- **Theorem 6 (`Match_A` Fixed-Point Property, ADR 0005 §3.1 #6)**:
    When a declared path is already converged (`obs = .presentLeaf desired`, `anc = .clear`,
    `overlap = .none`, self-owned with `lastApplied = desired` and `pending = none`),
    both `Mode.apply` and `Mode.prune` emit `decision = .noOp` and zero operations/updates. -/
theorem theorem6_matchA_fixed_point
    (path : PathID) (actor : ActorID) (val : Value) (orig : Origin) (granted : PermSet) (mode : Mode) :
    let inp : PathInput := {
      path := path
      actor := actor
      declared := some val
      obs := .presentLeaf val
      anc := .clear
      owner := some { manager := actor, origin := orig, lastApplied := val, pending := none }
      granted := granted
      overlap := .none
      hasUnresolvedSelfPending := false
    }
    (planPath inp mode).decision = .noOp ∧
    (planPath inp mode).envOp = none ∧
    (planPath inp mode).metadataCandidate = none ∧
    (planPath inp mode).ownershipUpdate = none := by
  intro inp
  unfold planPath pureRecoverPath classifyPath selectModeKind selectModeAction selectModePerms authorizeSelection buildEnvOp buildMetadataCandidate buildOwnershipUpdate
  simp [inp, isObservationUnknown, isProtectedStructure]

/-- **Theorem 7 (Determinism of `planPath` and `gateBatch`, ADR 0005 §3.1 #7)**:
    Identical inputs always produce identical plans, decisions, and batch gate outcomes. -/
theorem theorem7_plan_determinism
    (inp₁ inp₂ : PathInput) (m₁ m₂ : Mode)
    (hInp : inp₁ = inp₂) (hMode : m₁ = m₂) :
    planPath inp₁ m₁ = planPath inp₂ m₂ := by
  subst hInp hMode
  rfl

/-- **Theorem 8a (Intra-Mode Blocker All-or-Nothing Gate, ADR 0005 §3.1 #8)**:
    If any path in the batch has a blocking decision (`blocker` or `policyDenied`), or if
    `unresolvedRecovery` or `quarantined` is true, `gateBatch` suppresses all `envChanges`,
    `metadataCandidates`, and `ownershipUpdates`. -/
theorem theorem8_all_or_nothing_gate
    (plans : List PathPlan) (unresolvedRecovery quarantined : Bool)
    (hBlock : (unresolvedRecovery || quarantined || plans.any (fun p => isBlockingDecision p.decision || p.recovery.unresolved)) = true) :
    let g := gateBatch plans unresolvedRecovery quarantined
    g.blocked = true ∧ g.envChanges = [] ∧ g.metadataCandidates = [] ∧ g.ownershipUpdates = [] := by
  unfold gateBatch
  simp [hBlock]

/-- **Theorem 8b (`Deferred` and `NoOp` Never Require Execution Permissions, ADR 0005 §3.1 #8, Counterexample #39)**:
    Paths selected as `.deferred` or `.noOp` never produce `.policyDenied`, regardless of `granted` permissions. -/
theorem theorem8_deferred_perm_free
    (sel : SelectionKind) (reqPerms granted : PermSet)
    (hSel : sel = .deferred ∨ sel = .noOp) :
    authorizeSelection sel reqPerms granted ≠ .policyDenied := by
  rcases hSel with rfl | rfl <;> intro h <;> contradiction

/-- **Theorem 9 (Explicit Resolution Safety, ADR 0005 §3.1 #9)**:
    Explicit resolution (`adopt` / `release`) never emits environment changes (`envChanges = []`),
    only targets `req.path`, leaves `nextOwner = req.owner` when rejected, and when accepted:
    1. requires `Own + Resolve` permissions (`permOwnResolve`),
    2. never hijacks foreign ownership or overrides a pending intent, and
    3. never produces `Origin.created`. -/
theorem theorem9_explicit_resolution_safety
    (req : ResolutionRequest) :
    let res := planExplicitResolution req
    res.envChanges = [] ∧
    res.targetPath = req.path ∧
    (res.accepted = false → res.nextOwner = req.owner) ∧
    (res.accepted = true →
      PermSet.allows req.granted permOwnResolve = true ∧
      (∀ e, req.owner = some e → (e.manager != req.actor) = false ∧ e.pending.isSome = false) ∧
      (∀ e', res.nextOwner = some e' → e'.origin = .modified ∧ e'.manager = req.actor)) := by
  dsimp only
  refine ⟨?_, ?_, ?_, ?_⟩
  · unfold planExplicitResolution
    repeat (first | split | rfl)
  · unfold planExplicitResolution
    repeat (first | split | rfl)
  · unfold planExplicitResolution
    dsimp only
    repeat (first | split | (intro hRej; rfl) | (intro hRej; contradiction))
  · intro hAcc
    unfold planExplicitResolution at hAcc ⊢
    by_cases hPre : (req.quarantined || req.hasUnresolvedSelfPending || !req.expectedTokenMatch || !req.expectedRevisionMatch) = true
    · simp [hPre] at hAcc
    · simp only [Bool.not_eq_true] at hPre
      simp only [hPre, Bool.false_eq_true, ↓reduceIte] at hAcc ⊢
      by_cases hPerm : PermSet.allows req.granted permOwnResolve = true
      · have hNotPerm : (!PermSet.allows req.granted permOwnResolve) = false := by simp [hPerm]
        simp only [hNotPerm, Bool.false_eq_true, ↓reduceIte] at hAcc ⊢
        by_cases hObsUnk : isObservationUnknown req.obs req.anc = true
        · simp [hObsUnk] at hAcc
        · simp only [Bool.not_eq_true] at hObsUnk
          simp only [hObsUnk, Bool.false_eq_true, ↓reduceIte] at hAcc ⊢
          cases hKind : req.kind with
          | adopt =>
            simp only [hKind] at hAcc ⊢
            by_cases hOv : ((req.anc != .clear) || req.hasOverlap) = true
            · simp [hOv] at hAcc
            · simp only [Bool.not_eq_true] at hOv
              simp only [hOv, Bool.false_eq_true, ↓reduceIte] at hAcc ⊢
              cases hObs : req.obs with
              | absent | presentRecord | presentOpaque | unknown =>
                simp [hObs] at hAcc
              | presentLeaf val =>
                simp only [hObs] at hAcc ⊢
                cases hOwn : req.owner with
                | none =>
                  refine ⟨hPerm, ?_, ?_⟩
                  · intro _ hSome; contradiction
                  · intro e' hNext
                    simp only [Option.some.injEq] at hNext
                    subst hNext
                    exact ⟨rfl, rfl⟩
                | some e =>
                  simp only [hOwn] at hAcc ⊢
                  by_cases hBad : (e.manager != req.actor || e.pending.isSome) = true
                  · simp [hBad] at hAcc
                  · simp only [Bool.not_eq_true] at hBad
                    simp only [hBad, Bool.false_eq_true, ↓reduceIte] at hAcc ⊢
                    simp only [Bool.or_eq_false_iff] at hBad
                    refine ⟨hPerm, ?_, ?_⟩
                    · intro e₀ hSome
                      simp only [Option.some.injEq] at hSome
                      subst hSome
                      exact hBad
                    · intro e' hNext
                      simp only [Option.some.injEq] at hNext
                      subst hNext
                      exact ⟨rfl, rfl⟩
          | release =>
            simp only [hKind] at hAcc ⊢
            cases hOwn : req.owner with
            | none => simp [hOwn] at hAcc
            | some e =>
              simp only [hOwn] at hAcc ⊢
              by_cases hBad : (e.manager != req.actor || e.pending.isSome) = true
              · simp [hBad] at hAcc
              · simp only [Bool.not_eq_true] at hBad
                simp only [hBad, Bool.false_eq_true, ↓reduceIte] at hAcc ⊢
                simp only [Bool.or_eq_false_iff] at hBad
                refine ⟨hPerm, ?_, ?_⟩
                · intro e₀ hSome
                  simp only [Option.some.injEq] at hSome
                  subst hSome
                  exact hBad
                · intro _ hNext; contradiction
      · simp [hPerm] at hAcc

/-- **Theorem 10 (Cross-Mode Anomaly Isolation & Global Blocker Exceptions, ADR 0005 §3.1 #10)**:
    1. Independent retired `Created` anomalies and prunable leaves are `.deferred` in `Apply` and `.blocker`/`.candidate` in `Prune`.
    2. Independent declared conflicts and retired `Modified` unknowns are `.blocker` in `Apply` and `.deferred` in `Prune`.
    3. `migrationRequired` and `unresolvedPending` are `.blocker` in both `Apply` and `Prune`. -/
theorem theorem10_cross_mode_anomaly_isolation :
    (selectMode .retiredCreatedAnomaly .apply).1 = .deferred ∧
    (selectMode .retiredCreatedAnomaly .prune).1 = .blocker ∧
    (selectMode .retiredCreatedPrunable .apply).1 = .deferred ∧
    (selectMode .retiredCreatedPrunable .prune).1 = .candidate ∧
    (selectMode .declaredConflict .apply).1 = .blocker ∧
    (selectMode .declaredConflict .prune).1 = .deferred ∧
    (selectMode .retiredModifiedUnknown .apply).1 = .blocker ∧
    (selectMode .retiredModifiedUnknown .prune).1 = .deferred ∧
    (selectMode .migrationRequired .apply).1 = .blocker ∧
    (selectMode .migrationRequired .prune).1 = .blocker ∧
    (selectMode .unresolvedPending .apply).1 = .blocker ∧
    (selectMode .unresolvedPending .prune).1 = .blocker := by
  decide

/-- **Theorem 11a (MetadataCandidate Purity & No-Env-Pending Invariant, ADR 0005 §3.1 #11)**:
    Whenever `planPath` emits a `metadataCandidate`, it never emits an `envOp` and its
    `ownershipUpdate` has `createsPending = false`. -/
theorem theorem11_metadata_candidate_purity
    (inp : PathInput) (mode : Mode)
    (hMeta : (planPath inp mode).metadataCandidate.isSome = true) :
    (planPath inp mode).envOp = none ∧
    ∀ u, (planPath inp mode).ownershipUpdate = some u → u.createsPending = false := by
  change (buildMetadataCandidate inp.path (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs).isSome = true at hMeta
  change buildEnvOp inp.path (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs = none ∧
         ∀ u, buildOwnershipUpdate inp.path inp.actor (planPath inp mode).decision (planPath inp mode).action inp.declared (planPath inp mode).recovery.virtualOwner inp.obs = some u → u.createsPending = false
  unfold buildMetadataCandidate at hMeta
  unfold buildEnvOp buildOwnershipUpdate
  cases hDec : (planPath inp mode).decision <;> simp [hDec] at hMeta ⊢
  cases hAct : (planPath inp mode).action <;>
    cases hDecl : inp.declared <;>
    cases hVirt : (planPath inp mode).recovery.virtualOwner <;>
    cases hObs : inp.obs <;>
    simp [hAct, hDecl, hVirt, hObs, OwnershipUpdate.createsPending] at hMeta ⊢

/-- **Theorem 11b (Metadata-Only Batch Never Requires `DurableIntent` or `ApplyBatch`, ADR 0005 §3.1 #11, Counterexample #57)**:
    When a gated batch has no environment changes (`g.envChanges = []`), neither `DurableIntent`
    persistence nor `Provider.ApplyBatch` execution is requested. -/
theorem theorem11_metadata_only_batch_no_intent_or_apply
    (g : BatchGateResult) (hNoEnv : g.envChanges = []) :
    g.requiresDurableIntent = false ∧ g.requiresApplyBatch = false := by
  unfold BatchGateResult.requiresDurableIntent BatchGateResult.requiresApplyBatch
  simp [hNoEnv]

end Kisage
