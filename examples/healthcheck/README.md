# kisageで設定するURL死活監視

開発用APIや社内サービスをまとめて確認する、小さなCLIの作例です。各URLへGETし、期待するHTTPステータスと応答時間を表示します。監視先の追加・変更・削除をkisageで管理し、端末ごとのタイムアウト設定は手元に残します。

```text
OK   api              HTTP 200 (1ms)
FAIL worker           HTTP 503, want 200 (0s)
1/2 healthy
```

Linuxと、このリポジトリをビルドできるGo環境で動きます。追加のGo依存はありません。

## まず動かす

リポジトリのルートで実行します。

```sh
go build -o /tmp/kisage ./cmd/kisage
go build -o /tmp/kisage-healthcheck ./examples/healthcheck

# 新しいディレクトリを指定。既存ディレクトリへの上書きは拒否します。
/tmp/kisage-healthcheck init /tmp/kisage-healthcheck-demo
export KISAGE_HOST_CONFIG=/tmp/kisage-healthcheck-demo/host.json

# 宣言を確認して適用。
/tmp/kisage plan /tmp/kisage-healthcheck-demo/desired.lkal
/tmp/kisage apply /tmp/kisage-healthcheck-demo/desired.lkal
```

別のターミナルでデモ用APIを起動します。`/health`は200、`/unhealthy`は503を返します。

```sh
go run ./examples/healthcheck/demo-server
```

最初のターミナルで監視します。

```sh
/tmp/kisage-healthcheck -config /tmp/kisage-healthcheck-demo/config.lkal
```

冒頭のように`api`が成功、`worker`が失敗します。デモ用APIの停止はCtrl+Cです。18080番ポートが使用中なら、`demo-server -listen 127.0.0.1:18081`で起動し、宣言のURLを変更して再適用します。

## 普段使いする

初期化先の`desired.lkal`を、自分のサービスに合わせて編集します。リポジトリ内の同名ファイルは初期化用の雛形です。

```lkal
@lkal "0.1"
checks {
  api {
    url = "http://127.0.0.1:8080/health"
    status = 200
  }
  web {
    url = "http://127.0.0.1:3000/"
    status = 200
  }
}
```

編集後に同じ`plan`と`apply`を実行します。監視CLIは実行のたびに適用済みの`config.lkal`を読みます。cronからの定期実行にも使えます。

| 終了コード | 意味 |
| :--- | :--- |
| 0 | 全監視先が期待するHTTPステータスを返した |
| 1 | ステータス不一致・タイムアウト・接続失敗がある |
| 2 | 引数・設定・初期化のエラー |

HTTP/HTTPSに対応し、リダイレクトは追跡せず、その応答コードを判定します。監視は記載順に1回ずつ行います。応答本文の内容は検査しません。`status`は100〜599の整数、`local/timeout_ms`は1〜60000の整数です。URLや値の業務的な検証は監視CLIが行い、kisageは宣言された値の照合と適用を行います。

## kisageの動きを試す

**再適用は変更なし。** 適用後に次を実行すると、差分なしで終了コード0になります。

```sh
/tmp/kisage plan /tmp/kisage-healthcheck-demo/desired.lkal -detailed-exitcode
```

**手元の設定を残す。** `config.lkal`の`local/timeout_ms`を`1500`などに直接変更してから、監視先を変更して`apply`しても、その値とコメントは残ります。ホスト設定がkisageに許可する変更範囲は`/checks`配下だけです。

**削除は明示する。** `desired.lkal`から監視先のブロックを丸ごと除いても、`apply`は適用済みの監視先を削除しません。`plan`で確認してから`prune`すると、kisageが作成した退役済みの葉が削除されます。宣言には少なくとも1件の監視先を残してください。

```sh
/tmp/kisage plan /tmp/kisage-healthcheck-demo/desired.lkal
/tmp/kisage prune /tmp/kisage-healthcheck-demo/desired.lkal
```

**外部変更を検出する。** 適用済みの`config.lkal`で`checks/api/url`を直接変更すると、次の`apply`は`K203`で停止し、ファイルへの通常変更を抑止します。`explain`で状態を確認できます。試す場合は最後に行ってください。

```sh
/tmp/kisage explain /tmp/kisage-healthcheck-demo/desired.lkal /checks/api/url
```

`init`は専用ディレクトリとストア・ロックを0700、設定ファイルを0600で作り、絶対パスと実際のデバイスIDを`host.json`に登録します。初期化先の親ディレクトリは事前に存在している必要があります。この作例は手元の利用者がホスト設定を管理する想定です。ホスト設定の信頼境界と本来の運用手順は[CLIドキュメント](../../docs/cli.md)を参照してください。

## 検証

```sh
go test ./examples/healthcheck/...
```

外部ネットワークを使わず、ローカルHTTPサーバーで成功・503・リダイレクト・タイムアウト・接続失敗を検査します。実ファイルと所有ストアを使った統合テストで、読み取り専用plan・apply後の収束・再適用・ローカル設定とコメントの保存・prune・Scope外への変更拒否・ドリフト時の書き込み抑止も確認します。
