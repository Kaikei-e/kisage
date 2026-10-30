import Kisage
import Lean

open Kisage Lean

def stateClassToString : StateClass → String
  | .outsideDomain => "OutsideDomain"
  | .converged => "Converged"
  | .declaredSet => "DeclaredSet"
  | .declaredAdoptModified => "DeclaredAdoptModified"
  | .declaredConflict => "DeclaredConflict"
  | .deferredUntilRelease => "DeferredUntilRelease"
  | .retiredModifiedKnown => "RetiredModifiedKnown"
  | .retiredModifiedUnknown => "RetiredModifiedUnknown"
  | .retiredCreatedAbsent => "RetiredCreatedAbsent"
  | .retiredCreatedPrunable => "RetiredCreatedPrunable"
  | .retiredCreatedAnomaly => "RetiredCreatedAnomaly"
  | .migrationRequired => "MigrationRequired"
  | .unresolvedPending => "UnresolvedPending"

def selectionToString : SelectionKind → String
  | .noOp => "NoOp"
  | .deferred => "Deferred"
  | .candidate => "Candidate"
  | .blocker => "Blocker"

def decisionToString : DecisionKind → String
  | .noOp => "NoOp"
  | .deferred => "Deferred"
  | .authorized => "Authorized"
  | .policyDenied => "PolicyDenied"
  | .blocker => "Blocker"
  | .suppressed => "Suppressed"

def actionToString : ActionKind → String
  | .none => ""
  | .setLeaf => "Set"
  | .deleteLeaf => "Delete"
  | .adoptModified => "AdoptModified"
  | .unmanageModified => "Unmanage"
  | .clearAbsentRecord => "ClearRecord"
  | .explicitAdopt => "ExplicitAdopt"
  | .explicitRelease => "ExplicitRelease"

def modeToString : Mode → String
  | .apply => "Apply"
  | .prune => "Prune"

structure OwnerCase where
  tag : String
  entry : Option OwnerEntry

def ownerCases : List OwnerCase :=
  [ { tag := "none", entry := none }
  , { tag := "self_created_10", entry := some { manager := 1, origin := .created, lastApplied := 10, pending := none } }
  , { tag := "self_modified_10", entry := some { manager := 1, origin := .modified, lastApplied := 10, pending := none } }
  , { tag := "foreign_created_10", entry := some { manager := 2, origin := .created, lastApplied := 10, pending := none } }
  , { tag := "self_pending_create_10", entry := some {
        manager := 1, origin := .modified, lastApplied := 10,
        pending := some { op := .set, expected := none, target := some 10, previousEntry := none }
      } }
  , { tag := "self_pending_update_created_20", entry := some {
        manager := 1, origin := .created, lastApplied := 10,
        pending := some { op := .set, expected := some 10, target := some 20, previousEntry := some (.created, 10) }
      } }
  , { tag := "self_pending_delete_10", entry := some {
        manager := 1, origin := .created, lastApplied := 10,
        pending := some { op := .delete, expected := some 10, target := none, previousEntry := some (.created, 10) }
      } }
  ]

def obsCases : List (String × Observed) :=
  [ ("absent", .absent)
  , ("leaf_10", .presentLeaf 10)
  , ("leaf_20", .presentLeaf 20)
  , ("record", .presentRecord)
  , ("opaque", .presentOpaque)
  , ("unknown", .unknown)
  ]

def ancCases : List (String × AncestorStatus) :=
  [ ("clear", .clear)
  , ("protected", .protectedAncestor)
  , ("unknown", .unknownAncestor)
  ]

def declCases : List (String × Option Value) :=
  [ ("undeclared", none)
  , ("declared_10", some 10)
  ]

def permCases : List (String × PermSet) :=
  [ ("none", permNone)
  , ("own", permOwn)
  , ("regular", permRegular)
  ]

def overlapCases : List (String × OverlapKind) :=
  [ ("none", .none)
  , ("retired_created", .retiredCreatedOverlap)
  , ("retired_modified", .retiredModifiedPendingRelease)
  , ("foreign_conflict", .foreignConflict)
  ]

/-- Checks whether `(decl, ocTag, ov)` corresponds to a non-corrupted ownership store in ADR 0002 §1
    (no two distinct overlapping paths simultaneously present in `dom(σ)`). -/
def isValidDRTCombination (decl : Option Value) (ocTag : String) (ov : OverlapKind) : Bool :=
  match ov with
  | .none => true
  | .retiredCreatedOverlap =>
    (decl.isSome && ocTag == "none") || (decl.isNone && ocTag == "self_created_10")
  | .retiredModifiedPendingRelease =>
    decl.isSome && ocTag == "none"
  | .foreignConflict =>
    decl.isSome && ocTag == "none"

/-- Serialization covers every value-bearing field of the formal model. Revisions,
    batch IDs and diagnostics are Go-specific and are checked by Go conformance tests. -/
def originJson : Origin → Json
  | .created => toJson "Created"
  | .modified => toJson "Modified"

def previousJson : Option (Origin × Value) → Json
  | none => .null
  | some (o, v) => Json.mkObj [("origin", originJson o), ("lastApplied", toJson v)]

def pendingJson : Option PendingIntent → Json
  | none => .null
  | some p => Json.mkObj [("op", toJson (if p.op == .set then "Set" else "Delete")),
      ("expected", toJson p.expected), ("target", toJson p.target), ("previous", previousJson p.previousEntry)]

def ownerJson : Option OwnerEntry → Json
  | none => .null
  | some e =>
    let creating := match e.pending with
      | some p => p.previousEntry.isNone
      | none => false
    Json.mkObj [("manager", toJson e.manager),
      ("origin", if creating then toJson "None" else originJson e.origin),
      ("lastApplied", if creating then .null else toJson e.lastApplied),
      ("pending", pendingJson e.pending)]

def envJson : Option EnvOp → Json
  | none => .null
  | some (.set path expected target) => Json.mkObj [("path", toJson path),
      ("op", toJson "Set"), ("expected", toJson expected), ("target", toJson target)]
  | some (.delete path expected) => Json.mkObj [("path", toJson path),
      ("op", toJson "Delete"), ("expected", toJson expected), ("target", .null)]

def metaJson (actor : ActorID) : Option MetadataCandidate → Json
  | none => .null
  | some m =>
    let (action, after) := match m with
      | .adoptModified _ v => ("AdoptModified", some { manager := actor, origin := .modified, lastApplied := v : OwnerEntry })
      | .unmanageModified _ => ("Unmanage", none)
      | .clearAbsentRecord _ => ("ClearRecord", none)
    Json.mkObj [("path", toJson m.path), ("action", toJson action), ("after", ownerJson after)]

def updateJson : Option OwnershipUpdate → Json
  | none => .null
  | some u =>
    let after := match u with
      | .upsert _ e _ => some e
      | .remove _ _ => none
    Json.mkObj [("path", toJson u.path), ("createsPending", toJson u.createsPending), ("after", ownerJson after)]

def recoveryKindString (r : RecoveryResult) : String :=
  if r.isCandidate && !r.authorized then "PolicyDenied"
  else match r.kind with
    | .none => "None"
    | .createDemotedModified | .updateConfirmed => "CommitSet"
    | .createCanceled | .updateCanceled | .deleteCanceled => "Rollback"
    | .deleteConfirmed => "ClearDelete"
    | .pendingUnknown => "RetainedUnknown"
    | .pendingConflict => "Conflict"

def permsJson (p : PermSet) : Json :=
  Json.mkObj [("set", toJson p.set), ("delete", toJson p.delete),
    ("own", toJson p.own), ("resolve", toJson p.resolve)]

def main : IO Unit := do
  for mode in [Mode.apply, Mode.prune] do
    for (declTag, decl) in declCases do
      for (obsTag, obs) in obsCases do
        for (ancTag, anc) in ancCases do
          for oc in ownerCases do
            for (permTag, perms) in permCases do
              for (ovTag, ov) in overlapCases do
                if isValidDRTCombination decl oc.tag ov then
                  let inp : PathInput := {
                    path := 1
                    actor := 1
                    declared := decl
                    obs := obs
                    anc := anc
                    owner := oc.entry
                    granted := perms
                    overlap := ov
                    hasUnresolvedSelfPending := false
                  }
                  let p := planPath inp mode
                  if p.inMA then
                    let result := Json.mkObj [
                      ("mode", toJson (modeToString mode)), ("decl", toJson declTag),
                      ("obs", toJson obsTag), ("anc", toJson ancTag), ("owner", toJson oc.tag),
                      ("perms", toJson permTag), ("overlap", toJson ovTag),
                      ("stateClass", toJson (stateClassToString p.stateClass)),
                      ("selection", toJson (selectionToString p.selection)),
                      ("decision", toJson (decisionToString p.decision)),
                      ("action", toJson (actionToString p.action)),
                      ("hasEnvOp", toJson p.envOp.isSome), ("hasMetaCand", toJson p.metadataCandidate.isSome),
                      ("recAuthorized", toJson p.recovery.authorized), ("recUnresolved", toJson p.recovery.unresolved),
                      ("recoveryKind", toJson (recoveryKindString p.recovery)),
                      ("recIsCandidate", toJson p.recovery.isCandidate), ("requiredPerms", permsJson p.requiredPerms),
                      ("virtualOwner", ownerJson p.recovery.virtualOwner),
                      ("envOp", envJson p.envOp), ("metadataCandidate", metaJson inp.actor p.metadataCandidate),
                      ("ownershipUpdate", updateJson p.ownershipUpdate)]
                    IO.println result.compress
