# kisage

**English** | **[日本語 (README.ja.md)](README.ja.md)**

**kisage** is a deterministic, level-triggered leaf-path reconciler for structured configuration files. It reads an [LKAL](https://github.com/Kaikei-e/LKAL) document as the desired state (reference surface plate), observes the live environment under a verifiable Provider contract, and conditionally applies only authorized leaf-path changes under explicit `Scope` permissions and a persistent shared ownership store ($\sigma$).

> **Current Status — Phase 0 (Specification & Architecture Design)**
> This repository currently defines the design charter ([`docs/PRINCIPLE.md`](docs/PRINCIPLE.md)), the Phase 0 Architecture Decision Records ([`docs/ADR/`](docs/ADR/README.md), `proposed`), and the 60 normative counterexample scenarios ([`docs/samples/required-counterexamples.md`](docs/samples/required-counterexamples.md)). The Lean 4 formal proofs, Go reference implementation, differential random testing (DRT), and fault-injection suites described below are **mandatory development gates**, not yet completed artifacts.

---

## Why kisage?

When coding agents or automation tools modify configuration files directly, full-file regeneration and unconstrained edits risk clobbering undeclared sibling fields, silently overwriting human changes, or deleting settings the agent never created.

kisage separates **Policy** (what state is desired) from **Mechanism** (how state transitions are bounded, verified, and applied):

- **No New Computation Language**: kisage does not extend LKAL with templates, variables, or conditionals. It operates strictly on LKAL's syntax-tree path model and deterministic equality (`Equal`).
- **Bounded Blast Radius**: Only declared leaf paths $D = \mathrm{Paths}(d)$ and paths owned by the active Actor $\mathrm{dom}(\sigma_A)$ are inspected ($M_A(d, \sigma) = D \cup \mathrm{dom}(\sigma_A)$). Undeclared sibling leaves and their reachability are preserved.
- **Conservative Deletion (`apply` vs. `prune`)**: Creating/updating (`apply`) and deleting retired leaves (`prune`) are separate operations. A leaf can be automatically deleted by `prune` **only** if kisage verified an `Absent → Present` creation by the same Actor at the start of the current unbroken ownership session (`Origin = Created`). Adopting an existing matching value or recovering from an unverified crash records `Origin = Modified`, which never grants automatic deletion rights.
- **No Speculative Conflict Resolution**: Ownership conflicts, structural conflicts (`Leaf` vs. `Record`/`Opaque`), and external drift (both value edits and deletions) halt execution with structured diagnostics (`K102`–`K603`). Normal `apply` stops on drift even if the externally modified value happens to match the new declaration (`Observed = Desired ≠ LastApplied`).
- **Explicit Metadata-Only Resolution (`adopt` / `release`)**: Operators resolve drift or relinquish ownership through explicit commands (`kisage adopt` / `kisage release`) guarded by a separate `Resolve` permission, an observation condition token (`--expect TOKEN`), and an ownership store revision (`--owner-revision REV`), without mutating the target file.

> **Scope of Guarantees**: kisage bounds *where* and *under what preconditions* changes occur. It does **not** verify that a declaration matches user intent or domain security policy—if an agent declares an unsafe target value within its allowed `Scope`, kisage will converge toward that target. Domain policy validation belongs to declaration approval or external validators.

---

## Core Architecture

### 1. Leaf-Path Projection & Observation Sum Type ([ADR 0001](docs/ADR/0001-leaf-path-projection-and-observation-sum-type.md))

kisage projects an LKAL document $d$ into the set of leaf paths $D = \mathrm{Paths}(d)$ whose values are scalars or `List`s (`List` is compared and replaced as a whole atomic leaf; `Ref` is compared as an opaque path literal without secret resolution).

- **Asymmetric Empty-Record Rule (`K102`)**: Non-root empty `Record`s (`{}`) in a declaration $d$ are rejected with `K102: EmptyRecordNotSupported` to prevent accidental erasure of undeclared subtrees. An empty root document ($D = \varnothing$) is permitted to express retiring all managed leaves. Empty `Record`s observed in the target file are valid `Present(Record)` nodes.
- **Epistemological vs. Ontological Observation**: Observation separates whether a path could be read from what kind of node exists:
  ```text
  Observed = Unknown(reason) | Absent | Present(Node)
  Node     = Leaf(lkal.Value) | Record | Opaque(reason)
  ```
  `Unknown` represents observation failure (never guessed as `Absent`). `Opaque` represents a value confirmed to exist in the target format that cannot be losslessly mapped to an LKAL leaf (e.g., JSON `null` or arrays of objects in future providers). `Set` and `Delete` are rejected if the target is `Record`/`Opaque` or if any true ancestor is `Leaf`/`Opaque`.

### 2. Shared Ownership Store ($\sigma$ / $\sigma_A$) & Permissions ([ADR 0002](docs/ADR/0002-shared-ownership-store-scope-and-trust-boundary.md))

Ownership is tracked in a serialized shared store $\sigma$ keyed by `(ResourceID, PathSegments)`:

```text
OwnerEntry = { Manager, Origin, LastApplied?, Pending? }
Origin     = None | Created | Modified
```

- **Actor Isolation & Prefix Exclusivity**: Actor $A$ evaluates $M_A(d, \sigma) = D \cup \mathrm{dom}(\sigma_A)$. Unrelated paths owned by Actor $B$ do not trigger conflicts or prevent Actor $A$ from converging. However, if any path in $D$ is identical to—or an ancestor/descendant of—a path owned or reserved (`Pending`) by another Actor, planning halts with `OwnershipConflict` (`K201`), even if the values are equal.
- **Segment-Level `Scope` (`Set` / `Delete` / `Own` / `Resolve`)**: Permissions are checked on parsed path segments rather than string prefixes. `Own` is required for adopting, updating, or releasing ownership records; `Resolve` is required alongside `Own` for `adopt` and `release` and is never implied by `Own`.

### 3. Five-Stage Pure Planning Pipeline & Mode Gate ([ADR 0003](docs/ADR/0003-pure-plan-pipeline-mode-separation-and-explicit-resolution.md))

The pure `Plan` function evaluates paths in deterministic segment order through five stages:

```text
PureRecover(observed, σ_A)          → RecoveryCandidates, σ_virtual, Unresolved
Classify(d, observed, σ_virtual, A) → StateClass per path
SelectMode(StateClass, Mode)        → NoOp | Deferred | Candidate(action, perms) | Blocker
Authorize(Candidate, Scope)         → Authorized | PolicyDenied
Gate(all results)                   → ExecutableBatch | Blocked
```

- **State Classification Before Scope**: `NoOp`, `Deferred`, `Blocker`, and `Unknown` states do not demand write permissions, ensuring that already-converged paths never fail with spurious `PolicyDenied` errors and conflicts are never masked by permission errors.
- **Mode Isolation**: `Apply` handles declared `Set`/adoption, retired `Modified` unmanagement, and record cleanup for retired `Created` paths confirmed `Absent`. `Prune` handles conditional `Delete` of retired `Created` paths matching `LastApplied` (and record cleanup when `Absent`). Independent anomalies belonging to the other mode become `Deferred` rather than blocking the active mode, whereas `MigrationRequired` (`K204`, overlapping retired `Created` and new declared leaves) and unresolved self-Actor `Pending` entries block both modes.
- **Two-Step `Modified` Structural Migration (`DeferredUntilRelease`, `K603`)**: When a newly declared leaf overlaps as an ancestor/descendant purely with retired `Modified` paths of the same Actor, the new path is classified as `DeferredUntilRelease` before `StructureConflict`, allowing the first `apply` to unmanage the old `Modified` record and the second `apply` to inspect the migrated structure.
- **All-or-Nothing Mode Gate**: If any path in the active mode produces a `Blocker` or `PolicyDenied`, all normal `Changes` and normal `OwnershipUpdates` for that invocation are suppressed (`Suppressed`).

### 4. File-Batch Persistence, `Stabilize` Recovery & In-Lock Validation ([ADR 0004](docs/ADR/0004-file-batch-persistence-stabilize-recovery-and-provider-contract.md))

```text
ExecutionBatch = { BatchID, EnvironmentOps[], MetadataCandidates[] } // volatile in-memory plan
DurableIntent  = { BatchID, EnvironmentOps[] }                       // persisted only for Set/Delete
```

- **Volatile Metadata Candidates**: Record-only changes (`MetadataCandidates`: matching adoption, `Modified` unmanage, `Absent` record cleanup) never create `Pending` entries on disk and are discarded on crash.
- **Unified Lock Order & In-Lock Contract Validation**: Locks are acquired in strict `Shared Store (σ) → Resource` order. After `Provider.ApplyBatch` replaces the target file (`write temp → file fsync → Root.Rename → parent dir fsync`), kisage re-observes the file **while still holding the locks** to verify `Set(v) → Leaf(v)`, `Delete → Absent`, and sibling preservation **before** committing ownership updates to $\sigma$. Any contract violation (`K501`) aborts the ownership commit and quarantines the Resource (`K503`).
- **`Provider.Stabilize` Before Recovery Commit**: Crash recovery inspects only $\sigma_A$ without relying on whole-file digests. Before committing any recovered state (or record-only batch), kisage runs `Provider.Stabilize` under lock to `fsync` the current file and parent directory and re-verify observations—preventing a race where a second process commits ownership based on an un-synced page-cache rename that later rolls back on power loss. Unverified new creations (`Origin = None` with `Observed == Target`) downgrade conservatively to `Origin = Modified` (`K601: CreatedOriginUnverified`).

---

## CLI & MCP Interface ([ADR 0005](docs/ADR/0005-cli-mcp-result-aggregation-and-verification-gates.md))

| Command | MCP Tool | Description |
| :--- | :--- | :--- |
| `kisage plan FILE [-json] [-detailed-exitcode]` | `kisage_plan` | Read-only preview. Simulates `PureRecover` in memory and computes `Apply` and `Prune` plans and diagnostics. |
| `kisage apply FILE [-json]` | `kisage_apply` | Runs self-Actor recovery, conditional `Set`, matching adoption, and retired `Modified` unmanagement. Never deletes environment values. |
| `kisage prune FILE [-json]` | `kisage_prune` | Runs self-Actor recovery and conditional `Delete` for retired `Origin = Created` leaves matching `LastApplied`. |
| `kisage explain PATH [-json]` | `kisage_explain` | Read-only inspection of observation, ownership, `Scope`, blockers, condition token (`TOKEN`), and owner revision (`REV`). |
| `kisage adopt PATH --expect TOKEN --owner-revision REV [-json]` | `kisage_adopt` | Explicitly adopts a representable leaf as `Origin = Modified` (`LastApplied = observed`). Requires `Own + Resolve`. Writes no file values. |
| `kisage release PATH --expect TOKEN --owner-revision REV [-json]` | `kisage_release` | Explicitly releases a self-owned record and relinquishes `Created` deletion rights. Requires `Own + Resolve`. Deletes no file values. |

### Exit Codes & Result Aggregation

- **Standard Exit Codes**: `0` (completed / no-op), `1` (conflict, blocker, failure, or indeterminate), `64` (invalid input / usage error).
- **`plan -detailed-exitcode`**: `0` (both `Apply` and `Prune` are `NoOp`), `2` (changes or `Deferred` work present with no blockers), `1` (any blocker, `PolicyDenied`, unresolved recovery, or input error).
- **Phase-Separated JSON Output**: Reports `recovery` and `normalExecution` separately alongside `summary.execution` (`Indeterminate → PartiallyApplied → Failed → Blocked → Planned → Completed → NoOp`) and `summary.convergence` (`Converged | NotConverged | Unknown`).

---

## Three-Layer Guarantee Boundary & Explicit Limitations

| Layer | Guarantee / Premise | Verification / Enforcement Mechanism |
| :--- | :--- | :--- |
| **1. Logical Frame** | Normal plans and recovery stay within $M_A(d, \sigma)$; explicit resolution stays within the target path; all mutations satisfy required `Scope` (`Set` / `Delete` / `Own` / `Resolve`). | **Lean 4 mechanized theorems** over valid inputs + Differential Random Testing (DRT) against Go. |
| **2. Provider Preservation** | Structural protection, preservation of untouched sibling leaves, in-lock precondition re-check, and adherence to `ApplyBatch` / `Stabilize` durability steps. | **Provider conformance tests, property-based testing (PBT), and real-file fault injection**. (Not a formal proof of the Go compiler or OS kernel.) |
| **3. Execution Boundary** | Protection of trust roots (`Scope`, host-bound `Actor`, $\sigma$ store, lock files, parent directory) and isolation against direct file edits bypassing kisage. | **OS permissions, sandboxing, and host MCP configuration**. Outside kisage's standalone guarantee. |

### Explicit Limitations (Non-Guarantees)

1. **Same-Value ABA on Value-Only Providers**: A provider that checks only value equality (`Equal`) cannot detect if an external process deleted a leaf and recreated it with the exact same value between runs. `Origin = Created` proves creation at the start of the ownership session and valid transitions thereafter, not physical lineage across external ABA edits.
2. **No Cross-Phase ACID Transactions**: Recovery persistence, target file replacement, ownership store Commit, and `apply` vs. `prune` are distinct durability phases.
3. **No Cross-Leaf Domain Invariants**: kisage does not validate semantic co-occurrence rules between multiple configuration keys.
4. **No Automatic `Leaf ↔ Record` Structural Migration**: Overlapping `Created` leaves halt with `MigrationRequired` (`K204`); `Record → Leaf` transitions require external structural edits because LKAL path deletion preserves empty `Record` blocks.

---

## Documentation & Verification Graph

The specification is organized as a machine-verified document graph managed by [DocDag](https://github.com/Kaikei-e/DocDag) (`v0.5.0`):

- **[`docs/PRINCIPLE.md`](docs/PRINCIPLE.md)**: The kisage Design Charter (7 Core Principles, 3-Layer Guarantee Boundary, Root of Trust, Explicit Limitations, Non-Goals).
- **[`docs/ADR/`](docs/ADR/README.md)**: Phase 0 Architecture Decision Records (`0001`–`0005`).
- **[`docs/samples/required-counterexamples.md`](docs/samples/required-counterexamples.md)**: 60 normative counterexample scenarios (#1–#60) serving as the shared specification for PBT, Lean/Go DRT, fault injection, and integration tests.

To validate the ADR dependency DAG and section invariants locally:

```sh
docdag validate
docdag lint --strict
docdag query --binding
docdag stats
```

---

## 日本語概要 (Japanese Overview)

日本語の完全な README は **[README.ja.md](README.ja.md)** を参照してください。

- **設計憲章**: [`docs/PRINCIPLE.md`](docs/PRINCIPLE.md)
- **アーキテクチャ決定記録 (ADR 0001〜0005)**: [`docs/ADR/README.md`](docs/ADR/README.md)
- **必須反例サンプル (#1〜#60)**: [`docs/samples/required-counterexamples.md`](docs/samples/required-counterexamples.md)

---

## License

Apache License 2.0. See [LICENSE](LICENSE).

