# kisage

**[English (README.md)](README.md)** | **日本語**

**kisage**（きさげ）は、[LKAL](https://github.com/Kaikei-e/LKAL) 文書を目標状態の基準（定盤）として読み、明示的な変更権限（`Scope`）・共有所有ストア（$\sigma$）・検証可能な Provider 契約の下で、許可された葉パスの差分のみを条件付きで適用する決定論的な Reconciler です。

> **ステータス: Phase 0〜2 形式検証・参照実装**
> Go の計画・所有・回復・LKAL ファイル Provider・原子ファイルストアの参照実装に加え、Lean 4 による形式証明（`Kisage/*.lean`：純粋 `Plan` 安全性11定理・プロトコル9帰納的不変条件と遷移安全性定理・有限実行収束と ESR 候補ランク関数を `sorryAx` ゼロで証明）および操作・所有記録の内容まで比較する Lean/Go 差動試験（`kisage-drt`）を備えています。ADR は `proposed` であり、実機電源断耐久性はプロセス試験の対象外です。[検証状況](docs/verification-status.md) を参照してください。

## 主な特徴

- **Policy と Mechanism の分離**: LKAL に変数展開やテンプレート等の計算言語を足さず、構文木と等価比較（`Equal`）のみを扱います。「許可された範囲内で事前条件を検査して収束させる機構」に特化し、宣言値の業務的妥当性は外部検証に委ねます。
- **変更範囲の厳格な限定**: 宣言葉パス集合と自 Actor の所有履歴 $M_A(d, \sigma) = D \cup \mathrm{dom}(\sigma_A)$ のみを照合し、宣言外の兄弟設定を保存します。
- **保守的な削除（`apply` と `prune` の分離）**: 作成・更新（`apply`）と退役削除（`prune`）を分離します。自動削除が許されるのは、自 Actor による `Absent → Present` の作成が確認された `Origin = Created` の葉のみです。
- **競合・ドリフト時の推測解決禁止**: 所有権衝突、構造不一致（`Leaf` と `Record`/`Opaque`）、外部変更ドリフトを推測で上書きせず、診断コード（`K102`〜`K603`）を返して当該モードの通常変更を全件抑止します。
- **実環境を変えない明示的解消（`adopt` / `release`）**: ドリフト追認や所有権放棄は、独立した `Resolve` 権限・観測条件トークン（`--expect`）・所有版（`--owner-revision`）を要求する明示コマンドで行い、対象ファイル自体は書き換えません。

## CLI / MCP インターフェース

| コマンド | MCP ツール | 概要 |
| :--- | :--- | :--- |
| `kisage plan FILE` | `kisage_plan` | 読み取り専用プレビュー（回復案・`Apply`/`Prune` 計画・診断の表示） |
| `kisage apply FILE` | `kisage_apply` | 条件付き `Set`、一致採用、退役 `Modified` の管理解除を実行（環境削除はしない） |
| `kisage prune FILE` | `kisage_prune` | 退役した `Origin = Created` 葉の条件付き削除を実行（作成・更新はしない） |
| `kisage explain FILE PATH` | `kisage_explain` | 観測・所有・権限・停止理由・`--expect` トークン・`--owner-revision` を表示 |
| `kisage adopt FILE PATH --expect TOKEN --owner-revision REV` | `kisage_adopt` | 既存の表現可能な葉を `Origin = Modified` として明示採用（`Own + Resolve` 必須、ファイル不変） |
| `kisage release FILE PATH --expect TOKEN --owner-revision REV` | `kisage_release` | 自所有の記録と `Created` 削除権を明示放棄（`Own + Resolve` 必須、ファイル不変） |

`go build -o /tmp/kisage ./cmd/kisage` でビルドできます。保護されたホスト設定・ストア初期化・MCP stdio の起動手順は [CLI / MCP の起動](docs/cli.md) を参照してください。

## ドキュメント

実用的な作例として、[kisageで設定するURL死活監視CLI](examples/healthcheck/README.md)があります。ローカルAPIですぐに試せ、監視先の更新・手元の設定の保存・明示的な削除・ドリフト検出を体験できます。

仕様群は [DocDag](https://github.com/Kaikei-e/DocDag) により依存関係と文書構造が検証されています（`docdag validate && docdag lint --strict`）。

- **[`docs/PRINCIPLE.md`](docs/PRINCIPLE.md)** — 設計憲章（7原則・3層安全保証境界・信頼の根・明示的限界）
- **[`docs/ADR/`](docs/ADR/README.md)** — アーキテクチャ決定記録（[ADR 0001](docs/ADR/0001-leaf-path-projection-and-observation-sum-type.md)〜[0005](docs/ADR/0005-cli-mcp-result-aggregation-and-verification-gates.md)）
- **[`docs/samples/required-counterexamples.md`](docs/samples/required-counterexamples.md)** — 必須反例サンプル #1〜#60

## ライセンス

Apache License 2.0（詳細は [LICENSE](LICENSE) を参照）。
