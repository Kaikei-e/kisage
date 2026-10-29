# kisage

**English** | **[日本語 (README.ja.md)](README.ja.md)**

**kisage** is a deterministic, level-triggered leaf-path reconciler for structured configuration files. It takes an [LKAL](https://github.com/Kaikei-e/LKAL) document as the desired state, observes the live environment through a verifiable Provider contract, and conditionally applies only authorized leaf-path diffs under explicit `Scope` permissions and a shared ownership store ($\sigma$).

> **Status: Phase 0 (Specification & Architecture Design)**
> This repository currently defines the design charter, Architecture Decision Records (`proposed`), and normative counterexamples. The Lean 4 formal model, Go implementation, and fault-injection suites are target verification gates for subsequent phases.

## Key Characteristics

- **Policy vs. Mechanism Separation**: Operates strictly on LKAL's syntax tree and deterministic equality (`Equal`) without adding templates, variables, or conditionals. Bounds *where* and *under what preconditions* changes occur (domain validation of declared values remains outside kisage).
- **Bounded Blast Radius**: Evaluates only declared leaf paths and the active Actor's owned paths ($M_A(d, \sigma) = D \cup \mathrm{dom}(\sigma_A)$), preserving undeclared sibling settings.
- **Conservative Deletion (`apply` / `prune`)**: Separates creation/update (`apply`) from retirement (`prune`). Automatic deletion is permitted only for leaves proven created (`Absent → Present`) by the same Actor (`Origin = Created`).
- **Fail-Closed on Conflict & Drift**: Never guesses on ownership conflicts, structural mismatches (`Leaf` vs. `Record`/`Opaque`), or external edits—halts with structured diagnostics (`K102`–`K603`) and suppresses normal batch execution if any blocker exists in the active mode.
- **Explicit Metadata Resolution (`adopt` / `release`)**: Resolves drift or relinquishes ownership via explicit commands guarded by `Resolve` permission, an observation condition token (`--expect`), and an owner store revision (`--owner-revision`), without mutating the target file.

## CLI & MCP Interface

| Command | MCP Tool | Description |
| :--- | :--- | :--- |
| `kisage plan FILE` | `kisage_plan` | Read-only preview of recovery candidates, `Apply`/`Prune` plans, and diagnostics. |
| `kisage apply FILE` | `kisage_apply` | Applies declared `Set`s, matching adoptions, and retired `Modified` unmanagement. Never deletes environment values. |
| `kisage prune FILE` | `kisage_prune` | Conditionally deletes retired `Origin = Created` leaves matching `LastApplied`. |
| `kisage explain PATH` | `kisage_explain` | Inspects observation, ownership, `Scope`, blockers, `--expect` token, and `--owner-revision`. |
| `kisage adopt PATH --expect TOKEN --owner-revision REV` | `kisage_adopt` | Adopts an existing representable leaf as `Origin = Modified` (`Own + Resolve` required; file unchanged). |
| `kisage release PATH --expect TOKEN --owner-revision REV` | `kisage_release` | Releases a self-owned record and `Created` deletion rights (`Own + Resolve` required; file unchanged). |

## Documentation

The specification is maintained as a [DocDag](https://github.com/Kaikei-e/DocDag)-verified document graph (`docdag validate && docdag lint --strict`):

- **[`docs/PRINCIPLE.md`](docs/PRINCIPLE.md)** — Design Charter (7 Core Principles, 3-Layer Guarantee Boundary, Root of Trust, and Explicit Limitations)
- **[`docs/ADR/`](docs/ADR/README.md)** — Architecture Decision Records ([0001](docs/ADR/0001-leaf-path-projection-and-observation-sum-type.md)–[0005](docs/ADR/0005-cli-mcp-result-aggregation-and-verification-gates.md))
- **[`docs/samples/required-counterexamples.md`](docs/samples/required-counterexamples.md)** — 60 Normative Counterexample Scenarios (#1–#60)

## License

Apache License 2.0. See [LICENSE](LICENSE).
