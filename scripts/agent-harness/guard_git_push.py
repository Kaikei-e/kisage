#!/usr/bin/env python3
"""PreToolUse guard blocking git/gh push, release, PR-creation, and remote-mutation commands."""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Any

_SELF_DIR = str(Path(__file__).resolve().parent)
if _SELF_DIR not in sys.path:
    sys.path.insert(0, _SELF_DIR)

from hook_payload import HookPayloadError, parse_shell_hook_stdin  # noqa: E402

SHELL_NAMES = frozenset({"sh", "bash", "zsh", "dash"})
ENV_ASSIGN_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*=")

GIT_VALUE_FLAGS = frozenset(
    {
        "-C",
        "-c",
        "--git-dir",
        "--work-tree",
        "--namespace",
        "--exec-path",
        "--super-prefix",
        "--config-env",
        "--list-cmds",
    }
)
GH_MUTATING_ACTIONS: dict[str, frozenset[str]] = {
    "pr": frozenset({"create", "merge"}),
    "release": frozenset({"create", "upload", "delete", "edit"}),
    "repo": frozenset({"sync"}),
    "workflow": frozenset({"run"}),
    "run": frozenset({"rerun"}),
}
GIT_REMOTE_MUTATING_ACTIONS = frozenset({"add", "set-url", "update"})

RULE_REASONS: dict[str, str] = {
    "G1": (
        "Rule G1: git push and remote-mutating git commands are blocked by repository policy "
        "unless run explicitly by the user."
    ),
    "G2": (
        "Rule G2: GitHub CLI release/PR/workflow-mutating commands (gh pr create|merge, "
        "gh release create|upload|delete|edit, gh repo sync, gh workflow run, gh run rerun) "
        "are blocked by repository policy unless run explicitly by the user."
    ),
}

UNREADABLE_REASON = (
    "Hook payload or command was unreadable and could not be analysed, so execution is blocked."
)

DENY_GUIDANCE_SENTENCE = (
    "Do not retry or rephrase the command to bypass the guard; ask the user to run the push "
    "or release operation manually, or quote literal text if searching."
)

RULE_PRIORITY: dict[str, int] = {"G1": 2, "G2": 1}
MAX_RECURSION_DEPTH = 32


@dataclass(frozen=True)
class Token:
    text: str
    quoted: bool


def _emit_decision(dialect: str, permission_decision: str, reason: str) -> None:
    if dialect == "antigravity":
        payload: dict[str, Any] = {
            "decision": permission_decision,
            "reason": reason,
        }
    else:
        payload = {
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": permission_decision,
                "permissionDecisionReason": reason,
            }
        }
    print(json.dumps(payload))


def _pick_higher_rule(current: str | None, candidate: str | None) -> str | None:
    if candidate is None:
        return current
    if current is None or RULE_PRIORITY[candidate] > RULE_PRIORITY[current]:
        return candidate
    return current


def _split_shell_segments(command: str) -> list[list[Token]]:
    """Tokenize a shell command and split it on ; && || | & ( ) and newlines."""
    segments: list[list[Token]] = []
    current_seg: list[Token] = []
    buf: list[str] = []
    has_token = False
    has_quote = False
    state = "NORMAL"
    i = 0
    n = len(command)

    def flush_token() -> None:
        nonlocal has_token, has_quote
        if has_token:
            current_seg.append(Token("".join(buf), has_quote))
            buf.clear()
            has_token = False
            has_quote = False

    def flush_segment() -> None:
        flush_token()
        if current_seg:
            segments.append(list(current_seg))
            current_seg.clear()

    while i < n:
        ch = command[i]
        if state == "SINGLE":
            if ch == "'":
                state = "NORMAL"
            else:
                buf.append(ch)
            i += 1
            continue

        if state == "DOUBLE":
            if ch == "\\":
                if i + 1 < n and command[i + 1] == "\n":
                    i += 2
                    continue
                if i + 1 < n:
                    buf.append(command[i + 1])
                    i += 2
                    continue
            elif ch == '"':
                state = "NORMAL"
                i += 1
                continue
            buf.append(ch)
            i += 1
            continue

        if ch == "'":
            state = "SINGLE"
            has_token = True
            has_quote = True
            i += 1
        elif ch == '"':
            state = "DOUBLE"
            has_token = True
            has_quote = True
            i += 1
        elif ch == "\\":
            if i + 1 < n and command[i + 1] == "\n":
                i += 2
            elif i + 1 < n:
                buf.append(command[i + 1])
                has_token = True
                i += 2
            else:
                i += 1
        elif ch == "#" and not has_token:
            while i < n and command[i] != "\n":
                i += 1
        elif ch in ("\n", ";", "(", ")"):
            flush_segment()
            i += 1
        elif ch in ("&", "|"):
            flush_segment()
            if i + 1 < n and command[i + 1] == ch:
                i += 2
            else:
                i += 1
        elif ch.isspace():
            flush_token()
            i += 1
        else:
            buf.append(ch)
            has_token = True
            i += 1

    flush_segment()
    return segments


def _extract_substitutions(command: str) -> list[str]:
    """Extract inner command strings from $(...) and `...` outside single quotes."""
    subs: list[str] = []
    i = 0
    n = len(command)
    in_double = False

    while i < n:
        ch = command[i]
        if ch == "\\" and i + 1 < n:
            i += 2
            continue
        if not in_double and ch == "'":
            i += 1
            while i < n and command[i] != "'":
                i += 1
            i += 1
            continue
        if ch == '"':
            in_double = not in_double
            i += 1
            continue
        if ch == "$" and i + 1 < n and command[i + 1] == "(":
            start = i + 2
            j = start
            depth = 1
            sub_state = "NORMAL"
            while j < n and depth > 0:
                c = command[j]
                if sub_state == "SINGLE":
                    if c == "'":
                        sub_state = "NORMAL"
                    j += 1
                    continue
                if sub_state == "DOUBLE":
                    if c == "\\" and j + 1 < n:
                        j += 2
                        continue
                    if c == '"':
                        sub_state = "NORMAL"
                    j += 1
                    continue
                if c == "\\" and j + 1 < n:
                    j += 2
                    continue
                if c == "'":
                    sub_state = "SINGLE"
                elif c == '"':
                    sub_state = "DOUBLE"
                elif c == "(":
                    depth += 1
                elif c == ")":
                    depth -= 1
                    if depth == 0:
                        break
                j += 1
            subs.append(command[start:j])
            i = j + 1
            continue
        if ch == "`":
            start = i + 1
            j = start
            while j < n:
                if command[j] == "\\" and j + 1 < n:
                    j += 2
                    continue
                if command[j] == "`":
                    break
                j += 1
            subs.append(command[start:j])
            i = j + 1
            continue
        i += 1

    return subs


def _classify_git_segment(tokens: list[str]) -> str | None:
    """Classify a `git ...` invocation starting at tokens[0]."""
    if PurePosixPath(tokens[0]).name != "git":
        return None

    i = 1
    n = len(tokens)
    subcmd: str | None = None
    while i < n:
        tok = tokens[i]
        if tok == "--":
            break
        if tok in GIT_VALUE_FLAGS:
            i += 2
            continue
        if tok.startswith("-"):
            i += 1
            continue
        subcmd = tok
        i += 1
        break

    if subcmd is None:
        return None
    if subcmd == "push":
        return "G1"
    if subcmd == "remote":
        for tok in tokens[i:]:
            if tok == "--":
                break
            if tok.startswith("-"):
                continue
            if tok in GIT_REMOTE_MUTATING_ACTIONS:
                return "G1"
            break
    if subcmd == "fetch":
        for tok in tokens[i:]:
            if tok == "--":
                break
            if tok in ("push", "--push"):
                return "G1"
    return None


def _classify_gh_segment(tokens: list[str]) -> str | None:
    """Classify a `gh ...` invocation starting at tokens[0]."""
    if PurePosixPath(tokens[0]).name != "gh":
        return None

    non_flags: list[str] = []
    for tok in tokens[1:]:
        if tok == "--":
            break
        if tok.startswith("-"):
            continue
        non_flags.append(tok)
        if len(non_flags) >= 2:
            break

    if len(non_flags) < 2:
        return None
    group, action = non_flags[0], non_flags[1]
    if action in GH_MUTATING_ACTIONS.get(group, frozenset()):
        return "G2"
    return None


def _extract_shell_c_script(tokens: list[Token], start_idx: int) -> str | None:
    """If tokens[start_idx] is a shell with a -c / -lc flag, return the script argument."""
    i = start_idx + 1
    n = len(tokens)
    while i < n:
        tok = tokens[i].text
        if tok == "--":
            return None
        if tok.startswith("-") and not tok.startswith("--"):
            if "c" in tok[1:]:
                return tokens[i + 1].text if i + 1 < n else None
            i += 1
            continue
        if tok.startswith("--"):
            i += 1
            continue
        return None
    return None


def _classify_tokens(tokens: list[Token], *, depth: int) -> str | None:
    """Classify a segment or argv token list, scanning bare git/gh/shell invocations."""
    best_rule: str | None = None
    for idx, tok in enumerate(tokens):
        if ENV_ASSIGN_RE.match(tok.text):
            continue
        if tok.quoted:
            continue
        base = PurePosixPath(tok.text).name
        if base == "git":
            rule = _classify_git_segment([t.text for t in tokens[idx:]])
            best_rule = _pick_higher_rule(best_rule, rule)
        elif base == "gh":
            rule = _classify_gh_segment([t.text for t in tokens[idx:]])
            best_rule = _pick_higher_rule(best_rule, rule)
        elif base in SHELL_NAMES:
            script = _extract_shell_c_script(tokens, idx)
            if script is not None:
                rule = classify_command(script, depth=depth + 1)
                best_rule = _pick_higher_rule(best_rule, rule)
    return best_rule


def classify_command(command: str, *, depth: int = 0) -> str | None:
    """Return matching rule id (G1..G2) for a shell command string, or None."""
    if depth > MAX_RECURSION_DEPTH:
        raise ValueError("command nesting exceeded maximum recursion depth")
    if not command.strip():
        return None

    best_rule: str | None = None
    for seg in _split_shell_segments(command):
        rule = _classify_tokens(seg, depth=depth)
        best_rule = _pick_higher_rule(best_rule, rule)

    for sub in _extract_substitutions(command):
        rule = classify_command(sub, depth=depth + 1)
        best_rule = _pick_higher_rule(best_rule, rule)

    return best_rule


def classify_argv(argv: list[str]) -> str | None:
    """Classify an already-split argv command list without re-joining or re-splitting."""
    if not argv:
        return None
    if PurePosixPath(argv[0]).name in SHELL_NAMES:
        tokens = [Token(part, quoted=False) for part in argv]
        script = _extract_shell_c_script(tokens, 0)
        if script is not None:
            return classify_command(script, depth=1)
    tokens = [Token(part, quoted=False) for part in argv]
    return _classify_tokens(tokens, depth=0)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="PreToolUse guard blocking git/gh push and release commands."
    )
    parser.add_argument(
        "--on-match",
        choices=("ask", "deny"),
        default="deny",
        help="Permission decision emitted when a command matches or payload is unreadable.",
    )
    args = parser.parse_args(argv)
    on_match: str = args.on_match

    dialect = "claude_codex"
    try:
        raw_bytes = sys.stdin.buffer.read()
        raw = raw_bytes.decode("utf-8")
        shell_payload, dialect = parse_shell_hook_stdin(raw)
        if shell_payload is None:
            if dialect == "antigravity":
                print(json.dumps({"decision": "allow"}))
            return 0
        if isinstance(shell_payload.command, list):
            rule = classify_argv(shell_payload.command)
        else:
            rule = classify_command(shell_payload.command)
    except HookPayloadError as exc:
        _emit_decision(exc.dialect, on_match, UNREADABLE_REASON)
        return 0
    except (UnicodeDecodeError, RecursionError, Exception):
        _emit_decision(dialect, on_match, UNREADABLE_REASON)
        return 0

    if rule is not None:
        reason = RULE_REASONS[rule]
        if on_match == "deny":
            reason = f"{reason} {DENY_GUIDANCE_SENTENCE}"
        _emit_decision(dialect, on_match, reason)
    elif dialect == "antigravity":
        print(json.dumps({"decision": "allow"}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
