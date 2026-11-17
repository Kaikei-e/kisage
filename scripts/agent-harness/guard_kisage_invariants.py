#!/usr/bin/env python3
"""PreToolUse guard protecting pure Go determinism/layering and Lean 4 zero-sorry invariants."""

from __future__ import annotations

import posixpath
import re
import sys
from pathlib import Path, PurePosixPath

_SELF_DIR = str(Path(__file__).resolve().parent)
if _SELF_DIR not in sys.path:
    sys.path.insert(0, _SELF_DIR)

from hook_payload import (  # noqa: E402
    emit_edit_allow_and_exit,
    emit_edit_block_and_exit,
    read_edit_hook_stdin_or_exit,
)

GUARDED_PURE_GO_GLOBS: list[str] = [
    "model/*.go",
    "observation/*.go",
    "projection/*.go",
    "scope/*.go",
    "structure/*.go",
    "ownership/*.go",
    "plan/*.go",
]
GUARDED_LEAN_GLOBS: list[str] = [
    "Kisage/*.lean",
    "Kisage.lean",
]
GUARDED_GLOBS: list[str] = [*GUARDED_PURE_GO_GLOBS, *GUARDED_LEAN_GLOBS]

WALL_CLOCK_RE = re.compile(r"\btime\.(?:Now|Since|Until)\s*\(")

FORBIDDEN_PURE_GO_IMPORTS: tuple[str, ...] = (
    "os",
    "os/exec",
    "syscall",
    "net",
    "net/http",
    "github.com/Kaikei-e/kisage/provider",
    "github.com/Kaikei-e/kisage/protocol",
    "github.com/Kaikei-e/kisage/host",
    "github.com/Kaikei-e/kisage/cli",
    "github.com/Kaikei-e/kisage/mcp",
    "github.com/Kaikei-e/kisage/verification",
    "github.com/Kaikei-e/kisage/cmd/kisage",
)
_IMPORT_LINE_RE = re.compile(
    r'^\s*(?:import\s+)?(?:[A-Za-z_][A-Za-z0-9_]*|\.|_)?\s*"([^"\n]+)"\s*(?:;.*)?$'
)
_INLINE_IMPORT_RE = re.compile(
    r'\bimport\s+(?:[A-Za-z_][A-Za-z0-9_]*|\.|_)?\s*"([^"\n]+)"'
)

FORBIDDEN_LEAN_IDENTIFIERS: tuple[str, ...] = (
    "sorry",
    "sorryAx",
    "admit",
    "axiom",
    "unsafe",
    "implemented_by",
    "extern",
)
_LEAN_IDENT_PATTERNS: tuple[tuple[str, re.Pattern[str]], ...] = tuple(
    (ident, re.compile(rf"(?<![A-Za-z0-9_']){re.escape(ident)}(?![A-Za-z0-9_'])"))
    for ident in FORBIDDEN_LEAN_IDENTIFIERS
)


def _glob_to_regex(pattern: str) -> re.Pattern[str]:
    pat = pattern.strip().lstrip("./")
    out: list[str] = ["^"]
    i = 0
    n = len(pat)
    while i < n:
        if pat[i : i + 3] == "**/":
            out.append("(?:.+/)?")
            i += 3
        elif pat[i : i + 3] == "/**" and i + 3 == n:
            out.append("/.+")
            i += 3
        elif pat[i : i + 2] == "**":
            out.append(".*")
            i += 2
        elif pat[i] == "*":
            out.append("[^/]*")
            i += 1
        elif pat[i] == "?":
            out.append("[^/]")
            i += 1
        else:
            out.append(re.escape(pat[i]))
            i += 1
    out.append("$")
    return re.compile("".join(out))


_PURE_GO_REGEXES: tuple[re.Pattern[str], ...] = tuple(
    _glob_to_regex(pat) for pat in GUARDED_PURE_GO_GLOBS
)
_LEAN_REGEXES: tuple[re.Pattern[str], ...] = tuple(
    _glob_to_regex(pat) for pat in GUARDED_LEAN_GLOBS
)


def _matches_any_suffix(raw_path: str, regexes: tuple[re.Pattern[str], ...]) -> bool:
    norm = posixpath.normpath(raw_path.replace("\\", "/")).lstrip("./")
    if not norm:
        return False
    parts = [part for part in PurePosixPath(norm).parts if part not in ("/", "")]
    for idx in range(len(parts)):
        suffix = "/".join(parts[idx:])
        if any(rx.match(suffix) for rx in regexes):
            return True
    return False


def is_guarded_pure_go_path(raw_path: str) -> bool:
    """Return True for non-test Go files inside kisage's pure planning/model packages."""
    norm = posixpath.normpath(raw_path.replace("\\", "/")).lstrip("./")
    if not norm or norm.endswith("_test.go"):
        return False
    return _matches_any_suffix(norm, _PURE_GO_REGEXES)


def is_guarded_lean_path(raw_path: str) -> bool:
    """Return True for Lean 4 model/proof files under Kisage/ or Kisage.lean."""
    return _matches_any_suffix(raw_path, _LEAN_REGEXES)


def strip_go_comments(text: str, *, mask_strings: bool = False) -> str:
    """Strip Go // and /* */ comments while respecting string and rune literals."""
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
            i += 1
            continue

        if in_block_comment:
            if ch == "*" and nxt == "/":
                in_block_comment = False
                out.append(" ")
                i += 2
            else:
                out.append("\n" if ch == "\n" else " ")
                i += 1
            continue

        if in_double:
            if ch == "\\" and i + 1 < n:
                out.append(" " if mask_strings else ch + nxt)
                i += 2
                continue
            if ch == '"':
                in_double = False
                out.append('"')
                i += 1
                continue
            out.append(("\n" if ch == "\n" else " ") if mask_strings else ch)
            i += 1
            continue

        if in_raw:
            if ch == "`":
                in_raw = False
                out.append("`")
                i += 1
                continue
            out.append(("\n" if ch == "\n" else " ") if mask_strings else ch)
            i += 1
            continue

        if in_rune:
            if ch == "\\" and i + 1 < n:
                out.append(" " if mask_strings else ch + nxt)
                i += 2
                continue
            if ch == "'":
                in_rune = False
                out.append("'")
                i += 1
                continue
            out.append(" " if mask_strings else ch)
            i += 1
            continue

        if ch == "/" and nxt == "/":
            in_line_comment = True
            i += 2
            continue
        if ch == "/" and nxt == "*":
            in_block_comment = True
            out.append(" ")
            i += 2
            continue
        if ch == '"':
            in_double = True
            out.append('"')
            i += 1
            continue
        if ch == "`":
            in_raw = True
            out.append("`")
            i += 1
            continue
        if ch == "'":
            in_rune = True
            out.append("'")
            i += 1
            continue

        out.append(ch)
        i += 1

    return "".join(out)


def find_pure_go_violation(added_text: str) -> str | None:
    """Return a violation token if added_text introduces wall-clock calls or forbidden imports."""
    code_with_strings = strip_go_comments(added_text, mask_strings=False)
    forbidden_set = set(FORBIDDEN_PURE_GO_IMPORTS)

    for line in code_with_strings.splitlines():
        m_line = _IMPORT_LINE_RE.match(line)
        if m_line is not None and m_line.group(1) in forbidden_set:
            return f'import "{m_line.group(1)}"'
        for m_inline in _INLINE_IMPORT_RE.finditer(line):
            if m_inline.group(1) in forbidden_set:
                return f'import "{m_inline.group(1)}"'

    code_masked = strip_go_comments(added_text, mask_strings=True)
    m_clock = WALL_CLOCK_RE.search(code_masked)
    if m_clock is not None:
        return m_clock.group(0).rstrip()

    return None


def strip_lean_comments_and_strings(text: str) -> str:
    """Strip Lean -- line comments, nested /- ... -/ block comments, and string literals."""
    out: list[str] = []
    i = 0
    n = len(text)
    depth = 0
    in_line_comment = False
    in_string = False

    while i < n:
        ch = text[i]
        nxt = text[i + 1] if i + 1 < n else ""

        if in_line_comment:
            if ch == "\n":
                in_line_comment = False
                out.append("\n")
            i += 1
            continue

        if in_string:
            if ch == "\\" and i + 1 < n:
                out.append(" ")
                i += 2
                continue
            if ch == '"':
                in_string = False
                out.append('"')
                i += 1
                continue
            out.append("\n" if ch == "\n" else " ")
            i += 1
            continue

        if depth > 0:
            if ch == "/" and nxt == "-":
                depth += 1
                out.append(" ")
                i += 2
                continue
            if ch == "-" and nxt == "/":
                depth -= 1
                out.append(" ")
                i += 2
                continue
            out.append("\n" if ch == "\n" else " ")
            i += 1
            continue

        if ch == "/" and nxt == "-":
            depth = 1
            out.append(" ")
            i += 2
            continue
        if ch == "-" and nxt == "-":
            in_line_comment = True
            i += 2
            continue
        if ch == '"':
            in_string = True
            out.append('"')
            i += 1
            continue

        out.append(ch)
        i += 1

    return "".join(out)


def find_lean_violation(added_text: str) -> str | None:
    """Return the offending Lean identifier if added_text introduces sorry/axiom/unsafe/etc."""
    code = strip_lean_comments_and_strings(added_text)
    for ident, rx in _LEAN_IDENT_PATTERNS:
        if rx.search(code) is not None:
            return ident
    return None


def main() -> int:
    payload, dialect = read_edit_hook_stdin_or_exit()

    violations: list[str] = []
    for path in payload.paths:
        added = payload.added_text.get(path, "")
        if is_guarded_pure_go_path(path):
            token = find_pure_go_violation(added)
            if token is not None:
                violations.append(
                    f"Blocked edit to {path}: found '{token}' in pure deterministic package. "
                    "Pure Go packages (model, observation, projection, scope, structure, ownership, plan) "
                    "must remain deterministic without wall-clock, OS/network I/O, or upward layer imports "
                    "(see docs/PRINCIPLE.md Principles 1 & 7 and ADR-0001..ADR-0003)."
                )
        elif is_guarded_lean_path(path):
            token = find_lean_violation(added)
            if token is not None:
                violations.append(
                    f"Blocked edit to {path}: found forbidden Lean identifier '{token}'. "
                    "All Lean 4 proofs in Kisage/ must verify with zero sorry/sorryAx/axiom/unsafe "
                    "(see ADR-0005 §3 and verification/lean_drt_test.go)."
                )

    if violations:
        emit_edit_block_and_exit(dialect, violations)
    emit_edit_allow_and_exit(dialect)


if __name__ == "__main__":
    sys.exit(main())
