# CLI / MCP の起動

Linux 用の入口は `cmd/kisage`。リポジトリのルートでビルドする。

```sh
go build -o /tmp/kisage ./cmd/kisage
/tmp/kisage --help
```

## ホスト設定

既定の設定ファイルは `/etc/kisage/host.json`。ホストが注入する `KISAGE_HOST_CONFIG` で変更できる。これは信頼情報であり、エージェントが任意に設定パスや環境変数を変更できる実行境界では使わない。CLI 引数や MCP ツール引数から Actor・Resource・Scope を設定する機能は提供しない。

設定ファイルは root または実行 UID 所有の通常ファイル、権限 `0600`、リンク数1を要求する。経路のシンボリックリンクと、group/other が書き込める直近の親ディレクトリを拒否する。祖先ディレクトリ・設定の注入元・同じ UID からの直接編集を保護する責任は引き続きホストにある。

次の例の絶対パスと `deviceId` を実環境に合わせる。`deviceId` は対象ファイルの `stat -c %d /absolute/path/target.lkal` で確認し、ホストの登録に固定する。inode は原子的置換で変わるため起動ごとの別名検査に使い、継続束縛にはパスとデバイスを使う。

```json
{
  "version": 1,
  "actor": "operator-a",
  "storeId": "shared-store",
  "storeDirectory": "/absolute/private/state",
  "lockDirectory": "/absolute/private/locks",
  "resources": [
    {
      "id": "app",
      "managementRoot": "/absolute/private/managed",
      "targetPath": "/absolute/private/managed/target.lkal",
      "deviceId": 123,
      "declarationFile": "/absolute/declarations/desired.lkal",
      "scope": [
        {"path": "/app", "match": "subtree", "permissions": ["Set", "Own", "Delete"]},
        {"path": "/app/port", "match": "exact", "permissions": ["Resolve"]}
      ]
    }
  ]
}
```

`scope` は `exact`（指定パスのみ）、`subtree`（指定パスと子孫）、`root`（全葉。`path` は `/` または空）を指定する。権限は `Set / Delete / Own / Resolve`。空の Scope は全拒否。未知のフィールド・権限・match・設定版、デバイス不一致、重複 Resource・物理別名を拒否する。

1つの Resource（`targetPath`）に対して `declarationFile` は1対1で固定する。これは、(1) MCP エージェント等が未登録の任意ファイル（`/tmp/...` 等）を指定して対象へ適用することを防ぐ第3層の信頼境界（[ADR 0002 §4](ADR/0002-shared-ownership-store-scope-and-trust-boundary.md)、[ADR 0005 §1](ADR/0005-cli-mcp-result-aggregation-and-verification-gates.md)）であり、かつ (2) 同一 `(Actor, Resource)` に対する宣言を単一の完全な意図集合 $D$ として扱い、複数の部分宣言を交互に適用して他方の所有葉を誤って退役（$\sigma_A \setminus D$）させることを防ぐ不変条件（[ADR 0002 §1](ADR/0002-shared-ownership-store-scope-and-trust-boundary.md)）である。

## 初期化と実行

ホストが先にストアとロックのディレクトリを作り、実行 UID 所有の `0700` にする。対象は既存の通常 LKAL ファイルで、宣言とは別ファイルにする。管理ルート以下は group/other 書込不可とし、Provider の Linux/FS・メタデータ条件を満たす必要がある。

ホスト管理者が一度だけ実行する初期化は次のとおり。既存のストアを上書きせず、通常実行と同じ共有ストアロックを使う。通常実行ではストアの欠落・破損・停止マーカーを空ストアとして扱わない。

```sh
export KISAGE_HOST_CONFIG=/absolute/private/host.json
/tmp/kisage init-store
/tmp/kisage plan /absolute/declarations/desired.lkal -detailed-exitcode -json
/tmp/kisage apply /absolute/declarations/desired.lkal -json
/tmp/kisage prune /absolute/declarations/desired.lkal -json
/tmp/kisage explain /absolute/declarations/desired.lkal /app/port -json
/tmp/kisage adopt /absolute/declarations/desired.lkal /app/port --expect TOKEN --owner-revision REV -json
/tmp/kisage release /absolute/declarations/desired.lkal /app/port --expect TOKEN --owner-revision REV -json
```

`TOKEN` と `REV` は `explain` の現在の出力（JSON では `.explanation.conditionToken` と `.explanation.ownerRevision`）を使う。`init-store` は初期化専用の管理操作で、隔離・PendingConflict・停止状態を解除する復旧機能ではない。また、収束後の対象 `.lkal` ファイルから `.env` 等の外部形式へ値を投影する場合は、自前で LKAL 構文をパースする代わりに `lkal json -plain /absolute/private/managed/target.lkal`（または `lkal get -r`）を用いることで、ブロック構文とラベル圧縮構文の差異を意識せず正規化された JSON として読み取れる。

MCP ホストは同じ保護された環境で `/tmp/kisage mcp` を起動する。stdin/stdout は改行区切り JSON-RPC 専用、起動エラーは stderr に出す。公開ツールは ADR 0005 の6件だけで、初期化機能を公開しない。

## 結果と検証範囲

`-json` は宣言不備や実行開始前の失敗にも結果スキーマを返す。宣言を読めた場合は、パース失敗でもそのバイト列の digest を保持する。読込前の失敗は `declarationDigest: ""`、Resource を選べなかった失敗は `resource: ""` とし、値を捏造せず理由を診断に残す。失敗時の収束は `Unknown`。通常の入力エラーは64、`plan -detailed-exitcode` の入力エラーは1。MCP も同じ文書を `structuredContent` と JSON テキストの双方に返す。

通常のテキスト表示もパス別状態・操作・観測値・目標値・権限・回復候補・診断・次操作を表示する。`plan` の回復は仮想であり、Stabilize と保存を成功済みと扱わない。

Go の履歴検査は、初期 `Created` に独立したテスト履歴を要求し、新規作成の観測・Applied 契約検証・Stabilize・Commit の証跡から履歴を引き継ぐ。ストアの `Created` という値だけを履歴の根拠にしない。これらは実行可能な適合性検査であり、Lean の証明ではない。[検証状況](verification-status.md) と ADR 0005 の未完了ゲートは引き続き適用される。
