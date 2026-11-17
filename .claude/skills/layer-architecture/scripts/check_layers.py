#!/usr/bin/env python3
"""Go package layer boundary and purity checker for kisage.

Usage:
  check_layers.py                    check all tracked/workspace non-test Go files
  check_layers.py <dir>              check non-test Go files under <dir>
  check_layers.py --file <path>      check a single Go file
  check_layers.py --changed          check modified/untracked Go files in git status
  check_layers.py --hook             PostToolUse hook handler (Claude Code / Codex / Antigravity)
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path

MODULE_PREFIX = "github.com/Kaikei-e/kisage/"

PURE_FORBIDDEN_STDLIB: frozenset[str] = frozenset(
    {"os", "os/exec", "syscall", "net", "net/http", "time"}
)
MCP_FORBIDDEN_STDLIB: frozenset[str] = frozenset({"os", "os/exec", "syscall"})

ALLOWED_INTERNAL_IMPORTS: dict[str, frozenset[str]] = {
    "model": frozenset(),
    "observation": frozenset({"model"}),
    "projection": frozenset({"model"}),
    "scope": frozenset({"model"}),
    "structure": frozenset({"model", "observation"}),
    "ownership": frozenset({"model", "observation", "projection"}),
    "plan": frozenset(
        {"model", "observation", "projection", "scope", "structure", "ownership"}
    ),
    "provider": frozenset({"model", "observation", "structure", "ownership", "plan"}),
    "protocol": frozenset(
        {
            "model",
            "observation",
            "projection",
            "scope",
            "structure",
            "ownership",
            "plan",
            "provider",
        }
    ),
    "verification": frozenset(
        {
            "model",
            "observation",
            "projection",
            "scope",
            "structure",
            "ownership",
            "plan",
            "provider",
            "protocol",
        }
    ),
    "cli": frozenset(
        {
            "model",
            "observation",
            "projection",
            "scope",
            "structure",
            "ownership",
            "plan",
            "provider",
            "protocol",
        }
    ),
    "mcp": frozenset({"model", "plan", "cli"}),
    "host": frozenset({"model", "scope", "ownership", "provider", "protocol", "cli"}),
    "cmd/kisage": frozenset({"cli", "host", "mcp"}),
    "examples": frozenset({"host", "provider"}),
}

PURE_PACKAGES = frozenset(
    {"model", "observation", "projection", "scope", "structure", "ownership", "plan"}
)

_IMPORT_SINGLE_RE = re.compile(
    r'\bimport\s+(?:[A-Za-z_][A-Za-z0-9_]*|\.|_)?\s*"([^"\n]+)"'
)
_IMPORT_BLOCK_START_RE = re.compile(r"\bimport\s*\(")
_IMPORT_SPEC_RE = re.compile(r'(?:[A-Za-z_][A-Za-z0-9_]*|\.|_)?\s*"([^"\n]+)"')
_PATCH_PREFIXES: tuple[str, ...] = (
    "*** Add File: ",
    "*** Update File: ",
    "*** Move to: ",
)


@dataclass(frozen=True)
class Violation:
    rule: str
    rel_path: str
    line: int
    message: str
    fix: str


def _resolve_repo_root(start: Path) -> Path:
    res = subprocess.run(
        ["git", "-C", str(start.resolve()), "rev-parse", "--show-toplevel"],
        capture_output=True,
        text=True,
        check=False,
    )
    if res.returncode == 0 and res.stdout.strip():
        return Path(res.stdout.strip()).resolve()
    return Path(__file__).resolve().parents[4]


def _mask_go_comments_preserve_lines(text: str) -> str:
    """Replace Go // and /* */ comments with spaces while keeping line numbers intact."""
    out: list[str] = []
    i = 0
    n = len(text)
    in_double = False
    in_raw = False
    in_rune = False
    in_line_comment = False
    in_block_comment = False

    while i < n:
        ch = text[i]
        nxt = text[i + 1] if i + 1 < n else ""

        if in_line_comment:
            if ch == "\n":
                in_line_comment = False
                out.append("\n")
            else:
                out.append(" ")
            i += 1
            continue

        if in_block_comment:
            if ch == "*" and nxt == "/":
                in_block_comment = False
                out.append("  ")
                i += 2
            else:
                out.append("\n" if ch == "\n" else " ")
                i += 1
            continue

        if in_double:
            out.append(ch)
            if ch == "\\" and i + 1 < n:
                out.append(nxt)
                i += 2
                continue
            if ch == '"':
                in_double = False
            i += 1
            continue

        if in_raw:
            out.append(ch)
            if ch == "`":
                in_raw = False
            i += 1
            continue

        if in_rune:
            out.append(ch)
            if ch == "\\" and i + 1 < n:
                out.append(nxt)
                i += 2
                continue
            if ch == "'":
                in_rune = False
            i += 1
            continue

        if ch == "/" and nxt == "/":
            in_line_comment = True
            out.append("  ")
            i += 2
            continue
        if ch == "/" and nxt == "*":
            in_block_comment = True
            out.append("  ")
            i += 2
            continue
        if ch == '"':
            in_double = True
        elif ch == "`":
            in_raw = True
        elif ch == "'":
            in_rune = True

        out.append(ch)
        i += 1

    return "".join(out)


def extract_go_imports(text: str) -> list[tuple[int, str]]:
    """Return sorted (1-based line_no, import_path) pairs from Go source text."""
    cleaned = _mask_go_comments_preserve_lines(text)
    lines = cleaned.splitlines()
    results: list[tuple[int, str]] = []
    in_import_block = False

    for line_no, line in enumerate(lines, start=1):
        stripped = line.strip()
        if not stripped:
            continue

        if in_import_block:
            if ")" in stripped:
                before_close = stripped.split(")", 1)[0]
                for m in _IMPORT_SPEC_RE.finditer(before_close):
                    results.append((line_no, m.group(1)))
                in_import_block = False
                continue
            for m in _IMPORT_SPEC_RE.finditer(stripped):
                results.append((line_no, m.group(1)))
            continue

        m_block = _IMPORT_BLOCK_START_RE.search(stripped)
        if m_block is not None:
            after_open = stripped[m_block.end() :]
            if ")" in after_open:
                inside = after_open.split(")", 1)[0]
                for m in _IMPORT_SPEC_RE.finditer(inside):
                    results.append((line_no, m.group(1)))
            else:
                for m in _IMPORT_SPEC_RE.finditer(after_open):
                    results.append((line_no, m.group(1)))
                in_import_block = True
            continue

        for m_single in _IMPORT_SINGLE_RE.finditer(stripped):
            results.append((line_no, m_single.group(1)))

    return results


def package_scope_for_relpath(rel_path: Path) -> str | None:
    parts = rel_path.parts
    if len(parts) < 2:
        return None
    if parts[0] == "cmd" and len(parts) >= 3 and parts[1] == "kisage":
        return "cmd/kisage"
    if parts[0] in ALLOWED_INTERNAL_IMPORTS:
        return parts[0]
    return None


def _internal_target_name(imp_path: str) -> str:
    rel = imp_path.removeprefix(MODULE_PREFIX)
    if rel == "cmd/kisage" or rel.startswith("cmd/kisage/"):
        return "cmd/kisage"
    return rel.split("/", 1)[0]


def check_go_file(repo_root: Path, file_path: Path) -> list[Violation]:
    """Check one Go source file against kisage's layer and purity rules."""
    resolved_root = repo_root.resolve()
    resolved_file = file_path.resolve()
    try:
        rel_path = resolved_file.relative_to(resolved_root)
    except ValueError:
        return []

    if rel_path.suffix != ".go" or rel_path.name.endswith("_test.go"):
        return []
    if any(part.startswith(".") for part in rel_path.parts):
        return []

    pkg = package_scope_for_relpath(rel_path)
    if pkg is None:
        return []

    rel_str = rel_path.as_posix()
    text = resolved_file.read_text(encoding="utf-8", errors="replace")
    imports = extract_go_imports(text)
    violations: list[Violation] = []
    allowed_internal = ALLOWED_INTERNAL_IMPORTS[pkg]

    for line_no, imp in imports:
        if imp.startswith(MODULE_PREFIX):
            target = _internal_target_name(imp)
            if target not in allowed_internal:
                violations.append(
                    Violation(
                        rule="internal-layer-boundary",
                        rel_path=rel_str,
                        line=line_no,
                        message=f"package '{pkg}' must not import '{imp}'",
                        fix=(
                            f"move cross-layer logic out of '{pkg}' or restrict imports in "
                            f"'{pkg}' to {{{', '.join(sorted(allowed_internal)) or 'none'}}}."
                        ),
                    )
                )
        elif pkg in PURE_PACKAGES and imp in PURE_FORBIDDEN_STDLIB:
            violations.append(
                Violation(
                    rule="pure-package-io-forbidden",
                    rel_path=rel_str,
                    line=line_no,
                    message=f"pure deterministic package '{pkg}' must not import '{imp}'",
                    fix=(
                        f"keep OS, filesystem, network, and wall-clock I/O in 'provider', "
                        f"'protocol', or 'host' and pass pure snapshots into '{pkg}'."
                    ),
                )
            )
        elif pkg == "mcp" and imp in MCP_FORBIDDEN_STDLIB:
            violations.append(
                Violation(
                    rule="mcp-os-boundary",
                    rel_path=rel_str,
                    line=line_no,
                    message=f"package 'mcp' must not import '{imp}' directly",
                    fix=(
                        "keep filesystem and OS process access inside 'host', 'provider', "
                        "and 'protocol'; invoke them through 'cli' dependencies."
                    ),
                )
            )

    return violations


def collect_repo_go_files(repo_root: Path, scope_dir: Path | None = None) -> list[Path]:
    target = (scope_dir or repo_root).resolve()
    files: list[Path] = []
    for p in sorted(target.rglob("*.go")):
        try:
            rel = p.relative_to(repo_root.resolve())
        except ValueError:
            continue
        if any(part.startswith(".") for part in rel.parts):
            continue
        if p.name.endswith("_test.go"):
            continue
        files.append(p)
    return files


def collect_changed_go_files(repo_root: Path) -> list[Path]:
    res = subprocess.run(
        [
            "git",
            "-C",
            str(repo_root),
            "-c",
            "core.quotePath=false",
            "status",
            "--porcelain",
            "-uall",
        ],
        capture_output=True,
        text=True,
        check=False,
    )
    if res.returncode != 0:
        raise RuntimeError(res.stderr.strip() or "git status failed")

    files: list[Path] = []
    for line in res.stdout.splitlines():
        if len(line) < 4:
            continue
        xy = line[:2]
        if "D" in xy and "R" not in xy:
            continue
        raw_path = line[3:]
        if "R" in xy or "C" in xy:
            raw_path = raw_path.split(" -> ", 1)[-1]
        if len(raw_path) >= 2 and raw_path[0] == '"' and raw_path[-1] == '"':
            raw_path = raw_path[1:-1]
        candidate = (repo_root / raw_path).resolve()
        if candidate.is_file() and candidate.suffix == ".go" and not candidate.name.endswith("_test.go"):
            files.append(candidate)
    return files


def _extract_hook_paths_and_dialect(raw_stdin: str) -> tuple[list[str], bool]:
    try:
        payload = json.loads(raw_stdin)
    except json.JSONDecodeError:
        return [], False
    if not isinstance(payload, dict):
        return [], False

    is_ag = "toolCall" in payload or "hookEventName" in payload
    if is_ag:
        tool_call = payload.get("toolCall")
        if not isinstance(tool_call, dict):
            return [], True
        if tool_call.get("name") not in ("write_file", "edit_file"):
            return [], True
        args = tool_call.get("args")
        if not isinstance(args, dict):
            return [], True
        raw_path = args.get("Path")
        if isinstance(raw_path, str) and raw_path.strip():
            return [raw_path.strip()], True
        return [], True

    tool_input = payload.get("tool_input")
    if not isinstance(tool_input, dict):
        return [], False

    paths: list[str] = []
    for key in ("file_path", "notebook_path"):
        val = tool_input.get(key)
        if isinstance(val, str) and val.strip():
            paths.append(val.strip())
    if payload.get("tool_name") == "apply_patch":
        cmd = tool_input.get("command")
        text = cmd if isinstance(cmd, str) else (
            "\n".join(p for p in cmd if isinstance(p, str)) if isinstance(cmd, list) else ""
        )
        for line in text.splitlines():
            for prefix in _PATCH_PREFIXES:
                if line.startswith(prefix):
                    cand = line[len(prefix) :].strip()
                    if cand:
                        paths.append(cand)
                    break
    return paths, False


def format_violations(violations: list[Violation]) -> list[str]:
    lines: list[str] = []
    fixes_seen: set[str] = set()
    for v in violations:
        lines.append(f"VIOLATION {v.rule} {v.rel_path}:{v.line}: {v.message}")
        fix_line = f"FIX {v.rule}: {v.fix}"
        if fix_line not in fixes_seen:
            fixes_seen.add(fix_line)
            lines.append(fix_line)
    return lines


def run_hook_mode(repo_root: Path) -> int:
    raw = sys.stdin.read()
    paths, is_ag = _extract_hook_paths_and_dialect(raw)
    base_cwd = Path.cwd()
    try:
        payload = json.loads(raw) if raw.strip() else {}
        if isinstance(payload, dict) and isinstance(payload.get("cwd"), str) and payload["cwd"]:
            base_cwd = Path(payload["cwd"])
    except json.JSONDecodeError:
        pass

    violations: list[Violation] = []
    for raw_path in paths:
        p = Path(raw_path)
        candidate = p if p.is_absolute() else (base_cwd / p)
        if not candidate.is_file():
            continue
        file_root = _resolve_repo_root(candidate.parent)
        violations.extend(check_go_file(file_root, candidate))

    if violations:
        for line in format_violations(violations):
            print(line, file=sys.stderr)
        print(
            "See .claude/skills/layer-architecture/SKILL.md for allowed dependencies.",
            file=sys.stderr,
        )
        return 2

    if is_ag:
        print("{}")
    return 0


def print_usage() -> int:
    print(
        "Usage: check_layers.py [<dir> | --file <path> | --changed | --hook]",
        file=sys.stderr,
    )
    return 2


def main(argv: list[str] | None = None) -> int:
    args = sys.argv[1:] if argv is None else argv
    repo_root = _resolve_repo_root(Path.cwd())

    if not args:
        files = collect_repo_go_files(repo_root)
    elif args[0] == "--hook":
        if len(args) != 1:
            return print_usage()
        return run_hook_mode(repo_root)
    elif args[0] == "--changed":
        if len(args) != 1:
            return print_usage()
        files = collect_changed_go_files(repo_root)
    elif args[0] == "--file":
        if len(args) != 2:
            return print_usage()
        target = Path(args[1])
        if not target.is_absolute():
            target = (Path.cwd() / target).resolve()
        if not target.is_file():
            print(f"Error: file not found: {args[1]}", file=sys.stderr)
            return 2
        repo_root = _resolve_repo_root(target.parent)
        files = [target]
    elif len(args) == 1 and not args[0].startswith("-"):
        target_dir = Path(args[0])
        if not target_dir.is_absolute():
            target_dir = (Path.cwd() / target_dir).resolve()
        if not target_dir.is_dir():
            print(f"Error: directory not found: {args[0]}", file=sys.stderr)
            return 2
        repo_root = _resolve_repo_root(target_dir)
        files = collect_repo_go_files(repo_root, target_dir)
    else:
        return print_usage()

    violations: list[Violation] = []
    for f in files:
        violations.extend(check_go_file(repo_root, f))

    if violations:
        for line in format_violations(violations):
            print(line)
        return 1

    print(f"OK: checked {len(files)} Go source file(s), 0 layer violations")
    return 0


if __name__ == "__main__":
    sys.exit(main())
