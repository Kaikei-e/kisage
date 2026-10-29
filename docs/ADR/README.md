# Architecture Decision Records (kisage)

本ディレクトリは、[kisage 設計憲章](../PRINCIPLE.md) および構想書（ドラフト v7）に基づく Phase 0 の規範的アーキテクチャ決定記録（ADR）を管理する。実装に必要な規則は ADR 本文と [必須反例サンプル #1〜#60](../samples/required-counterexamples.md) に保持し、Git 管理外の構想書・フィードバック・会話履歴への参照を前提にしない。

文書の型付き有向非巡回グラフ（DAG）と必須セクション構造は [`../../docdag.yaml`](../../docdag.yaml) に従い、`docdag validate` および `docdag lint` によって機械検証される。これらは内容の安全性・実装・証明・反例試験の完了を検証するものではない。各 ADR は `proposed` として起票しており、採否は原案者が判断する。採用後の `accepted` も抽象的な設計決定の採用を表すにとどまり、[0005 の Phase 0 実装前決定事項](0005-cli-mcp-result-aggregation-and-verification-gates.md#4-60件の必須反例スイートと開発フェーズゲート) にある具体方式の未確定を完了扱いしない。

## 読む順序と依存構造

1. **[0001 — LKAL葉パス射影と観測の直和型](0001-leaf-path-projection-and-observation-sum-type.md)**
   基礎となるデータモデル。LKAL 文書から葉パス集合 $D = \mathrm{Paths}(d)$ への射影規則、非ルート空 `Record` の拒否（`K102`）と空ルート宣言 $D = \varnothing$ の許可、存在の3値とノード種別を分離した観測型 `Observed = Unknown | Absent | Present(Leaf | Record | Opaque)`、および中間 `Record` の保護規則を定める。
2. **[0002 — 共有所有ストア・4種操作権限・3層保証境界](0002-shared-ownership-store-scope-and-trust-boundary.md)**（depends-on: `0001`）
   マルチ Actor 環境での共有所有ストア $\sigma$ と自 Actor 射影 $\sigma_A$ の分離、照合対象集合 $M_A(d, \sigma) = D \cup \mathrm{dom}(\sigma_A)$、単一所有者排他と `Origin = None | Created | Modified` の履歴不変条件（および値同値 ABA の限界）、`Set / Delete / Own / Resolve` の4権限、3層保証境界と信頼の根を定める。
3. **[0003 — 純粋計画関数の5段階パイプライン・Apply/Prune分離・明示的競合解消](0003-pure-plan-pipeline-mode-separation-and-explicit-resolution.md)**（depends-on: `0001`, `0002`）
   純粋計画関数 `Plan` を `PureRecover → Classify → SelectMode → Authorize → Gate` の5段階パイプラインとして定義し、`Apply` と `Prune` の分離、モード外の異常を `Deferred` とする `SelectMode` 規範表、退役 `Modified` の解除待ち（`DeferredUntilRelease`）の優先順位、モード内阻害要因による全件抑止ゲート、実環境を書き換えない明示的な記録解消コマンド `adopt` / `release`、および版間の構造移行（葉↔Record）の非自動化を定める。
4. **[0004 — ファイル内バッチ永続化・Stabilize回復・ロック内契約検証とProvider契約](0004-file-batch-persistence-stabilize-recovery-and-provider-contract.md)**（depends-on: `0001`, `0002`, `0003`）
   揮発的な `ExecutionBatch` と環境操作のみを永続化する `DurableIntent` の分離、ロック順序（$\sigma \to \text{Resource}$）を統一した7段階バッチ手順、所有 Commit 前のロック内契約検証と違反時の Resource 隔離（`K503`）、観測スナップショットを耐久化する `Provider.Stabilize` を前提としたクラッシュ回復、および Linux ローカル LKAL ファイル Provider の実装契約を定める。
5. **[0005 — CLI/MCPインターフェース・フェーズ別結果集約・Lean 4形式検証と60反例ゲート](0005-cli-mcp-result-aggregation-and-verification-gates.md)**（depends-on: `0002`, `0003`, `0004`）
   CLI / MCP の6操作インターフェース、終了コード（`plan -detailed-exitcode` を含む）、`recovery` と `normalExecution` を分離した JSON 出力スキーマと `summary.execution` の決定論的集約表、診断コード `K102`〜`K604`、Lean 4 で証明する `Plan` 安全性11定理・プロトコル不変条件・有限実行収束、および [60件の必須反例サンプル](../samples/required-counterexamples.md) と Phase 0〜5 の完了ゲートを定める。

## 規範の変更と適合性

- 実装・形式モデル・テストは、ADR と対応サンプルの期待値を両方満たすこと。矛盾は検証ゲート未達として解消する。
- サンプル番号は回帰テスト識別子として固定し、期待値の変更は対応 ADR の変更と同時に理由を記録する。
- 要約・分割・移動の際も、モード分類、必要権限、条件トークン・所有版、ロック、耐久化、失敗・結果不明時の規則、収束の前提を省略しない。

## 検証コマンド

```sh
docdag validate
docdag lint
docdag query --binding
docdag stats
```

