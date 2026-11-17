#!/usr/bin/env python3
"""Pre-edit ADR graph validator and governing-decision brief for kisage agent harnesses.

Usage:
  adr_preflight.py <path>...          brief for the scopes those paths fall in
  adr_preflight.py --changed          same, for files in `git status --porcelain -uall`
  adr_preflight.py --package <name>   active ADRs governing <name> (id<TAB>status<TAB>title<TAB>path)
  adr_preflight.py --service <name>   alias for --package <name>
  adr_preflight.py --hook             dispatch Claude Code / Codex / Antigravity hook JSON from stdin

Exit codes:
  0  allowed / valid (or disabled, exempt path, SessionStart, Antigravity PreToolUse JSON)
  1  CLI (<path>... or --changed): docdag validate failed or docdag missing
  2  Claude/Codex hook PreToolUse held or blocked (stderr), CLI usage error, or --package failure
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
from dataclasses import dataclass
from functools import lru_cache
from pathlib import Path
from typing import Any

DEFAULT_PACKAGE_ADRS: dict[str, tuple[str, ...]] = {
    "model": ("0001", "0002"),
    "observation": ("0001",),
    "projection": ("0001", "0002"),
    "scope": ("0002", "0003"),
    "structure": ("0001", "0003"),
    "ownership": ("0002", "0004"),
    "plan": ("0001", "0002", "0003"),
    "provider": ("0001", "0004"),
    "protocol": ("0002", "0003", "0004"),
    "verification": ("0003", "0004", "0005"),
    "cli": ("0003", "0005"),
    "mcp": ("0002", "0005"),
    "host": ("0002", "0005"),
    "cmd": ("0002", "0005"),
    "Kisage": ("0001", "0002", "0003", "0004", "0005"),
    "examples": ("0004", "0005"),
    ".github": ("0005",),
    "scripts": ("0005",),
    ".claude": ("0005",),
    ".agents": ("0005",),
    ".codex": ("0005",),
    "repo-root": ("0001", "0002", "0003", "0004", "0005"),
}

PATCH_LINE_PREFIXES: tuple[str, ...] = (
    "*** Add File: ",
    "*** Update File: ",
    "*** Delete File: ",
    "*** Move to: ",
)

MAX_BRIEF_ADRS = 8
MAX_TITLE_CHARS = 120
MAX_BRIEF_CHARS = 6000
MAX_FINDING_LINES = 40
WORKFLOW_REL_PATH = ".github/workflows/ci.yml"
SELF_REL_PATH = ".claude/skills/adr-preflight/scripts/adr_preflight.py"


class MissingDocdagError(RuntimeError):
    """Raised when the docdag executable cannot be found on PATH."""


@dataclass(frozen=True)
class ValidateResult:
    missing_binary: bool
    returncode: int
    output: str
    summary_line: str


@dataclass(frozen=True)
class GoverningAdr:
    adr_id: str
    status: str
    title: str
    rel_path: str
    affected_packages: tuple[str, ...]


def is_disabled() -> bool:
    return os.environ.get("KISAGE_ADR_PREFLIGHT") == "disabled"


def get_state_dir() -> Path:
    override = os.environ.get("KISAGE_ADR_PREFLIGHT_STATE_DIR")
    if override:
        return Path(override)
    xdg_state = os.environ.get("XDG_STATE_HOME")
    base = Path(xdg_state) if xdg_state else Path.home() / ".local" / "state"
    return base / "kisage-adr-preflight"


def sanitise_token(raw: str, fallback: str) -> str:
    cleaned = re.sub(r"[^A-Za-z0-9_.-]", "_", raw)
    if not cleaned or cleaned in (".", ".."):
        return fallback
    return cleaned


def derive_scope_from_relpath(rel_path: Path, *, is_dir: bool = False) -> str | None:
    parts = rel_path.parts
    if not parts:
        return None
    if len(parts) == 1 and not is_dir:
        return "repo-root"
    return parts[0]


def _unquote_yaml_scalar(raw: str) -> str:
    val = raw.strip()
    if len(val) >= 2 and val[0] == val[-1] and val[0] in ('"', "'"):
        return val[1:-1].strip()
    return val


def _split_inline_yaml_list(inner: str) -> list[str]:
    items: list[str] = []
    for part in inner.split(","):
        cleaned = _unquote_yaml_scalar(part)
        if cleaned:
            items.append(cleaned)
    return items


def parse_affected_packages_from_file(adr_path: Path) -> list[str] | None:
    """Return declared affected_packages from ADR frontmatter, or None if undeclared."""
    try:
        lines = adr_path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError:
        return None
    if not lines or lines[0].lstrip("\ufeff").strip() != "---":
        return None
    end_idx: int | None = None
    for idx in range(1, len(lines)):
        if lines[idx].strip() == "---":
            end_idx = idx
            break
    if end_idx is None:
        return None

    fm_lines = lines[1:end_idx]
    i = 0
    while i < len(fm_lines):
        line = fm_lines[i]
        if re.match(r"^affected_packages\s*:", line):
            rest = line.split(":", 1)[1].strip()
            if rest.startswith("[") and rest.endswith("]"):
                return _split_inline_yaml_list(rest[1:-1])
            if rest:
                val = _unquote_yaml_scalar(rest)
                return [val] if val else []
            items: list[str] = []
            j = i + 1
            while j < len(fm_lines):
                sub = fm_lines[j]
                if not sub.strip() or sub.lstrip().startswith("#"):
                    j += 1
                    continue
                if not sub[0].isspace():
                    break
                m = re.match(r"^\s*-\s+(.*)$", sub)
                if m is not None:
                    val = _unquote_yaml_scalar(m.group(1))
                    if val:
                        items.append(val)
                j += 1
            return items
        i += 1
    return None


def extract_patch_paths(command_text: str) -> list[str]:
    extracted: list[str] = []
    for line in command_text.splitlines():
        for prefix in PATCH_LINE_PREFIXES:
            if line.startswith(prefix):
                candidate = line[len(prefix) :].strip()
                if candidate:
                    extracted.append(candidate)
                break
    return extracted


def _is_antigravity_payload(payload: dict[str, Any]) -> bool:
    return "toolCall" in payload or "hookEventName" in payload


def extract_hook_paths(payload: dict[str, Any]) -> list[str]:
    if _is_antigravity_payload(payload):
        tool_call = payload.get("toolCall")
        if not isinstance(tool_call, dict):
            return []
        if tool_call.get("name") not in ("write_file", "edit_file"):
            return []
        args = tool_call.get("args")
        if not isinstance(args, dict):
            return []
        raw_path = args.get("Path")
        if isinstance(raw_path, str) and raw_path.strip():
            return [raw_path.strip()]
        return []

    tool_input = payload.get("tool_input")
    if not isinstance(tool_input, dict):
        return []

    paths: list[str] = []
    for field in ("file_path", "notebook_path"):
        value = tool_input.get(field)
        if isinstance(value, str) and value.strip():
            paths.append(value.strip())

    if payload.get("tool_name") == "apply_patch":
        cmd = tool_input.get("command")
        if isinstance(cmd, str):
            paths.extend(extract_patch_paths(cmd))
        elif isinstance(cmd, list):
            joined = "\n".join(part for part in cmd if isinstance(part, str))
            paths.extend(extract_patch_paths(joined))
    return paths


def cap_lines(text: str, limit: int = MAX_FINDING_LINES) -> str:
    lines = text.strip("\n").splitlines()
    if len(lines) <= limit:
        return "\n".join(lines)
    kept = lines[: limit - 1]
    omitted = len(lines) - (limit - 1)
    kept.append(f"... ({omitted} more lines omitted)")
    return "\n".join(kept)


def is_exempt_relpath(rel_path: Path, scope: str | None) -> bool:
    return scope is None or scope == "docs" or rel_path.parts == ("docdag.yaml",)


def resolve_git_dir(directory: Path) -> tuple[Path, Path] | None:
    res = subprocess.run(
        [
            "git",
            "-C",
            str(directory.resolve()),
            "rev-parse",
            "--path-format=absolute",
            "--show-toplevel",
            "--git-common-dir",
        ],
        capture_output=True,
        text=True,
        check=False,
    )
    if res.returncode != 0:
        return None
    lines = [ln.strip() for ln in res.stdout.splitlines() if ln.strip()]
    if len(lines) < 2:
        return None
    return Path(lines[0]).resolve(), Path(lines[1]).resolve()


@lru_cache(maxsize=1)
def get_script_git_identity() -> tuple[Path, Path]:
    info = resolve_git_dir(Path(__file__).resolve().parent)
    if info is None:
        raise RuntimeError("adr_preflight.py is not inside a git repository")
    return info


def resolve_file_in_repo(
    raw_path: str,
    base_cwd: Path,
    script_common_dir: Path,
    git_dir_cache: dict[Path, tuple[Path, Path] | None],
) -> tuple[Path, Path, bool] | None:
    joined = Path(os.path.abspath(os.path.join(str(base_cwd), raw_path)))
    target_is_dir = joined.is_dir()
    probe = joined if target_is_dir else joined.parent
    while not probe.is_dir():
        if probe.parent == probe:
            return None
        probe = probe.parent

    probe_real = probe.resolve()
    tail = joined.relative_to(probe)
    abs_target = Path(os.path.normpath(str(probe_real / tail)))

    if probe_real not in git_dir_cache:
        git_dir_cache[probe_real] = resolve_git_dir(probe_real)

    git_info = git_dir_cache[probe_real]
    if git_info is None:
        return None

    root, common_dir = git_info
    if common_dir != script_common_dir:
        return None

    try:
        rel_path = abs_target.relative_to(root)
    except ValueError:
        return None

    if not rel_path.parts:
        return None
    return root, rel_path, target_is_dir


def _run_git_stdout(root: Path, args: list[str]) -> str:
    res = subprocess.run(
        ["git", "-C", str(root), *args],
        capture_output=True,
        text=True,
        check=False,
    )
    if res.returncode != 0:
        detail = res.stderr.strip() or res.stdout.strip() or f"exit {res.returncode}"
        raise RuntimeError(f"git {' '.join(args)} failed: {detail}")
    return res.stdout


def _rev_parse_or_absent(root: Path, spec: str) -> str:
    res = subprocess.run(
        ["git", "-C", str(root), "rev-parse", "--verify", "--quiet", spec],
        capture_output=True,
        text=True,
        check=False,
    )
    if res.returncode == 0 and res.stdout.strip():
        return res.stdout.strip()
    return "uncommitted"


def _extract_porcelain_paths(porcelain_output: str) -> list[str]:
    paths: list[str] = []
    for line in porcelain_output.splitlines():
        if len(line) < 4:
            continue
        xy = line[:2]
        raw_path = line[3:]
        if "R" in xy or "C" in xy:
            raw_path = raw_path.split(" -> ", 1)[-1]
        if len(raw_path) >= 2 and raw_path[0] == '"' and raw_path[-1] == '"':
            raw_path = raw_path[1:-1]
        paths.append(raw_path)
    return paths


def compute_fingerprint(root: Path) -> str:
    canonical_root = str(root.resolve())
    adr_tree = _rev_parse_or_absent(root, "HEAD:docs/ADR")
    cfg_blob = _rev_parse_or_absent(root, "HEAD:docdag.yaml")
    porcelain = _run_git_stdout(
        root,
        [
            "--no-optional-locks",
            "-c",
            "core.quotePath=false",
            "status",
            "--porcelain",
            "-uall",
            "--",
            "docs/ADR",
            "docdag.yaml",
        ],
    )
    parts: list[str] = [canonical_root, adr_tree, cfg_blob, porcelain]
    for rel_str in _extract_porcelain_paths(porcelain):
        target = root / rel_str
        try:
            st = target.stat()
            parts.append(f"{rel_str}\t{st.st_size}:{st.st_mtime_ns}")
        except FileNotFoundError:
            parts.append(f"{rel_str}\tabsent")

    digest = hashlib.sha256()
    for part in parts:
        encoded = part.encode("utf-8", errors="surrogateescape")
        digest.update(f"{len(encoded)}:".encode("ascii"))
        digest.update(encoded)
        digest.update(b"\0")
    return digest.hexdigest()


def is_git_ignored(root: Path, rel_path: Path) -> bool:
    res = subprocess.run(
        ["git", "-C", str(root), "check-ignore", "-q", "--", str(rel_path)],
        capture_output=True,
        text=True,
        check=False,
    )
    return res.returncode == 0


def read_pinned_docdag_version(root: Path | None) -> str:
    if root is None:
        return "<DOCDAG_VERSION>"
    wf_path = root / WORKFLOW_REL_PATH
    try:
        content = wf_path.read_text(encoding="utf-8")
    except OSError:
        return "<DOCDAG_VERSION>"
    match = re.search(r"^\s*DOCDAG_VERSION:\s*([^\s#]+)", content, re.MULTILINE)
    if not match:
        return "<DOCDAG_VERSION>"
    return _unquote_yaml_scalar(match.group(1))


def format_missing_docdag_fix(root: Path | None) -> str:
    version = read_pinned_docdag_version(root)
    return (
        "FIX: install docdag with "
        f"`go install github.com/Kaikei-e/DocDag/cmd/docdag@{version}` "
        f"(pinned by DOCDAG_VERSION in {WORKFLOW_REL_PATH}), then retry the edit."
    )


def run_docdag_validate(root: Path) -> ValidateResult:
    try:
        res = subprocess.run(
            ["docdag", "validate"],
            cwd=root,
            capture_output=True,
            text=True,
            check=False,
        )
    except FileNotFoundError:
        return ValidateResult(
            missing_binary=True,
            returncode=127,
            output="",
            summary_line="",
        )

    stdout_lines = [ln.strip() for ln in res.stdout.splitlines() if ln.strip()]
    summary = stdout_lines[-1] if stdout_lines else "OK"
    combined = "\n".join(
        chunk.rstrip("\n") for chunk in (res.stdout, res.stderr) if chunk.strip()
    )
    return ValidateResult(
        missing_binary=False,
        returncode=res.returncode,
        output=combined,
        summary_line=summary,
    )


def _default_packages_for_adr(adr_id: str) -> tuple[str, ...]:
    return tuple(
        pkg for pkg, adr_ids in DEFAULT_PACKAGE_ADRS.items() if adr_id in adr_ids
    )


def load_active_adrs(root: Path) -> list[GoverningAdr]:
    """Load both accepted (binding) and active un-superseded proposed ADRs via docdag export."""
    try:
        res = subprocess.run(
            ["docdag", "export", "--format", "json"],
            cwd=root,
            capture_output=True,
            text=True,
            check=False,
        )
    except FileNotFoundError as exc:
        raise MissingDocdagError("docdag is not on PATH") from exc

    if res.returncode != 0:
        detail = (res.stderr or res.stdout).strip()
        raise RuntimeError(f"docdag export failed (exit {res.returncode}): {detail}")

    try:
        graph = json.loads(res.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"docdag export returned malformed JSON: {exc}") from exc

    if not isinstance(graph, dict):
        raise RuntimeError("docdag export returned non-object JSON")

    nodes = graph.get("nodes") or []
    links = graph.get("links") or []
    status_by_id: dict[str, str] = {}
    for node in nodes:
        if isinstance(node, dict) and isinstance(node.get("id"), str):
            status_by_id[node["id"]] = str(node.get("status") or "")

    superseded_targets: set[str] = set()
    for link in links:
        if not isinstance(link, dict) or link.get("type") != "supersedes":
            continue
        src = link.get("source")
        dst = link.get("target")
        if not isinstance(src, str) or not isinstance(dst, str):
            continue
        if status_by_id.get(src) in ("accepted", "superseded"):
            superseded_targets.add(dst)

    records: list[GoverningAdr] = []
    for node in nodes:
        if not isinstance(node, dict):
            continue
        adr_id = str(node.get("id") or "")
        status = str(node.get("status") or "")
        title = str(node.get("title") or "")
        rel_path = str(node.get("path") or "")
        if not adr_id or status not in ("accepted", "proposed"):
            continue
        if adr_id in superseded_targets:
            continue

        declared_pkgs = (
            parse_affected_packages_from_file(root / rel_path) if rel_path else None
        )
        pkgs = (
            tuple(declared_pkgs)
            if declared_pkgs is not None
            else _default_packages_for_adr(adr_id)
        )
        records.append(
            GoverningAdr(
                adr_id=adr_id,
                status=status,
                title=title,
                rel_path=rel_path,
                affected_packages=pkgs,
            )
        )

    records.sort(key=lambda item: item.adr_id, reverse=True)
    return records


def governing_adrs_for_scope(
    scope: str,
    active_adrs: list[GoverningAdr],
) -> list[GoverningAdr]:
    return [adr for adr in active_adrs if scope in adr.affected_packages]


def _build_brief_parts(
    summary_lines: list[str],
    scope_sections: list[tuple[str, list[GoverningAdr]]],
) -> tuple[str, str]:
    listing_lines: list[str] = list(summary_lines)
    listing_lines.append("")

    for scope, adrs in scope_sections:
        total = len(adrs)
        if total == 0:
            listing_lines.append(
                f"Scope {scope}: 0 governing ADRs "
                "(inspect active ADRs with: "
                "docdag export --format json)"
            )
        else:
            shown = min(total, MAX_BRIEF_ADRS)
            if total > shown:
                listing_lines.append(
                    f"Scope {scope}: {total} governing ADRs ({shown} newest shown):"
                )
            else:
                noun = "ADR" if total == 1 else "ADRs"
                listing_lines.append(f"Scope {scope}: {total} governing {noun}:")
            for adr in adrs[:shown]:
                shown_title = adr.title
                if len(shown_title) > MAX_TITLE_CHARS:
                    shown_title = shown_title[: MAX_TITLE_CHARS - 1] + "…"
                listing_lines.append(f"  {adr.adr_id} [{adr.status}]  {shown_title}")

    if len(scope_sections) == 1:
        only_scope = scope_sections[0][0]
        pkg_arg = "<name>" if only_scope == "repo-root" else only_scope
    else:
        pkg_arg = "<name>"

    guidance_lines = [
        "Read one ADR: docdag context <id> --depth 2 --budget 1500",
        f"List all for a package: python3 {SELF_REL_PATH} --package {pkg_arg}",
        "If a planned change contradicts a governing ADR, stop and supersede it "
        "with a new ADR rather than contradicting it silently.",
    ]
    return "\n".join(listing_lines), "\n".join(guidance_lines)


def build_brief_body(
    summary_lines: list[str],
    scope_sections: list[tuple[str, list[GoverningAdr]]],
) -> str:
    listings, guidance = _build_brief_parts(summary_lines, scope_sections)
    return f"{listings}\n\n{guidance}"


def build_hook_brief(
    summaries: list[str],
    scope_sections: list[tuple[str, list[GoverningAdr]]],
) -> str:
    scope_list = ", ".join(scope for scope, _ in scope_sections)
    header = f"ADR PREFLIGHT (not an error) — scope(s): {scope_list}"
    trailer = "Receipt recorded for this session; retrying the same edit now succeeds."
    listings, guidance = _build_brief_parts(summaries, scope_sections)
    candidate = f"{header}\n{listings}\n\n{guidance}\n{trailer}\n"
    if len(candidate) < MAX_BRIEF_CHARS:
        return candidate

    trunc_line = "... (brief truncated; run the --package command per scope)"
    suffix = f"{trunc_line}\n{guidance}\n{trailer}\n"
    budget = MAX_BRIEF_CHARS - len(header) - len(suffix) - 3
    cut_listings = listings[: max(0, budget)]
    if "\n" in cut_listings:
        cut_listings = cut_listings.rsplit("\n", 1)[0]
    if not cut_listings:
        return f"{header}\n{suffix}"
    return f"{header}\n{cut_listings}\n{suffix}"


def root_key_for(root: Path) -> str:
    encoded = str(root.resolve()).encode("utf-8", errors="surrogateescape")
    return hashlib.sha256(encoded).hexdigest()[:16]


def receipt_path_for(
    session_id: str,
    agent_id: str | None,
    root: Path,
    scope: str,
) -> Path:
    sess_tok = sanitise_token(session_id, "default")
    agent_tok = sanitise_token(agent_id or "main", "main")
    scope_tok = sanitise_token(scope, "repo-root")
    return get_state_dir() / sess_tok / agent_tok / root_key_for(root) / scope_tok


def has_valid_receipt(
    session_id: str,
    agent_id: str | None,
    root: Path,
    scope: str,
    expected_fingerprint: str,
) -> bool:
    path = receipt_path_for(session_id, agent_id, root, scope)
    try:
        stored = path.read_text(encoding="utf-8").strip()
    except (FileNotFoundError, NotADirectoryError):
        return False
    return stored == expected_fingerprint


def write_receipt(
    session_id: str,
    agent_id: str | None,
    root: Path,
    scope: str,
    fingerprint: str,
) -> None:
    path = receipt_path_for(session_id, agent_id, root, scope)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(f"{fingerprint}\n", encoding="utf-8")


def format_validate_blocked(root: Path, vres: ValidateResult) -> str:
    if vres.missing_binary:
        return (
            "ADR PREFLIGHT BLOCKED: docdag is not on PATH.\n"
            f"{format_missing_docdag_fix(root)}\n"
        )
    lines = [
        f"ADR PREFLIGHT BLOCKED: docdag validate exited with code {vres.returncode}."
    ]
    if vres.output:
        lines.append(cap_lines(vres.output))
    if vres.returncode == 3:
        lines.append(
            "FIX: the configuration (docdag.yaml) or the docdag binary is stale; "
            f"update docdag to DOCDAG_VERSION in {WORKFLOW_REL_PATH} or repair "
            "the configuration, re-run `docdag validate`, then retry the edit."
        )
    else:
        lines.append(
            "FIX: repair the findings above, re-run `docdag validate`, "
            "then retry the edit."
        )
    return "\n".join(lines) + "\n"


def _emit_pretool_allow(is_antigravity: bool) -> int:
    if is_antigravity:
        sys.stdout.write(json.dumps({"decision": "allow"}) + "\n")
    return 0


def _emit_pretool_block(is_antigravity: bool, message: str) -> int:
    if is_antigravity:
        sys.stdout.write(
            json.dumps({"decision": "deny", "reason": message.rstrip("\n")}) + "\n"
        )
        return 0
    sys.stderr.write(message)
    return 2


def handle_pre_tool_use(payload: dict[str, Any]) -> int:
    is_ag = _is_antigravity_payload(payload)
    try:
        if is_disabled():
            return _emit_pretool_allow(is_ag)

        raw_paths = extract_hook_paths(payload)
        if not raw_paths:
            return _emit_pretool_allow(is_ag)

        cwd_val = payload.get("cwd")
        base_cwd = (
            Path(cwd_val) if isinstance(cwd_val, str) and cwd_val else Path.cwd()
        )
        session_raw = (
            payload.get("conversationId") if is_ag else payload.get("session_id")
        )
        session_id = (
            session_raw
            if isinstance(session_raw, str) and session_raw
            else "default"
        )
        agent_raw = payload.get("agent_id")
        agent_id = agent_raw if isinstance(agent_raw, str) and agent_raw else None

        _, script_common_dir = get_script_git_identity()
        git_dir_cache: dict[Path, tuple[Path, Path] | None] = {}
        fingerprint_cache: dict[Path, str] = {}
        pending_by_root: dict[Path, list[str]] = {}

        for raw_path in raw_paths:
            resolved = resolve_file_in_repo(
                raw_path, base_cwd, script_common_dir, git_dir_cache
            )
            if resolved is None:
                continue
            root, rel_path, is_dir = resolved
            scope = derive_scope_from_relpath(rel_path, is_dir=is_dir)
            if scope is None:
                continue
            if is_exempt_relpath(rel_path, scope):
                continue

            if root not in fingerprint_cache:
                fingerprint_cache[root] = compute_fingerprint(root)
            fp = fingerprint_cache[root]

            if has_valid_receipt(session_id, agent_id, root, scope, fp):
                continue

            if is_git_ignored(root, rel_path):
                continue

            scopes_for_root = pending_by_root.setdefault(root, [])
            if scope not in scopes_for_root:
                scopes_for_root.append(scope)

        if not pending_by_root:
            return _emit_pretool_allow(is_ag)

        summaries: list[str] = []
        scope_sections: list[tuple[str, list[GoverningAdr]]] = []

        for root, scopes in pending_by_root.items():
            vres = run_docdag_validate(root)
            if vres.missing_binary or vres.returncode != 0:
                return _emit_pretool_block(is_ag, format_validate_blocked(root, vres))

            summaries.append(vres.summary_line)
            active_adrs = load_active_adrs(root)
            for scope in scopes:
                scope_sections.append(
                    (scope, governing_adrs_for_scope(scope, active_adrs))
                )

        for root, scopes in pending_by_root.items():
            fp = fingerprint_cache[root]
            for scope in scopes:
                write_receipt(session_id, agent_id, root, scope, fp)

        return _emit_pretool_block(is_ag, build_hook_brief(summaries, scope_sections))
    except Exception as exc:
        err_detail = f"{type(exc).__name__}: {exc}"
        msg = (
            f"ADR PREFLIGHT BLOCKED: internal error ({err_detail}).\n"
            "FIX: inspect KISAGE_ADR_PREFLIGHT_STATE_DIR or the preflight script; "
            "set KISAGE_ADR_PREFLIGHT=disabled as the documented bypass if needed.\n"
        )
        return _emit_pretool_block(is_ag, msg)


def cleanup_session_state(session_id: str) -> None:
    target = get_state_dir() / sanitise_token(session_id, "default")
    shutil.rmtree(target, ignore_errors=True)


def _emit_session_start(is_antigravity: bool, msg: str) -> int:
    if is_antigravity:
        sys.stdout.write(
            json.dumps(
                {"injectSteps": [{"systemMessage": {"systemMessage": msg.rstrip()}}]}
            )
            + "\n"
        )
    else:
        sys.stdout.write(msg if msg.endswith("\n") else msg + "\n")
    return 0


def handle_session_start(payload: dict[str, Any]) -> int:
    is_ag = _is_antigravity_payload(payload)
    try:
        if is_disabled():
            return _emit_session_start(
                is_ag,
                "adr_preflight_disabled: ADR preflight gate is off "
                "(KISAGE_ADR_PREFLIGHT=disabled).\n",
            )

        session_raw = (
            payload.get("conversationId") if is_ag else payload.get("session_id")
        )
        session_id = (
            session_raw
            if isinstance(session_raw, str) and session_raw
            else "default"
        )
        cleanup_session_state(session_id)

        root, _ = get_script_git_identity()
        vres = run_docdag_validate(root)
        if vres.missing_binary:
            return _emit_session_start(
                is_ag,
                "adr_preflight_enabled: docdag is not on PATH; code edits will be "
                "blocked until it is installed and fixed.\n"
                f"{format_missing_docdag_fix(root)}\n",
            )

        if vres.returncode != 0:
            findings = cap_lines(vres.output)
            msg = (
                "adr_preflight_enabled: docdag validate failed; code edits will be "
                "blocked until it is fixed."
            )
            full_msg = f"{msg}\n{findings}\n" if findings else f"{msg}\n"
            return _emit_session_start(is_ag, full_msg)

        return _emit_session_start(
            is_ag,
            f"adr_preflight_enabled: {vres.summary_line}. "
            "The first edit to each top-level directory in this session is held once "
            "to surface its governing ADRs before retrying.\n",
        )
    except Exception as exc:
        err_detail = f"{type(exc).__name__}: {exc}"
        return _emit_session_start(
            is_ag,
            f"adr_preflight_enabled: internal error ({err_detail}); "
            "code edits will be blocked until it is fixed.\n",
        )


def run_hook_mode() -> int:
    raw_stdin = sys.stdin.read()
    try:
        payload = json.loads(raw_stdin)
    except json.JSONDecodeError:
        return 0
    if not isinstance(payload, dict):
        return 0

    event = payload.get("hookEventName") or payload.get("hook_event_name")
    if event == "PreToolUse":
        return handle_pre_tool_use(payload)
    if event == "SessionStart":
        return handle_session_start(payload)
    return 0


def collect_changed_paths(root: Path) -> list[str]:
    res = subprocess.run(
        [
            "git",
            "-c",
            "core.quotePath=false",
            "-C",
            str(root),
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

    return [str(root / raw_path) for raw_path in _extract_porcelain_paths(res.stdout)]


def run_cli_paths_mode(raw_paths: list[str], *, fallback_root: Path) -> int:
    base_cwd = Path.cwd()
    _, script_common_dir = get_script_git_identity()
    git_dir_cache: dict[Path, tuple[Path, Path] | None] = {}
    scopes_by_root: dict[Path, list[str]] = {}

    for raw_path in raw_paths:
        resolved = resolve_file_in_repo(
            raw_path, base_cwd, script_common_dir, git_dir_cache
        )
        if resolved is None:
            continue
        root, rel_path, is_dir = resolved
        scope = derive_scope_from_relpath(rel_path, is_dir=is_dir)
        if scope is None:
            continue
        if is_exempt_relpath(rel_path, scope):
            continue
        if is_git_ignored(root, rel_path):
            continue
        scopes = scopes_by_root.setdefault(root, [])
        if scope not in scopes:
            scopes.append(scope)

    roots_to_validate = list(scopes_by_root.keys()) or [fallback_root]

    summaries: list[str] = []
    scope_sections: list[tuple[str, list[GoverningAdr]]] = []

    for root in roots_to_validate:
        vres = run_docdag_validate(root)
        if vres.missing_binary:
            sys.stdout.write(
                "docdag is not on PATH.\n"
                f"{format_missing_docdag_fix(root)}\n"
            )
            return 1
        if vres.returncode != 0:
            if vres.output:
                sys.stdout.write(f"{vres.output}\n")
            return 1
        summaries.append(vres.summary_line)
        if root in scopes_by_root:
            active_adrs = load_active_adrs(root)
            for scope in scopes_by_root[root]:
                scope_sections.append(
                    (scope, governing_adrs_for_scope(scope, active_adrs))
                )

    if not scope_sections:
        sys.stdout.write("No non-exempt scopes in the requested paths.\n")
        return 0

    body = build_brief_body(summaries, scope_sections)
    sys.stdout.write(f"{body}\n")
    return 0


def _resolve_active_checkout_root() -> Path:
    script_root, script_common_dir = get_script_git_identity()
    cwd_info = resolve_git_dir(Path.cwd())
    if cwd_info is not None and cwd_info[1] == script_common_dir:
        return cwd_info[0]
    return script_root


def run_cli_package_mode(package_name: str) -> int:
    root = _resolve_active_checkout_root()
    try:
        active_adrs = load_active_adrs(root)
    except MissingDocdagError:
        sys.stderr.write(
            "adr_preflight.py: docdag is not on PATH\n"
            f"{format_missing_docdag_fix(root)}\n"
        )
        return 2
    except (OSError, RuntimeError) as exc:
        sys.stderr.write(f"adr_preflight.py: {exc}\n")
        return 2

    for adr in active_adrs:
        if package_name in adr.affected_packages:
            sys.stdout.write(
                f"{adr.adr_id}\t{adr.status}\t{adr.title}\t{adr.rel_path}\n"
            )
    return 0


def print_usage() -> int:
    sys.stderr.write(
        "Usage: adr_preflight.py <path>... | --changed | --package <name> | --hook\n"
    )
    return 2


def main(argv: list[str]) -> int:
    if not argv:
        return print_usage()

    first = argv[0]
    if first == "--hook":
        if len(argv) != 1:
            return print_usage()
        return run_hook_mode()

    if first == "--changed":
        if len(argv) != 1:
            return print_usage()
        root = _resolve_active_checkout_root()
        changed = collect_changed_paths(root)
        return run_cli_paths_mode(changed, fallback_root=root)

    if first in ("--package", "--service"):
        if len(argv) != 2 or not argv[1].strip():
            return print_usage()
        return run_cli_package_mode(argv[1].strip())

    if first == "--":
        path_args = argv[1:]
        if not path_args:
            return print_usage()
    elif any(arg.startswith("-") for arg in argv):
        return print_usage()
    else:
        path_args = argv

    fallback_root = _resolve_active_checkout_root()
    return run_cli_paths_mode(path_args, fallback_root=fallback_root)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
