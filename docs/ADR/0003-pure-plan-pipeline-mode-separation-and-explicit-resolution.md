---
title: "純粋計画関数の5段階パイプライン・Apply/Prune分離・全件可否ゲート・明示的競合解消（adopt/release）"
status: proposed
date: 2026-10-29
depends-on: ["0001", "0002"]
---

# 0003: 純粋計画関数の5段階パイプライン・Apply/Prune分離・全件可否ゲート・明示的競合解消（adopt/release）

## コンテキスト

[[0001]] の観測直和型（存在の3値とノード種別）と [[0002]] の所有・権限モデルの上で、純粋計画関数 `Plan` と競合解消の仕様を定めるにあたり、次の4つの設計課題を解決する必要がある。

1. **権限検査と状態分類の評価順序**:
   パスごとに必要となる権限（`Set+Own`、`Own`、`Delete+Own`、あるいは権限不要の `NoOp` / `Deferred`）は、そのパスがどの状態分岐に該当するかによって決まる。もし状態分類より前に一律で書込権限を検査すると、既に収束済みのパス（`NoOp`）や別モードの作業（`Deferred`）に対して架空の `PolicyDenied` が発生し、収束の不動点定理（`Match_A` 成立時に計画が空になる性質）が崩れる。また、`Conflict` や `Unknown` を `PolicyDenied` で隠蔽してはならない。
2. **`Apply` と `Prune` の分離およびモード外の異常の扱い**:
   削除操作を人間の承認にかけやすくするため、作成・更新・管理解除を行う `Apply` と、退役 `Created` 葉を削除する `Prune` を分離する。このとき、`Apply` が退役 `Created` 葉を誤って管理解除（Unmanage）すると、`Prune` の対象と削除権が失われる。また、当該モードの操作対象ではない独立パスの異常によって、現在実行中のモードまで停止すべきかを明確にする必要がある。
3. **部分適用（半適用）の防止と版間構造移行の順序**:
   単一宣言内の一部のパスが競合で止まっているにもかかわらず残りの葉だけを適用すると、設定ファイル全体として中途半端な状態が作られる。また、退役 `Modified` 葉と新宣言葉が親子関係で重なる場合に、1回目の `apply` で旧記録の解除を進められるよう、`StructureConflict` との優先順位を定める必要がある。
4. **外部ドリフトからの正規の回復経路**:
   外部による更新（`DriftChanged`）と削除（`DriftDeleted`）を対称に停止させる方針において、観測値と宣言値が一致している（`Observed = Desired ≠ LastApplied`）からといって自動で追認すると、「新しい宣言の適用前に外部の書き手がたまたま同じ目標値を先に書き込んだ競合」まで黙って呑み込んでしまう。一方で、`Force` 上書きフラグを持たないまま解消手段を用意しなければ、`Created` パスが外部変更された際にどの宣言を与えても回復不能なデッドロックに陥る。

## 決定

純粋計画関数 `Plan` を `PureRecover → Classify → SelectMode → Authorize → Gate` の5段階パイプラインとして構成し、`Apply` と `Prune` の責務およびモード内阻害要因による全件抑止ゲートを分離するとともに、通常の `apply` は同値を含めすべての外部ドリフトで停止させ、実環境を書き換えない明示的な記録解消コマンド `adopt` / `release`（`Own + Resolve` 権限および条件トークン・所有版必須）によってのみドリフト・所有放棄を解消する。

### 1. 純粋計画関数 `Plan` の5段階パイプライン
パスをセグメント列の固定順序で並べ、以下の5段階を順に適用する。

```text
Plan(d, observed, σ, Scope, Actor, Mode)
  → { Changes, OwnershipUpdates, DeferredActions, Diagnostics }
Mode = Apply | Prune

PureRecover(observed, σ_A)           → RecoveryCandidates, σ_virtual, Unresolved
Classify(d, observed, σ_virtual, A)  → StateClass per path
SelectMode(StateClass, Mode)         → NoOp | Deferred | Candidate(action, perms) | Blocker
Authorize(Candidate, Scope)          → Authorized | PolicyDenied
Gate(all results)                    → ExecutableBatch | Blocked
```

1. **入力・隔離・回復ゲート**: 不正な宣言・所有ストア・Resource 束縛、または Resource 隔離状態（`K503`）は全体を停止する。`PureRecover` の回復候補にも `Own` 権限を検査し、回復不能または拒否された自 Actor の `Pending` が1件でもあれば通常処理を停止する。
2. **Scope に依存しない状態分類（`Classify`）**: 所属（宣言内 $D$・退役 `Modified`・退役 `Created`）、パス重複関係、観測状態を識別し、成立する診断をすべて保持する。
   - 新宣言葉について、まず他 Actor の重複所有・予約（`OwnershipConflict`, `K201`）を検査し、次に重なる旧所有に自 Actor の退役 `Created` が含まれれば `MigrationRequired`（`K204`）、重なる旧所有がすべて自 Actor の退役 `Modified` であれば `DeferredUntilRelease`（`K603`）と分類する。この解除待ちの回では、新側の `Unknown` と構造衝突を含む観測診断を保持しつつ、実行上の `Blocker` に昇格させない。旧側は独立に分類し、Apply では旧側の `Unknown` または `Own` 不足により通常候補を全件抑止する。Prune では退役 `Modified` は `Deferred` であり、その解除のための権限を検査しない。自 Actor の未解決 `Pending` と隔離状態は入力・回復ゲートで先に停止する。
   - この例外以外の主要理由は `Unknown`（`K301`）→ 所有衝突（`K201`）→ 構造衝突・表現不能（`K202`/`K302`）→ 未所有値の不一致（`K206`）→ ドリフト（`K203`）の固定順とする。
3. **モード別選択（`SelectMode`）**: 下表に従い、モード外の操作および独立したモード外の異常を `Deferred` とする。ただし、旧 `Created` と新宣言の親子重複（`MigrationRequired`）は両モード共通の `Blocker` とする。

| 所属・状態 | Apply | Prune |
| :--- | :--- | :--- |
| 宣言内、完全収束（`v = LastApplied = d(p)`） | `NoOp` | `NoOp` |
| 宣言内、`Set`（未所有 `Absent` または自所有 `v = LastApplied ≠ d(p)`） | `Candidate(Set, Set+Own)` | `Deferred` |
| 宣言内、未所有一致採用（`Equal(v, d(p))`） | `Candidate(AdoptModified, Own)` | `Deferred` |
| 宣言内、異常・所有衝突・未所有不一致・ドリフト（`Absent` / `v ≠ LastApplied`） | `Blocker` | `Deferred`（`MigrationRequired` を除く） |
| 宣言内、旧 `Modified` の解除待ち | `DeferredUntilRelease` | `DeferredUntilRelease` |
| 退役 `Modified`、対象と必要祖先が既知（`Absent`/`Leaf`/`Record`/`Opaque`） | `Candidate(Unmanage, Own)` | `Deferred` |
| 退役 `Modified`、対象または必要祖先 `Unknown` | `Blocker` | `Deferred` |
| 退役 `Created`、正常な `Absent`（必要祖先も既知・構造衝突なし） | `Candidate(ClearRecord, Own)` | `Candidate(ClearRecord, Own)` |
| 退役 `Created`、`Leaf(LastApplied)`、祖先の異常なし | `Deferred(PruneRequired, K604)` | `Candidate(Delete, Delete+Own)` |
| 独立した退役 `Created`、異常（ドリフト・構造衝突・`Unknown`） | `Deferred`（診断・記録保持） | `Blocker` |
| 旧 `Created` と新宣言の親子重複集合 | `Blocker(MigrationRequired)` | `Blocker(MigrationRequired)` |

4. **実行候補の認可（`Authorize`）**: `Candidate(action, perms)` に対してのみ `perms ⊆ Scope(p)` を検査し、不足していれば `PolicyDenied`（`K401`）に変換する。`NoOp`・`Deferred`・`Blocker` に実行権限は要求せず、`Conflict` を `PolicyDenied` で上書きしない。
5. **全件可否ゲート（`Gate`）**: 当該モードの `Blocker` または `PolicyDenied` が1件でもあれば、通常の `Changes` と通常の `OwnershipUpdates` を全件抑止し、候補は `Suppressed` として表示する。`Deferred` だけでは抑止しない。

`Set` は未所有の `Absent` なら `Expected = Absent`、自所有の `Leaf(v)` で `v = LastApplied ≠ d(p)` なら `Expected = Leaf(v)` とする。`Expected` と `Target` が同値になる `Set` は生成しない。未所有の不一致値は `UnownedValueConflict`（`K206`、正規の解消は `adopt → apply`）、自所有の不在は `DriftDeleted`、自所有の `v ≠ LastApplied` は目標との一致に関係なく `DriftChanged` とする。`Set` / `Delete` 候補の生成には [[0001]] の対象・必要祖先の構造条件も要求する。

`plan` は書き込み・ファイル置換・`fsync`・回復保存を行わない。`PureRecover` の候補のうち `Own` が許可されたものだけを仮想 $\sigma$ に反映し、拒否された `Pending` は残す。その同じ仮想状態から `Apply` と `Prune` をそれぞれ独立に計算し、`Apply → Prune` の逐次実行を模擬してはならない。回復案、必要な `Stabilize`、未解決項目、条件付き通常計画を分けて表示する。回復・耐久化の成功は表示上の仮定であり、実行済みと報告しない。同じ入力スナップショットでは実行と同じ分類を使うが、後の `apply` との出力完全一致は保証しない。

### 2. 収束条件 `Match_A(d)`
$$\mathrm{Converged}_A(p) \iff o(p) = \mathrm{Present}(\mathrm{Leaf}(d(p))) \land \sigma(p).\mathrm{Manager} = A \land \sigma(p).\mathrm{LastApplied} = d(p) \land \sigma(p).\mathrm{Pending} = \varnothing$$
$$\mathrm{Match}_A(d) \iff (\forall p \in D.\ \mathrm{Converged}_A(p)) \land \mathrm{dom}(\sigma_A) \subseteq D$$

- `apply` は削除待ちの退役 `Created` 葉を管理解除せず `PruneRequired`（`K604`）として残すため、`prune` 完了まで `Match_A(d)` は偽（`NotConverged`）となる。

### 3. 明示的な記録解消コマンド：`adopt` / `release`
通常の `apply` は `v = d(p) ≠ LastApplied` の場合を含め外部の更新・削除ドリフトで常に停止し、自動追認や `Force` 上書きは行わない。正規の解消経路として、実環境の値を一切書き換えない以下の2操作を提供する。

- `kisage adopt FILE PATH --expect TOKEN --owner-revision REV`:
  現在の表現可能な葉（`Present(Leaf(v))`）を自 Actor の `Origin = Modified` として記録し、`LastApplied = v` に更新する。未所有または自所有の確定記録に対してのみ許可し、`Created` からの降格時は将来の自動削除権を失う（`K602`）。宣言との事前一致は要求しない。
- `kisage release FILE PATH --expect TOKEN --owner-revision REV`:
  自所有の確定記録を解除し、`Created` の削除権も明示的に放棄する（`K602`）。対象と必要祖先が `Unknown` 以外（`Absent` / `Leaf` / `Record` / `Opaque`）で既知なら実行可能。

両操作とも `Own + Resolve` 権限を要求し、別 Actor 所有・重複予約、対象または必要祖先の `Unknown`、自 Actor の未解決 `Pending`、Resource 隔離状態では拒否する。`adopt` は自 Actor を含む他パスの確定所有との祖先・子孫重複も拒否し、旧所有の `release` を先行させる。ランタイムによる `adopt` / `release` の自動呼び出しは禁止する。

`FILE` は `apply` / `prune` と同じ宣言ファイルであり、Resource は `FILE` に対応する保護された登録から選ぶ。宣言は Resource の選択と、`PATH` が宣言に含まれるかの判定（診断用）に使い、`adopt` / `release` の可否に宣言との一致は要求しない。

`TOKEN` は `explain FILE PATH` が返す Resource・パス・現在観測の条件トークン、`REV` はストアの所有エントリ版を指す。未所有状態にも版を持たせる。トークンは認証でも作成証明でもなく、値同値 ABA の検出も保証しない。具体的な符号化と版の更新規則は [[0005]] の Phase 0 で固定する。

実行手順は次の順序に限定する。

1. 共有所有ストア $\to$ Resource の順にロックを取得する。
2. 同じロック内で `TOKEN` が対象 Resource・パス・現在の値と構造に一致すること、`REV` が現在の所有版に一致すること、および上記の所有・予約・権限・観測・隔離条件を再検査する。不一致なら環境も記録も変更しない。
3. 判断に用いる現在観測を `Provider.Stabilize` で耐久化・再検査する。同期失敗または状態変化なら記録を保存しない。
4. 同じロックを保持したまま記録を原子的に保存する。ストア Commit の結果が不明なら新しい通常変更を停止し、再読込で旧版・新版を判定するまで成功とも未変更とも断定しない。

これらの操作で `Pending` を強制消去してはならない。`PATH` が `FILE` の宣言に残ったまま `release` した場合は、次回 `apply` で再管理されうることを診断に出す。ホストは `Resolve` を通常エージェントへ無条件に付与しない。

### 4. 版間の構造移行（葉↔Record）の非自動化

葉↔Record の版間移行は自動化しない。

- 退役 `Created` 葉と新宣言葉が祖先・子孫で重なる集合を `MigrationRequired`（`K204`）とし、その集合内の削除も抑止する。上記 SelectMode 表のとおり両モード共通の `Blocker` であり、通常候補を全件抑止する。
- 新宣言葉に重なる自 Actor の旧所有がすべて退役 `Modified` であり、他 Actor の重複所有・予約や自 Actor の未解決 `Pending` がない場合は、新側を `DeferredUntilRelease`（`K603`）とし、旧側は独立に解除候補・`Unknown` 等へ分類する。退役 `Modified` は環境を変更しないため、観測が既知で必要な `Own` があれば解除できる。新旧の祖先・子孫所有を同じバッチで入れ替えない。
- 実環境がまだ旧構造でも、1回目の `apply` で旧記録の解除を進められる。新側の構造・観測上の診断は保持するが、この回の全件抑止要因にはしない。旧側自体が `Unknown`・権限不足なら解除は抑止される。旧記録の解除後の次回呼び出しでは新側を通常分類し、外部で構造移行済みなら一致採用、未移行なら `StructureConflict` となる。明示 `release` も利用できる。
- 葉→Record は `prune → apply` で技術的には可能な場合があるが、新構造を作成できる前に旧値を消す事故を避けるため自動移行を禁止する。
- Record→葉は、子の削除後も空 `Record` が残り（[[0001]]）、その置換を認めないため、自動操作だけでは移行できない。
- 構造移行は別途、信頼された管理手順で行う。これは正常な宣言変更にもある制限である。
- `MigrationRequired`（`K204`）の正規の次操作は次の順とし、診断の「必要な次操作」にもこれを示す。ランタイムはいずれも自動実行しない。
  1. 重なる旧 `Created` を明示 `release` し、削除権を放棄する（上記 §3 の条件・`Own + Resolve` を満たすこと）。
  2. 管理者が実環境を新構造へ移行し、不要になった旧値を整理する。旧値の削除は `release` 後の kisage の削除権に依らない。
  3. `apply` で新側を通常分類する。移行済みで値が一致すれば採用、`Absent` なら `Set`、未移行なら `StructureConflict` で停止する。

## 根拠（調査結果・出典）

1. **内部不変条件（同値ドリフトの非自動追認）**:
   $\mathrm{Observed} = d(p) \neq \mathrm{LastApplied}$ は、「人間が実環境を変更した後に宣言 $d(p)$ を合わせた」場合と、「宣言 $d(p)$ を変更して未適用の間に外部プロセスが偶然同じ値を書き込んだ」場合で状態上区別できない。したがって値の一致を承認の証拠とせず、条件トークン `TOKEN` と所有版 `REV` を伴う明示的な `adopt` / `release` に分離する必要がある。
2. **内部不変条件（退役 `Modified` の2段階移行）**:
   旧所有 `/a/b`（`Modified`）を退役させて新宣言 `/a/b/c` に移行する際、実環境が未移行のまま1回目の `apply` を実行すると、新側 `/a/b/c` の祖先 `/a/b` はまだ `Leaf` である。もし1回目から新側を `StructureConflict`（`Blocker`）にすると、全件抑止ゲートによって旧 `/a/b` の管理解除自体が阻害される。新側を `DeferredUntilRelease` とすることで、1回目で旧記録の解除を完了させ、2回目で構造検査（移行済みなら採用、未移行なら `StructureConflict`）へ進む決定論的な2段階遷移が成立する。
3. **参考（本決定の根拠ではない）: マルチエージェント環境における競合通知と修復の分離**:
   CoAgent は、長い推論を捨てる OCC 再試行のコストを問題視し、事前に定めたコミット順序の下で書き込みを即時に適用して undo を事前登録し、競合時にはランタイムが影響を受けたエージェントへ通知して計画を見直させる方式を提案する。kisage の短いパス単位の条件検査と停止とは粒度も機構も異なるため、この論文を OCC 一般の推奨としては引用しない。競合時には診断を返し、推論全体の自動再試行を強制しないという方針の参考にとどめる。
   - [CoAgent: Concurrency Control for Multi-Agent Systems (arXiv:2606.15376v1)](https://arxiv.org/abs/2606.15376v1)

## 検討した代替案

- **A. `Observed == d(p) != LastApplied` の場合に `apply` が自動で `Modified` に降格して追認する**:
  不採用。宣言変更と外部書き込みの競合を見逃し、「外部ドリフトは必ず通知・停止する」という設計原則3に反するため。
- **B. 計画段階で競合していない一部の葉パスだけを適用する（ベストエフォート部分適用）**:
  不採用。同一ファイル内の設定項目間に暗黙の依存がある場合に半適用状態を生み出すため、v0.1 では当該モードの阻害要因が1件でもあれば通常候補を全件抑止する保守的な設計を採る。
- **C. `apply` に `--force` フラグを追加してドリフトや未所有不一致値を上書きする**:
  不採用。「状態の確認と所有記録の更新（`adopt`/`release`）」と「実環境への値の書き込み（`apply`）」を分離した方が、各操作の事前条件（`TOKEN`/`REV`）と権限（`Resolve` vs `Set`）が最小化されるため。

## 影響とトレードオフ

- **得るもの**:
  - `NoOp` や `Deferred` に不要な書込権限を要求せず、`Conflict` を `PolicyDenied` で隠さない一意な純粋関数 `Plan` が得られる。
  - 外部ドリフト、外部削除からの再作成（`release → apply`）、既存不一致値の初期導入（`adopt → apply`）、およびドリフトした退役 `Created` の放棄（`release`）がすべて正規コマンドの有限列で到達可能になる。
- **失うもの・引き受けるリスク**:
  - 既存の不一致値を初期導入する際やドリフト解消時には、`explain → adopt/release → apply` の明示的なステップが必要となり、単発の `--force` より操作回数が増える。
  - 独立したモード外の異常を `Deferred` とするため、削除対象と宣言内パスの業務上の独立性までは保証しない。

## 関連ADR

- [[0001]]（観測モデル）および [[0002]]（所有ストアと権限）を前提とする。
- [[0004]]（バッチ永続化と `Stabilize`）および [[0005]]（CLI/MCP 出力と形式検証定理）が本 ADR のパイプラインと決定規則を実装・検証する。
