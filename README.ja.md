# kisage

**[English (README.md)](README.md)** | **日本語**

**kisage**（きさげ）は、[LKAL](https://github.com/Kaikei-e/LKAL) 文書を目標状態の基準（定盤）として読み、明示的な変更権限（`Scope`）・共有所有ストア（$\sigma$）・検証可能な Provider 契約の下で、許可された葉パスの差分のみを条件付きで適用する決定論的な Reconciler です。

> **ステータス: Phase 0（仕様策定・アーキテクチャ設計）**
> 本リポジトリは現在、設計憲章・ADR（`proposed`）・必須反例サンプルを定義した段階です。Lean 4 形式証明、Go 参照実装、障害注入テストは次フェーズ以降の開発完了ゲートです。

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
| `kisage explain PATH` | `kisage_explain` | 観測・所有・権限・停止理由・`--expect` トークン・`--owner-revision` を表示 |
| `kisage adopt PATH --expect TOKEN --owner-revision REV` | `kisage_adopt` | 既存の表現可能な葉を `Origin = Modified` として明示採用（`Own + Resolve` 必須、ファイル不変） |
| `kisage release PATH --expect TOKEN --owner-revision REV` | `kisage_release` | 自所有の記録と `Created` 削除権を明示放棄（`Own + Resolve` 必須、ファイル不変） |

## ドキュメント

仕様群は [DocDag](https://github.com/Kaikei-e/DocDag) により依存関係と文書構造が検証されています（`docdag validate && docdag lint --strict`）。

- **[`docs/PRINCIPLE.md`](docs/PRINCIPLE.md)** — 設計憲章（7原則・3層安全保証境界・信頼の根・明示的限界）
- **[`docs/ADR/`](docs/ADR/README.md)** — アーキテクチャ決定記録（[ADR 0001](docs/ADR/0001-leaf-path-projection-and-observation-sum-type.md)〜[0005](docs/ADR/0005-cli-mcp-result-aggregation-and-verification-gates.md)）
- **[`docs/samples/required-counterexamples.md`](docs/samples/required-counterexamples.md)** — 必須反例サンプル #1〜#60

## ライセンス

Apache License 2.0（詳細は [LICENSE](LICENSE) を参照）。
