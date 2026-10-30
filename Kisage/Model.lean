namespace Kisage

abbrev ActorID := Nat
abbrev PathID := Nat
abbrev Value := Nat
abbrev Token := Nat
abbrev Revision := Nat

/-- Observation sum type corresponding to ADR 0001 §2 (`Absent | PresentLeaf | PresentRecord | PresentOpaque | Unknown`). -/
inductive Observed where
  | absent
  | presentLeaf (val : Value)
  | presentRecord
  | presentOpaque
  | unknown
  deriving DecidableEq, Repr, Inhabited

/-- Ancestor structural observation along a path's prefix chain (ADR 0001 §3). -/
inductive AncestorStatus where
  | clear
  | protectedAncestor
  | unknownAncestor
  deriving DecidableEq, Repr, Inhabited

/-- Ownership origin distinguishing `Absent → Present` creation (`created`) from adoption (`modified`) (ADR 0002 §1). -/
inductive Origin where
  | created
  | modified
  deriving DecidableEq, Repr, Inhabited

/-- Pending environment intent operation kind (ADR 0002 §1, ADR 0004 §1). -/
inductive PendingOp where
  | set
  | delete
  deriving DecidableEq, Repr, Inhabited

/-- Durable pending environment intent attached to an ownership record (ADR 0002 §1, ADR 0004 §1–3).
    When `previousEntry = none`, the entry represents an in-flight creation from `Absent` (`Origin = None` in ADR 0002 §2).
    When `previousEntry = some (prevOrig, prevVal)`, the entry represents an in-flight update or delete on a settled record. -/
structure PendingIntent where
  op : PendingOp
  expected : Option Value
  target : Option Value
  previousEntry : Option (Origin × Value)
  deriving DecidableEq, Repr, Inhabited

/-- Single-path ownership record in σ (ADR 0002 §1). -/
structure OwnerEntry where
  manager : ActorID
  origin : Origin
  lastApplied : Value
  pending : Option PendingIntent := none
  deriving DecidableEq, Repr, Inhabited

/-- Four-bit permission set (`Set`, `Delete`, `Own`, `Resolve`) where `Resolve` is independent (ADR 0002 §3). -/
structure PermSet where
  set : Bool := false
  delete : Bool := false
  own : Bool := false
  resolve : Bool := false
  deriving DecidableEq, Repr, Inhabited

def permNone : PermSet := {}
def permOwn : PermSet := { own := true }
def permSetOwn : PermSet := { set := true, own := true }
def permDeleteOwn : PermSet := { delete := true, own := true }
def permOwnResolve : PermSet := { own := true, resolve := true }
def permRegular : PermSet := { set := true, delete := true, own := true }
def permAll : PermSet := { set := true, delete := true, own := true, resolve := true }

/-- Checks whether `granted` includes every permission required by `req` (ADR 0002 §3). -/
def PermSet.allows (granted req : PermSet) : Bool :=
  (!req.set || granted.set) &&
  (!req.delete || granted.delete) &&
  (!req.own || granted.own) &&
  (!req.resolve || granted.resolve)

/-- Reconciliation mode (`Apply` vs. `Prune`) (ADR 0003 §1). -/
inductive Mode where
  | apply
  | prune
  deriving DecidableEq, Repr, Inhabited

/-- Ancestor/descendant ownership overlap relation on a path (ADR 0002 §2, ADR 0003 §1). -/
inductive OverlapKind where
  | none
  | foreignConflict
  | retiredCreatedOverlap
  | retiredModifiedPendingRelease
  deriving DecidableEq, Repr, Inhabited

/-- Returns true when an observation is `unknown` or an ancestor is `unknownAncestor`. -/
def isObservationUnknown (obs : Observed) (anc : AncestorStatus) : Bool :=
  match obs, anc with
  | .unknown, _ => true
  | _, .unknownAncestor => true
  | _, _ => false

/-- Returns true when an observation or ancestor is a protected structure (`Record` or `Opaque`). -/
def isProtectedStructure (obs : Observed) (anc : AncestorStatus) : Bool :=
  match obs, anc with
  | _, .protectedAncestor => true
  | .presentRecord, _ => true
  | .presentOpaque, _ => true
  | _, _ => false

/-- Returns true when `p` belongs to the active actor's evaluation domain `M_A(d, σ) = D ∪ dom(σ_A)` (ADR 0002 §2). -/
def inActiveDomain (actor : ActorID) (declared : Option Value) (owner : Option OwnerEntry) : Bool :=
  match declared, owner with
  | some _, _ => true
  | none, some e => e.manager == actor
  | none, none => false

end Kisage
