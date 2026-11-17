#!/usr/bin/env python3
"""PreToolUse guard blocking edits to protected env, key, secret, .git, .lake, and .kisage paths."""

from __future__ import annotations

import os
import sys
from pathlib import Path

_SELF_DIR = str(Path(__file__).resolve().parent)
if _SELF_DIR not in sys.path:
    sys.path.insert(0, _SELF_DIR)

from hook_payload import (  # noqa: E402
    emit_edit_allow_and_exit,
    emit_edit_block_and_exit,
    read_edit_hook_stdin_or_exit,
)

ENV_ALLOWED_SUFFIXES: tuple[str, ...] = (".example", ".template", ".sample")
KEY_SUFFIXES: tuple[str, ...] = (".key", ".pem", ".crt", ".p8", ".p12")
PROTECTED_DIRS = frozenset({"secrets", ".kisage", ".git", ".lake"})


def _is_protected_env_name(name: str) -> bool:
    is_env = name == ".env" or name.startswith(".env.") or name.endswith(".env")
    return is_env and not name.endswith(ENV_ALLOWED_SUFFIXES)


def _guidance_for_dir(seg: str) -> str:
    if seg == ".git":
        return "Use standard git CLI commands instead of editing .git internals directly."
    if seg == ".lake":
        return "Lake build outputs are managed by `lake build`; edit Lean sources under Kisage/ instead."
    if seg == ".kisage":
        return "Host ownership store and runtime state under .kisage are managed by kisage / host operators."
    return "Secret files are operator-managed and must not be written directly by agents."


def check_target_path(raw_path: str) -> str | None:
    """Return a concise block message if raw_path is protected, else None."""
    norm = os.path.normpath(raw_path.replace("\\", "/"))
    p = Path(norm)
    name = p.name
    dir_parts = p.parts[:-1]

    if _is_protected_env_name(name):
        return (
            f"Blocked edit to {raw_path}: environment files matching .env are protected "
            "unless they end in .example, .template, or .sample."
        )

    if name.endswith(KEY_SUFFIXES):
        return (
            f"Blocked edit to {raw_path}: private key and certificate files "
            "(*.key, *.pem, *.crt, *.p8, *.p12) must not be modified directly."
        )

    for seg in (*dir_parts, name):
        if seg in PROTECTED_DIRS:
            return (
                f"Blocked edit to {raw_path}: paths inside '{seg}' are protected. "
                f"{_guidance_for_dir(seg)}"
            )

    return None


def main() -> int:
    payload, dialect = read_edit_hook_stdin_or_exit()

    violations: list[str] = []
    for path in payload.paths:
        msg = check_target_path(path)
        if msg is not None:
            violations.append(msg)

    if violations:
        emit_edit_block_and_exit(dialect, violations)
    emit_edit_allow_and_exit(dialect)


if __name__ == "__main__":
    sys.exit(main())
