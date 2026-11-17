#!/usr/bin/env python3
"""Shared PreToolUse stdin JSON parser and response emitter for Claude Code, Codex, and Antigravity."""

from __future__ import annotations

import json
import sys
from dataclasses import dataclass
from typing import Any, NoReturn

CLAUDE_CODEX_EDIT_TOOLS = frozenset({"Edit", "Write", "NotebookEdit", "apply_patch"})
ANTIGRAVITY_EDIT_TOOLS = frozenset({"write_file", "edit_file"})

CLAUDE_CODEX_SHELL_TOOLS = frozenset({"Bash", "shell"})
ANTIGRAVITY_SHELL_TOOLS = frozenset({"run_command"})

PATCH_OPEN_PREFIXES: tuple[str, ...] = (
    "*** Add File: ",
    "*** Update File: ",
    "*** Delete File: ",
)
PATCH_MOVE_PREFIX = "*** Move to: "


class HookPayloadError(ValueError):
    """Raised when stdin is not a valid PreToolUse payload."""

    def __init__(self, message: str, *, dialect: str = "claude_codex") -> None:
        super().__init__(message)
        self.dialect = dialect


@dataclass(frozen=True)
class HookPayload:
    dialect: str
    tool_name: str
    paths: tuple[str, ...]
    added_text: dict[str, str]


@dataclass(frozen=True)
class ShellHookPayload:
    dialect: str
    tool_name: str
    command: str | list[str]


def _append_unique(ordered: list[str], seen: set[str], path: str) -> None:
    if path not in seen:
        seen.add(path)
        ordered.append(path)


def extract_patch_details(command_text: str) -> tuple[list[str], dict[str, str]]:
    """Extract target file paths and added lines per path from an apply_patch body."""
    ordered: list[str] = []
    seen: set[str] = set()
    lines_by_path: dict[str, list[str]] = {}
    current_paths: list[str] = []

    for line in command_text.splitlines():
        matched_open = False
        for prefix in PATCH_OPEN_PREFIXES:
            if line.startswith(prefix):
                candidate = line[len(prefix) :].strip()
                if candidate:
                    _append_unique(ordered, seen, candidate)
                    lines_by_path.setdefault(candidate, [])
                    current_paths = [candidate]
                else:
                    current_paths = []
                matched_open = True
                break
        if matched_open:
            continue

        if line.startswith(PATCH_MOVE_PREFIX):
            candidate = line[len(PATCH_MOVE_PREFIX) :].strip()
            if candidate:
                _append_unique(ordered, seen, candidate)
                lines_by_path.setdefault(candidate, [])
                if candidate not in current_paths:
                    current_paths.append(candidate)
            continue

        if line.startswith("*** "):
            current_paths = []
            continue

        if line.startswith("+") and current_paths:
            added = line[1:]
            for target in current_paths:
                lines_by_path[target].append(added)

    return ordered, {p: "\n".join(lines) for p, lines in lines_by_path.items()}


def _extract_added_text_for_claude(tool_name: str, tool_input: dict[str, Any]) -> str:
    if tool_name == "Write":
        val = tool_input.get("content")
        return val if isinstance(val, str) else ""
    if tool_name == "Edit":
        val = tool_input.get("new_string")
        return val if isinstance(val, str) else ""
    if tool_name == "NotebookEdit":
        for key in ("new_source", "content"):
            val = tool_input.get(key)
            if isinstance(val, str):
                return val
    return ""


def _extract_added_text_for_antigravity(tool_name: str, args: dict[str, Any]) -> str:
    if tool_name == "write_file":
        val = args.get("Content")
        return val if isinstance(val, str) else ""
    if tool_name == "edit_file":
        val = args.get("Replacement")
        return val if isinstance(val, str) else ""
    return ""


def is_antigravity_payload(payload: dict[str, Any]) -> bool:
    """Return True when a parsed JSON hook object uses Antigravity's camelCase schema."""
    return "toolCall" in payload or "hookEventName" in payload


def _decode_payload_object(raw: str) -> dict[str, Any]:
    try:
        payload = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise HookPayloadError(
            f"guard could not read the hook payload: invalid JSON ({exc.msg})"
        ) from exc

    if not isinstance(payload, dict):
        raise HookPayloadError(
            "guard could not read the hook payload: expected a JSON object"
        )
    return payload


def parse_edit_hook_stdin(raw: str) -> tuple[HookPayload | None, str]:
    """Parse PreToolUse JSON for edit tools across Claude Code, Codex, and Antigravity.

    Returns (None, dialect) when the tool is outside the watched edit tools.
    Raises HookPayloadError when stdin is malformed or a watched tool yields no path.
    """
    payload = _decode_payload_object(raw)

    if is_antigravity_payload(payload):
        dialect = "antigravity"
        tool_call = payload.get("toolCall")
        if not isinstance(tool_call, dict):
            raise HookPayloadError(
                "guard could not read the hook payload: missing toolCall",
                dialect=dialect,
            )
        tool_name = tool_call.get("name")
        if not isinstance(tool_name, str) or not tool_name.strip():
            raise HookPayloadError(
                "guard could not read the hook payload: missing toolCall.name",
                dialect=dialect,
            )
        if tool_name not in ANTIGRAVITY_EDIT_TOOLS:
            return None, dialect

        args = tool_call.get("args")
        if not isinstance(args, dict):
            raise HookPayloadError(
                f"guard could not read the hook payload: missing toolCall.args for {tool_name}",
                dialect=dialect,
            )
        raw_path = args.get("Path")
        if not isinstance(raw_path, str) or not raw_path.strip():
            raise HookPayloadError(
                f"guard could not read the hook payload: {tool_name} yielded no target path",
                dialect=dialect,
            )
        cleaned = raw_path.strip()
        added = _extract_added_text_for_antigravity(tool_name, args)
        return (
            HookPayload(
                dialect=dialect,
                tool_name=tool_name,
                paths=(cleaned,),
                added_text={cleaned: added},
            ),
            dialect,
        )

    dialect = "claude_codex"
    tool_name = payload.get("tool_name")
    if not isinstance(tool_name, str) or not tool_name.strip():
        raise HookPayloadError(
            "guard could not read the hook payload: missing tool_name",
            dialect=dialect,
        )
    if tool_name not in CLAUDE_CODEX_EDIT_TOOLS:
        return None, dialect

    tool_input = payload.get("tool_input")
    if not isinstance(tool_input, dict):
        raise HookPayloadError(
            f"guard could not read the hook payload: missing tool_input for {tool_name}",
            dialect=dialect,
        )

    ordered: list[str] = []
    seen: set[str] = set()
    added_text: dict[str, str] = {}

    claude_added = _extract_added_text_for_claude(tool_name, tool_input)
    for field in ("file_path", "notebook_path"):
        value = tool_input.get(field)
        if isinstance(value, str) and value.strip():
            cleaned = value.strip()
            _append_unique(ordered, seen, cleaned)
            added_text[cleaned] = claude_added

    if tool_name == "apply_patch":
        cmd = tool_input.get("command")
        if isinstance(cmd, str):
            patch_text = cmd
        elif isinstance(cmd, list):
            patch_text = "\n".join(part for part in cmd if isinstance(part, str))
        else:
            patch_text = ""

        patch_paths, patch_added = extract_patch_details(patch_text)
        for path in patch_paths:
            _append_unique(ordered, seen, path)
            if path in added_text:
                combined = "\n".join(
                    part for part in (added_text[path], patch_added.get(path, "")) if part
                )
                added_text[path] = combined
            else:
                added_text[path] = patch_added.get(path, "")

    if not ordered:
        raise HookPayloadError(
            f"guard could not read the hook payload: {tool_name} yielded no target path",
            dialect=dialect,
        )

    return (
        HookPayload(
            dialect=dialect,
            tool_name=tool_name,
            paths=tuple(ordered),
            added_text=added_text,
        ),
        dialect,
    )


def parse_hook_stdin(raw: str) -> HookPayload | None:
    """Backward-compatible wrapper returning HookPayload | None (raising HookPayloadError on error)."""
    payload, _ = parse_edit_hook_stdin(raw)
    return payload


def read_edit_hook_stdin_or_exit() -> tuple[HookPayload, str]:
    """Read stdin for an edit guard, exiting immediately when unwatched or fail-closed."""
    raw = sys.stdin.read()
    try:
        payload, dialect = parse_edit_hook_stdin(raw)
    except HookPayloadError as exc:
        emit_edit_block_and_exit(exc.dialect, [str(exc)])

    if payload is None:
        emit_edit_allow_and_exit(dialect)
    return payload, dialect


def emit_edit_allow_and_exit(dialect: str) -> NoReturn:
    """Allow the edit according to the caller hook dialect and exit 0."""
    if dialect == "antigravity":
        print(json.dumps({"decision": "allow"}))
    sys.exit(0)


def emit_edit_block_and_exit(dialect: str, reasons: list[str]) -> NoReturn:
    """Block the edit according to the caller hook dialect and exit."""
    message = "\n".join(reasons)
    if dialect == "antigravity":
        print(json.dumps({"decision": "deny", "reason": message}))
        sys.exit(0)
    print(message, file=sys.stderr)
    sys.exit(2)


def parse_shell_hook_stdin(raw: str) -> tuple[ShellHookPayload | None, str]:
    """Parse PreToolUse JSON for shell tools across Claude Code, Codex, and Antigravity."""
    payload = _decode_payload_object(raw)

    if is_antigravity_payload(payload):
        dialect = "antigravity"
        tool_call = payload.get("toolCall")
        if not isinstance(tool_call, dict):
            raise HookPayloadError("missing toolCall", dialect=dialect)
        tool_name = tool_call.get("name")
        if not isinstance(tool_name, str) or not tool_name.strip():
            raise HookPayloadError("missing toolCall.name", dialect=dialect)
        if tool_name not in ANTIGRAVITY_SHELL_TOOLS:
            return None, dialect
        args = tool_call.get("args")
        if not isinstance(args, dict):
            raise HookPayloadError(f"missing toolCall.args for {tool_name}", dialect=dialect)
        cmd = args.get("CommandLine")
        if isinstance(cmd, str):
            return ShellHookPayload(dialect=dialect, tool_name=tool_name, command=cmd), dialect
        raise HookPayloadError(f"missing or invalid CommandLine for {tool_name}", dialect=dialect)

    dialect = "claude_codex"
    tool_name = payload.get("tool_name")
    if not isinstance(tool_name, str) or not tool_name.strip():
        raise HookPayloadError("missing tool_name", dialect=dialect)
    if tool_name not in CLAUDE_CODEX_SHELL_TOOLS:
        return None, dialect

    tool_input = payload.get("tool_input")
    if not isinstance(tool_input, dict):
        raise HookPayloadError(f"missing tool_input for {tool_name}", dialect=dialect)

    cmd = tool_input.get("command")
    if isinstance(cmd, str):
        return ShellHookPayload(dialect=dialect, tool_name=tool_name, command=cmd), dialect
    if isinstance(cmd, list) and all(isinstance(part, str) for part in cmd):
        return ShellHookPayload(dialect=dialect, tool_name=tool_name, command=cmd), dialect
    raise HookPayloadError(f"missing or invalid command for {tool_name}", dialect=dialect)
