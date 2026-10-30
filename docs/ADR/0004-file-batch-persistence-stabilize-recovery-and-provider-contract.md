---
title: "ファイル内バッチ永続化・DurableIntentとMetadataCandidatesの分離・Stabilize回復・ロック内契約検証"
status: proposed
date: 2026-10-29
depends-on: ["0001", "0002", "0003"]
---

# 0004: ファイル内バッチ永続化・DurableIntentとMetadataCandidatesの分離・Stabilize回復・ロック内契約検証

## コンテキスト

[[0001]]・[[0002]]・[[0003]] で定めた計画と所有モデルを実環境のファイルと永続所有ストア $\sigma$ に適用する際、プロセス停止（SIGKILL 等）や電源断、および協調的な別 Actor との並行実行に対して以下の4つの問題を解決する必要がある。

1. **未宣言兄弟パスと共存するクラッシュ回復**:
   もし適用完了の証跡として「対象ファイル全体の期待ダイジェスト（`TxID`）」を使うと、クラッシュから再起動までの間に人間や別 Actor が同じファイル内の無関係な兄弟パスを編集しただけでダイジェストが不一致となり、永久に回復不能になる。
2. **OS キャッシュ上の可視性とディスク耐久化の乖離**:
   プロセス1が `Root.Rename` を実行した直後・親ディレクトリの `fsync` 完了前に停止し、直後に起動したプロセス2が OS キャッシュ上で `Target` 値を観測して所有記録を Commit した後に電源断が発生すると、対象ファイルの rename が失われて旧値に戻る一方、所有ストア $\sigma$ だけが新値を確定してしまう（削除時も同様に、所有記録だけ消えて実環境の値が復活しうる）。
3. **環境変更と記録のみ変更（`MetadataCandidates`）の永続化境界**:
   既存一致値の採用（`Modified`）や管理解除は実環境を書かない。これらにまで環境変更と同じ `Pending` を永続化すると、不要なストア書き込みと回復規則の複雑化を招く。
4. **Provider 契約違反の誤診（TOCTOU）と不正 `Created` 記録の防止**:
   `ApplyBatch` の後にロックを解放してから再観測すると、直後に割り込んだ別 Actor の書き込みを `ProviderContractViolation`（`K501`）と誤診する。また、契約検証より先に `Origin = Created` を Commit してしまうと、Provider が不正な値を書いていた場合に誤った削除権が永続化されてしまう。

## 決定

メモリ上の実行案 `ExecutionBatch`（`EnvironmentOps` と `MetadataCandidates`）から環境変更のみを永続化する `DurableIntent` を分離し、ロック順序を共有所有ストア $\sigma \to \text{Resource}$ に統一した7段階バッチ手順（ロック内での契約検証完了後にのみ所有記録を原子的 Commit し、違反時は Resource を隔離）および `Provider.Stabilize` による観測耐久化を前提とした自 Actor（$\sigma_A$）限定のクラッシュ回復規則を定める。

### 1. `ExecutionBatch`（揮発）と `DurableIntent`（永続）の分離
```text
Pending        = { BatchID, Op: Set|Delete, Expected, Target?, PreviousEntry? }
ExecutionBatch = { BatchID, EnvironmentOps[], MetadataCandidates[] } // メモリ上の実行案
DurableIntent  = { BatchID, EnvironmentOps[] }                       // 耐久化する環境操作のみ
```

- `MetadataCandidates`（一致採用・管理解除・不在確認済み `Created` の記録削除）は必要な観測条件・所有リビジョン・権限を持つが、メモリ上にのみ保持し、`Pending` や新規所有予約としては永続化しない。クラッシュ時は自動再生せず破棄し、環境 `Pending` の回復後に最新状態から再計画する。
- `DurableIntent` に含めてストアへ事前保存するのは `Set` / `Delete` を伴う `EnvironmentOps` と、その各パスの `PreviousEntry`・`Expected`・`Target` のみとする。
- `BatchID` は相関識別子であり、作成帰属の証明には使用しない。

### 2. ロック順序と7段階のバッチ永続化手順
ロック取得順序は回復を含めて常に **共有所有ストア $\sigma \to \text{Resource}$** に統一し、以下の7段階を単一のロック保護区間内で実行する。1バッチの対象葉パスは互いに祖先・子孫として重ならないものに限定する。

1. **観測・回復・計画**: 共有ストア $\to$ Resource の順にロックを取得し、開始時の宣言スナップショット、現在の Scope・観測・共有所有権を読む。自 Actor（$\sigma_A$）の回復を完了させた後、[[0003]] に従って通常計画を作る。
2. **全件可否**: 当該モードの阻害要因があれば通常候補を全件抑止する（回復済みの記録は巻き戻さない）。通常候補がすべて空なら手順3〜5を省略して手順7へ進む。
3. **環境 Intent のみ保存**: `EnvironmentOps` が非空の場合に限り、`DurableIntent` と各環境操作の `Pending`・`PreviousEntry` を原子的に保存し耐久化する。新規環境作成以外の確定 `Origin` / `LastApplied` は変えない。`MetadataCandidates` に `Pending` は作らず、`EnvironmentOps` が空ならこの手順を省略する。
4. **環境適用または観測耐久化**:
   - `EnvironmentOps` が非空なら、Provider が全 `Expected`・祖先構造・保存対象を、ランタイムが Scope・共有所有権を同じ保護区間内で再検査する。全件一致時のみ `Provider.ApplyBatch` がファイルを1回置換・耐久化して3値 `Receipt`（`NotApplied | Applied | Indeterminate`）を返す。不一致なら環境変更はゼロとする。
   - `EnvironmentOps` が空で `MetadataCandidates` が非空なら、`ApplyBatch` とファイル置換は呼ばず、候補の観測条件・所有リビジョン・権限を再検査して `Provider.Stabilize` を実行する。この経路で `Applied` Receipt を合成してはならない。
5. **ロック内検証と原子的 Commit**:
   - `Applied` の場合、**同じロックを保持したまま再観測**し、全変更の `Set(v) → Leaf(v)`、`Delete → Absent`、および対象外葉値の保存契約を検証する。さらに `MetadataCandidates` の観測・所有条件と権限を再検査し、判断に使う最新スナップショットを `MetadataCandidates` の有無にかかわらず `Stabilize` する。
   - 記録だけの経路では、手順4の条件検査と `Stabilize` の成功を Commit の前提とする。環境 `Applied` を前提にしない。
   - いずれの経路も全検査の成功後にのみ、1回の原子的ストア Commit で環境操作の所有確定（新規作成は `Created`、更新は `Origin` 維持・`LastApplied` 更新、`Delete` は記録削除）と `MetadataCandidates` の確定（一致採用は `Modified`、管理解除・不在確認済み退役葉は記録削除）をまとめて保存する。環境 `Pending` がある場合は同じ Commit で解消する。
6. **失敗・結果不明・隔離**:
   - `NotApplied` なら環境 `Pending` を `PreviousEntry` へ戻し、`MetadataCandidates` も保存しない。
   - `Indeterminate`、検証後の `Stabilize` 失敗、契約違反、または所有 Commit の保存失敗時は新しい通常変更へ進まない。環境適用後に環境だけを巻き戻そうとせず、残る `Pending` と結果を返す。記録だけの経路で `Stabilize` 前後の検査に失敗した場合はストア Commit を行わず、新たな `Pending` も作らない。
   - ストア Commit の結果自体が不明な場合は、`Pending` が残ったとも消えたとも断定せず、再読込で旧版・新版を確定するまで成功とも未変更とも報告しない。
   - 手順5で契約違反（`K501`）が判明した場合は、所有 Commit を行わず Resource に隔離状態（`K503`）を永続化し、全 Actor の変更と通常回復による自動成功扱いを禁止して管理者の検査を待つ。隔離情報自体の保存に失敗した場合はランタイムを停止し、同じ Resource を通常再開しない。実際に環境が変更された可能性を結果から省略してはならない。
7. **最終確認**: 同じロック内で最終 `Observe → Plan` により収束を判定し、フェーズ別結果を集約してロックを解放する。収束はこの観測時点の結果であり、解放後の外部変更まで防ぐものではない。解放後の観測差だけを根拠に Provider 契約違反と判定しない。

最終 `Observe` が失敗した場合は開始時の観測で代用せず、確定済みフェーズ結果を保持し、`convergence = Unknown` と `K301` を返す。適用直前の再観測に失敗した場合も旧観測で検査を続けず、環境変更を行わずに Intent を復元する。

`Applied` はファイルと必要な親ディレクトリの同期まで成功した場合だけ返す。rename 後の同期失敗を `NotApplied` としてはならず、`Indeterminate` とする。環境適用とストア Commit は別の耐久化フェーズである。全件可否は通常計画と1回のファイル置換の境界であり、回復・環境・ストア全体の同時 Commit を意味しない。クラッシュ時は環境操作だけをパスごとに回復し、揮発的な `MetadataCandidates` は失われるため、元バッチ全体の履歴上の原子性も保証しない。

### 3. `Provider.Stabilize` を前提とするクラッシュ回復規則（$\sigma_A$ 限定）
```text
Observe(Resource, Paths)             → Snapshot(nodes, ancestors, diagnostics)
ApplyBatch(Resource, BatchID, Ops[]) → Receipt(NotApplied | Applied | Indeterminate, diagnostics)
Stabilize(Resource, Snapshot)        → DurableSnapshot | Indeterminate | Conflict
```

`Stabilize` は現在の同じ通常ファイルと親ディレクトリの同期、および観測条件の再検査を行う。協調的書き手を同じロックで排除した区間でのみ有効であり、履歴の帰属や全外部書き手の意図は証明しない。ロック解放後の観測差は外部ドリフトまたは原因不明の差として扱い、それだけで Provider 契約違反とはしない。

- 回復の対象は自 Actor の $\sigma_A$ にある `Pending` に限定し、最新宣言で保存済み `Expected` / `Target` を書き換えてはならない。他 Actor の無関係な `Pending` は回復も全体停止も行わず、当該モードの実行対象への同一・祖先・子孫の予約重複を `OwnershipConflict` として扱う。
- 下記はパス別の回復候補の分類規則であり、上から順に評価する。回復更新の候補に `Own` 権限を検査し、失効していれば記録を書き換えず `PolicyDenied` とする。認可された回復候補を永続保存する前に、ロックを保持したまま `Provider.Stabilize`（対象通常ファイルと親ディレクトリの `fsync` および観測条件の再検査）を実行する。`Expected` への Rollback と `Absent` による記録削除にも同じ耐久化を要求する。同期失敗または状態変化なら `Pending` を維持して `Indeterminate` または `Conflict` として停止する。読み取り専用の `plan` はこの I/O を実行しない。
  1. **対象または必要祖先が `Unknown`**: 保留し `Pending` を維持する。
  2. **`Set`、観測が `Target` の葉値と一致**: `LastApplied = Target` として Commit する。ただし `Origin == None`（新規作成中）の場合は、作成帰属の永続証跡を持たないため **`Origin = Modified` に保守降格**し、`CreatedOriginUnverified`（`K601`）を警告する。従来の `Created` / `Modified` は維持する。
  3. **`Set`、観測が `Expected` と一致**: `PreviousEntry` へ戻す。現在が事前状態と整合するという判断であり、適用が一度も起きなかったという履歴の証明ではない。
  4. **`Delete`、観測が `Absent`**: 所有記録を削除する。
  5. **`Delete`、観測が `Expected` と一致**: `PreviousEntry` へ戻す。
  6. **それ以外（第3の葉値、`Record`、`Opaque`、祖先構造衝突）**: `PendingConflict`（`K205`）として環境も記録も変更せず `Pending` を維持する。
- 自 Actor の未解決 `Pending`（保留・`PendingConflict`・`Own` 権限失効による `PolicyDenied`）が1件でも残れば、自 Actor による当該 Resource の新しい通常変更を停止する。
- 回復判定は各パスの観測から行い、回復更新はストアへまとめて原子的に保存する。値観測だけからファイルバッチ全件の履歴を断定しない。他 Actor は非重複の予約・最新の兄弟値を保存して実行できるが、Resource 隔離中は全 Actor の変更を停止する。
- `apply` は過去の `PendingDelete` が完了済みなら記録を回復できるが、`Delete` を新規発行・再発行してはならない。未適用なら記録を通常状態へ戻し、次の `prune` を待つ。Scope 失効時も環境変更を行わない。

### 4. 管理者による復旧の境界

`PendingConflict` と Resource 隔離の管理者復旧は通常エージェントへ公開しない。記録を削除して再開する場合は、停止中の実行が存在しないこと、対象の耐久化状態、作成帰属を再検査し、帰属不明は `Modified` へ降格する。値一致だけを根拠に `Created` を再付与してはならない。

具体的な認証・バックアップ・復旧インターフェースと隔離保存失敗後の再開手順は [[0005]] の Phase 0 で ADR として固定する。それが完成するまで、本番で隔離後の自動再開を提供しない。「安全な管理手順」という記載だけでこの未確定事項を完了扱いしてはならない。

### 5. Linux ローカル LKAL ファイル Provider の実装契約
- 初期対応は Linux のローカルファイルシステム上の既存通常ファイルとし、管理ルート境界には Go 標準ライブラリの `os.OpenRoot` および `Root.Rename` を使用する。対象ファイル全体の作成・削除とネットワーク FS は扱わない。宣言ファイルと対象ファイルは別にする。
- シンボリックリンクおよび特殊ファイル（FIFO、デバイス、ソケット等）の混入と検査中の差し替え（TOCTOU）を防ぐため、末端だけでなく各経路成分を保護する。事前 `Lstat` だけに頼らず、固定した親ディレクトリハンドルと Linux の no-follow 解決・非ブロッキング open 後の `fstat` によって通常ファイルであることを確認する。デバイス等の open 自体を安全化するため、親ディレクトリを書き換えられる主体も制限する。
- 同じロック規約に従う主体について、読み直し $\to$ 全件条件検査 $\to$ メモリ上のパッチ $\to$ 同一ディレクトリ内の一時通常ファイルへの出力 $\to$ ファイル `fsync` $\to$ `Root.Rename` $\to$ 親ディレクトリ `fsync` を1つのロック保護区間で行う。
- ロックファイルは置換される対象ファイルとは別の、保護された安定したパスに置く。ロックを無視する書き手との競合防止は保証しない。
- 対象・一時ファイル・親のファイルシステムを検査し、マウント境界を越える対象は登録時に拒否する。実行中のマウント変更は実行環境側で禁止する。同じ物理対象の別名・別ストア登録を禁止し、単一ファイル Provider ではハードリンク自体も拒否する。
- 置換時の mode / owner と許容する書式正規化を実装前に規定し、維持できない ACL・拡張属性等を持つファイルは非対応として拒否する。保持すべきメタデータと書式を適合性テストで別々に確認し、未変更項目のコメント等を保持する LKAL パス編集の契約を検査する。
- LKAL Provider で中核プロトコルと回復を検証した後、次段階（Phase 4）で JSON Provider の意味写像 ADR と実用性比較評価に進む。

#### 5.1 参照実装の具体方式

- **Resource の継続識別**: 保護された単一の登録簿にある管理ルート・正規絶対パス・StoreID・デバイスを継続束縛とする。登録時の `(device, inode)` は別名検査に使うが、置換後も同じ inode であることは要求しない。`Root.Rename` は inode を変えるため、プロセス内の inode 更新表を再起動時の根拠にしない。毎回、経路・デバイス・通常ファイル・リンク数1を再検査し、値の変更は通常のドリフト判定に従う。独立した登録簿の併用や bind mount による別名登録はホストが禁止する。
- **固定した親と一時ファイル**: 管理ルートより前も含め、読み取りの各ディレクトリ成分を `O_NOFOLLOW` で開く。管理ルート以下の group/other 書込可能ディレクトリを拒否する。置換と同期は検査した親の `(device, inode)` と一致する固定 `os.Root` で行う。一時名は BatchID を含めず、128 bit の乱数と `O_EXCL` を用い、当該呼び出しが作成した一時ファイルだけを除去する。ロック名も識別子の SHA-256 から生成し、識別子を経路として解釈しない。
- **メタデータ**: mode の通常権限ビットと UID/GID を保存し、保存失敗なら rename 前に中止する。setuid/setgid/sticky と全拡張属性（ACL を含む）は非対応として拒否する。属性列挙には可変長バッファを使い、列挙エラーを「属性なし」と解釈しない。
- **隔離保存失敗**: `HaltRuntime` でリポジトリを停止し、全 Actor の後続実行・回復・明示解消を拒否する。ファイルストアでは同一プロセス内の再オープンも拒否し、可能なら `runtime-halted` マーカーを同期保存する。媒体障害でマーカーさえ保存できない場合、プロセス再起動を禁止する責任は外部ホストにある。通常 API に停止解除や自動再初期化は提供しない。管理者の認証・バックアップ・再開手順の Phase 0 決定は引き続き未完了である。

#### 5.2 所有ストアのファイル形式と原子保存

`FileStoreRepository` は、所有記録・未所有版の tombstone・各環境操作の Pending/PreviousEntry と Resource 隔離表を、保護されたディレクトリの `ownership.json` にまとめて保存する。環境操作以外の予約は保存しない。

- 形式は version 1 の JSON envelope とし、generation と payload の SHA-256 を検査する。LKAL 葉値は JSON 数値へ変換せず LKAL リテラルとして保存し、読込時に型・所有状態・重複パスを検証する。ハッシュは破損検出であり、認証や作成帰属の証明ではない。
- 更新は同じディレクトリの排他作成したランダム一時ファイルに書き、ファイル `fsync → Root.Rename → 親 fsync` の順に行う。rename 前の確定失敗は旧版を維持し、rename 以降の失敗は `Indeterminate` とする。
- 実行時の再読込は完全な旧版または新版を検査し、見えているストア本体と親を同期してから利用する。`ReadOnlyState` は同期・保存なしで `plan`/`explain` 用の可視状態を返し、仮想回復の耐久化成功は条件付きのままとする。
- 初期化は `InitializeFileStoreRepository` による明示的な一度だけの操作とし、既存ストアを上書きしない。通常の open/load は欠落・破損・未知形式を空ストアとして補完しない。ディレクトリは実行 UID 所有かつ他利用者に非公開、ストアは通常ファイルかつリンク数1を要求する。呼び出し側は同じ共有ストアロックを取得する。

この方式の記録は、Lean 証明、対応 Linux/FS の電源断試験、Phase 0/1/2 全体の完了宣言ではない。[検証状況](../verification-status.md) に実施済みと未実施を分けて記録する。

### 6. JSON Provider の次段階の写像契約

JSON Provider は LKAL-JSON 変換器と同一視しない。null、異種配列、オブジェクト配列、数値精度、`1` と `1.0`、キー制約、重複キーの拒否を別 ADR で決める。未対応値は `Opaque` として保持し、対象外の `Opaque` な兄弟があるだけでファイル全体を拒否してはならない。パーサーが文書全体を解釈できない場合はファイル全体を観測不能として扱う。

## 根拠（調査結果・出典）

1. **内部不変条件（保守降格による誤削除防止と兄弟共存の両立）**:
   新規作成中にクラッシュして再起動した際、観測値が `Target` と一致していても、それが自身の `ApplyBatch` によるものか外部主体が偶然同じ値を作ったものかは、ファイル全体ハッシュなしには区別できない。ここで `Origin = Modified` に降格して採用すれば、「自身が作成したと確定していない値は将来 `prune` で自動削除しない」という安全性不変条件が追加のジャーナルなしで厳密に保たれる。
2. **ファイルと親ディレクトリの耐久化仕様**:
   POSIX / Linux ファイルシステムにおいて、ファイル本体の `fsync` はディレクトリエントリ（`rename` の結果）の永続化を保証せず、親ディレクトリの `fsync` が不可欠である。また、他プロセスが未同期の rename 結果を観測してメタデータを先に耐久化する逆転を防ぐため、回復時の `Stabilize` が必要となる。
   - [Linux man-pages: fsync(2)](https://man7.org/linux/man-pages/man2/fsync.2.html)
   - [Atomic Commit In SQLite](https://www.sqlite.org/atomiccommit.html)
3. **Go `os.Root` の境界特性**:
   Go 1.25 で導入された `os.Root.Rename` はルート外への脱出を防ぐが、ルート内のシンボリックリンクやデバイス境界のすべてを自動遮断するわけではないため、ハンドル検査との組み合わせが必須となる。
   - [Go Standard Library: os.Root.Rename](https://pkg.go.dev/os#Root.Rename)

## 検討した代替案

- **A. `Pending` に適用後の期待ファイル全体ダイジェスト（`TxID`）を記録して照合する**:
  不採用。未宣言の兄弟パスの変更が許容される環境では、兄弟パスが変更された瞬間にダイジェストが不一致となり回復不能になるため。
- **B. Provider 独自の永続 WAL / ジャーナルを v0.1 から必須にする**:
  不採用。作成帰属未確認時の `Modified` 降格によって誤削除防止の不変条件はすでに守られている。v0.1 で引き受ける主な損失は「新規作成直後のクラッシュ時に、将来の自動削除権が `Modified` に落ちる（`K601` 警告）」ことであり、状態機械の単純さを優先する。値同値 ABA の限界はジャーナルの有無と別に残る（[[0002]]）。
- **C. `ApplyBatch` 成功後にまず所有記録を Commit し、ロック解放後に再観測する**:
  不採用。ロック解放後の再観測は他 Actor の書き込みを `ProviderContractViolation` と誤診し、また Commit 先行は不正な `Created` 記録を残すため。

## 影響とトレードオフ

- **得るもの**:
  - 回復が兄弟パスの変更に影響されない。「未確認作成の非昇格」と「回復記録より前の観測耐久化」を、電源断を含む故障モデル上の帰納的不変条件として証明する対象にできる（証明は [[0005]] のゲートで行い、本 ADR の時点では未証明）。電源断・永続化喪失に対する実効性は、Phase 0 で固定する耐久性モデル・対応 FS・`Stabilize` 実装の別検証を要する。
  - `MetadataCandidates` のみの実行では `Intent` 書き込みとファイル置換が発生しない（`Stabilize` の同期と1回のストア Commit は必要）。
- **失うもの・引き受けるリスク**:
  - 新規作成の `ApplyBatch` 成功後・所有 Commit 前にクラッシュした場合、そのパスの `Origin` は `Modified` に降格するため、後日退役させる際には自動削除されず手動削除が必要になる。
  - ロック規約を無視して対象ファイルを直接書き換える非協調的な外部プロセスとの同時書き込みに対しては、原子性は保証されない。

## 関連ADR

- [[0001]]（観測モデル）、[[0002]]（所有ストア）、[[0003]]（計画パイプライン）を前提とする。
- [[0005]]（結果集約・形式検証・反例スイート）が本 ADR のプロトコル状態遷移と異常系を検証対象とする。
