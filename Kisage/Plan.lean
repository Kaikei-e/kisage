import Kisage.Model
import Kisage.Recover

namespace Kisage

/-- 11-row state classification from ADR 0003 §1 Stage 2 (`Classify`). -/
inductive StateClass where
  | outsideDomain
  | converged
  | declaredSet
  | declaredAdoptModified
  | declaredConflict
  | deferredUntilRelease
  | retiredModifiedKnown
  | retiredModifiedUnknown
  | retiredCreatedAbsent
  | retiredCreatedPrunable
  | retiredCreatedAnomaly
  | migrationRequired
  | unresolvedPending
  deriving DecidableEq, Repr, Inhabited

/-- Mode selection kind from ADR 0003 §1 Stage 3 (`SelectMode`). -/
inductive SelectionKind where
  | noOp
  | deferred
  | candidate
  | blocker
  deriving DecidableEq, Repr, Inhabited

/-- Action kind requiring specific `PermSet` authorization (ADR 0002 §3, ADR 0003 §1). -/
inductive ActionKind where
  | none
  | setLeaf
  | deleteLeaf
  | adoptModified
  | unmanageModified
  | clearAbsentRecord
  | explicitAdopt
  | explicitRelease
  deriving DecidableEq, Repr, Inhabited

/-- Required `PermSet` for each `ActionKind` (ADR 0002 §3). -/
def requiredPermissions : ActionKind → PermSet
  | .none => permNone
  | .setLeaf => permSetOwn
  | .deleteLeaf => permDeleteOwn
  | .adoptModified => permOwn
  | .unmanageModified => permOwn
  | .clearAbsentRecord => permOwn
  | .explicitAdopt => permOwnResolve
  | .explicitRelease => permOwnResolve

/-- Per-path decision after Stage 4 (`Authorize`) and Stage 5 (`Gate`) (ADR 0003 §1). -/
inductive DecisionKind where
  | noOp
  | deferred
  | authorized
  | policyDenied
  | blocker
  | suppressed
  deriving DecidableEq, Repr, Inhabited

/-- Environment mutation operation (`Set` or `Delete`) emitted only for `setLeaf` and `deleteLeaf` (ADR 0004 §1). -/
inductive EnvOp where
  | set (path : PathID) (expected : Option Value) (target : Value)
  | delete (path : PathID) (expected : Value)
  deriving DecidableEq, Repr, Inhabited

def EnvOp.path : EnvOp → PathID
  | .set p _ _ => p
  | .delete p _ => p

/-- Metadata-only candidate (`AdoptModified`, `Unmanage`, `ClearRecord`) that never emits a `PendingIntent` (ADR 0004 §1, Theorem 11). -/
inductive MetadataCandidate where
  | adoptModified (path : PathID) (val : Value)
  | unmanageModified (path : PathID)
  | clearAbsentRecord (path : PathID)
  deriving DecidableEq, Repr, Inhabited

def MetadataCandidate.path : MetadataCandidate → PathID
  | .adoptModified p _ => p
  | .unmanageModified p => p
  | .clearAbsentRecord p => p

/-- Post-batch ownership store update (`upsert` or `remove`) (ADR 0003 §1). -/
inductive OwnershipUpdate where
  | upsert (path : PathID) (entry : OwnerEntry) (createsPending : Bool)
  | remove (path : PathID) (createsPending : Bool)
  deriving DecidableEq, Repr, Inhabited

def OwnershipUpdate.path : OwnershipUpdate → PathID
  | .upsert p _ _ => p
  | .remove p _ => p

def OwnershipUpdate.createsPending : OwnershipUpdate → Bool
  | .upsert _ _ b => b
  | .remove _ b => b

/-- Stage 2 (`Classify`): Scope-independent classification of a single path in `M_A(d, σ_virtual)` (ADR 0003 §1). -/
def classifyPath
    (actor : ActorID)
    (declared : Option Value)
    (obs : Observed)
    (anc : AncestorStatus)
    (virtualOwner : Option OwnerEntry)
    (overlap : OverlapKind)
    (hasUnresolvedSelfPending : Bool) : StateClass :=
  match declared with
  | some desired =>
    match overlap with
    | .retiredCreatedOverlap => .migrationRequired
    | .foreignConflict => .declaredConflict
    | .retiredModifiedPendingRelease =>
      match virtualOwner with
      | some e =>
        if e.manager != actor then .declaredConflict
        else if e.pending.isSome || hasUnresolvedSelfPending then .unresolvedPending
        else .deferredUntilRelease
      | none =>
        if hasUnresolvedSelfPending then .unresolvedPending
        else .deferredUntilRelease
    | .none =>
      match virtualOwner with
      | some e =>
        if e.manager != actor then .declaredConflict
        else if e.pending.isSome then .unresolvedPending
        else if isObservationUnknown obs anc || isProtectedStructure obs anc then .declaredConflict
        else
          match obs with
          | .presentLeaf actual =>
            if actual != e.lastApplied then .declaredConflict
            else if actual == desired then .converged
            else .declaredSet
          | _ => .declaredConflict
      | none =>
        if isObservationUnknown obs anc || isProtectedStructure obs anc then .declaredConflict
        else
          match obs with
          | .absent => .declaredSet
          | .presentLeaf actual =>
            if actual == desired then .declaredAdoptModified
            else .declaredConflict
          | _ => .declaredConflict
  | none =>
    match virtualOwner with
    | none => .outsideDomain
    | some e =>
      if e.manager != actor then .outsideDomain
      else if e.pending.isSome then .unresolvedPending
      else
        match e.origin with
        | .modified =>
          if isObservationUnknown obs anc then .retiredModifiedUnknown
          else .retiredModifiedKnown
        | .created =>
          if overlap == .retiredCreatedOverlap then .migrationRequired
          else if isObservationUnknown obs anc || isProtectedStructure obs anc || overlap == .foreignConflict then
            .retiredCreatedAnomaly
          else
            match obs with
            | .absent => .retiredCreatedAbsent
            | .presentLeaf actual =>
              if actual == e.lastApplied then .retiredCreatedPrunable
              else .retiredCreatedAnomaly
            | _ => .retiredCreatedAnomaly

/-- Stage 3 (`SelectMode`): 11-row mode selection table (ADR 0003 §1 item 3). -/
def selectModeKind (sc : StateClass) (mode : Mode) : SelectionKind :=
  match sc, mode with
  | .outsideDomain, _ => .noOp
  | .converged, _ => .noOp
  | .declaredSet, .apply => .candidate
  | .declaredSet, .prune => .deferred
  | .declaredAdoptModified, .apply => .candidate
  | .declaredAdoptModified, .prune => .deferred
  | .declaredConflict, .apply => .blocker
  | .declaredConflict, .prune => .deferred
  | .deferredUntilRelease, _ => .deferred
  | .retiredModifiedKnown, .apply => .candidate
  | .retiredModifiedKnown, .prune => .deferred
  | .retiredModifiedUnknown, .apply => .blocker
  | .retiredModifiedUnknown, .prune => .deferred
  | .retiredCreatedAbsent, _ => .candidate
  | .retiredCreatedPrunable, .apply => .deferred
  | .retiredCreatedPrunable, .prune => .candidate
  | .retiredCreatedAnomaly, .apply => .deferred
  | .retiredCreatedAnomaly, .prune => .blocker
  | .migrationRequired, _ => .blocker
  | .unresolvedPending, _ => .blocker

def selectModeAction (sc : StateClass) : ActionKind :=
  match sc with
  | .declaredSet => .setLeaf
  | .declaredAdoptModified => .adoptModified
  | .retiredModifiedKnown => .unmanageModified
  | .retiredCreatedAbsent => .clearAbsentRecord
  | .retiredCreatedPrunable => .deleteLeaf
  | _ => .none

def selectModePerms (sc : StateClass) (mode : Mode) : PermSet :=
  match selectModeKind sc mode with
  | .candidate => requiredPermissions (selectModeAction sc)
  | _ => permNone

def selectMode (sc : StateClass) (mode : Mode) : SelectionKind × ActionKind × PermSet :=
  (selectModeKind sc mode, selectModeAction sc, selectModePerms sc mode)

/-- Stage 4 (`Authorize`): Checks `Scope` only for `SelectionKind.candidate` (Counterexample #39). -/
def authorizeSelection (sel : SelectionKind) (reqPerms granted : PermSet) : DecisionKind :=
  match sel with
  | .noOp => .noOp
  | .deferred => .deferred
  | .blocker => .blocker
  | .candidate =>
    if PermSet.allows granted reqPerms then .authorized
    else .policyDenied

/-- Pure builder for environment operations (`Set` or `Delete`). -/
def buildEnvOp
    (path : PathID) (dec : DecisionKind) (act : ActionKind)
    (declared : Option Value) (virtualOwner : Option OwnerEntry) (obs : Observed) : Option EnvOp :=
  match dec with
  | .authorized =>
    match act, declared, virtualOwner, obs with
    | .setLeaf, some desired, none, .absent => some (.set path none desired)
    | .setLeaf, some desired, some _, .presentLeaf actual => some (.set path (some actual) desired)
    | .deleteLeaf, none, some e, .presentLeaf _ => some (.delete path e.lastApplied)
    | _, _, _, _ => none
  | _ => none

/-- Pure builder for metadata-only candidates (`AdoptModified`, `Unmanage`, `ClearRecord`). -/
def buildMetadataCandidate
    (path : PathID) (dec : DecisionKind) (act : ActionKind)
    (declared : Option Value) (virtualOwner : Option OwnerEntry) (obs : Observed) : Option MetadataCandidate :=
  match dec with
  | .authorized =>
    match act, declared, virtualOwner, obs with
    | .adoptModified, some desired, none, .presentLeaf _ => some (.adoptModified path desired)
    | .unmanageModified, none, some _, _ => some (.unmanageModified path)
    | .clearAbsentRecord, none, some _, .absent => some (.clearAbsentRecord path)
    | _, _, _, _ => none
  | _ => none

/-- Pure builder for post-batch ownership updates (`upsert` or `remove`). -/
def buildOwnershipUpdate
    (path : PathID) (actor : ActorID) (dec : DecisionKind) (act : ActionKind)
    (declared : Option Value) (virtualOwner : Option OwnerEntry) (obs : Observed) : Option OwnershipUpdate :=
  match dec with
  | .authorized =>
    match act, declared, virtualOwner, obs with
    | .setLeaf, some desired, none, .absent =>
      some (.upsert path { manager := actor, origin := .created, lastApplied := desired } true)
    | .setLeaf, some desired, some e, .presentLeaf _ =>
      some (.upsert path { manager := actor, origin := e.origin, lastApplied := desired } true)
    | .deleteLeaf, none, some _, .presentLeaf _ =>
      some (.remove path true)
    | .adoptModified, some desired, none, .presentLeaf _ =>
      some (.upsert path { manager := actor, origin := .modified, lastApplied := desired } false)
    | .unmanageModified, none, some _, _ =>
      some (.remove path false)
    | .clearAbsentRecord, none, some _, .absent =>
      some (.remove path false)
    | _, _, _, _ => none
  | _ => none

/-- Input descriptor for a single path in the planning pipeline. -/
structure PathInput where
  path : PathID
  actor : ActorID
  declared : Option Value
  obs : Observed
  anc : AncestorStatus
  owner : Option OwnerEntry
  granted : PermSet
  overlap : OverlapKind := .none
  hasUnresolvedSelfPending : Bool := false
  deriving DecidableEq, Repr, Inhabited

/-- Complete single-path planning result across Stages 1–4 and candidate emission. -/
structure PathPlan where
  input : PathInput
  inMA : Bool
  recovery : RecoveryResult
  stateClass : StateClass
  selection : SelectionKind
  action : ActionKind
  requiredPerms : PermSet
  decision : DecisionKind
  envOp : Option EnvOp
  metadataCandidate : Option MetadataCandidate
  ownershipUpdate : Option OwnershipUpdate
  deriving DecidableEq, Repr, Inhabited

/-- Runs Stages 1–4 (`PureRecover → Classify → SelectMode → Authorize`) and builds candidate operations for a single path. -/
def planPath (inp : PathInput) (mode : Mode) : PathPlan :=
  let recRes := pureRecoverPath inp.actor inp.owner inp.obs inp.anc inp.granted
  let inMA := inActiveDomain inp.actor inp.declared recRes.virtualOwner || inActiveDomain inp.actor inp.declared inp.owner
  let sc := classifyPath inp.actor inp.declared inp.obs inp.anc recRes.virtualOwner inp.overlap (inp.hasUnresolvedSelfPending || recRes.unresolved)
  let sel := selectModeKind sc mode
  let act := selectModeAction sc
  let reqPerms := selectModePerms sc mode
  let dec :=
    if recRes.unresolved then DecisionKind.blocker
    else authorizeSelection sel reqPerms inp.granted
  { input := inp
    inMA := inMA
    recovery := recRes
    stateClass := sc
    selection := sel
    action := act
    requiredPerms := reqPerms
    decision := dec
    envOp := buildEnvOp inp.path dec act inp.declared recRes.virtualOwner inp.obs
    metadataCandidate := buildMetadataCandidate inp.path dec act inp.declared recRes.virtualOwner inp.obs
    ownershipUpdate := buildOwnershipUpdate inp.path inp.actor dec act inp.declared recRes.virtualOwner inp.obs }

/-- Stage 5 (`Gate`): All-or-nothing batch gate across planned paths (ADR 0003 §1 item 5). -/
structure BatchGateResult where
  blocked : Bool
  envChanges : List EnvOp
  metadataCandidates : List MetadataCandidate
  ownershipUpdates : List OwnershipUpdate
  deriving DecidableEq, Repr, Inhabited

/-- True iff the batch requires `DurableIntent` persistence (ADR 0004 §1, Theorem 11). -/
def BatchGateResult.requiresDurableIntent (g : BatchGateResult) : Bool :=
  !g.blocked && !g.envChanges.isEmpty

/-- True iff the batch requires `Provider.ApplyBatch` execution (ADR 0004 §2, Theorem 11). -/
def BatchGateResult.requiresApplyBatch (g : BatchGateResult) : Bool :=
  !g.blocked && !g.envChanges.isEmpty

/-- True iff the batch requires `Provider.Stabilize` before store commit (ADR 0004 §2). -/
def BatchGateResult.requiresStabilize (g : BatchGateResult) : Bool :=
  !g.blocked && (!g.envChanges.isEmpty || !g.metadataCandidates.isEmpty)

def isBlockingDecision (d : DecisionKind) : Bool :=
  d == .blocker || d == .policyDenied

def gateBatch (plans : List PathPlan) (unresolvedRecovery quarantined : Bool) : BatchGateResult :=
  let hasBlocker := unresolvedRecovery || quarantined || plans.any (fun p => isBlockingDecision p.decision || p.recovery.unresolved)
  if hasBlocker then
    { blocked := true, envChanges := [], metadataCandidates := [], ownershipUpdates := [] }
  else
    { blocked := false
      envChanges := plans.filterMap (fun p => p.envOp)
      metadataCandidates := plans.filterMap (fun p => p.metadataCandidate)
      ownershipUpdates := plans.filterMap (fun p => p.ownershipUpdate) }

/-- Explicit metadata resolution command kind (`adopt` vs. `release`) (ADR 0003 §3). -/
inductive ResolutionKind where
  | adopt
  | release
  deriving DecidableEq, Repr, Inhabited

/-- Request for `planExplicitResolution` (ADR 0003 §3). -/
structure ResolutionRequest where
  kind : ResolutionKind
  actor : ActorID
  path : PathID
  obs : Observed
  anc : AncestorStatus
  owner : Option OwnerEntry
  granted : PermSet
  expectedTokenMatch : Bool
  expectedRevisionMatch : Bool
  hasOverlap : Bool := false
  quarantined : Bool := false
  hasUnresolvedSelfPending : Bool := false
  deriving DecidableEq, Repr, Inhabited

/-- Result of `planExplicitResolution` (ADR 0003 §3, Theorem 9). -/
structure ResolutionResult where
  accepted : Bool
  targetPath : PathID
  envChanges : List EnvOp
  nextOwner : Option OwnerEntry
  deriving DecidableEq, Repr, Inhabited

/-- Pure explicit metadata resolution planner for `adopt` and `release` (ADR 0003 §3, Counterexamples #36–#38). -/
def planExplicitResolution (req : ResolutionRequest) : ResolutionResult :=
  let rejected : ResolutionResult :=
    { accepted := false, targetPath := req.path, envChanges := [], nextOwner := req.owner }
  if req.quarantined || req.hasUnresolvedSelfPending || !req.expectedTokenMatch || !req.expectedRevisionMatch then
    rejected
  else if !PermSet.allows req.granted permOwnResolve then
    rejected
  else if isObservationUnknown req.obs req.anc then
    rejected
  else
    match req.kind with
    | .adopt =>
      if req.anc != .clear || req.hasOverlap then rejected
      else
        match req.obs with
        | .presentLeaf val =>
          match req.owner with
          | none =>
            { accepted := true
              targetPath := req.path
              envChanges := []
              nextOwner := some { manager := req.actor, origin := .modified, lastApplied := val, pending := none } }
          | some e =>
            if e.manager != req.actor || e.pending.isSome then rejected
            else
              { accepted := true
                targetPath := req.path
                envChanges := []
                nextOwner := some { manager := req.actor, origin := .modified, lastApplied := val, pending := none } }
        | _ => rejected
    | .release =>
      match req.owner with
      | none => rejected
      | some e =>
        if e.manager != req.actor || e.pending.isSome then rejected
        else
          { accepted := true
            targetPath := req.path
            envChanges := []
            nextOwner := none }

end Kisage
