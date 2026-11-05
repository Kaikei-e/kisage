# kisage

**English** | **[日本語 (README.ja.md)](README.ja.md)**

**kisage** is a deterministic, level-triggered leaf-path reconciler for structured configuration files. It takes an [LKAL](https://github.com/Kaikei-e/LKAL) document as the desired state, observes the live environment through a verifiable Provider contract, and conditionally applies only authorized leaf-path diffs under explicit `Scope` permissions and a shared ownership store ($\sigma$).

> **Status: Phase 0–3 Formal Verification & Reference Implementation**
> Go reference code covers pure planning (`plan/`), shared ownership & atomic file storage (`ownership/`), locked execution & crash recovery (`protocol/`), the Linux LKAL file provider (`provider/`), and the host-bound CLI & MCP stdio server (`host/`, `cli/`, `mcp/`, `cmd/kisage`). This is paired with Lean 4 formal proofs (`Kisage/*.lean`, verified with zero `sorry`/`sorryAx` across the 11 pure `Plan` safety theorems including `1a`–`1c` and `10b`, 9 protocol inductive invariants and transition safety theorems, and finite-execution convergence with the candidate ESR rank function) and 1,728-case Lean/Go differential testing of operation and ownership payloads (`kisage-drt`). ADRs remain `proposed`, and physical power-loss durability remains out of scope of process-level tests. See [verification status](docs/verification-status.md).

## Key Characteristics

- **Policy vs. Mechanism Separation**: Operates strictly on LKAL's syntax tree and deterministic equality (`Equal`) without adding templates, variables, or conditionals. Bounds *where* and *under what preconditions* changes occur (domain validation of declared values remains outside kisage).
- **Bounded Blast Radius & Structural Preservation**: Evaluates only declared leaf paths and the active Actor's owned paths ($M_A(d, \sigma) = D \cup \mathrm{dom}(\sigma_A)$), preserving undeclared sibling settings, comments, and `Opaque` nodes while materializing absent intermediate `Record` blocks without auto-cleaning emptied ancestors.
- **Conservative Deletion (`apply` / `prune`)**: Separates creation/update (`apply`) from retirement (`prune`). Automatic deletion is permitted only for leaves proven created (`Absent → Present`) by the same Actor (`Origin = Created`).
- **Fail-Closed on Conflict & Drift**: Never guesses on ownership conflicts, structural mismatches (`Leaf` vs. `Record`/`Opaque`), or external edits—halts with structured diagnostics (`K101`–`K604`) and `Scope`-aware `nextAction` guidance, suppressing normal batch execution if any blocker or policy denial exists in the active mode.
- **Explicit Metadata Resolution (`adopt` / `release`)**: Resolves drift or relinquishes ownership via explicit commands guarded by `Own + Resolve` permissions, an observation condition token (`--expect`, `.explanation.conditionToken`), and an owner store revision (`--owner-revision`, `.explanation.ownerRevision`), without mutating the target file.
- **Host-Bound Trust Boundary**: Binds Actor identity, 1-to-1 `declarationFile ↔ targetPath` resource mappings, `deviceId`, and `Scope` rules exclusively from a protected (`0600`) host policy (`KISAGE_HOST_CONFIG`, default `/etc/kisage/host.json`), ignoring any self-declared `--actor` flag or MCP `actor` argument.

## CLI & MCP Interface

| Command | MCP Tool | Description |
| :--- | :--- | :--- |
| `kisage plan FILE [-json] [-detailed-exitcode]` | `kisage_plan` | Read-only preview of virtual recovery candidates, `Apply`/`Prune` plans, and diagnostics. |
| `kisage apply FILE [-json]` | `kisage_apply` | Applies declared `Set`s, matching adoptions, and retired `Modified` unmanagement. Never deletes environment values. |
| `kisage prune FILE [-json]` | `kisage_prune` | Conditionally deletes retired `Origin = Created` leaves matching `LastApplied`. |
| `kisage explain FILE PATH [-json]` | `kisage_explain` | Inspects observation, ownership, `Scope`, blockers, `--expect` token (`.explanation.conditionToken`), and `--owner-revision` (`.explanation.ownerRevision`). |
| `kisage adopt FILE PATH --expect TOKEN --owner-revision REV [-json]` | `kisage_adopt` | Adopts an existing representable leaf as `Origin = Modified` (`Own + Resolve` required; file unchanged). |
| `kisage release FILE PATH --expect TOKEN --owner-revision REV [-json]` | `kisage_release` | Releases a self-owned record and `Created` deletion rights (`Own + Resolve` required; file unchanged). |

Build with `go build -o /tmp/kisage ./cmd/kisage`. The binary also provides `kisage init-store` (one-time administrative store setup, never exposed via MCP), `kisage mcp` (newline-delimited JSON-RPC 2.0 over stdio), and `--version` / `--help`. Standard exit codes are `0` (OK), `1` (Error/Blocked), and `64` (Input/Usage error); `plan -detailed-exitcode` returns `0` (NoOp), `1` (Error/Blocked), or `2` (Planned/Deferred diff). See [CLI / MCP setup](docs/cli.md) for host configuration and startup details.

## Verification & Documentation

```sh
lake build
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
docdag validate && docdag lint --strict
```

Try the [HTTP health checker example](examples/healthcheck/README.md): a small CLI with local demo endpoints that demonstrates configuration updates, preservation of local settings, explicit pruning, and drift detection.

The specification is maintained as a [DocDag](https://github.com/Kaikei-e/DocDag)-verified document graph:

- **[`docs/PRINCIPLE.md`](docs/PRINCIPLE.md)** — Design Charter (7 Core Principles, 3-Layer Guarantee Boundary, Root of Trust, and Explicit Limitations)
- **[`docs/ADR/`](docs/ADR/README.md)** — Architecture Decision Records ([0001](docs/ADR/0001-leaf-path-projection-and-observation-sum-type.md)–[0005](docs/ADR/0005-cli-mcp-result-aggregation-and-verification-gates.md))
- **[`docs/samples/required-counterexamples.md`](docs/samples/required-counterexamples.md)** — 60 Normative Counterexample Scenarios (#1–#60)
- **[`docs/verification-status.md`](docs/verification-status.md)** — Formal Verification & Implementation Status Matrix

## License

Apache License 2.0. See [LICENSE](LICENSE).
