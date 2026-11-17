#!/usr/bin/env python3
"""Tests for .claude/skills/layer-architecture/scripts/check_layers.py."""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT_PATH = Path(__file__).resolve().parent / "check_layers.py"
REPO_ROOT = SCRIPT_PATH.parents[4]


def _run_checker(
    args: list[str],
    *,
    cwd: Path,
    stdin_obj: dict[str, object] | None = None,
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(SCRIPT_PATH), *args],
        cwd=str(cwd),
        input=json.dumps(stdin_obj) if stdin_obj is not None else None,
        capture_output=True,
        text=True,
        check=False,
    )


def _git(repo: Path, *args: str) -> None:
    subprocess.run(
        ["git", "-C", str(repo), *args],
        capture_output=True,
        text=True,
        check=True,
    )


class CheckLayersTests(unittest.TestCase):
    def test_real_kisage_repository_is_clean(self) -> None:
        res = _run_checker([], cwd=REPO_ROOT)
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)
        self.assertIn("0 layer violations", res.stdout)

    def test_detects_upward_pure_io_and_mcp_violations_and_exempts_tests(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo = Path(tmp) / "kisage"
            repo.mkdir()
            _git(repo, "init")
            _git(repo, "config", "user.email", "test@example.com")
            _git(repo, "config", "user.name", "Test User")

            (repo / "plan").mkdir()
            (repo / "plan" / "bad_upward.go").write_text(
                'package plan\nimport "github.com/Kaikei-e/kisage/provider"\n',
                encoding="utf-8",
            )
            (repo / "plan" / "bad_io.go").write_text(
                'package plan\nimport (\n\t"os"\n)\n',
                encoding="utf-8",
            )
            (repo / "plan" / "good_test.go").write_text(
                'package plan_test\nimport (\n\t"os"\n\t"github.com/Kaikei-e/kisage/provider"\n)\n',
                encoding="utf-8",
            )
            (repo / "model").mkdir()
            (repo / "model" / "bad_cycle.go").write_text(
                'package model\nimport "github.com/Kaikei-e/kisage/plan"\n',
                encoding="utf-8",
            )
            (repo / "mcp").mkdir()
            (repo / "mcp" / "bad_os.go").write_text(
                'package mcp\nimport "os"\n',
                encoding="utf-8",
            )
            (repo / "protocol").mkdir()
            (repo / "protocol" / "good.go").write_text(
                'package protocol\n// import "github.com/Kaikei-e/kisage/cli"\nimport "github.com/Kaikei-e/kisage/provider"\n',
                encoding="utf-8",
            )

            res_bad = _run_checker(["--file", "plan/bad_upward.go"], cwd=repo)
            self.assertEqual(res_bad.returncode, 1)
            self.assertIn("VIOLATION internal-layer-boundary", res_bad.stdout)
            self.assertIn("FIX internal-layer-boundary:", res_bad.stdout)

            res_io = _run_checker(["--file", "plan/bad_io.go"], cwd=repo)
            self.assertEqual(res_io.returncode, 1)
            self.assertIn("VIOLATION pure-package-io-forbidden", res_io.stdout)

            res_test = _run_checker(["--file", "plan/good_test.go"], cwd=repo)
            self.assertEqual(res_test.returncode, 0, res_test.stdout + res_test.stderr)

            res_model = _run_checker(["--file", "model/bad_cycle.go"], cwd=repo)
            self.assertEqual(res_model.returncode, 1)
            self.assertIn("VIOLATION internal-layer-boundary", res_model.stdout)

            res_mcp = _run_checker(["--file", "mcp/bad_os.go"], cwd=repo)
            self.assertEqual(res_mcp.returncode, 1)
            self.assertIn("VIOLATION mcp-os-boundary", res_mcp.stdout)

            res_proto = _run_checker(["--file", "protocol/good.go"], cwd=repo)
            self.assertEqual(res_proto.returncode, 0, res_proto.stdout + res_proto.stderr)

            res_changed = _run_checker(["--changed"], cwd=repo)
            self.assertEqual(res_changed.returncode, 1)
            self.assertIn("plan/bad_upward.go", res_changed.stdout)
            self.assertIn("plan/bad_io.go", res_changed.stdout)
            self.assertIn("model/bad_cycle.go", res_changed.stdout)
            self.assertIn("mcp/bad_os.go", res_changed.stdout)
            self.assertNotIn("good_test.go", res_changed.stdout)

            res_hook_claude = _run_checker(
                ["--hook"],
                cwd=repo,
                stdin_obj={
                    "tool_name": "Write",
                    "tool_input": {"file_path": str(repo / "plan" / "bad_upward.go")},
                },
            )
            self.assertEqual(res_hook_claude.returncode, 2)
            self.assertIn("VIOLATION internal-layer-boundary", res_hook_claude.stderr)

            res_hook_ag_clean = _run_checker(
                ["--hook"],
                cwd=repo,
                stdin_obj={
                    "hookEventName": "PostToolUse",
                    "cwd": str(repo),
                    "toolCall": {
                        "name": "edit_file",
                        "args": {"Path": str(repo / "protocol" / "good.go")},
                    },
                },
            )
            self.assertEqual(res_hook_ag_clean.returncode, 0, res_hook_ag_clean.stderr)
            self.assertEqual(json.loads(res_hook_ag_clean.stdout), {})


if __name__ == "__main__":
    unittest.main(verbosity=2)
