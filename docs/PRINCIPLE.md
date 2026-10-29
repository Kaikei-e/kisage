# kisage 設計憲章（Design Principles）

本書は、設定・状態収束基盤 **kisage** の不変の設計原則、責任境界、安全保証の3層構造、および非目標を定める憲章である。個別のアーキテクチャ決定は [docs/ADR/](ADR/README.md) に記録し、[DocDag](https://github.com/Kaikei-e/DocDag) によって依存グラフと文書構造を機械検証する。期待動作は [必須反例サンプル #1〜#60](samples/required-counterexamples.md) に保持する。内容への適合性は形式モデル・適合性テスト・障害注入で別に検査し、文書構造の検証成功だけで安全性を証明済みとはしない。

---

## 1. 使命と位置づけ（Mission & Scope）

kisage は、[LKAL](https://github.com/Kaikei-e/LKAL) 文書を望ましい状態の基準（定盤）として読み、明示的な変更権限（Scope）、永続所有記録（$\sigma$）、および検証可能な Provider 契約の下で実環境を観測し、許可された葉パスの差分のみを条件付きで適用する決定論的な Reconciler である。

1. **LKAL との直交分離**: kisage は LKAL とは別リポジトリで開発し、LKAL の構文・意味論・比較規則（`Equal`）を一切拡張しない。
2. **Policy（目標状態の宣言）と Mechanism（収束機構）の分離**: kisage の価値は、生成された宣言の実行範囲を限定し、競合や不確実性を構造化して返すことにある。宣言がユーザーの業務上の意図に合致すること、あるいは宣言内容自体が意味論的・セキュリティ的に妥当であることは保証しない。エージェントや利用者が誤った目標を宣言すれば、kisage は許可された範囲内でその誤った目標へ収束する。意図の妥当性検査は、宣言の承認側または独立した検証器の責務である。
3. **比喩と保証の分離**: 名称の「きさげ」は工作機械の摺動面を定盤にすり合わせる工程（定盤＝宣言、アタリ＝観測差分、削る＝条件付き変更、再度当てる＝適用後の再観測）に由来するが、比喩を技術的保証の根拠にはしない。すべての保証は明示的な前提条件、形式モデル、適合性テスト、および実行環境境界によって定義される。

---

## 2. 7つの基本設計原則（Seven Core Principles）

kisage のすべての設計・実装・ADR は、以下の7原則に適合しなければならない（参照：[Kubernetes Controller Guidelines](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-api-machinery/controllers.md)）。

### 原則1：レベル駆動の再計画（Level-Triggered Planning）
毎回、開始時点の宣言スナップショット、現在の実環境観測、および永続所有記録から計画を立て直す。LLM の会話履歴やプロセス内の前回の揮発状態を実行の根拠にしない。

### 原則2：葉パス射影と未宣言兄弟値の保存（Leaf Projection & Sibling Preservation）
スカラーまたは `List` を値とする宣言葉パス集合 $D = \mathrm{Paths}(d)$ と、自 Actor の所有履歴 $\mathrm{dom}(\sigma_A)$ の和集合 $M_A(d, \sigma) = D \cup \mathrm{dom}(\sigma_A)$ のみを照合対象とする。管理対象外の兄弟葉値とその到達可能性を保存し、他 Actor の無関係な所有パスを理由に偽の競合を報告しない。

### 原則3：衝突・ドリフトの推測解決禁止（No Speculative Resolution）
所有権衝突（同一パスおよび祖先・子孫の重複）、構造衝突（葉と `Record` / `Opaque` の不一致）、および外部による値の更新・削除ドリフトを推測で上書きしない。通常の `apply` は観測値と目標値がたまたま一致していても `LastApplied` と異なればドリフトとして停止し、実環境を書き換えない明示的な記録解消操作（`adopt` / `release`）と厳格に分離する。

### 原則4：作成帰属に基づく保守的な削除（Conservative Deletion by Verified Creation）
作成・更新を行う `apply` と、退役葉を削除する `prune` を分離する。自動削除（`prune`）が許されるのは、現在の所有の開始において自 Actor による `Absent → Present` の適用完了が確認された `Origin = Created` の葉のみとする。既存の未所有値の一致採用や、クラッシュ回復で作成帰属を確認できなかったパスは `Origin = Modified` とし、将来の自動削除権を一切持たない。

### 原則5：観測不能・表現不能への非盲目性（No Blind Mutation on Unknown or Opaque）
存在自体が確認できない状態（`Unknown`）、不在が確認できた状態（`Absent`）、および存在するが LKAL の葉へ損失なく写せない状態（`Present(Opaque)`）を直和型として厳密に区別する。欠落した観測を `Absent` と補完してはならず、対象または必要祖先が `Unknown` / `Opaque` / 構造衝突の状態にあるパスへ盲目的に書き込まない。

### 原則6：ロック保護下の条件付き適用・観測耐久化・契約検証（Verified Conditional Execution & Durability）
適用時は排他ロックの下で事前状態（`Expected`）だけでなく祖先構造・共有所有権・権限（Scope）を再検査する。環境変更の成功後は、同じロックを保持したまま再観測して契約（`Set(v) → Leaf(v)`、`Delete → Absent`、兄弟値保存）を検証し、検証成功後にのみ所有記録を Commit する。クラッシュ回復の確定および環境変更を伴わない記録更新でも、判断根拠となる観測状態を `Stabilize`（ファイルおよび親ディレクトリの同期）によって耐久化してから記録を保存する。

### 原則7：決定論的な計画と全件可否・フェーズ分離（Deterministic Planning & All-or-Nothing Mode Gate）
同一の入力状態からは常に同一の計画・診断・順序を出力する。通常計画では、当該モード（`Apply` または `Prune`）に関係する阻害要因（`Blocker` または `PolicyDenied`）が1件でも存在すれば通常変更と通常記録更新を全件抑止する。出力では「クラッシュ回復の結果」と「通常実行の結果」、および「当該モードの操作完了（`execution`）」と「宣言全体の収束（`convergence`）」を混同せず分離して報告する。

---

## 3. 3層の安全保証境界と信頼の根（Guarantee Layers & Root of Trust）

kisage は「宣言外を壊さない」という性質を単一層の万能な保証として誇張せず、責任主体と検証手段が異なる以下の3層に分離する。

| 層 | 名称 | 保証・前提の内容 | 検証・強制の手段 |
| :--- | :--- | :--- | :--- |
| **第1層** | **Logical Frame** | 通常計画と回復が生成する環境変更・所有記録変更は $M_A(d, \sigma)$ 内、明示解消は指定された1パス内に収まり、それぞれ必要な Scope（`Set` / `Delete` / `Own` / `Resolve`）を満たす。 | 有効入力に対する **Lean 4 実行可能モデルの定理証明**、および Go 実装との差分ランダムテスト（DRT）。 |
| **第2層** | **Provider Preservation** | 構造の保護、バッチ対象外の既存葉値の保存、排他ロック下での条件再検査、宣言した永続化・同期手順（`ApplyBatch` / `Stabilize`）の遵守。 | **Provider 適合性テスト、プロパティベーステスト（PBT）、実ファイル障害注入テスト**。Go ランタイムや OS カーネル全体の形式証明ではない。 |
| **第3層** | **Execution Boundary** | 信頼情報と管理対象ファイルへの書き込み経路を実行環境が保護し、エージェント等による直接編集（バイパス）やロック無視の同時書き込みを遮断する。 | **OS 権限、サンドボックス、ホストの MCP 設定**。kisage 単独では強制できない外部前提である。 |

### 信頼の根（Root of Trust）
kisage の保証は、以下の要素が改ざんされないことを前提として成立する。

- **Scope ポリシーおよび Resource 登録情報**（管理ルート、Provider 種別、対象ファイル束縛）
- **Actor の注入元**（CLI の任意の `--actor` 引数やツール引数の自己申告は信頼せず、保護された実行環境またはホスト設定が Actor を束縛する）
- **共有所有ストア（$\sigma$）とその原子的永続化経路**
- **ロックファイルおよび対象ファイルの親ディレクトリ**
- **Provider 実装および固定された LKAL 実装リビジョン**

---

## 4. 明示的な限界と非保証事項（Explicit Limitations）

仕様の誠実性を保つため、v0.1 の設計が保証しない境界を以下に明記する。

1. **値同値 Provider における ABA 問題の限界**:
   値の同値性（`Equal`）のみを事前条件とする Provider は、kisage の実行間に外部主体が「葉を削除し、まったく同じ値で再作成（ABA）」した事象を識別できない。したがって `Origin = Created` は「所有開始時の作成確認と以後の正当な遷移」という履歴不変条件を表すが、現在の値の物理的な出自までは保証しない（生涯同一性の保証には世代番号等を持つ Provider が別途必要となる）。
2. **フェーズ間・宣言全体の ACID 非保証**:
   All-or-Nothing ゲートは「通常計画の可否判定」と「1回のファイル置換バッチ」の境界である。クラッシュ回復の保存、実環境のファイル置換、所有ストアの Commit、および `apply` と `prune` の間は別々の永続化フェーズであり、1回のコマンド呼び出し全体や宣言全体にわたる ACID トランザクションは保証しない。
3. **葉パス間の意味的共起制約の非検証**:
   kisage は各葉パスを構造的に扱う。複数の設定項目間の業務上の依存関係（例：フラグAを有効にするなら設定Bも特定の値でなければならない等）や、削除対象と宣言内パスの業務上の独立性は検査しない。
4. **版間の構造移行（葉↔Record）の非自動化**:
   旧所有葉と新宣言葉が祖先・子孫として重なる構造移行は自動実行しない。`Origin = Created` を含む重複は `MigrationRequired`（`K204`）として両モードで停止し、`Origin = Modified` のみの重複は旧記録の解除と新側の保留（`DeferredUntilRelease`, `K603`）を別の呼び出しに分ける。また LKAL の削除仕様により空 `Record` が残る `Record → 葉` の移行は、信頼された管理手順による構造変更を必要とする。

---

## 5. 非目標（Non-Goals）

kisage は以下を目的としない。

- 変数展開・条件分岐・テンプレート合成等を持つ新しい計算言語または評価器
- LLM の推論ループやマルチエージェント・ワークフローのオーケストレーション
- 宣言自体の業務的正しさの証明、または一般的な未承認パッチのセキュリティ承認器
- LKAL の `Ref`（参照リテラル）に対する外部シークレット解決・展開
- 事前確認時の計画スナップショットを固定適用する Saved Plan Apply（v0.1 の `plan` は読み取り専用プレビュー）
- 分散ロック・分散所有権サービス、複数ファイル間の原子的トランザクション、任意のシェル実行・ソースコード AST Provider（v0.1）

---

## 6. 規範 ADR 体系（Normative Decision Graph）

本憲章に基づく具体的な規範仕様は、[docs/ADR/](ADR/README.md) 配下の Architecture Decision Records に分割して定義する。

- [ADR 0001](ADR/0001-leaf-path-projection-and-observation-sum-type.md): LKAL 葉パス射影と観測の直和型（存在の3値 `Unknown` / `Absent` / `Present` とノード種別 `Leaf` / `Record` / `Opaque` の分離）
- [ADR 0002](ADR/0002-shared-ownership-store-scope-and-trust-boundary.md): 共有所有ストア（$\sigma$ と $\sigma_A$）・4種操作権限（`Set`/`Delete`/`Own`/`Resolve`）・3層保証境界
- [ADR 0003](ADR/0003-pure-plan-pipeline-mode-separation-and-explicit-resolution.md): 純粋計画関数の5段階パイプライン・`Apply`/`Prune` 分離・全件可否ゲート・明示的解消（`adopt`/`release`）
- [ADR 0004](ADR/0004-file-batch-persistence-stabilize-recovery-and-provider-contract.md): ファイル内バッチ永続化・`DurableIntent` と `MetadataCandidates` の分離・`Stabilize` 回復・ロック内契約検証
- [ADR 0005](ADR/0005-cli-mcp-result-aggregation-and-verification-gates.md): CLI/MCP インターフェース・フェーズ別結果集約・Lean 4 形式検証と60反例ゲート

実装は各 ADR と [必須反例サンプル](samples/required-counterexamples.md) の両方に適合しなければならない。具体的な所有ストア・Resource 識別・条件トークンと所有版・管理者復旧・対応 Linux/FS と `Stabilize`・依存リビジョンは ADR 0005 の Phase 0 決定事項として固定する。これらの未確定部分を、抽象的な契約や「安全な管理手順」という表現だけで完了扱いしない。
