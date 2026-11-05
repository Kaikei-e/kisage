# HTTP Health Checker Configured with kisage / kisageで設定するURL死活監視CLI

**[English](#english)** | **[日本語](#japanese)**

---

<a id="english"></a>

## English

A lightweight HTTP health-checker CLI (`kisage-healthcheck`) that sends `GET` requests to a list of endpoints and verifies that each returns the expected HTTP status code.

This example demonstrates how **kisage** reconciles shared endpoint definitions (`/checks/*`) from a declaration file into a live configuration file while strictly preserving machine-local preferences (`/local/timeout_ms`) and comments.

```text
OK   api              HTTP 200 (1ms)
FAIL worker           HTTP 503, want 200 (0s)
1/2 healthy
```

> **Requirements:** Linux and a Go toolchain capable of building this repository. No external network access or third-party services are required.

### How the Files Fit Together

Running `kisage-healthcheck init /tmp/kisage-healthcheck-demo` creates an isolated sandbox directory containing the following files:

| Path in Sandbox | Role | Who Manages It? |
| :--- | :--- | :--- |
| `desired.lkal` | **Declaration file** (desired state for `/checks/*`) | **You** edit this file and pass it to `kisage` |
| `config.lkal` | **Target runtime config** read by `kisage-healthcheck` | `/local/*` is edited locally; `/checks/*` is reconciled by **kisage** |
| `host.json` | **Protected host policy (`0600`)** binding `desired.lkal` $\leftrightarrow$ `config.lkal` and restricting `Scope` to `/checks` (`Set`, `Own`, `Delete`) | Created by `init` (`KISAGE_HOST_CONFIG`) |
| `state/`, `locks/` | **Shared ownership store ($\sigma$)** and lock directory (`0700`) | **kisage** |

### 1. Quick Start

Run the following from the repository root:

```sh
# 1. Build kisage and the example CLI
go build -o /tmp/kisage ./cmd/kisage
go build -o /tmp/kisage-healthcheck ./examples/healthcheck

# 2. Provision a fresh sandbox directory (refuses to overwrite an existing directory)
/tmp/kisage-healthcheck init /tmp/kisage-healthcheck-demo
export KISAGE_HOST_CONFIG=/tmp/kisage-healthcheck-demo/host.json
```

Before running `kisage`, compare the two LKAL files inside `/tmp/kisage-healthcheck-demo`:

- **`config.lkal` (Initial target file — has only local timeout settings):**
  ```lkal
  @lkal "0.1"
  ### Local preferences are outside kisage's scope. Keep this comment too.
  local {
    timeout_ms = 2000
  }
  ```
- **`desired.lkal` (Declaration file — defines the endpoints to check):**
  ```lkal
  @lkal "0.1"
  ### kisage manages only the checks subtree.
  checks {
    api {
      url = "http://127.0.0.1:18080/health"
      status = 200
    }
    worker {
      url = "http://127.0.0.1:18080/unhealthy"
      status = 200
    }
  }
  ```

Now preview and apply the declaration with `kisage`:

```sh
/tmp/kisage plan /tmp/kisage-healthcheck-demo/desired.lkal
/tmp/kisage apply /tmp/kisage-healthcheck-demo/desired.lkal
```

Inspect `/tmp/kisage-healthcheck-demo/config.lkal` after `apply`—`checks` has been merged in while `local` and its `###` comment remain intact:

```lkal
@lkal "0.1"

### Local preferences are outside kisage's scope. Keep this comment too.
local {
  timeout_ms = 2000
}
checks {
  api {
    status = 200
    url = "http://127.0.0.1:18080/health"
  }
  worker {
    status = 200
    url = "http://127.0.0.1:18080/unhealthy"
  }
}
```

In a **second terminal**, start the local demo server (`/health` returns `200`, `/unhealthy` returns `503`):

```sh
go run ./examples/healthcheck/demo-server
```

Back in the **first terminal**, run the health checker against `config.lkal`:

```sh
/tmp/kisage-healthcheck -config /tmp/kisage-healthcheck-demo/config.lkal
```

`api` succeeds (`HTTP 200`) and `worker` fails (`HTTP 503, want 200`), exiting with code `1`. (Stop `demo-server` anytime with `Ctrl+C`. If port `18080` is already in use, start `demo-server -listen 127.0.0.1:18081`, update the URLs in `desired.lkal`, and re-run `apply`.)

### 2. Hands-On: Exploring How kisage Works

Try these 5 scenarios in order using the sandbox created above.

#### Scenario A: Idempotent Re-apply (`NoOp`)

Once converged, running `plan` or `apply` again performs no writes. With `-detailed-exitcode`, `plan` returns `0` when there are no diffs (and `2` when changes or deferred actions exist):

```sh
/tmp/kisage plan /tmp/kisage-healthcheck-demo/desired.lkal -detailed-exitcode
```

#### Scenario B: Preserving Local Settings While Updating Endpoints

Change the local timeout directly in `config.lkal` (`2000` $\rightarrow$ `1500`), and update `worker`'s expected status to `503` in `desired.lkal`:

```sh
# Local edit in config.lkal (outside /checks)
sed -i 's/timeout_ms = 2000/timeout_ms = 1500/' /tmp/kisage-healthcheck-demo/config.lkal

# Declarative edit in desired.lkal (inside /checks)
cat << 'EOF' > /tmp/kisage-healthcheck-demo/desired.lkal
@lkal "0.1"
checks {
  api {
    url = "http://127.0.0.1:18080/health"
    status = 200
  }
  worker {
    url = "http://127.0.0.1:18080/unhealthy"
    status = 503
  }
}
EOF

/tmp/kisage apply /tmp/kisage-healthcheck-demo/desired.lkal
/tmp/kisage-healthcheck -config /tmp/kisage-healthcheck-demo/config.lkal
```

Both endpoints now report `OK` (`2/2 healthy`), and `cat /tmp/kisage-healthcheck-demo/config.lkal` confirms that `timeout_ms = 1500` and the `###` comment were preserved.

#### Scenario C: Explicit Deletion (`apply` vs. `prune`)

Remove the `worker` block from `desired.lkal` (leaving at least one endpoint in `checks`):

```sh
cat << 'EOF' > /tmp/kisage-healthcheck-demo/desired.lkal
@lkal "0.1"
checks {
  api {
    url = "http://127.0.0.1:18080/health"
    status = 200
  }
}
EOF
```

1. **Run `apply` first:**
   ```sh
   /tmp/kisage apply /tmp/kisage-healthcheck-demo/desired.lkal
   ```
   `apply` **never deletes** environment values—it keeps `/checks/worker/*` in `config.lkal` and emits notice `K604` indicating that `prune` is required to delete retired `Origin = Created` leaves.
2. **Preview with `plan` and delete with `prune`:**
   ```sh
   /tmp/kisage plan /tmp/kisage-healthcheck-demo/desired.lkal
   /tmp/kisage prune /tmp/kisage-healthcheck-demo/desired.lkal
   /tmp/kisage-healthcheck -config /tmp/kisage-healthcheck-demo/config.lkal
   ```
   `prune` removes `/checks/worker/url` and `/checks/worker/status` (leaving an empty `worker {}` record container in `config.lkal`, as kisage does not auto-delete ancestor records), and `kisage-healthcheck` now checks only `api` (`1/1 healthy`).

#### Scenario D: Rejecting Out-of-Scope Declarations (`K401`)

The host policy (`host.json`) grants `kisage` permissions only on `/checks`. If `desired.lkal` attempts to add a setting under `/local` (such as `/local/label`), `kisage` blocks the operation with `K401` (`PolicyDenied` — `granted: None, missing: Set+Own`) and leaves `config.lkal` untouched (similarly, attempting to overwrite the existing unowned `/local/timeout_ms` is blocked with `K206`):

```sh
cat << 'EOF' > /tmp/kisage-healthcheck-demo/desired.lkal
@lkal "0.1"
local {
  label = "out-of-scope"
}
checks {
  api {
    url = "http://127.0.0.1:18080/health"
    status = 200
  }
}
EOF

/tmp/kisage apply /tmp/kisage-healthcheck-demo/desired.lkal
```

Remove the `local { ... }` block from `desired.lkal` before continuing to Scenario E.

#### Scenario E: Fail-Closed Drift Detection (`K203`) and `explain`

If someone manually edits a `kisage`-owned leaf directly in `config.lkal`, `kisage` detects that the live value has drifted from `LastApplied` in the ownership store:

```sh
# Restore clean desired.lkal, then simulate an out-of-band manual edit on config.lkal
cat << 'EOF' > /tmp/kisage-healthcheck-demo/desired.lkal
@lkal "0.1"
checks {
  api {
    url = "http://127.0.0.1:18080/health"
    status = 200
  }
}
EOF
sed -i 's|/health|/manual|' /tmp/kisage-healthcheck-demo/config.lkal

# apply halts with K203 instead of overwriting the manual edit
/tmp/kisage apply /tmp/kisage-healthcheck-demo/desired.lkal

# Inspect ownership, observed value, conditionToken (--expect), and next action
/tmp/kisage explain /tmp/kisage-healthcheck-demo/desired.lkal /checks/api/url
```

### 3. Everyday Usage & CLI Reference

To check your own services (e.g., from a terminal or `cron`), edit `desired.lkal` in your initialized directory, run `kisage plan` / `kisage apply`, and execute `kisage-healthcheck -config /path/to/config.lkal`.

| Exit Code | Meaning |
| :--- | :--- |
| `0` | All configured endpoints returned their expected HTTP status code |
| `1` | One or more endpoints returned an unexpected status, timed out, or failed to connect |
| `2` | CLI usage, configuration validation, or sandbox initialization error |

- **Validation & Policy vs. Mechanism:** `kisage` reconciles LKAL leaf values; application-level validation belongs to `kisage-healthcheck`:
  - `checks/<name>/url`: Absolute `http://` or `https://` URL.
  - `checks/<name>/status`: Integer in `100..599`.
  - `local/timeout_ms`: Integer in `1..60000` (milliseconds).
  - Requests are sent sequentially in file order. Redirects (`3xx`) are **not** followed automatically so redirect responses themselves can be asserted. Response bodies are discarded (up to 4 KiB read).
- **Trust Boundary Note:** `init` creates the sandbox directory, `state/`, and `locks/` with mode `0700` and configuration files with `0600`, recording absolute paths and the target file's `deviceId` in `host.json`. This example assumes a single local user manages the sandbox; for production trust boundaries and multi-actor/MCP operation, see [`docs/cli.md`](../../docs/cli.md).

### 4. Running Tests

```sh
go test -v ./examples/healthcheck/...
```

Runs completely offline using local `httptest` servers and a real `kisage` file store:

- **HTTP behavior (`TestHTTPResults`):** `204` success, `503` mismatch, redirect (`302`) observation and assertion, request timeout, and connection refusal.
- **Consumer validation (`TestInvalidConfiguration`):** Out-of-range timeouts/statuses, wrong types, non-HTTP(S) schemes (`file://`), relative URLs, unknown fields, and unapplied/empty endpoint configs.
- **End-to-end kisage lifecycle (`TestKisageLifecycle`):** Sandbox overwrite protection, read-only `plan`, initial `apply` convergence, idempotent re-apply, preserving `local/timeout_ms` and comments across updates, `apply` vs. `prune` retirement, `K401` out-of-scope rejection, and `K203` drift write suppression.

---

<a id="japanese"></a>

## 日本語

開発用APIや社内サービスの死活をまとめて確認する、小さなHTTP監視CLI（`kisage-healthcheck`）の作例です。各URLへ`GET`リクエストを送り、期待するHTTPステータスコードと応答時間を表示します。

このサンプルでは、**「端末ごとに手元で変えたい設定（`/local/timeout_ms`やコメント）」を壊さずに、「共有したい監視先の一覧（`/checks/*`）」だけをkisageで安全に追加・更新・削除する流れ**を体験できます。

```text
OK   api              HTTP 200 (1ms)
FAIL worker           HTTP 503, want 200 (0s)
1/2 healthy
```

> **動作環境:** Linux と、このリポジトリをビルドできる Go 環境で動作します。外部ネットワーク通信や追加の依存パッケージは不要です。

### ファイル構成と役割

`kisage-healthcheck init /tmp/kisage-healthcheck-demo` を実行すると、専用のサンドボックスディレクトリに以下のファイルが作成されます。

| サンドボックス内のパス | 役割 | 管理主体 |
| :--- | :--- | :--- |
| `desired.lkal` | **宣言ファイル（目標状態）**: 監視したい `/checks/*` を記述する | **利用者**が編集し、`kisage` に渡す |
| `config.lkal` | **実環境の設定ファイル（適用先）**: 監視CLIが読み込むファイル | `/local/*` は手元で直接編集し、`/checks/*` は **kisage** が反映する |
| `host.json` | **ホスト設定（`0600`）**: `desired.lkal` $\leftrightarrow$ `config.lkal` の対応と、kisageの変更許可範囲（`/checks` 配下の `Set`, `Own`, `Delete`）を定義 | `init` が生成（`KISAGE_HOST_CONFIG` で指定） |
| `state/`, `locks/` | **所有ストア（$\sigma$）とロック用ディレクトリ（`0700`）**: kisageが作成・更新した葉パスの履歴を記録 | **kisage** |

### 1. まず動かす（クイックスタート）

リポジトリのルートで以下を実行します。

```sh
# 1. kisage 本体とサンプル CLI をビルド
go build -o /tmp/kisage ./cmd/kisage
go build -o /tmp/kisage-healthcheck ./examples/healthcheck

# 2. 新しいサンドボックスディレクトリを初期化（既存ディレクトリへの上書きは拒否されます）
/tmp/kisage-healthcheck init /tmp/kisage-healthcheck-demo
export KISAGE_HOST_CONFIG=/tmp/kisage-healthcheck-demo/host.json
```

`kisage` を実行する前に、初期状態の2つの `.lkal` ファイルを見比べてみてください。

- **`config.lkal`（適用先ファイルの初期状態 — 手元のタイムアウト設定だけがある）:**
  ```lkal
  @lkal "0.1"
  ### Local preferences are outside kisage's scope. Keep this comment too.
  local {
    timeout_ms = 2000
  }
  ```
- **`desired.lkal`（宣言ファイル — kisage で反映したい監視先が書かれている）:**
  ```lkal
  @lkal "0.1"
  ### kisage manages only the checks subtree.
  checks {
    api {
      url = "http://127.0.0.1:18080/health"
      status = 200
    }
    worker {
      url = "http://127.0.0.1:18080/unhealthy"
      status = 200
    }
  }
  ```

`kisage plan` で差分を確認し、`kisage apply` で `config.lkal` に反映します。

```sh
/tmp/kisage plan /tmp/kisage-healthcheck-demo/desired.lkal
/tmp/kisage apply /tmp/kisage-healthcheck-demo/desired.lkal
```

適用後の `/tmp/kisage-healthcheck-demo/config.lkal` を見ると、`local` の設定値と `###` コメントがそのまま残った状態で、`checks` ブロックが追加されています。

```lkal
@lkal "0.1"

### Local preferences are outside kisage's scope. Keep this comment too.
local {
  timeout_ms = 2000
}
checks {
  api {
    status = 200
    url = "http://127.0.0.1:18080/health"
  }
  worker {
    status = 200
    url = "http://127.0.0.1:18080/unhealthy"
  }
}
```

**別のターミナル**を開き、ローカルのデモ用APIサーバーを起動します（`/health` は `200`、`/unhealthy` は `503` を返します）。

```sh
go run ./examples/healthcheck/demo-server
```

**元のターミナル**に戻り、適用済みの `config.lkal` を使って死活監視を実行します。

```sh
/tmp/kisage-healthcheck -config /tmp/kisage-healthcheck-demo/config.lkal
```

冒頭の出力例のように `api` が成功（`OK`）、`worker` が失敗（`FAIL`）となり、終了コード `1` が返ります。（デモ用APIの停止は `Ctrl+C` です。`18080` 番ポートが使用中の場合は `demo-server -listen 127.0.0.1:18081` で起動し、`desired.lkal` のURLを書き換えて再度 `apply` してください。）

### 2. ステップで試す kisage の5つの特徴

上のクイックスタートで作った環境を使って、kisageの代表的な安全機構を順番に試せます。

#### シナリオA: 再適用しても無駄な書き込みをしない（べき等性）

すでに目標状態へ収束しているため、もう一度 `plan` や `apply` を実行してもファイルは書き換わりません。`-detailed-exitcode` を付けると、差分なしなら終了コード `0`（差分や保留があれば `2`）になります。

```sh
/tmp/kisage plan /tmp/kisage-healthcheck-demo/desired.lkal -detailed-exitcode
```

#### シナリオB: 手元の設定を残したまま、監視先の条件を更新する

`config.lkal` の `local/timeout_ms` を手元で `1500` に直接書き換えたあと、`desired.lkal` で `worker` の期待ステータスを `503` に変更して `apply` してみます。

```sh
# 1. config.lkal の手元設定（/checks 外）を直接編集
sed -i 's/timeout_ms = 2000/timeout_ms = 1500/' /tmp/kisage-healthcheck-demo/config.lkal

# 2. desired.lkal の監視設定（/checks 内）を更新
cat << 'EOF' > /tmp/kisage-healthcheck-demo/desired.lkal
@lkal "0.1"
checks {
  api {
    url = "http://127.0.0.1:18080/health"
    status = 200
  }
  worker {
    url = "http://127.0.0.1:18080/unhealthy"
    status = 503
  }
}
EOF

# 3. 反映して死活監視を再実行
/tmp/kisage apply /tmp/kisage-healthcheck-demo/desired.lkal
/tmp/kisage-healthcheck -config /tmp/kisage-healthcheck-demo/config.lkal
```

今度は `2/2 healthy`（終了コード `0`）になります。`cat /tmp/kisage-healthcheck-demo/config.lkal` で確認すると、直接書き換えた `timeout_ms = 1500` と `###` コメントがそのまま保たれています。

#### シナリオC: 誤削除を防ぐ2段階の退役削除（`apply` と `prune`）

`desired.lkal` から `worker` ブロックを削除し、`api` のみにしてみます（監視先は最低1件残してください）。

```sh
cat << 'EOF' > /tmp/kisage-healthcheck-demo/desired.lkal
@lkal "0.1"
checks {
  api {
    url = "http://127.0.0.1:18080/health"
    status = 200
  }
}
EOF
```

1. **まず `apply` を実行する:**
   ```sh
   /tmp/kisage apply /tmp/kisage-healthcheck-demo/desired.lkal
   ```
   `apply` は「作成と更新」専用のモードであり、**実環境から値を削除しません**。代わりに「退役した `Origin = Created` の葉を削除するには `prune` が必要（`K604`）」と通知し、`config.lkal` の `worker` はまだ残ります。
2. **`plan` で確認してから `prune` で明示的に削除する:**
   ```sh
   /tmp/kisage plan /tmp/kisage-healthcheck-demo/desired.lkal
   /tmp/kisage prune /tmp/kisage-healthcheck-demo/desired.lkal
   /tmp/kisage-healthcheck -config /tmp/kisage-healthcheck-demo/config.lkal
   ```
   `prune` によって kisage が作成した `/checks/worker/url` と `/checks/worker/status` の葉が削除され（kisage は空になった親レコード `worker {}` を自動消去しない設計のため空ブロックのみ残ります）、死活監視を実行すると `api` のみが検査されて `1/1 healthy` になります。

#### シナリオD: 許可範囲（`Scope`）外の変更を拒否する（`K401`）

`host.json` が kisage に許可している変更範囲（`Scope`）は `/checks` 配下のみです。もし `desired.lkal` に `local { label = "out-of-scope" }` を書いて許可範囲外の設定を追加しようとすると、`K401`（`PolicyDenied`: `granted: None, missing: Set+Own`）で拒否され、`config.lkal` は一切変更されません（既存の未所有葉 `local/timeout_ms` を別値で上書きしようとした場合も `K206` で停止します）。

```sh
cat << 'EOF' > /tmp/kisage-healthcheck-demo/desired.lkal
@lkal "0.1"
local {
  label = "out-of-scope"
}
checks {
  api {
    url = "http://127.0.0.1:18080/health"
    status = 200
  }
}
EOF

/tmp/kisage apply /tmp/kisage-healthcheck-demo/desired.lkal
```

次のシナリオEに進む前に、`desired.lkal` から `local { ... }` ブロックを取り除いておきます。

#### シナリオE: 手動書き換え（ドリフト）を検出して安全停止する（`K203` と `explain`）

kisage が管理している `/checks/api/url` を、誰かが `config.lkal` 側で直接書き換えてしまった場合を試します。

```sh
# desired.lkal を正常な状態に戻した上で、config.lkal 側の URL を手動で書き換える
cat << 'EOF' > /tmp/kisage-healthcheck-demo/desired.lkal
@lkal "0.1"
checks {
  api {
    url = "http://127.0.0.1:18080/health"
    status = 200
  }
}
EOF
sed -i 's|/health|/manual|' /tmp/kisage-healthcheck-demo/config.lkal

# ドリフト（K203）を検出し、推測で上書きせず停止する
/tmp/kisage apply /tmp/kisage-healthcheck-demo/desired.lkal

# 観測値・所有状態・トークン・必要な対処（nextAction）を確認する
/tmp/kisage explain /tmp/kisage-healthcheck-demo/desired.lkal /checks/api/url
```

### 3. 普段使いと仕様リファレンス

自分の開発環境や社内サービスの監視に使う場合は、初期化先の `desired.lkal` を編集して `kisage plan` / `kisage apply` を実行し、`kisage-healthcheck -config /path/to/config.lkal` を実行します（`cron` からの定期実行にもそのまま使えます）。

| 終了コード | 意味 |
| :--- | :--- |
| `0` | すべての監視先が期待するHTTPステータスコードを返した |
| `1` | ステータス不一致・タイムアウト・接続失敗のいずれかが発生した |
| `2` | コマンド引数・設定ファイルの検証・初期化のエラー |

- **入力検証と責務の分離（Policy vs. Mechanism）:** `kisage` は宣言された LKAL 葉パスの照合と反映（機構）を担い、設定値の業務的な妥当性検査（ポリシー）は監視CLI側で行います。
  - `checks/<name>/url`: `http://` または `https://` の絶対URL
  - `checks/<name>/status`: `100`〜`599` の整数
  - `local/timeout_ms`: `1`〜`60000` の整数（ミリ秒）
  - 監視は記載順に1回ずつ行います。リダイレクト（`3xx`）は自動追跡せず、その応答コード自体を判定します。応答本文の内容は検査しません。
- **信頼境界とサンドボックスについて:** `init` は専用ディレクトリと `state/`・`locks/` を `0700`、設定ファイルを `0600` で作成し、絶対パスと実ファイルの `deviceId` を `host.json` に登録します。このサンプルは手元の利用者が1人でホスト設定を管理する想定です。本来の運用における信頼境界や MCP サーバーとしての起動手順は [`docs/cli.md`](../../docs/cli.md) を参照してください。

### 4. テストの実行

```sh
go test -v ./examples/healthcheck/...
```

外部ネットワークを使わず、ローカルHTTPサーバーと実ファイル・所有ストアを使って以下を検証します。

- **HTTP判定テスト（`TestHTTPResults`）:** 正常応答（`204`）・ステータス不一致（`503`）・リダイレクト（`302`）の観測と期待値判定・タイムアウト・接続失敗
- **設定検証テスト（`TestInvalidConfiguration`）:** タイムアウトやステータスの範囲外・型不一致・非HTTP(S)スキーム（`file://`）・相対URL・未知フィールド・未適用や空の監視先設定の拒否
- **kisage統合ライフサイクルテスト（`TestKisageLifecycle`）:** 既存ディレクトリの上書き拒否・読み取り専用の `plan`・`apply` による収束・再適用のべき等性・`local/timeout_ms` とコメントの保存・`apply` と `prune` の分離および `prune` 後の監視実行・`Scope` 外変更の拒否（`K401`）・ドリフト発生時の書き込み抑止（`K203`）
