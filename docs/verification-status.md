# 検証状況

2026-10-30 時点。文書構造、Go テスト、Lean 証明、電源断耐久性は別の検査である。

| 対象 | 実装・確認済みの範囲 | 未完了 |
| :--- | :--- | :--- |
| 計画・所有・純粋回復 | Go 参照実装、番号付き反例の回帰テスト、Lean 4 実行可能モデル（`Kisage/Model.lean`, `Kisage/Recover.lean`, `Kisage/Plan.lean`）、純粋 `Plan` 安全性11定理（`Kisage/Theorems.lean`、`sorryAx` ゼロ）、Lean/Go DRT（`Kisage/DRT.lean`, `verification/lean_drt_test.go` による 1,728 組合せの差動検証） | Go 実装 AST に対する直接の演繹的精緻化証明（現状は Lean/Go DRT と適合性検査で橋渡し） |
| 実行プロトコル | 環境 Intent とメタデータ候補の分離、ロック内検証、Stabilize、隔離と停止、Lean 4 の12種の遷移ラベル・9帰納的不変条件と遷移ごとの安全性定理（`Kisage/Protocol.lean`）、新規作成・更新・削除・回復・Gate・他 Actor 保護の機械検証回帰（`Kisage/ProtocolTests.lean`）、有限実行収束・`Apply`/`Prune` 分離・構造移行補題・ESR 候補ランク関数の進捗例での厳密減少証明（`Kisage/Convergence.lean`） | 対象1パスへの射影モデルから複数パスのストア全体への精緻化、Phase 5 の ESR 本採用（ADR 0005 §3.3 の候補ランク関数のみ形式化済み） |
| LKAL Provider | 原子的置換、兄弟保存の検査、同期失敗の注入、別インスタンスでの再開、メタデータ非対応条件の拒否 | 対応 Linux/FS の確定、電源断・永続化喪失モデルでの検証 |
| 所有ストア | 原子ファイル保存、Pending と型付き LKAL 値、未所有版、隔離表、破損拒否、保存境界の故障注入 | ホストの配置・権限・バックアップ運用、媒体障害時の管理者復旧手順 |
| 実プロセス停止 | Intent 保存後、環境 rename 後・親同期前、所有 Commit rename 後で子プロセスを SIGKILL し、新しい Provider/ストアで再開 | 電源断後のディスク状態の再現。SIGKILL の成功を電源断耐久性の証明としない |
| 外部境界 | `cmd/kisage` のCLI・MCP stdio起動、保護されたホスト設定と既存ファイルストアの束縛（`host/`）、6操作・入力エラーを含むフェーズ別 JSON・終了コード・Actor/Scope検査、別プロセス間の実行・所有継続の統合試験 | ホスト OS による登録簿の保護と直接ファイル編集の遮断 |
| Go の適合性検査器 | 完全な計画比較、明示解消を含む11性質の入力ごとの検査、初期履歴を明示した Created の引継ぎ、観測・Applied検証・耐久化・Commit証跡の欠落／不整合の拒否、有限収束の具体例検査 | 全入力・全遷移についての形式証明は Lean 4 側（`Kisage/*.lean`）で担い、Go 側は DRT および適合性検査器で検証。初期履歴は独立したテスト前提として与え、読み込んだ Created フラグから生成しない |

Lean 4 形式検証（`Kisage/*.lean`）は `lake build` により `sorryAx` なしで機械検証される。`go test -count=1 ./verification/...` は実行前に現在の Lean ソースをビルドし、生成された `.lake/build/bin/kisage-drt` を用いて 1,728 ケースを比較する。バイナリ未作成によるスキップは行わない。比較には分類・権限・候補の有無に加え、環境操作の種別・パス・Expected・Target、メタデータ候補、所有更新の Pending 区分と更新後の記録、回復の分類・仮想所有記録（Manager・Origin・LastApplied・Pending の内容）を含む。Revision・BatchID・条件トークン・診断は Lean の抽象モデルに含まず、Go の適合性検査・回帰試験で別に検証する。

プロトコルでは `Plan` が実際の `gateBatch` を保存し、通常実行は開いた Gate と一致する計画・Intent を要求する。`Created` の新しい履歴証跡は条件付き `Absent → Leaf` 適用時にだけ発行され、Validate・Stabilize 後に確定できる。回復遷移は対象・祖先の現在観測を用いた `pureRecoverPath` と一致し、確認・取消・削除記録の解消を扱う。初期所有記録・重複する他 Actor 予約・非重複の他 Actor Pending は明示入力とし、初期 Created の履歴根拠は記録のフラグから推定せず独立に要求する。

`Kisage/ProtocolTests.lean` は正常な作成・両 Origin の更新・Prune 削除の実行列、適用前 Validate/Commit の拒否、別パスの阻害要因による全件抑止、非空の他 Actor 状態、回復の確認・取消・Unknown/権限拒否、耐久化前の電源断による作成取消を検証する。`TestLean4DifferentialRejectsPayloadMutations` は Adopt の Created 化、Target・Expected・操作種別・パス・メタデータ値・Pending 区分・回復値・残存 Pending の Target の9種の誤変更を DRT が検出することを確認する。プロトコルと Go の対応は障害注入で検査し、Plan の DRT をプロトコル全遷移の差動検証とは扱わない。ESR 本体は ADR 0005 の Phase 5 に属する。

Lean 4 および Go の確認コマンド:

```sh
lake build
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
```

主な追加回帰は `provider/review_regression_test.go`、`protocol/review_regression_test.go`、`protocol/file_repo_test.go`、`ownership/encoding_test.go`、`cli/cli_test.go`、`mcp/mcp_test.go`、`verification/verification_test.go`、各パッケージの `review_regression_test.go`、`cmd/kisage/main_test.go` にある。これらの合格だけで60サンプル全層の網羅や Phase 0〜2 の完了とはしない。
