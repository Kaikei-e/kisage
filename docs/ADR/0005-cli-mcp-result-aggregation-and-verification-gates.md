---
title: "CLI/MCPインターフェース・フェーズ別結果集約・Lean 4形式検証と60反例ゲート"
status: proposed
date: 2026-10-29
depends-on: ["0002", "0003", "0004"]
---

# 0005: CLI/MCPインターフェース・フェーズ別結果集約・Lean 4形式検証と60反例ゲート

## コンテキスト

[[0002]]・[[0003]]・[[0004]] で定義した中核仕様を、人間およびエージェントが CLI / MCP から決定論的に呼び出し、かつ実装が仕様から逸脱していないことを継続的に検証するためには、次の3点を固定する必要がある。

1. **フェーズ別結果と全体集約の一意性**:
   1回の呼び出しの中で「クラッシュ回復（`recovery`）」と「通常実行（`normalExecution`）」が順に走り、さらに「当該モードの操作完了（`execution`）」と「宣言全体の収束（`convergence`）」は異なる概念である。これらを単一のステータス値に潰すと、「回復は完了したが通常計画は `Blocked`」「環境適用は成功したがストア Commit が未確定（`Indeterminate` vs `PartiallyApplied`）」「`apply` は完了したが `prune` 待ちで未収束」といった状態をエージェントが誤判定する。
2. **形式検証（Lean 4）の優先順位とスコープ設定**:
   v0.1 は常駐デーモンではなく有限回で終了する CLI である。時相論理（TLA 等）を要する常駐ループの Eventually Stable Reconciliation（ESR）証明を最初からゲートにすると開発が停滞する一方、純粋関数 `Plan` の性質だけを証明してもプロトコル側（クラッシュ回復や `Created` の履歴根拠）のバグは防げない。
3. **仕様としての必須反例スイート**:
   v4 から v7 までの設計検証で洗い出された境界条件（60件）を回帰テスト識別子として固定し、Phase 0〜5 の完了ゲートに対応づける必要がある。

## 決定

CLI / MCP の6操作（`plan / apply / prune / explain / adopt / release`）と終了コード規約、`recovery` と `normalExecution` を分離した JSON スキーマおよび `summary.execution` の8段階優先集約表を固定し、Lean 4 による検証を「`Plan` の安全性11定理」「プロトコル状態機械の帰納的不変条件」「限定条件下の有限実行収束（および常駐化時の ESR）」の3段階で構成するとともに、60件の必須反例スイートを Phase 0〜5 の完了ゲートとする。

### 1. CLI コマンドと MCP ツール公開
以下の6操作を CLI サブコマンドおよび対応する MCP ツール（`kisage_plan` / `kisage_explain` / `kisage_apply` / `kisage_prune` / `kisage_adopt` / `kisage_release`）として公開する。

| コマンド | 動作 |
| :--- | :--- |
| `kisage plan FILE [-json] [-detailed-exitcode]` | 読み取り専用。メモリ上で `PureRecover` を合成した仮想 $\sigma$ から `Apply` と `Prune` の計画・回復案・診断を表示 |
| `kisage apply FILE [-json]` | 開始時の宣言スナップショットを読み、回復、作成・更新・許可された記録更新、ロック内再観測を実行。環境削除は行わない |
| `kisage prune FILE [-json]` | 開始時の宣言スナップショットを読み、回復、退役 `Created` 葉の条件付き削除、ロック内再観測を実行。`Set` や不一致採用は行わない |
| `kisage explain FILE PATH [-json]` | 読み取り専用。観測・所有・権限、宣言から計算した `Apply` / `Prune` 各モードの分類と停止理由、条件トークン `TOKEN`・所有版 `REV` を表示 |
| `kisage adopt FILE PATH --expect TOKEN --owner-revision REV [-json]` | 明示的に現在の表現可能な葉を `Modified` として採用。実環境の値は書かない |
| `kisage release FILE PATH --expect TOKEN --owner-revision REV [-json]` | 自所有の確定記録と `Created` の削除権を明示放棄。実環境の値は消さない |

- `plan` は [[0003]] の規則に従い、認可済み回復だけを反映した同じ仮想所有状態から両モードを独立に計算する。`Stabilize`・回復保存を実行せず、それらの成功を仮定した条件付き計画であることを表示する。
- すべてのコマンドは宣言 `FILE` を必須とし、開始時に宣言を一度読み、その呼び出し中のスナップショットとして使用し、ダイジェストを結果に出す。途中の宣言変更は次回実行で扱う。Resource とポリシーは `FILE` に対応する保護された登録から選び、宣言自身に権限を付与させない。
- `adopt` / `release` は [[0003]] の条件トークン・所有版の再検査、`Stabilize`、原子的記録保存を必須とする。宣言は Resource の選択と診断（宣言に残したままの `release` の再管理可能性等）に使い、`adopt` / `release` の可否に宣言との一致は要求しない。
- MCP のツール注釈（`ToolAnnotations`）はクライアントへのヒントであり認可機構ではない。`apply` も既存値の更新により破壊的になりうるため、一律に非破壊とは注釈しない。

### 2. JSON 出力スキーマ・決定論的集約表・終了コード
JSON 出力は `schemaVersion / mode / resource / declarationDigest / recovery / normalExecution / pathResults[] / summary` を持つ。すべてのコマンドが宣言 `FILE` を入力にとるため、`declarationDigest` は省略しない。`recovery` には回復候補・耐久化・保存の結果、`normalExecution` には通常操作の全件抑止とバッチ結果を分けて返す。`summary` は `execution / convergence / counts / diagnostics[]` を持ち、`counts` に環境適用・記録更新・競合・観測不能・権限拒否・削除待ち等の件数を、`diagnostics[]` にコード・パス・理由・必要な次操作を含める。診断の順序はパス順・コード順で固定する。

`plan` は `Apply` と `Prune` の2計画を返すため、`mode = Plan` とし、`normalExecution / pathResults[] / summary` の代わりに `plans.apply` と `plans.prune` がそれぞれ `normalExecution / pathResults[] / summary` を持つ。`recovery`（仮想回復案）は両モードに共通の1つとする。各モードの `summary.execution` は下表をそのモードについて評価する。トップレベルの `summary` は `execution` と `convergence` だけを持ち、`execution` は両モードの `summary.execution` のうち下表で優先順の高い値とする（どちらかが `Blocked` なら `Blocked`、そうでなくどちらかが `Planned` なら `Planned`、両方 `NoOp` なら `NoOp`。`plan -detailed-exitcode` と同じ規則）。`convergence` はモードに依存しないためトップレベルにだけ置く。`apply` / `prune` / `explain` / `adopt` / `release` の出力形は上記の単数形のままとする。

`summary.execution` は以下の表を上から順に評価して一意に決定する。

| 優先順 | 条件 | `summary.execution` |
| :--- | :--- | :--- |
| 1 | 環境適用・ストア Commit の最終結果が不明、または `Stabilize` が `Indeterminate` を返した | `Indeterminate` |
| 2 | この呼び出しで Provider 契約違反（`K501`）を検出し Resource を隔離した（隔離情報の保存失敗を含む） | `Failed` |
| 3 | 環境適用済みだが記録確定が未完了であることを確認済み（`Applied` 後の `Stabilize` が `Conflict` を返した場合を含む） | `PartiallyApplied` |
| 4 | 環境を適用していない経路での確定した I/O 失敗（Intent・回復・記録だけの経路・`adopt` / `release` のストア保存の確定失敗等） | `Failed` |
| 5 | 当該モードの `Blocker`・`PolicyDenied`・未解決回復、呼び出し開始時点の Resource 隔離（`K503`）、`NotApplied`（条件不一致で環境変更ゼロ）、環境を適用していない経路での `Stabilize` の `Conflict`、または `adopt` / `release` の拒否条件（`TOKEN` / `REV` 不一致等） | `Blocked` |
| 6 | 読み取り専用 `plan` で実行・回復候補または `Deferred` の残作業がある | `Planned` |
| 7 | `recovery` または `normalExecution` で1件以上の変更を確定した | `Completed` |
| 8 | 両フェーズが `NoOp` で、変更確定も実行候補もない | `NoOp` |

- `recovery = Completed` かつ `normalExecution = NoOp` の場合は `Completed` とする。`recovery = Completed` かつ `normalExecution = Blocked` の場合は `Blocked` を優先し、確定済み回復の件数を `counts` から省略しない。
- 環境適用後のストア Commit の結果が不明なら、`Pending` の残存を確認するまでは `PartiallyApplied` でなく `Indeterminate` とする。`Deferred` の診断だけでは `Failed` / `Blocked` としない。`plan` が未解決回復を検出した場合は候補の有無より `Blocked` を優先する。
- 契約違反は環境が変更された可能性があっても `PartiallyApplied` ではなく `Failed` とする。環境変更の可能性、残る `Pending`、`K501` / `K503` を `counts` と `diagnostics[]` から省略しない。
- `summary.convergence` は `Converged / NotConverged / Unknown` を返し、`Completed` でも `Deferred` や `prune` 待ちがあれば `NotConverged` とする。
- **終了コード**:
  - 通常時：操作完了 `0`、エラー・競合・未確定 `1`、入力・用法エラー `64`。
  - `plan -detailed-exitcode`：`Apply` / `Prune` のどちらかに `Blocker`・`PolicyDenied`・未解決回復（または入力不備）があれば `1`、それらがなくどちらかに候補または `Deferred` の残作業があれば `2`、両方 `NoOp` なら `0` とする。所有記録のみの差分も `2` に含め、エラーと差分が共存する場合は `1` を優先する。
- **診断コードの最低集合**:
  `K102`（空 Record）、`K201`（所有衝突）、`K202`（構造衝突）、`K203`（外部ドリフト）、`K204`（移行必要）、`K205`（Pending 衝突）、`K206`（未所有値の不一致）、`K301`（観測不能）、`K302`（表現不能）、`K401`（権限拒否）、`K501`（Provider 契約違反）、`K502`（結果不明）、`K503`（Resource 隔離）、`K601`（作成帰属の未確認）、`K602`（明示的な削除権放棄）、`K603`（旧所有解除待ち）、`K604`（削除待ち）。
  `K206` の必要な次操作は `adopt → apply`、`K604` は `prune`、`K204` は [[0003]] の構造移行の正規手順とする。

### 3. Lean 4 による形式検証の3段階構成
1. **純粋関数 `Plan` の安全性11定理（Phase 1 ゲート）**:
   妥当な入力状態に対して、①変更範囲と Scope の遵守（$M_A$ 内、明示解消は1パス＋`Own+Resolve`）、②構造非破壊、③保守的削除（`Prune` モード・宣言外・自所有・`Created`・`Pending` なし・観測=LastApplied の葉のみ）、④対象または必要祖先が `Unknown` なら環境変更も所有記録更新も非生成、⑤別 Actor 所有・ドリフト（`Absent` を含む）の非上書き、⑥ `Match_A` の不動点性、⑦決定論性、⑧モード内阻害要因による全件抑止と `Deferred` の権限不要性、⑨明示解消の非環境変更・非昇格・非乗っ取り、⑩独立したモード外異常の非 `Blocker` 化（`MigrationRequired` と自 Actor 未解決 `Pending` を除く）、⑪ `MetadataCandidates` に対する環境 `Pending` の非生成および記録だけの経路の `Intent` 保存・`ApplyBatch` 非実行、を `sorryAx` なしで証明する。無効入力は検査で拒否し、壊れた所有ストアから正当な履歴を推定しない。
2. **プロトコル状態機械の帰納的不変条件（Phase 1 ゲート）**:
   Phase 1 ゲートでは少なくとも、回復保存前の観測耐久化、明示解消（`Adopt` / `Release`）の不変条件、`Created` の所有履歴根拠を `sorryAx` なしで証明する。Go 実装との対応は Phase 2 の障害注入でも検査する。
   `Observe / Plan / Intent / ProviderApply / Validate / Stabilize / Commit / Crash / Recover / Adopt / Release / ExternalWrite` を遷移とする状態機械を定義し、永続状態と揮発状態、ファイルの可視化と耐久化、所有権予約を区別する。`Created` の所有開始時からの履歴根拠、未確認作成の非昇格、Actor 間排他、`Pending` 中の新しい環境削除の禁止、結果不明時の記録維持、回復保存前の観測耐久化（`Stabilize`）、通常バッチの全件可否、他 Actor の非重複 `Pending` 保存、および `Resolve` 操作で削除権が増えないことを帰納的不変条件として証明する。「Plan が Created だけを消す」と「Created の設定が正当である」は別に証明する。
   ストアの原子保存、Provider の条件付き適用、ロック規約、認証・権限境界を明示的な仮定とする。Go 実装とは DRT / PBT・障害注入で対応を検査し、実装の精緻化証明を完了したとは主張しない。
3. **限定条件下の有限実行収束（Phase 1〜2）と常駐化時の ESR（Phase 5）**:
   - v0.1 では、宣言・Scope・Actor が固定され、対象が有限、観測が利用可能で競合・構造移行がなく、必要な `Own` と `Set` / `Delete` 権限が付与され、Provider とストア操作が成功する条件下で、独立した葉が `apply → prune` の有限列で `Match_A(d)` に到達することを証明する。`Scope` の固定だけでは権限充足とみなさない。
   - Apply 対象だけで解除待ちもなければ、1回の回復耐久化・回復保存・計画・バッチ適用・契約検証・記録確定・再観測で Apply が許す未完了操作がなくなる。退役 `Created` があれば `apply` だけで完全収束するとはせず、`prune` だけで `Modified` の解除や新宣言の `Set` が終わるとも主張しない。
   - 旧 `Modified` の解除待ちは別補題とし、外部で新構造への移行が済んでいる場合に限り、解除後の再 `apply` を含む有限列での収束を示す。旧構造のままなら2回目は `StructureConflict` で停止し、解除だけから収束を導かない。`Created` を含む葉↔Record 移行と外部削除の自動修復は定理の対象外とする。
   - 常駐ループ（Phase 5）では、安定時点以降の宣言・権限・Actor の固定、観測の最終的可用性、外部干渉の停止、未解決衝突・構造移行の解消、Provider / ストアの最終的成功、クラッシュの有限回性、必要な `apply` / `prune` の公平な実行を仮定する。`prune` が永久に承認されない環境に完全収束を要求しない。
   - ESR の順位関数の候補はパス別順位の総和（値変更要＝`2`、記録確定要＝`1`、完了＝`0`）とする。退役 `Modified` は記録解除だけなので `1`、退役 `Created` は値が残れば `2`、不在で記録だけ残れば `1` とする。これは証明済みではない。最後の故障後の安定実行について、停滞を許す内部ステップと完了時に厳密減少する進捗ステップを定義する。Intent・観測・NoOp での厳密減少や、耐久化前の電源断を含む全ステップでの非増加を要求しない。時相証明基盤は Phase 5 で選定する。

### 4. 60件の必須反例スイートと開発フェーズゲート
構想書 v7 §11 から移した [必須反例 #1〜#60](../samples/required-counterexamples.md) を本 ADR の規範的な適合性サンプルとし、回帰テスト識別子・シナリオ・期待値を保持する（番号と期待値は v7 のまま、シナリオ列は構想書なしで読める文に書き換えた）。実装・形式モデル・テストは ADR 本文とこの期待値の両方に適合しなければならない。既存番号を再利用・再採番せず、期待値の変更は対応 ADR の変更と同時に理由を記録する。非公開の構想書や会話履歴を読まなければ期待値が分からない状態を認めない。

純粋関数 PBT、Lean / Go DRT、実ファイルの障害注入、CLI / MCP 統合テストを使い分け、対応するサンプル番号をテストに残す。プロセス kill と電源断・永続化喪失は異なるため、後者は耐久性モデルと対応 FS で別に検証する。文書の存在・構造検査の成功を反例試験の合格とみなさない。

| Phase | 作業内容 | 完了ゲート |
| :--- | :--- | :--- |
| **Phase 0** | 本 ADR 群（[[0001]]〜[[0005]]）と下記の実装前決定事項の確定 | 権限・所有・回復・モードの規範が一意、60反例の期待値が確定し、全実装前決定が記録され、`docdag validate` / `docdag lint` が通る |
| **Phase 1** | Lean 4 の `Plan` とプロトコル状態機械、Go 参照実装、DRT | 安全性11定理、回復耐久化・明示解消・所有履歴の不変条件の証明（`sorryAx` なし）、有限実行収束の前提機械化 |
| **Phase 2** | LKAL ファイル Provider、原子ストア、バッチ永続化、回復 | 保存契約・各永続化境界の障害注入テスト・反例スイートが通り、有限実行収束をモデルで確認 |
| **Phase 3** | CLI / MCP サーバー、実行環境の束縛 | `apply/prune/adopt/release` 分離、ロック内再観測、フェーズ別結果、権限・Actor 境界の統合検証 |
| **Phase 4** | JSON Provider とベンチマーク比較評価 | JSON 写像 ADR、`Opaque` 保持、直接編集・全体再生成との定量比較評価。ここで実用価値を判断する |
| **Phase 5** | 常駐モードと ESR、必要に応じ複数 Provider | 故障・公平性モデルと時相証明基盤による ESR 証明。複数ファイル原子性は別設計とする |

Phase 0 の実装前決定事項は以下とする。現時点では具体方式・値を未確定とし、それぞれを追加または更新 ADR で固定するまで Phase 0 完了を宣言しない。抽象的な契約が `accepted` であることと、具体方式の決定・実装・証明の完了を区別する。

| 決定事項 | 固定して検証する内容 | 状態 |
| :--- | :--- | :--- |
| 所有ストア | 原子保存・耐久化・版管理・結果不明時の再読込と旧版/新版の判定方式 | 未確定 |
| Resource 識別 | 物理対象の束縛、別名・ハードリンク・別ストアの重複登録拒否方式 | 未確定 |
| 条件トークンと所有版 | Resource・パス・観測への束縛、未所有を含む版の具体表現・更新規則 | 未確定 |
| 管理者復旧 | 認証・バックアップ・PendingConflict/隔離の解消・隔離保存失敗後の再開インターフェース。[[0004]] の帰属再検査・降格・通常エージェントへの非公開を満たすこと | 未確定 |
| Linux / FS / Stabilize | 対応範囲、同期・再検査の実装、安定ロックパス、mode/owner とメタデータ保存、耐久性の故障モデル | 未確定 |
| 依存実装 | LKAL コミット・Go バージョンの固定と必要仕様の実装確認。未確認の要求版を確認済みと扱わない | 未確定 |

### 5. JSON Provider の比較評価

Phase 4 では、直接ファイル編集、全体再生成、kisage を同じ宣言・環境・競合条件で比較する。対象外葉の破壊率、誤削除率、競合検出率、完全収束率、介入回数、I/O・`fsync` 回数、所要時間、エージェントのトークンとステップ数を測る。安全に停止する代わりに収束率や運用負荷が悪化する可能性も報告し、意図自体の誤りを実行機構の誤りと分けて集計する。

## 根拠（調査結果・出典）

1. **検証駆動開発（VGD）と障害注入の参考事例**:
   形式モデルの証明と製品実装への差分ランダムテスト（DRT）を組み合わせる方針は Cedar の検証駆動開発を参考にする。モデルから Go 実装への対応は DRT / PBT と障害注入で検査し、実装の精緻化証明を済ませたとは主張しない。障害注入の設計はコントローラへの状態揺さぶりテストである Sieve を参考にする。いずれも kisage での有効性を示すものではない。
   - [How We Built Cedar: A Verification-Guided Approach (arXiv:2407.01688)](https://arxiv.org/abs/2407.01688)
   - [Sieve: Automatic Reliability Testing for Cluster Management Controllers (USENIX OSDI 2022)](https://www.usenix.org/conference/osdi22/presentation/sun)
2. **常駐化時の ESR の参考事例**:
   Phase 5 の Eventually Stable Reconciliation の定式化は、コントローラの活性検証である Anvil を参考にする。
   - [Anvil: Verifying Liveness of Cluster Management Controllers (USENIX OSDI 2024)](https://www.usenix.org/conference/osdi24/presentation/sun-xudong)
3. **終了コードと MCP ツール注釈の既存規約との整合**:
   CI パイプラインでのドリフト・差分検出において、`0`（差分なし）・`1`（エラー）・`2`（差分あり）の3値終了コードは広く使われており、通常時の用法エラーを `64`（`EX_USAGE`）に分けることで衝突を回避できる。
   - [Terraform CLI Documentation: plan -detailed-exitcode](https://developer.hashicorp.com/terraform/cli/commands/plan#detailed-exitcode)
   - [Model Context Protocol Specification: ToolAnnotations](https://modelcontextprotocol.io/specification/2025-11-25/schema#toolannotations)

## 検討した代替案

- **A. Phase 1 から時相論理による ESR（無限実行列上の活性）証明を必須ゲートにする**:
  不採用。v0.1 は有限回で終了する CLI であり、まずは「`Plan` の安全性11定理」「プロトコルの帰納的不変条件」「1回または有限列の実行による収束」を証明する方が、反例の発見効率と費用対効果が圧倒的に高いため。
- **B. `recovery` と `normalExecution` を分けず、1つの実行ステータスだけを返す**:
  不採用。クラッシュ回復の保存が成功した後に通常計画が `Blocked` になった場合、ストアが更新された事実が失われるか、あるいは `Blocked` が隠蔽されるため。

## 影響とトレードオフ

- **得るもの**:
  - エージェントおよび CI が、`summary.execution`・`summary.convergence`・診断コード（`K102`〜`K604`）から次の行動を決定論的に分岐できる。
  - 60件の反例シナリオが純粋関数 PBT・Lean/Go DRT・障害注入テスト・統合テストの共通仕様として機能する。
- **失うもの・引き受けるリスク**:
  - Lean 4 モデルと Go 実装の2重管理コストが発生するため、CI での DRT 自動実行が必須となる。
  - 実用上の定量効果（直接編集に対する破壊率低減やトークン消費のトレードオフ）は Phase 4 の JSON Provider 評価まで未実証であり、それ以前に実用効果を主張しない。

## 関連ADR

- [[0002]]（所有と権限境界）、[[0003]]（純粋計画パイプラインと `adopt`/`release`）、[[0004]]（バッチ永続化と Provider 契約）を統合し、外部インターフェースと検証ゲートを定める。
