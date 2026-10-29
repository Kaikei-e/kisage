# kisage

**[English (README.md)](README.md)** | **日本語**

**kisage**（きさげ）は、[LKAL](https://github.com/Kaikei-e/LKAL) 文書を望ましい状態の基準（定盤）として読み、明示的な変更権限（`Scope`）、永続所有記録（$\sigma$）、および検証可能な Provider 契約の下で実環境を観測し、許可された葉パスの差分のみを条件付きで適用する決定論的な Reconciler です。

> **現在のステータス — Phase 0（仕様策定・アーキテクチャ設計フェーズ）**
> 本リポジトリは現在、設計憲章（[`docs/PRINCIPLE.md`](docs/PRINCIPLE.md)）、Phase 0 のアーキテクチャ決定記録（[`docs/ADR/`](docs/ADR/README.md)、`status: proposed`）、および60件の必須反例サンプル（[`docs/samples/required-counterexamples.md`](docs/samples/required-counterexamples.md)）を定義した段階です。以下に記載する Lean 4 の定理証明、Go 参照実装、差分ランダムテスト（DRT）、および実ファイル障害注入テストは今後の**開発完了ゲート**であり、現時点で実装・証明が完了していることを意味しません。

---

## 背景と目的

コーディングエージェントや自動化ツールが設定ファイルを直接編集・全体再生成すると、宣言外の兄弟フィールドの破壊、人間による変更の暗黙上書き、あるいは自分が作成していない設定の誤削除が発生します。

kisage は **Policy（目標状態の宣言）** と **Mechanism（変更範囲の限定・競合検出・条件付き適用）** を分離します。

- **新しい計算言語を作らない**: LKAL の構文・意味論・等価比較（`Equal`）を一切拡張せず、変数展開やテンプレート合成は持ちません。
- **変更範囲の厳格な限定**: 宣言された葉パス集合 $D = \mathrm{Paths}(d)$ と自 Actor の所有履歴 $\mathrm{dom}(\sigma_A)$ の和集合 $M_A(d, \sigma) = D \cup \mathrm{dom}(\sigma_A)$ のみを照合対象とし、未宣言の兄弟葉値とその到達可能性を保存します。
- **作成帰属に基づく保守的な削除（`apply` と `prune` の分離）**: 作成・更新を行う `apply` と、退役した葉を削除する `prune` を分離します。`prune` による削除が許されるのは、現在の所有セッションの開始時点において自 Actor による `Absent → Present` の適用完了が確認された `Origin = Created` の葉のみです。既存の未所有値の一致採用や、クラッシュ回復で作成帰属を確認できなかったパスは `Origin = Modified` となり、自動削除権を持ちません。
- **競合・ドリフト時の推測解決禁止**: 所有権衝突、構造衝突（葉と `Record` / `Opaque`）、および外部による更新・削除ドリフトは推測で解決せず停止し、構造化された診断コード（`K102`〜`K603`）を返します。観測値と宣言値がたまたま一致していても `LastApplied` と異なれば `DriftChanged` として停止します。
- **実環境を書き換えない明示的解消（`adopt` / `release`）**: ドリフトの追認や所有権・削除権の放棄は、独立した `Resolve` 権限、観測条件トークン（`--expect TOKEN`）、および所有ストア版（`--owner-revision REV`）を要求する明示コマンド `kisage adopt` / `kisage release` に分離します。

> **保証範囲の境界**: kisage が担うのは「許可された範囲内で、事前条件と所有権を検査しながら安全に差分を適用すること」です。宣言内容自体がユーザーの業務意図に合致していることやセキュリティ上の妥当性は保証しません（誤った目標が宣言されれば、許可された範囲内でその目標へ収束します）。

---

## 中核アーキテクチャ

### 1. LKAL 葉パス射影と観測の直和型（[ADR 0001](docs/ADR/0001-leaf-path-projection-and-observation-sum-type.md)）

宣言文書 $d$ のうち、スカラーまたは `List` を値とする葉パス集合を $D = \mathrm{Paths}(d)$ と定義します（`List` は順序を保った1つの葉値として丸ごと比較・置換し、`Ref` は参照パスの不透明リテラルとして比較します）。

- **空 `Record` の非対称規則（`K102`）**: 宣言側の非ルート空 `Record`（`{}`）は子孫の暗黙削除を防ぐため `K102: EmptyRecordNotSupported` として拒否します。一方、全管理葉の退役を表す空のルート文書（$D = \varnothing$）は許可し、観測側（対象ファイル）に存在する空 `Record` は正常な `Present(Record)` として扱います。
- **存在の3値とノード種別の分離**:
  ```text
  Observed = Unknown(reason) | Absent | Present(Node)
  Node     = Leaf(lkal.Value) | Record | Opaque(reason)
  ```
  読み取り失敗等で存在自体が不明な `Unknown` と、存在は確認できたが LKAL の葉へ損失なく写像できない `Present(Opaque)`（将来の JSON Provider における `null` やオブジェクト配列等）を厳密に区別します。

### 2. 共有所有ストア（$\sigma$ と $\sigma_A$）と4種操作権限（[ADR 0002](docs/ADR/0002-shared-ownership-store-scope-and-trust-boundary.md)）

所有権は `(ResourceID, PathSegments)` をキーとする共有ストア $\sigma$ で直列化管理されます。

```text
OwnerEntry = { Manager, Origin, LastApplied?, Pending? }
Origin     = None | Created | Modified
```

- **単一所有者排他と非干渉**: 他 Actor の無関係な宣言外所有パスは照合対象に加えず、偽の衝突を起こしません。ただし宣言パス $D$ と他 Actor の所有パス（`Pending` 予約を含む）が同一または祖先・子孫として重なる場合は、値が一致していても共有所有にはせず `OwnershipConflict`（`K201`）で停止します。
- **4種の操作権限（`Set` / `Delete` / `Own` / `Resolve`）**: パース済みセグメント列に対して操作種別ごとに判定します。`Resolve` は明示的な競合解消専用であり、通常の `Own` からは導出されません。

### 3. 純粋計画関数の5段階パイプラインとモード分離（[ADR 0003](docs/ADR/0003-pure-plan-pipeline-mode-separation-and-explicit-resolution.md)）

純粋関数 `Plan` は以下の5段階パイプラインで構成されます。

```text
PureRecover(observed, σ_A)          → RecoveryCandidates, σ_virtual, Unresolved
Classify(d, observed, σ_virtual, A) → StateClass per path
SelectMode(StateClass, Mode)        → NoOp | Deferred | Candidate(action, perms) | Blocker
Authorize(Candidate, Scope)         → Authorized | PolicyDenied
Gate(all results)                   → ExecutableBatch | Blocked
```

- **権限検査より前の状態分類**: `NoOp`・`Deferred`・`Blocker`・`Unknown` には架空の書込権限を要求せず、収束済みパスでの不要な `PolicyDenied` や競合理由の隠蔽を防ぎます。
- **モード別選択と全件可否ゲート（`Gate`）**: 当該モード（`Apply` または `Prune`）の `Blocker` または `PolicyDenied` が1件でもあれば、通常の `Changes` と通常の `OwnershipUpdates` を全件抑止（`Suppressed`）します。一方、独立した別モードの異常は `Deferred` として保持し、現在モードの実行を妨げません（ただし旧 `Created` と新宣言が親子で重なる `MigrationRequired` と自 Actor の未解決 `Pending` は両モードを停止させます）。
- **退役 `Modified` の2段階移行（`DeferredUntilRelease`, `K603`）**: 新宣言葉と重なる自 Actor の旧所有がすべて退役 `Modified` の場合、1回目の `apply` では新側を `DeferredUntilRelease` と分類して旧記録の管理解除を先行させ、次回呼び出しで構造検査へ進みます。

### 4. ファイル内バッチ永続化・`Stabilize` 回復・ロック内契約検証（[ADR 0004](docs/ADR/0004-file-batch-persistence-stabilize-recovery-and-provider-contract.md)）

```text
ExecutionBatch = { BatchID, EnvironmentOps[], MetadataCandidates[] } // メモリ上の揮発実行案
DurableIntent  = { BatchID, EnvironmentOps[] }                       // 永続化する環境操作のみ
```

- **`MetadataCandidates` の非永続化**: 一致採用や管理解除などの記録のみの変更案は `Pending` を作らず、クラッシュ時は破棄して再計画します。
- **ロック内での契約検証と Resource 隔離（`K503`）**: ロック順序を `共有ストア (σ) → Resource` に統一します。`ApplyBatch` 成功後、**ロックを保持したまま再観測**して `Set(v) → Leaf(v)`・`Delete → Absent`・兄弟値保存を検証し、成功後にのみ所有記録を原子的 Commit します。契約違反（`K501`）時は Commit せず Resource を隔離します。
- **`Provider.Stabilize` による観測耐久化**: クラッシュ回復（$\sigma_A$ 限定）や記録のみ変更を保存する前に、ロック下で対象ファイルと親ディレクトリの `fsync` および条件再検査を行う `Stabilize` を実行します。これにより、未同期の rename を別プロセスが観測して所有記録だけを先に確定させた後に電源断でファイルが旧値へ戻る逆転を防ぎます。

---

## CLI / MCP インターフェース（[ADR 0005](docs/ADR/0005-cli-mcp-result-aggregation-and-verification-gates.md)）

| コマンド | MCP ツール | 動作概要 |
| :--- | :--- | :--- |
| `kisage plan FILE [-json] [-detailed-exitcode]` | `kisage_plan` | 読み取り専用。メモリ上で `PureRecover` を合成した仮想 $\sigma$ から `Apply` / `Prune` の計画・回復案・診断を表示 |
| `kisage apply FILE [-json]` | `kisage_apply` | 自 Actor の回復、条件付き `Set`、一致採用、退役 `Modified` 解除、ロック内再観測を実行（環境削除はしない） |
| `kisage prune FILE [-json]` | `kisage_prune` | 自 Actor の回復、退役 `Created` 葉の条件付き削除、ロック内再観測を実行（`Set` や採用はしない） |
| `kisage explain PATH [-json]` | `kisage_explain` | 読み取り専用。観測・所有・権限・停止理由・条件トークン `TOKEN`・所有版 `REV` を表示 |
| `kisage adopt PATH --expect TOKEN --owner-revision REV [-json]` | `kisage_adopt` | 現在の表現可能な葉を `Modified` として明示採用（`Own + Resolve` 必須、実環境は不変） |
| `kisage release PATH --expect TOKEN --owner-revision REV [-json]` | `kisage_release` | 自所有の確定記録と `Created` の削除権を明示放棄（`Own + Resolve` 必須、実環境は不変） |

---

## 3層の安全保証境界と明示的な限界

| 層 | 保証・前提の内容 | 検証・強制の手段 |
| :--- | :--- | :--- |
| **第1層: Logical Frame** | 通常計画と回復の変更は $M_A(d, \sigma)$ 内、明示解消は指定1パス内に収まり、必要な `Scope` を満たす。 | 有効入力に対する **Lean 4 実行可能モデルの定理証明** ＋ Go 実装との差分ランダムテスト（DRT）。 |
| **第2層: Provider Preservation** | 構造保護、対象外の既存葉値保存、排他ロック下の条件再検査、`ApplyBatch` / `Stabilize` の永続化手順。 | **Provider 適合性テスト、PBT、実ファイル障害注入テスト**（Go や OS 全体の形式証明ではない）。 |
| **第3層: Execution Boundary** | 信頼の根（`Scope`、Actor 束縛、所有ストア、ロック、親ディレクトリ）の保護と直接編集の遮断。 | **OS 権限、サンドボックス、ホストの MCP 設定**（kisage 単独の保証範囲外）。 |

### 明示的な非保証事項（限界）
1. **値同値 Provider における ABA 問題の限界**: 値の同値性のみを検査する Provider では、外部主体が「葉を削除し、同じ値で再作成（ABA）」した事象を識別できません。
2. **フェーズ間・宣言全体の ACID 非保証**: 回復保存、ファイル置換、所有 Commit、および `apply` と `prune` は別フェーズであり、単一の ACID トランザクションではありません。
3. **葉パス間の意味的共起制約の非検証**: 複数の設定項目間の業務上の依存関係は検査しません。
4. **版間の構造移行（葉↔Record）の非自動化**: `Created` を含む重複は `MigrationRequired`（`K204`）で停止し、LKAL の削除仕様で空 `Record` が残る `Record → 葉` の移行は外部の管理手順を要します。

---

## ドキュメント体系と検証コマンド

本リポジトリの設計仕様は [DocDag](https://github.com/Kaikei-e/DocDag)（`v0.5.0`）によって型付き有向非巡回グラフ（DAG）として管理・検証されています。

- **[`docs/PRINCIPLE.md`](docs/PRINCIPLE.md)**: kisage 設計憲章
- **[`docs/ADR/README.md`](docs/ADR/README.md)**: Phase 0 アーキテクチャ決定記録（ADR 0001〜0005）
- **[`docs/samples/required-counterexamples.md`](docs/samples/required-counterexamples.md)**: 必須反例サンプル #1〜#60

```sh
docdag validate
docdag lint --strict
docdag query --binding
docdag stats
```

---

## ライセンス

Apache License 2.0（詳細は [LICENSE](LICENSE) を参照）。

