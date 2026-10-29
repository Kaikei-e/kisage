# 必須反例サンプル #1〜#60

本書は構想書ドラフト v7（2026-10-29）§11 の60件を移した規範的な適合性サンプルである。番号と期待値は v7 のまま保ち、シナリオ列は構想書を読まずに前提が分かる文へ書き換えた（末尾の変更記録を参照）。[ADR 0005](../ADR/0005-cli-mcp-result-aggregation-and-verification-gates.md) が本書を検証ゲートとして採用する。実装とテストは、各行だけを独立した規則と解釈せず、対応 ADR のモード・権限・観測・永続化条件を満たさなければならない。

これは期待動作の仕様であり、実行済みテストや証明の報告ではない。入力形式・トークン符号化・ストア方式等の具体化は ADR 0005 の Phase 0 決定事項に従う。Git 管理外の議論記録への参照を必要としない。

## 固定した60件

| # | シナリオ | 期待値 |
| :--- | :--- | :--- |
| 1 | 対象ファイルに宣言外の兄弟葉があり、宣言内の葉だけを変更する | 論理値を保存 |
| 2 | 宣言葉が未所有で観測が Absent（必要祖先は作成可能） | Set 成功を確認して Created |
| 3 | 宣言葉の対象または必要祖先の観測が Unknown | 値も所有記録も変えない |
| 4 | 計画時の観測後、ApplyBatch のロック内再検査までに対象値が変化する | 条件不一致でバッチ全体を未適用 |
| 5 | Origin=Modified の自所有葉を宣言から除去して apply する | apply で記録だけ解除 |
| 6 | 新規作成（Origin=None）の Intent 保存後にプロセスが停止し（ApplyBatch の実行有無は不明）、その間に外部主体が Target と同じ値を作成する | 回復時 Modified に降格。後に prune しても消さない |
| 7 | 既存所有葉の更新 Set が Applied となった後、所有 Commit 前にプロセスが停止する | Target 観測で LastApplied を回復。従来 Origin を維持 |
| 8 | 宣言葉に別 Actor の所有記録があり、観測は Absent | OwnershipConflict |
| 9 | 宣言文書に非ルートの空 Record がある場合と、宣言文書全体が空の場合 | K102。空のルート宣言は許可 |
| 10 | Apply で、宣言葉の位置が Record（空を含む）または Opaque、または真の祖先が Leaf / Opaque | 構造を壊さず停止。空Recordも含む |
| 11 | 宣言葉が未所有で、観測値が宣言値と Equal | Own 権限下だけ Modified として採用 |
| 12 | Match_A 成立後に同じ宣言で apply / prune を再実行する | 環境変更・記録更新なし |
| 13 | Expected ≠ Target の同じ Change を、計画を経ずに ApplyBatch へ2回続けて渡す | 2回目は Expected 不一致 |
| 14 | plan 実行後、apply 開始前に宣言ファイルを変更する | apply 開始時の宣言で再計画し、digestを報告 |
| 15 | 引用符付きセグメント、親子関係にある Scope、文字列としては前方一致するが別セグメントのパス（例: `/a/b` と `/a/bc`） | セグメントと操作ごとに正しく判定 |
| 16 | CLI の `--actor` 引数や MCP ツール引数で別 Actor を名乗る | 引数を信頼せず、ホスト束縛を使用 |
| 17 | 対象パスの経路成分が symlink・FIFO・デバイス等の特殊ファイル、または検査から open までの間に差し替えられる | 経路・ハンドル検査で拒否。実行環境の前提も検査 |
| 18 | Provider が Applied を返すが、書いた値が Set(v) の v から変形・正規化されている | 同じ呼出しの再観測で契約違反を検出 |
| 19 | 所有衝突・ドリフト等の衝突を解消しないまま、同じ宣言で apply を繰り返す | 上書きせず停止。収束成功と数えない |
| 20 | 旧宣言の葉と新宣言の葉が祖先・子孫で重なる版間移行（重なる旧所有が Created を含む場合と、Modified だけの場合） | CreatedはMigrationRequired。Modifiedは旧記録解除と新側Deferredを分ける |
| 21 | Actor A の実行時、A の宣言と重ならないパスに Actor B の所有記録がある | B の値も記録も変更せず、偽の衝突なし |
| 22 | Pending のない自所有葉が外部主体に削除され Absent になる | DriftDeleted。再作成しない |
| 23 | 一致採用・退役 Modified の管理解除・回復更新の対象パスに Own 権限がない | Own 不足なら記録も変えない |
| 24 | 回復時、Pending の Expected とも Target とも異なる葉値、Record、Opaque、または祖先構造の衝突が観測される | PendingConflict。盲目的な Commit/Rollback なし |
| 25 | 回復時、Op=Delete の Pending に対し観測が Absent、または Expected と一致する | 記録消去／PreviousEntry 復元。apply は削除を再発行しない |
| 26 | ApplyBatch で Root.Rename の後、親ディレクトリの fsync が失敗する | Indeterminate と Pending 維持 |
| 27 | 新規作成が Applied となった後・所有 Commit 前に停止し、再開までに同じファイルの宣言外兄弟値が外部で変更される | 全体digestに依存せず Target を観測し Modified で回復 |
| 28 | 複数葉のバッチで一部の Expected が不一致になる、または適用・保存の途中でクラッシュする | 部分ファイル置換なし。記録の原子保存と回復を確認 |
| 29 | apply 実行時、宣言から外れた Created 葉が Leaf(LastApplied) のまま残っている | PruneRequired。削除権を維持し、prune でだけ削除 |
| 30 | 別 Actor が同じパスを同じ値で所有している、または祖先・子孫パスを所有している状態で宣言する | 共有所有・二重予約を生まない |
| 31 | 同じモード内に、実行可能な候補と Unknown・所有衝突等の Blocker が混在する | 通常変更と通常記録更新を全件抑止しBlocked。診断と候補を保存 |
| 32 | Set で中間 Record を暗黙作成した後、その下の最後の葉を prune で削除する | 空Recordを残し、兄弟値を保存 |
| 33 | 同じ物理ファイルを別パス名・ハードリンク・別の所有ストアで Resource 登録しようとする | 同じ対象を別所有として登録させない |
| 34 | Created 葉を自 Actor が値更新した後、外部主体がその葉を削除して同じ値で再作成する（ABA） | 履歴不変条件を検査し、ABA検出を保証に含めない |
| 35 | Intent 保存・ApplyBatch・所有 Commit・回復保存の各書込と同期の前後で、プロセス停止・電源断を注入する | 壊れた記録を成功扱いせず、削除権を推定しない |
| 36 | Created 葉が外部で値 v に変更され、その後宣言も v に変更される（観測 = 宣言 ≠ LastApplied） | 通常applyは停止。明示adoptでModifiedへ降格後に収束 |
| 37 | 外部削除された自所有葉を再作成したい場合と、未所有の既存不一致値を宣言値へ導入したい場合 | release→apply、adopt→applyの正規経路で解消 |
| 38 | adopt / release を、Resolve 権限なしで、別 Actor 所有パスに対して、または explain 後に値・構造・所有版が変わった古い TOKEN / REV で実行する | 値も記録も変えず拒否 |
| 39 | NoOp または Deferred に分類されるパスに Set / Delete 権限がない | 架空のPolicyDeniedを生成しない |
| 40 | Actor B の未解決 Pending が残る間に、Actor A が B の予約と重ならない（または重なる）パスを通常更新する | Bの値・予約を保存してAは実行可能。重複なら拒否 |
| 41 | 自 Actor の未回復 Pending がある状態で plan を実行する | 書込・fsyncなし。回復案と条件付き通常計画を表示 |
| 42 | ApplyBatch が Applied を返した直後に、同じロック規約に従う別主体がロックを待っている | 契約検証とCommitまでロック。解放後の変更をK501と誤診しない |
| 43 | Root.Rename の後・親ディレクトリ fsync の前にプロセスが停止し、回復時に Target が観測される | 回復時Stabilize後にだけ記録をCommit。失敗時Pending維持 |
| 44 | 回復保存が完了した後、同じ呼び出しの通常計画に Blocker がある | 回復記録は保持し、通常候補は全件抑止。結果を分離 |
| 45 | Provider が Applied を返すが、ロック内再観測で Set(v)→Leaf(v)・Delete→Absent・兄弟値保存のいずれかが成り立たない | Commit前に契約違反を検出し隔離。通常回復で成功にしない |
| 46 | EnvironmentOps と記録だけの採用・解除（MetadataCandidates）を含むバッチの ApplyBatch が NotApplied になる | NotApplied時に通常記録変更も適用せず復元 |
| 47 | 対象ファイル側に空 Record がある場合と、宣言側に非ルート空 Record がある場合 | 前者は正常観測、後者のみK102 |
| 48 | 同じモードの実行候補のうち1件だけ Own 権限が不足する | ほかのSet・記録更新も全件抑止 |
| 49 | 退役 Modified の旧葉（例 `/a/b`）と新宣言葉（例 `/a/b/c`）が親子で重なり、実環境は外部で新構造へ移行済み | 旧記録のみ解除、新側Deferred。再実行で一致採用 |
| 50 | adopt / release で Stabilize が失敗する、またはストア Commit が失敗・結果不明になる | 前者は記録未変更。Commit結果不明なら再読込まで成功とも未変更とも断定しない |
| 51 | Apply の実行候補があり、それと重ならない退役 Created 葉がドリフトまたは Unknown | 退役側はDeferredで記録保持。Apply候補は実行可能 |
| 52 | Prune の削除候補があり、それと重ならない宣言内葉または退役 Modified 葉に異常（ドリフト・所有衝突・Unknown 等）がある | 異常側はDeferred。Prune候補は実行可能 |
| 53 | 退役 Created 葉が正常な Absent または Unknown で、そのパスに Own 権限がない | 正常なAbsentのみOwnを検査し拒否で全件抑止。UnknownはApplyでDeferred |
| 54 | 退役 Created 葉と新宣言葉が親子で重なり、別に独立した実行候補もある | 両モードでMigrationRequiredをBlockerとし全件抑止 |
| 55 | 退役 Modified の旧葉と新宣言葉が親子で重なり、実環境は旧構造のまま（新側は構造衝突） | 初回は新側DeferredUntilRelease、旧記録のみ解除。次回はStructureConflict |
| 56 | 新宣言葉が旧 Modified の解除待ちで、旧側の観測が Unknown、または旧側に Own 権限がない | 新側をDeferredにしても旧側のBlockerで全件抑止 |
| 57 | 通常バッチが記録だけの変更（一致採用・管理解除・不在確認済み Created の記録削除）のみからなる | Intent/Pending/ApplyBatchなし。Stabilize成功後にストアCommit一回 |
| 58 | EnvironmentOps と MetadataCandidates を含むバッチで、Intent 保存後にプロセスが停止する | 環境操作だけを回復。記録候補の永続予約・自動再生なし |
| 59 | 回復で Pending を解消して保存し（recovery=Completed）、通常計画は変更なし（normalExecution=NoOp） | summary=Completed。両方NoOpのときだけNoOp |
| 60 | 回復保存後に通常計画が Blocked になる場合と、環境適用後にストア Commit の結果が不明になる場合 | 前者は回復件数を残してBlocked、後者はIndeterminate |

## テストへの具体化と ADR の対応

以下は主な検査層の対応であり、同じ番号を複数の層で検査してよい。実装時は、前提となる宣言・Actor・Resource・Scope、対象と必要祖先の観測、共有所有ストア（他 Actor の予約を含む）、モード、停止・同期失敗の位置を明示し、環境・記録・結果の期待値を検査する。成功件数だけでなく、変更されなかった値・記録・予約と抑止された候補も検査する。

| 規範 | 主な反例番号 | 主な検査層 |
| :--- | :--- | :--- |
| [ADR 0001: 射影・観測・構造保護](../ADR/0001-leaf-path-projection-and-observation-sum-type.md) | #1, #3, #9, #10, #32, #47 | 純粋関数 PBT・Lean/Go DRT・Provider 適合性 |
| [ADR 0002: 共有所有・Scope・信頼境界](../ADR/0002-shared-ownership-store-scope-and-trust-boundary.md) | #8, #11, #15, #16, #21, #23, #30, #33, #34, #38, #40 | 純粋関数 PBT・Lean/Go DRT・実行環境の統合検証 |
| [ADR 0003: モード・全件可否・明示解消](../ADR/0003-pure-plan-pipeline-mode-separation-and-explicit-resolution.md) | #2, #5, #12, #19, #20, #22, #29, #31, #36, #37, #38, #39, #48, #49, #50, #51, #52, #53, #54, #55, #56 | 純粋関数 PBT・Lean/Go DRT・CLI/MCP 統合 |
| [ADR 0004: Provider・耐久化・回復](../ADR/0004-file-batch-persistence-stabilize-recovery-and-provider-contract.md) | #4, #6, #7, #13, #17, #18, #24, #25, #26, #27, #28, #32, #35, #38, #40, #42, #43, #45, #46, #50, #57, #58 | プロトコル状態機械・Provider 適合性・実ファイル障害注入 |
| [ADR 0005: プレビュー・結果・検証ゲート](../ADR/0005-cli-mcp-result-aggregation-and-verification-gates.md) | #14, #16, #19, #31, #41, #44, #59, #60 | Lean/Go DRT・CLI/MCP 統合・結果集約 |

## 境界条件の具体化

### モードによる停止範囲（#3, #8, #29, #31, #48, #51〜#56）

- #3 の「値も所有記録も変えない」は、対象または必要祖先が Unknown であるパスへの変更非生成を検査する。同じ呼び出し全体の通常候補を止めるかは ADR 0003 の SelectMode と Gate に従い、独立したモード外異常は Deferred とする。
- #8 は別 Actor の所有権を値の不在だけで取得しないことを検査する。宣言内パスの所有衝突は Apply では Blocker、Prune では独立したモード外の Deferred である。
- #29 の PruneRequired は、退役 Created が Leaf(LastApplied) で必要祖先に異常がない場合である。正常な Absent は両モードの記録解除候補、独立したドリフト・Unknown は Apply で Deferred、Prune で Blocker とする。
- #31・#48 は当該モードの Blocker / PolicyDenied による通常環境変更と通常記録更新の全件抑止を検査する。先に確定した回復記録は巻き戻さない。
- #53 は正常な Absent のときだけ Own を検査する。Unknown を Absent と推定して権限検査や記録解除候補を生成しない。
- #54 は旧 Created と新宣言の親子重複集合を両モードの Blocker とし、独立候補があっても通常候補を全件抑止する。

### 旧 Modified の解除待ち（#20, #49, #55, #56）

例として、A が Modified として所有する `/a/b` を退役させ、新宣言で `/a/b/c` を要求する。旧所有に Pending がなく、他 Actor の重複所有・予約もないものとする。

1. 初回 Apply は新側を DeferredUntilRelease とし、新側の Unknown / StructureConflict を実行上の Blocker に昇格させない。
2. 旧側の対象と必要祖先が既知で Own があれば、旧記録のみを解除する。旧側が Unknown / Own 不足なら全件抑止する。
3. 次回は新側を通常分類する。外部で新構造へ移行済みで値が一致していれば Modified として採用し、旧構造のままなら StructureConflict で停止する。

旧記録の解除と新側の採用を同じバッチにまとめず、解除だけから収束を推論しない。

### 旧 Created を含む構造移行（#20, #54）

重なる旧所有に Created が含まれる場合は、両モードで MigrationRequired（K204）を Blocker とし、独立候補があっても通常候補を全件抑止する。診断の必要な次操作は ADR 0003 の正規手順（明示 release による削除権放棄 → 管理者による実環境の移行と旧値の整理 → apply で新側を通常分類）とし、ランタイムはいずれも自動実行しない。

### 明示解消の条件と耐久化（#36〜#38, #50）

explain / adopt / release は apply / prune と同じ宣言 FILE を受け取り、Resource は FILE に対応する保護された登録から選ぶ。`explain` の後に、値・必要祖先の構造・所有版・権限を変える場合を分けて検査する。未所有状態にも所有版を持たせる。adopt / release は共有ストア→Resource のロック内で Resource・パス・観測トークン・所有版・権限と拒否条件を再検査し、Stabilize 成功後にだけ原子的に記録を保存する。

TOKEN / REV の不一致や Stabilize 失敗なら値も記録も変えない。ストア Commit の結果が不明なら再読込まで成功とも旧版の維持とも断定しない。adopt は常に Modified とし、Created へ昇格しない。release は値を消さず、PATH が FILE の宣言に残っている場合は再管理されうることを診断する。adopt / release の可否に宣言との一致は要求しない。TOKEN / REV 不一致等の拒否は summary.execution=Blocked、Stabilize の Indeterminate は Indeterminate、ストア保存の確定失敗は Failed とする。ランタイムによる自動呼び出しは許可しない。

### ロック・契約違反・隔離（#17, #18, #35, #42, #45）

対象ファイルとは別の保護された安定パスにロックを置き、rename 後も同じロックで協調的書き手を排除できることを検査する。経路成分の差替え、特殊ファイル、ハードリンク、ファイル別名、マウント境界とメタデータ保存は Provider 契約として検査する。

Applied 後は同じロックを保持して再観測・保存契約を検証し、成功時だけ所有 Commit する。契約違反は隔離し、通常回復で成功にしない。契約違反を検出した呼び出しの summary.execution は、環境変更の可能性があっても PartiallyApplied ではなく Failed とし、K501・K503 と残る Pending を結果から省略しない。隔離情報の保存失敗ではランタイムを停止し、同じ Resource を通常再開しない。管理者復旧でも値一致だけで Created を再付与せず、帰属不明は Modified とする。ロック解放後の外部変更だけを K501 と診断しない。

### 回復と環境変更の分離（#6, #7, #24〜#28, #34, #35, #40, #43）

保存済み Expected / Target を現在の宣言で書き換えず、パスごとに回復を分類する。Own を検査し、現在観測を Stabilize した後に回復記録をまとめて保存する。Expected への復元や Absent による記録削除にも耐久化を要求する。

Set の Target 一致でも新規所有の Origin=None は Modified に降格し、K601 を返す。従来の Created / Modified は維持する。Expected 一致は現在の整合性を示すだけで未適用だった履歴の証明ではない。apply は PendingDelete を再発行しない。他 Actor の非重複 Pending は変更しない。rename 後の同期失敗は NotApplied でなく Indeterminate とする。

### 記録だけの経路と混在バッチ（#46, #50, #57, #58）

| 経路・停止位置 | 環境操作 | 記録・結果の期待値 |
| :--- | :--- | :--- |
| MetadataCandidates のみ | Intent / Pending / ApplyBatch / ファイル置換を生成しない | 観測条件・所有版・権限を再検査し、Stabilize 成功後にストア Commit を1回行う。Applied Receipt を合成しない |
| 記録だけの経路で条件検査・Stabilize が失敗 | 環境変更なし | Commit しない。新たな Pending を生成しない |
| EnvironmentOps と記録候補が混在し NotApplied | 環境変更なし | 環境 Pending を PreviousEntry へ戻し、記録候補も保存しない。summary.execution は Blocked |
| 混在バッチの Intent 後に停止 | 環境操作の Pending のみ永続化済み | 保存済み環境操作だけを回復し、記録候補は自動再生せず最新状態から再計画する |
| 環境適用済みで所有 Commit が未完了と確認済み | 環境だけを巻き戻さない | Pending と PartiallyApplied を返す |
| 所有 Commit の結果自体が不明 | 適用済み環境を再適用しない | 再読込まで Pending の有無も断定せず Indeterminate とする |

### 読み取り専用 plan と結果集約（#4, #14, #41, #44, #45, #59, #60）

plan は認可された回復だけを仮想所有状態に反映し、拒否された Pending は残す。同じ仮想状態から Apply と Prune を独立に計算し、書き込み・fsync をしない。回復の必要な Stabilize と保存を実行済みと表示しない。実行時は開始時の宣言を一度読み、その digest を報告する。plan の JSON は共通の recovery と、plans.apply / plans.prune ごとの normalExecution・pathResults・summary を持ち、トップレベルの summary.execution は両モードのうち集約表で優先順の高い値とする。

| recovery | normalExecution / 確実性 | summary.execution |
| :--- | :--- | :--- |
| Completed | NoOp | Completed |
| Completed | Blocked | Blocked。確定済み回復件数を保持 |
| 任意 | 環境適用・ストア Commit の結果不明、または Stabilize が Indeterminate | Indeterminate |
| 任意 | Provider 契約違反を検出して隔離（#45） | Failed |
| 任意 | 環境適用済み・記録未確定が確認済み（Applied 後の Stabilize Conflict を含む） | PartiallyApplied |
| 任意 | NotApplied（条件不一致で環境変更ゼロ、#4） | Blocked |

execution と convergence を分け、Deferred や prune 待ちを完了した宣言の収束と混同しない。plan の詳細終了コードは両モードの Blocker・PolicyDenied・未解決回復を差分より優先して1とし、記録だけの差分と Deferred の残作業も2の対象とする。

## 保持・変更規則

- #1〜#60 の番号を再利用・再採番しない。新しいシナリオは #61 以降へ追加する。
- 期待値を変える場合は対応 ADR を同時に更新し、変更理由と影響する番号を記録する。単なる要約で前提・失敗経路を削らない。
- 実装テストへの展開時は同じ番号を使用し、一つの番号に複数の必要な条件がある場合はすべてを検査する。
- プロセス kill だけで電源断や永続化喪失を検証済みとしない。各永続化境界の故障注入と耐久性モデル・対応 FS の検証を分ける。
- この文書と対応 ADR の矛盾を見つけた場合は検証ゲート未達として解消し、都合のよい側だけに従って適合としない。

## 変更記録

| 日付 | 変更 | 理由 | 影響する番号・ADR |
| :--- | :--- | :--- | :--- |
| 2026-10-30 | シナリオ列を構想書なしで前提が分かる文へ書き換えた。番号と期待値の列は v7 から変えていない | v7 の文脈に依存する略記が多く、ADR 0005 の「構想書を読まなくても期待値が分かる」要件を満たさないため | #1〜#60 |
| 2026-10-30 | 結果集約に契約違反（Failed）、NotApplied（Blocked）、Stabilize の Indeterminate / Conflict、plan の両モード集約を追加した | v7 の集約表では契約違反→Failed に到達せず、NotApplied と Stabilize 失敗の扱いが未定義だったため | #4, #41, #45, #50 / ADR 0005 |
| 2026-10-30 | explain / adopt / release が宣言 FILE を取るようにした | 宣言なしでは release の再管理診断と explain の停止理由を計算できず、Resource の選び方も未定義だったため | #36〜#38, #50 / ADR 0003, 0005 |
| 2026-10-30 | K206（未所有値の不一致）と K604（削除待ち）を追加し、K204 の正規の次操作を定めた | UnownedValueConflict と PruneRequired にコードがなく、K204 の次操作が未定義だったため | #20, #29, #37, #54 / ADR 0003, 0005 |
