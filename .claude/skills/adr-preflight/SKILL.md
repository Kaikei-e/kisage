---
name: adr-preflight
description: コード変更の前に DocDag の ADR グラフ健全性を検証し、変更先パッケージに効く設計決定（accepted および未置換の proposed ADR）を提示して既存決定との無自覚な衝突を防ぐ。新機能実装・バグ修正・リファクタリング・改善の着手時、編集フックが `ADR PREFLIGHT` または `ADR PREFLIGHT BLOCKED` で編集を保留したとき、あるいはユーザが「ADR 確認して」「preflight 回して」と言ったときに起動する。計画前の横断的な文脈収集には plan-context-loader、新規 ADR 執筆には kisage-adr-writer を使用する。
allowed-tools:
  - "Bash(python3 ${CLAUDE_SKILL_DIR}/scripts/adr_preflight.py *)"
  - "Bash(python3 .claude/skills/adr-preflight/scripts/adr_preflight.py *)"
  - Read
  - Grep
  - Glob
---

# ADR Preflight Gate

コード変更が既存の設計決定（`status: accepted` の binding ADR、および Phase 0 の未置換 `proposed` ADR）と無自覚に衝突するのを編集前に防ぐゲートである。

## 1. 適用タイミングとスコープ

- **着手時**: 新機能・バグ修正・リファクタリング・改善のいずれであっても、コード変更を始める前に実行・確認する。
- **対象リポジトリとスコープ**: 本ゲートはこのリポジトリ本体とその git worktree のみに適用され、別リポジトリのファイルは免除される。`PreToolUse` は git ルート直下の第 1 パス要素（ルート直下は `repo-root`）をスコープとし、`docs/` 配下とルート直下の `docdag.yaml`（決定コーパス自身を修復できるようにするため）、git-ignore 対象、リポジトリ外は免除する。
- **受領証のライフサイクル**: `SessionStart` は当該セッションの受領証（receipt）を消去して `docdag validate` を実行し、`adr_preflight_enabled`（または `adr_preflight_disabled`）を出力して常に exit `0` で終了する。同一スコープの 2 回目以降の編集は通過するが、`docs/ADR` または `docdag.yaml` が変化したとき、あるいは `SessionStart` 実行時に受領証は失効して再提示される。

## 2. フック出力の意味と対処手順

`PreToolUse` が編集を保留・拒否した場合、メッセージ先頭で対応を分岐する:

1. **`ADR PREFLIGHT BLOCKED ... FIX: ...`**: 原因は `docdag validate` 失敗、`docdag` 不在、またはゲート内部エラー（fail-closed）。`docs/` と `docdag.yaml` は修復用に編集可能なまま保たれるため、`docdag` の `fix:` / `FIX:` 行に従ってグラフを修復してから再試行する。
2. **`ADR PREFLIGHT (not an error)`**: グラフ健全。受領証を記録し、該当パッケージに効く ADR 一覧（`[accepted]` / `[proposed]`）を示すため 1 回だけ編集を保留する。関係する ADR を `docdag context <id> --depth 2 --budget 1500` で確認してから同じ編集を再試行する（再試行は通過する）。

## 3. 設計決定と矛盾する場合の原則

- 既存の ADR と矛盾する変更は**独断で実装せず**、直ちに作業を止めて衝突する ADR と理由をユーザへ報告する。
- 決定を更新する場合はユーザ合意のもと [.claude/skills/kisage-adr-writer/SKILL.md](../kisage-adr-writer/SKILL.md) に従って後継 ADR に `supersedes` を記述する。確定済み（`accepted`）ADR の本文は追記専用であり、置換時に frontmatter `status: superseded` を更新する以外は書き換えない。

## 4. CLI 実行と終了コード

```bash
# 変更予定パスまたは変更済みファイルからブリーフを stdout に出力（exit 0、validate 失敗・docdag 不在時は exit 1）
python3 .claude/skills/adr-preflight/scripts/adr_preflight.py <path>...
python3 .claude/skills/adr-preflight/scripts/adr_preflight.py --changed
# 指定パッケージに関係する有効 ADR を出力（id<TAB>status<TAB>title<TAB>path）
python3 .claude/skills/adr-preflight/scripts/adr_preflight.py --package <name>
```

## 5. 緊急停止スイッチ・関連スキル

- **緊急停止スイッチ（Kill switch）**: 環境変数 `KISAGE_ADR_PREFLIGHT=disabled`（ツールチェーン破損時の退避用であり、確認省略のために使ってはならない）。
- **既知の制約**: シェルコマンド経由のファイル変更は編集フックから見えないため、ファイル編集ツールを使うか事前に CLI を実行する。
- **より広い文脈収集**: [.claude/skills/plan-context-loader/SKILL.md](../plan-context-loader/SKILL.md)

## 6. マルチエージェント配線

- **Claude Code**: `.claude/settings.json` の `SessionStart` と `PreToolUse`（`Edit|Write|NotebookEdit`）。
- **Codex**: `.codex/hooks.json` の `SessionStart` と `PreToolUse`（`apply_patch|Edit|Write`）。
- **Antigravity**: `.agents/hooks.json` の `SessionStart` と `PreToolUse`（`write_file|edit_file`）。
