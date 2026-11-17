---
name: layer-architecture
description: kisage の 14 パッケージ非巡回階層と 3 層保証境界（Logical Frame / Provider Preservation / Execution Boundary）を検証し、純粋決定論層への I/O 混入や上位層への逆依存を実装前に防ぐ。新機能実装、リファクタリング、パッケージ間の移動、差分レビュー時に使用する。
allowed-tools:
  - "Bash(python3 ${CLAUDE_SKILL_DIR}/scripts/check_layers.sh *)"
  - "Bash(python3 .claude/skills/layer-architecture/scripts/check_layers.py *)"
  - Read
  - Grep
  - Glob
---

# Layer Architecture (kisage Package DAG)

`kisage` は `docs/PRINCIPLE.md` §3 の 3 層保証境界に基づき、Go パッケージの依存関係を厳密な有向非巡回グラフ（DAG）として維持する。

## 1. パッケージ階層と依存規則

| パッケージ | 責務（保証層） | 許可される kisage 内部 import | 禁止される依存 |
|---|---|---|---|
| `model` | 値・パス・診断コード（第1層基礎） | なし（標準ライブラリ + `LKAL` のみ） | `os`, `os/exec`, `syscall`, `net`, `net/http`, `time`, 他の `kisage/*` |
| `observation` | 観測の直和型・スナップショット | `model`（+ `LKAL`） | I/O 系標準ライブラリ・上位層 |
| `projection` | LKAL 葉パス射影 $D = \mathrm{Paths}(d)$ | `model`（+ `LKAL`） | I/O 系標準ライブラリ・上位層 |
| `scope` | セグメント単位 Scope・4権限 | `model` | I/O 系標準ライブラリ・上位層 |
| `structure` | 構造検査・中間 `Record` 保護 | `model`, `observation`（+ `LKAL`） | I/O 系標準ライブラリ・上位層 |
| `ownership` | 共有所有ストア $\sigma$ と $\sigma_A$・JSON 符号化 | `model`, `observation`, `projection`（+ `LKAL`） | `os`, `os/exec`, `syscall`, `net`, `net/http`, `time`, 上位層 |
| `plan` | 純粋計画 5 段階パイプライン（`Plan`） | `model`, `observation`, `projection`, `scope`, `structure`, `ownership`（+ `LKAL`） | `os`, `os/exec`, `syscall`, `net`, `net/http`, `time`, `provider`, `protocol`, `cli`, `mcp`, `host`, `cmd` |
| `provider` | Linux LKAL ファイル Provider・`os.Root`・ロック（第2層） | `model`, `observation`, `structure`, `ownership`, `plan`（+ `LKAL`） | `protocol`, `verification`, `cli`, `mcp`, `host`, `cmd` |
| `protocol` | バッチ永続化・ファイルリポ・回復・隔離（第2層） | `model`, `observation`, `projection`, `scope`, `structure`, `ownership`, `plan`, `provider` | `verification`, `cli`, `mcp`, `host`, `cmd` |
| `verification` | Lean/Go DRT・性質検査・定理検証 | `model`〜`protocol`（+ `LKAL`） | `cli`, `mcp`, `host`, `cmd` |
| `cli` | CLI 実行・結果集約・終了コード（第3層入口） | `model`〜`protocol`（+ `LKAL`） | `mcp`, `host`, `verification`, `cmd` |
| `mcp` | JSON-RPC stdio サーバー（`io.Reader`/`io.Writer`） | `model`, `plan`, `cli` | `os`, `os/exec`, `syscall`, `provider`, `protocol`, `host`, `verification`, `cmd` |
| `host` | `0600` `host.json` 信頼境界束縛（第3層） | `model`, `scope`, `ownership`, `provider`, `protocol`, `cli` | `mcp`, `verification`, `cmd` |
| `cmd/kisage` | Composition Root（バイナリ入口） | `cli`, `host`, `mcp` | `examples/*` |
| `examples/*` | 独立デモ（`healthcheck` 等） | `host`, `provider`（+ `LKAL`） | コアパッケージから `examples/*` への逆参照禁止 |

## 2. 検証コマンド（Run → Fix → Re-run）

```bash
# リポジトリ全体の検証
python3 .claude/skills/layer-architecture/scripts/check_layers.py

# 特定パッケージ・単一ファイル・変更ファイルのみの検証
python3 .claude/skills/layer-architecture/scripts/check_layers.py plan
python3 .claude/skills/layer-architecture/scripts/check_layers.py --file plan/pipeline.go
python3 .claude/skills/layer-architecture/scripts/check_layers.py --changed
```

`VIOLATION` が出た場合は直後の `FIX` 行に従って依存を内側・適切な境界へ移動し、再実行して 0 件にする。
