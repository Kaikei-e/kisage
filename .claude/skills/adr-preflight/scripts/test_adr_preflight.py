#!/usr/bin/env python3
"""Black-box unit and integration tests for adr_preflight.py."""

from __future__ import annotations

import json
import os
import shutil
import stat
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
REAL_SCRIPT_PATH = SCRIPT_DIR / "adr_preflight.py"
REAL_REPO_ROOT = SCRIPT_DIR.parents[3]
VALIDATE_SUMMARY = "OK: 4 docs, 1 typed edges, no cycles"


def _render_adr(
    adr_id: str,
    title: str,
    affected_block: str = "",
    status: str = "proposed",
    supersedes_line: str = "",
) -> str:
    extra = ""
    if affected_block:
        extra += f"{affected_block.rstrip()}\n"
    if supersedes_line:
        extra += f"{supersedes_line.rstrip()}\n"
    return textwrap.dedent(
        f"""\
        ---
        title: {title}
        date: 2026-10-29
        status: {status}
        {extra}---

        # {adr_id}: {title}

        ## コンテキスト

        Context for {adr_id}.

        ## 決定

        Decision for {adr_id}.

        ## 根拠（調査結果・出典）

        Evidence for {adr_id}.

        ## 検討した代替案

        Alternatives for {adr_id}.

        ## 影響とトレードオフ

        Tradeoffs for {adr_id}.

        ## 関連ADR

        なし
        """
    )


class AdrPreflightTests(unittest.TestCase):
    """Hermetic tests against a temporary git repository and stub docdag."""

    def setUp(self) -> None:
        self._tmpdir = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmpdir.cleanup)
        self.tmp_root = Path(self._tmpdir.name).resolve()

        self.repo = self.tmp_root / "repo"
        self.bin_dir = self.tmp_root / "bin"
        self.state_dir = self.tmp_root / "state"
        self.outside_dir = self.tmp_root / "outside"
        self.docdag_log = self.tmp_root / "docdag_invocations.jsonl"
        self.docdag_config = self.tmp_root / "docdag_config.json"

        self.repo.mkdir()
        self.bin_dir.mkdir()
        self.outside_dir.mkdir()
        (self.outside_dir / "external.go").write_text("package external\n", encoding="utf-8")

        (self.repo / "docdag.yaml").write_text(
            textwrap.dedent(
                """\
                preset: adr
                dir: docs/ADR
                id_width: 4
                """
            ),
            encoding="utf-8",
        )
        (self.repo / ".gitignore").write_text("ignored/\n", encoding="utf-8")

        adr_dir = self.repo / "docs" / "ADR"
        adr_dir.mkdir(parents=True)
        (adr_dir / "0001-model.md").write_text(
            _render_adr("0001", "Model projection", status="proposed"),
            encoding="utf-8",
        )
        (adr_dir / "0002-ownership.md").write_text(
            _render_adr(
                "0002",
                "Ownership store",
                affected_block="affected_packages: [ownership, protocol]",
                status="accepted",
            ),
            encoding="utf-8",
        )
        (adr_dir / "0003-plan.md").write_text(
            _render_adr(
                "0003",
                "Pure plan pipeline",
                affected_block="affected_packages:\n  - plan\n  - protocol",
                status="accepted",
            ),
            encoding="utf-8",
        )
        (adr_dir / "0004-old.md").write_text(
            _render_adr(
                "0004",
                "Retired record",
                affected_block="affected_packages: [plan]",
                status="superseded",
            ),
            encoding="utf-8",
        )

        fixture_scripts_dir = (
            self.repo / ".claude" / "skills" / "adr-preflight" / "scripts"
        )
        fixture_scripts_dir.mkdir(parents=True)
        self.fixture_script = fixture_scripts_dir / "adr_preflight.py"
        shutil.copy2(REAL_SCRIPT_PATH, self.fixture_script)

        (self.repo / ".github" / "workflows").mkdir(parents=True)
        (self.repo / ".github" / "workflows" / "ci.yml").write_text(
            "env:\n  DOCDAG_VERSION: v0.6.0\n",
            encoding="utf-8",
        )

        (self.repo / "plan").mkdir()
        (self.repo / "plan" / "pipeline.go").write_text("package plan\n", encoding="utf-8")
        (self.repo / "model").mkdir()
        (self.repo / "model" / "path.go").write_text("package model\n", encoding="utf-8")
        (self.repo / "ignored").mkdir()
        (self.repo / "ignored" / "tmp.go").write_text("package ignored\n", encoding="utf-8")

        self._git(self.repo, "init")
        self._git(self.repo, "config", "user.email", "test@example.com")
        self._git(self.repo, "config", "user.name", "Test User")
        self._git(self.repo, "add", "-A")
        self._git(self.repo, "commit", "-m", "init fixture")

        self._write_docdag_config(
            validate_exit=0,
            validate_stdout=VALIDATE_SUMMARY + "\n",
            validate_stderr="",
            export_graph={
                "nodes": [
                    {
                        "id": "0001",
                        "title": "Model projection",
                        "status": "proposed",
                        "path": "docs/ADR/0001-model.md",
                    },
                    {
                        "id": "0002",
                        "title": "Ownership store",
                        "status": "accepted",
                        "path": "docs/ADR/0002-ownership.md",
                    },
                    {
                        "id": "0003",
                        "title": "Pure plan pipeline",
                        "status": "accepted",
                        "path": "docs/ADR/0003-plan.md",
                    },
                    {
                        "id": "0004",
                        "title": "Retired record",
                        "status": "superseded",
                        "path": "docs/ADR/0004-old.md",
                    },
                ],
                "links": [],
            },
        )
        self._install_stub_docdag()

    def _git(self, repo: Path, *args: str) -> str:
        res = subprocess.run(
            ["git", "-C", str(repo), *args],
            capture_output=True,
            text=True,
            check=True,
        )
        return res.stdout.strip()

    def _write_docdag_config(
        self,
        *,
        validate_exit: int,
        validate_stdout: str,
        validate_stderr: str,
        export_graph: dict[str, object],
    ) -> None:
        self.docdag_config.write_text(
            json.dumps(
                {
                    "validate_exit": validate_exit,
                    "validate_stdout": validate_stdout,
                    "validate_stderr": validate_stderr,
                    "export_graph": export_graph,
                }
            ),
            encoding="utf-8",
        )

    def _install_stub_docdag(self) -> None:
        stub_path = self.bin_dir / "docdag"
        stub_path.write_text(
            textwrap.dedent(
                f"""\
                #!{sys.executable}
                import json
                import os
                import sys
                from pathlib import Path

                cfg = json.loads(Path(os.environ["STUB_DOCDAG_CFG"]).read_text(encoding="utf-8"))
                log_path = Path(os.environ["STUB_DOCDAG_LOG"])
                with log_path.open("a", encoding="utf-8") as fh:
                    fh.write(json.dumps({{"argv": sys.argv[1:], "cwd": os.getcwd()}}) + "\\n")

                cmd = sys.argv[1] if len(sys.argv) > 1 else ""
                if cmd == "validate":
                    sys.stdout.write(cfg["validate_stdout"])
                    sys.stderr.write(cfg["validate_stderr"])
                    sys.exit(cfg["validate_exit"])
                if cmd == "export":
                    sys.stdout.write(json.dumps(cfg["export_graph"]))
                    sys.exit(0)
                sys.stderr.write(f"unexpected subcommand: {{sys.argv[1:]}}\\n")
                sys.exit(2)
                """
            ),
            encoding="utf-8",
        )
        stub_path.chmod(stub_path.stat().st_mode | stat.S_IEXEC)

    def _run_preflight(
        self,
        args: list[str],
        *,
        stdin_obj: dict[str, object] | None = None,
        extra_env: dict[str, str] | None = None,
        with_docdag: bool = True,
        cwd: Path | None = None,
    ) -> subprocess.CompletedProcess[str]:
        env = os.environ.copy()
        env["STUB_DOCDAG_CFG"] = str(self.docdag_config)
        env["STUB_DOCDAG_LOG"] = str(self.docdag_log)
        env["KISAGE_ADR_PREFLIGHT_STATE_DIR"] = str(self.state_dir)
        env.pop("KISAGE_ADR_PREFLIGHT", None)
        if with_docdag:
            env["PATH"] = f"{self.bin_dir}:{env.get('PATH', '')}"
        else:
            env["PATH"] = "/usr/bin:/bin"
        if extra_env:
            env.update(extra_env)

        return subprocess.run(
            [sys.executable, str(self.fixture_script), *args],
            input=json.dumps(stdin_obj) if stdin_obj is not None else None,
            capture_output=True,
            text=True,
            cwd=str(cwd or self.repo),
            env=env,
            check=False,
        )

    def test_cli_paths_surfaces_proposed_and_accepted_adrs(self) -> None:
        res = self._run_preflight(["model/path.go", "plan/pipeline.go"])
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)
        self.assertIn("0001 [proposed]", res.stdout)
        self.assertIn("0003 [accepted]", res.stdout)
        self.assertNotIn("0004", res.stdout)

    def test_cli_package_flag(self) -> None:
        res = self._run_preflight(["--package", "protocol"])
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertIn("0002\taccepted\tOwnership store", res.stdout)
        self.assertIn("0003\taccepted\tPure plan pipeline", res.stdout)

    def test_cli_changed_mode(self) -> None:
        (self.repo / "plan" / "pipeline.go").write_text("package plan // changed\n", encoding="utf-8")
        res = self._run_preflight(["--changed"])
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)
        self.assertIn("Scope plan:", res.stdout)
        self.assertIn("0003 [accepted]", res.stdout)

    def test_claude_codex_hook_holds_once_then_passes_and_invalidates_on_adr_edit(self) -> None:
        s_res = self._run_preflight(
            ["--hook"],
            stdin_obj={"hook_event_name": "SessionStart", "session_id": "sess-1"},
        )
        self.assertEqual(s_res.returncode, 0)
        self.assertIn("adr_preflight_enabled:", s_res.stdout)

        payload = {
            "hook_event_name": "PreToolUse",
            "session_id": "sess-1",
            "tool_name": "Edit",
            "cwd": str(self.repo),
            "tool_input": {"file_path": "plan/pipeline.go"},
        }
        first = self._run_preflight(["--hook"], stdin_obj=payload)
        self.assertEqual(first.returncode, 2)
        self.assertIn("ADR PREFLIGHT (not an error)", first.stderr)
        self.assertIn("0003 [accepted]", first.stderr)

        second = self._run_preflight(["--hook"], stdin_obj=payload)
        self.assertEqual(second.returncode, 0, second.stderr)

        # Modify an ADR; fingerprint must invalidate receipt and hold again once.
        (self.repo / "docs" / "ADR" / "0003-plan.md").write_text(
            _render_adr(
                "0003",
                "Pure plan pipeline",
                affected_block="affected_packages: [plan]",
                status="accepted",
            )
            + "\n<!-- touched -->\n",
            encoding="utf-8",
        )
        third = self._run_preflight(["--hook"], stdin_obj=payload)
        self.assertEqual(third.returncode, 2)
        self.assertIn("ADR PREFLIGHT (not an error)", third.stderr)

    def test_antigravity_hook_session_start_and_pretooluse_contract(self) -> None:
        s_res = self._run_preflight(
            ["--hook"],
            stdin_obj={"hookEventName": "SessionStart", "conversationId": "ag-conv-1"},
        )
        self.assertEqual(s_res.returncode, 0)
        s_json = json.loads(s_res.stdout)
        self.assertIn("injectSteps", s_json)
        self.assertIn(
            "adr_preflight_enabled:",
            s_json["injectSteps"][0]["systemMessage"]["systemMessage"],
        )

        ag_edit = {
            "hookEventName": "PreToolUse",
            "conversationId": "ag-conv-1",
            "cwd": str(self.repo),
            "toolCall": {
                "id": "tool_1",
                "name": "edit_file",
                "args": {"Path": str(self.repo / "model" / "path.go"), "Replacement": "x"},
            },
        }
        first = self._run_preflight(["--hook"], stdin_obj=ag_edit)
        self.assertEqual(first.returncode, 0)
        first_json = json.loads(first.stdout)
        self.assertEqual(first_json["decision"], "deny")
        self.assertIn("ADR PREFLIGHT (not an error)", first_json["reason"])
        self.assertIn("0001 [proposed]", first_json["reason"])

        second = self._run_preflight(["--hook"], stdin_obj=ag_edit)
        self.assertEqual(second.returncode, 0)
        second_json = json.loads(second.stdout)
        self.assertEqual(second_json["decision"], "allow")

        # Non-edit Antigravity tool passes with allow
        ag_read = {
            "hookEventName": "PreToolUse",
            "conversationId": "ag-conv-1",
            "cwd": str(self.repo),
            "toolCall": {
                "id": "tool_2",
                "name": "read_file",
                "args": {"Path": str(self.repo / "plan" / "pipeline.go")},
            },
        }
        third = self._run_preflight(["--hook"], stdin_obj=ag_read)
        self.assertEqual(third.returncode, 0)
        self.assertEqual(json.loads(third.stdout)["decision"], "allow")

    def test_broken_docdag_blocks_code_but_exempts_docs_and_docdag_yaml(self) -> None:
        self._write_docdag_config(
            validate_exit=1,
            validate_stdout="",
            validate_stderr="ERROR dangling ref 9999\n",
            export_graph={"nodes": [], "links": []},
        )
        code_res = self._run_preflight(
            ["--hook"],
            stdin_obj={
                "hook_event_name": "PreToolUse",
                "session_id": "s2",
                "tool_name": "Write",
                "cwd": str(self.repo),
                "tool_input": {"file_path": "plan/new.go"},
            },
        )
        self.assertEqual(code_res.returncode, 2)
        self.assertIn("ADR PREFLIGHT BLOCKED", code_res.stderr)

        for exempt in ("docs/ADR/0003-plan.md", "docdag.yaml", "ignored/tmp.go"):
            res = self._run_preflight(
                ["--hook"],
                stdin_obj={
                    "hook_event_name": "PreToolUse",
                    "session_id": "s2",
                    "tool_name": "Edit",
                    "cwd": str(self.repo),
                    "tool_input": {"file_path": exempt},
                },
            )
            self.assertEqual(res.returncode, 0, f"expected {exempt} exempt: {res.stderr}")

        # Outside repository path is also exempt
        ext_res = self._run_preflight(
            ["--hook"],
            stdin_obj={
                "hook_event_name": "PreToolUse",
                "session_id": "s2",
                "tool_name": "Write",
                "cwd": str(self.repo),
                "tool_input": {"file_path": str(self.outside_dir / "external.go")},
            },
        )
        self.assertEqual(ext_res.returncode, 0, ext_res.stderr)

    def test_missing_docdag_and_kill_switch(self) -> None:
        blocked = self._run_preflight(
            ["--hook"],
            stdin_obj={
                "hook_event_name": "PreToolUse",
                "session_id": "s3",
                "tool_name": "Write",
                "cwd": str(self.repo),
                "tool_input": {"file_path": "plan/x.go"},
            },
            with_docdag=False,
        )
        self.assertEqual(blocked.returncode, 2)
        self.assertIn("docdag is not on PATH", blocked.stderr)
        self.assertIn("v0.6.0", blocked.stderr)

        bypassed = self._run_preflight(
            ["--hook"],
            stdin_obj={
                "hook_event_name": "PreToolUse",
                "session_id": "s3",
                "tool_name": "Write",
                "cwd": str(self.repo),
                "tool_input": {"file_path": "plan/x.go"},
            },
            with_docdag=False,
            extra_env={"KISAGE_ADR_PREFLIGHT": "disabled"},
        )
        self.assertEqual(bypassed.returncode, 0, bypassed.stderr)

    def test_live_kisage_repo_cli_smoke(self) -> None:
        if shutil.which("docdag") is None:
            self.skipTest("docdag binary not installed on PATH")
        res = subprocess.run(
            [sys.executable, str(REAL_SCRIPT_PATH), "plan/pipeline.go", "protocol/reconcile.go"],
            cwd=str(REAL_REPO_ROOT),
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)
        self.assertIn("Scope plan:", res.stdout)
        self.assertIn("0003 [proposed]", res.stdout)
        self.assertIn("Scope protocol:", res.stdout)
        self.assertIn("0004 [proposed]", res.stdout)


if __name__ == "__main__":
    unittest.main(verbosity=2)
