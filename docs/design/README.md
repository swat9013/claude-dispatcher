# 設計 doc

本ディレクトリが claude-dispatcher の設計の正本。実装の進行に合わせて最終形で書き換える (決定の理由は [`docs/adr/`](../adr/) に不変の記録として残す)。

## 索引

| file | 中身 |
|---|---|
| [`system.md`](system.md) | システム境界 / 全体図 / 解く問題 / 状態の表現 / 機械と LLM の線 / trigger / 起動・retry・打ち切り / workflow 定義 / 駆動 / worker に渡すもの / 配布と外部依存 / スコープ外 / CLI の seam |
| [`usecases.md`](usecases.md) | Cockburn 縮約テンプレートによるユースケース。冒頭に記述規約 |
| [`domain.puml`](domain.puml) | ドメインモデル図。entity・集約・多重度・不変条件と、各 entity の正本の持ち主 (外部 store / loop の記憶 / CLI の disk)。描画結果は `domain.svg` |
| [`statechart.puml`](statechart.puml) | claim の状態機械図。claim の段階と打ち切りが、どの出来事でどう遷移するかと、意図した未定義の組み合わせの理由。描画結果は `statechart.svg` |
| [`formats.md`](formats.md) | 外から観測できる形式。black-box テストの参照先 |
| [`internal/guide/guide.md`](../../internal/guide/guide.md) | 利用者のセッションへ出す案内。workflow 定義の書き方の正本で、action が守る約束 (`system.md` §7) と導入の流れ (`usecases.md` の UC-5) を利用者の語で添える。binary に埋め込み、`claude-dispatcher guide` が出す (ADR 0012)。本ディレクトリの外に置くのは embed のため |

用語は [`CONTEXT.md`](../../CONTEXT.md)。

## 設計を先に直す

構造を変えるときは、実装 (CLI のコード) より先に上の正本を直す。実装が先に着地すると、設計 doc が実装の後追い要約に落ちて正本でなくなる。

| 変えるもの | 正本 |
|---|---|
| 状態の表現と書き手、候補の定義式、機械と LLM の線、trigger の文法と評価の規則、起動・retry・打ち切り、workflow 定義の項目と検査の姿勢、駆動、worker に渡すもの、配布と外部依存、CLI の seam (部品の責務と seam の位置) | `system.md` |
| ユースケース | `usecases.md` |
| entity・集約・多重度・不変条件 | `domain.puml` (直したら `domain.svg` を描き直し、下の検証シナリオを通し直す) |
| claim の段階と遷移 (どの段階でどの出来事にどの遷移があるか) | `statechart.puml` (直したら `statechart.svg` を描き直し、下の網羅検査を通し直す)。遷移の条件と効果を決める規則は `system.md` §7 |
| 外から観測できる形式 | `formats.md` |
| workflow 定義の書き方 (項目・述語・template・何が誤りになるか・事前検査がどこを探すか) | `internal/guide/guide.md` (検査で CLI が何を出すかは `formats.md` §2.8・§2.9) |

振る舞いの変わらないリファクタリング・bug fix・実装内部だけの変更は先に直さなくてよい。迷ったら先に直す。決定を覆すときは ADR を新しく起こす (既存 ADR の本文は書き換えない)。

## ドメインモデル図

[![ドメインモデル図](domain.svg)](domain.svg)

正本は [`domain.puml`](domain.puml)。`domain.svg` はそこから描いた生成物なので、直すのは `.puml` の側で、直したら `plantuml -tsvg docs/design/domain.puml` で描き直して対で commit する。`-tsvg` は構文の誤りでも誤りの画像を出して exit 0 を返しうるので、先に `plantuml --check-syntax docs/design/domain.puml` の exit code で構文を確かめる。

entity に付けた印は、その entity の正本がどこにあるかを表す。dispatcher は外部 store に書かないので、`«外部 store»` の entity (issue・CL と、その置き場) の正本は dispatcher の外にある。adapter は読むたびにそれを作業対象へ写すだけで、写しを持ち越さない (`system.md` §13)。dispatcher が自分で持つのは、loop の process と寿命を共にする `«loop の記憶»` (claim・打ち切り) と、disk に残る `«CLI の disk»` (workspace) だけ。

## 検証シナリオ

ドメインモデル図が、具体の作業対象の遷移列を矛盾なく表せるかを確かめる。scope key はどれも `github.com/acme/widgets`、trigger は `implement` (issue 側) と `fix-ci` (CL 側)。

1. **正常系: issue の完了と、別の作業対象としての CL**
   1. `issue#42` が `implement` に当たる。loop に claim (`issue#42`, `implement`, attempt 1, 走っている, session `s-42a`) が立ち、workspace `issue-42` ができる
   2. worker が `CL#57` を開き、`issue#42` から着手可の label を外して終わる。claim は終わりを確かめ待ちになり、読み直した `issue#42` が `implement` から外れているので完了し、claim を解く。`issue#42` は open のままなので workspace `issue-42` は残る
   3. `CL#57` の CI が落ちて `fix-ci` に当たる。claim (`cl#57`, `fix-ci`, attempt 1) が立つ。`issue#42` と `cl#57` は種類の違う Ref なので、番号の空間を共有する GitHub でも、issue と merge request に別々に番号を振る GitLab でも取り違えない
   4. `CL#57` が merge され、`issue#42` が close される。終端を観測した tick が workspace `issue-42` を消す
2. **異常系: retry の上限で打ち切り、人が解く**
   1. `issue#43` の claim が attempt 1・2・3 (上限 3) と失敗する。3 回目の失敗で claim を解き、打ち切り (`issue#43`, `implement`) を立てる。同じ作業対象に claim と打ち切りが同時に立つ時点は無い
   2. 打ち切りの間は `issue#43` が `implement` に当たっても候補にならない。workspace `issue-43` は残る
   3. 人が label を外し、1 周期待ってから付け直す。外れたのを観測した tick が打ち切りを解き、次の tick で新しい claim (`issue#43`, `implement`, attempt 1) が立つ。attempt は新しい claim に積まれ、前の claim の数は引き継がない。workspace `issue-43` は同じ path のまま使う
3. **境界: loop の起動し直し**
   1. loop A に claim (`issue#44`, 走っている) と打ち切り (`issue#45`) がある。2 回の停止要求で worker を止め、loop A が止まる。claim と打ち切りは loop の記憶なので消える。workspace `issue-44` は disk に残る
   2. 同じ scope key で loop B を起動する。loop A がまだ動いていれば、scope key の lock で loop B は起動時に止まる (project 1 ─ 0..1 loop)
   3. loop B は起動時に終端の作業対象の workspace を掃除する。`issue#44` は open なので残す。`issue#44` が `implement` に当たったままなら、新しい claim (attempt 1) が立ち、同じ workspace の path で起動する。`issue#45` の打ち切りは消えているので、当たっていれば再び候補になる

3 シナリオとも、図の多重度 (作業対象 1 ─ 0..1 claim / 0..1 打ち切り / 0..1 workspace) と集約の境界 (loop の記憶は loop と共に消え、workspace は残る) のまま表せる。起動は claim (loop 集約) と workspace を続けて作るが、両者をまたぐ不変条件は無い (シナリオ 3 のとおり、claim の無い workspace は普通に在る)。そのため、2 つを別々の集約のままにしておける。

## claim の状態機械図

[![claim の状態機械図](statechart.svg)](statechart.svg)

正本は [`statechart.puml`](statechart.puml)。作業対象 1 件について、loop の記憶 (claim と打ち切り) が出来事ごとにどう移るかを描く。状態は claim なし・claim の段階 4 つ・打ち切り、出来事は `system.md` §7 と §9 が挙げるもの。完了と claim を解くは、claim なしへ戻る遷移の効果で表す。

- **網羅の正本は図、規則の正本は §7**: どの「段階 × 出来事」にどの遷移があるかは図が持ち、遷移の条件と効果を決める規則 (backoff の式・再起動の前の読み直し・曖昧な CL の扱い 等) は §7 が散文で持つ。同じ遷移を図と §7 の両方に列挙しない
- **当てない組み合わせには理由を書く**: 遷移の無い組み合わせは、図の中に `' uncovered: <状態> x <出来事> — <理由>` で理由を書く。理由の無い組み合わせは定義漏れとして検査が落とす
- **直したら 3 つを通し直す**: 先に `plantuml --check-syntax docs/design/statechart.puml` の exit code で構文を確かめ (`-tsvg` は構文の誤りでも exit 0 を返しうる)、`plantuml -tsvg docs/design/statechart.puml` で描き直して対で commit する。網羅は Claude Code plugin `swat-skills` の sud-design skill に同梱の `scripts/check-statechart.py` に `docs/design/statechart.puml` を渡し、exit 0 で確かめる (全組み合わせが遷移か理由付きの宣言で埋まり、どの状態にも初期状態から届くこと)
