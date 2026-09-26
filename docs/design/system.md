# システム構成

- 本 file が構造の正本。ユースケースは [`usecases.md`](usecases.md)、外から観測できる形式 (config / 指示ファイル / 決定ファイル / log / 出力) は [`formats.md`](formats.md) が正本
- 用語は [`CONTEXT.md`](../../CONTEXT.md)、決定の理由は [`docs/adr/`](../adr/)
- orchestrator の手順と spawn prompt の文面は CLI に同梱する契約 file が正本 (本 file は構造の決定だけを持つ)

## 1. システム境界

機械システムは **CLI `claude-dispatcher` 1 つと、その入出力 (宣言 config / snapshot / 指示ファイル / 決定ファイル / log)** だけ。LLM (orchestrator / worker)・人間・cron・外部 store はすべて境界の外のアクター。

CLI の subcommand は役割で 3 群に分かれる。

| 群 | subcommand | 書くもの |
|---|---|---|
| 駆動 | `tick` | state dir (log / 指示ファイル / lock / worker log)。外部 store には書かない |
| 観測 | `status` / `paths` | 何も書かない (読み取り専用) |
| 導入 | `setup` / `doctor` | `setup` だけが config の雛形・tracker label・crontab を書く (いずれも導入者の承認後)。Claude Code の settings はどちらも書かない |

`status` は log の `spawned` (起動記録) を起点に、wip の付いた issue と生きている worker process を 1 行ずつ並べ、process の生死・wip・紐づく CL を毎回読み直して出す。**process が死んでいるのに wip が残っている行が stale wip の手掛かり**になる (snapshot からは区別できないが、起動記録と process の生死を突き合わせる `status` からは見える)。

| アクター | 方向 | 関わり |
|---|---|---|
| cron | → | `tick` を定期起動する。取りこぼしは補償しない (次 tick が現実を読み直す) |
| tracker | ← 観測 / LLM が書く | issue・label の置き場。`tick` は**読むだけ**で、状態を表す label を書かない。例外は導入時の `setup` が導入者の承認を得て label を作ることだけ |
| CL host | ← 観測 / LLM が書く | CL の置き場。CLI は**読むだけ** |
| orchestrator (LLM) | ← 起動される / → 決定ファイル | 指示ファイルを読み、採否判断と実行 (wip 付与 / 見送り / 人へ返す) を行う。worker の起動は**決定ファイルに書く**だけで、プロセスは起こさない (§9) |
| worker (LLM) | — | 機械システムと直接やり取りしない。実装・CL 作成・終端信号 (label / issue コメント)・残タスクの issue 起票はすべて外部 store へ書く |
| plugin `swat-skills` | ← 読む | playbook と原則索引の置き場。CLI は path を解決して LLM に渡すだけで、中身を解釈しない (§11) |
| 人間 | — | triage (着手可の付与・人待ちの解消・stale wip の回収) と CL の merge。機械システムとの接点は log・`status`・`doctor` の読みだけ |

## 2. 全体図

```
 人間 ── triage (着手可 label / ready-for-human 解消 / stale wip 回収 / merge)
   ▲                                                      │
   │ 引き渡しコメント + ready-for-human                      │ label
   │                                                      ▼
 ┌──────────────────────── 外部 store (現実) ─────────────────────────┐
 │  tracker (issue / label)      CL host (CL / thread / CI / branch)  │
 └───────▲──────────────▲───────────▲──────────────▲─────────────────┘
         │観測           │wip付与    │CL作成・終端処理│観測
         │               │           │               │
 cron ──▶ tick           │           │               │
         │ config読み     │           │               │
         │ snapshot+指示  │           │               │
         │ log 1行append  │           │               │
         ├─ 指示0件 → 終了 │           │               │
         └─ 指示あり ──▶ orchestrator ──決定ファイル──▶ tick ──spawn──▶ worker ─┘
            (claude -p)   採否判断+wip付与   (決定どおり起動) (headless)  実装→CL→worktree削除
```

## 3. 解く問題と解き方

issue から CL までを無人で回す機構は、次の 4 つの失敗に陥りやすい。

1. **要素過剰** — 台帳・常駐 daemon・dashboard・pane 管理を持つと、目的 (issue → CL と CL の手直し) に対して構成が重くなり、改善のたびに全要素との整合を払う
2. **往復プロトコル** — orchestrator と worker の間で質問を中継すると、返信先の受け渡し・再起動後の送り直しが複雑さの大半を占める
3. **drift** — 台帳 (意図) と外部 store (現実) が食い違い、その解消を恒常的に管理し続ける
4. **silent な機械遷移** — 宣言の綴り誤りを根拠に機械が状態を書き換え、稼働中の作業を壊す

解き方は「**状態を成果物に置き、書くのを LLM に限る**」。台帳を持たなければ drift は存在せず、機械が状態を書かなければ silent 遷移も存在しない。往復は持たず、LLM は判断の根拠を成果物 (CL 説明文・引き渡しコメント) に残す。

## 4. 状態の表現

| 状態 | 表現 | 書き手 |
|---|---|---|
| 着手可 | triage の着手可 label (宣言 config で綴りを指定) | 人間 / triage |
| 着手中 (claim) | `dispatcher:wip` label | 付与 = orchestrator (新規着手も再入も)。人が手で付けて claim してもよい (dispatcher との二重着手を塞ぐ)。剥がし = worker の終了処理。orchestrator が剥がすのは、wip 付与や決定ファイルの書き込みに失敗した rollback と、anomaly を人へ返すときだけ |
| 実装済み (CL 待ち) | 紐づく open CL の存在 (label なし) | — (CL 作成が遷移) |
| 続行不能 (人待ち) | `ready-for-human` label + 引き渡しコメント | worker の終了処理 (anomaly からの人返しだけは orchestrator) |
| 完了 | CL の merge / issue close | 人間 (merge は人間の最終ゲート) |

状態の書き手はこの表が正本。他節と UC は書き手を言い直さず、この表を参照する。

- **候補 = 着手可 ∧ ¬wip ∧ ¬`ready-for-human` ∧ 紐づく open CL なし**。CL が merge されず close されると自動的に候補へ戻る (望ましい挙動)。`ready-for-human` は人が外すまで候補から外し続ける
- **途中成果の永続は remote branch** (worker が push したものだけが残る)。worktree は残らないので、再入は branch から worktree を作り直す
- **issue に紐づく CL** は、CL 置き場の open CL のうち、(a) CL 記述の closing reference がその issue を指すもの、または (b) head branch が worker の規約 `worktree-issue-<issue>` のもの。(b) は CLI が branch 名から機械的に判定するので、worker が closing reference を書き忘れても「実装済み」を見落として二重着手にならない
- 並列上限 N のカウンタは wip label の枚数
- stale wip の自動解消は持たない — triage の人間判断。人が手で付けた wip も N を 1 消費し、snapshot からは stale wip と区別できない (人が見つける手掛かりは `status`、§1)
- **どの状態も tick を跨いで機械が記憶しない**。毎 tick 外部 store を読み直して再構成する

## 5. 機械と LLM の線

**機械は外部 store に何も書かず、状態遷移を一切書かない**。機械が決めるのは snapshot から機械的に確定することだけで、選ぶ・見送る・人へ返すは LLM が決める。

| 主体 | やってよいこと | やらないこと |
|---|---|---|
| CLI (`tick`) | 観測の正規化 / **機械的に確定する指示の導出** / log への append / orchestrator の条件付き起動 / **決定ファイルに書かれた worker の起動** | 何を起動するかの決定 / label 操作 / tracker・CL への書き込み / 候補の優先順位付け |
| orchestrator | 指示の採否判断 / wip 付与 / worker の起動決定 (spawn prompt を決定ファイルに書く) / 見送り・人へ返す | tick を跨ぐ状態の保持 / worker への追加送信 / worker プロセスの起動 (理由は ADR 0002) |
| worker | 実装 / CL 作成 / 終端信号 (wip 剥がし・`ready-for-human`・引き渡しコメント) / 残タスクと範囲外の欠陥の issue 起票 (triage 用 label だけを付ける — §10) / worktree の自己削除 | orchestrator への問い合わせ / merge / 自分が起票した issue への着手可 label の付与 |

指示は「出す」だけで実行しないので、誤った指示は orchestrator が実行前に棄却できる。宣言誤りによる事故は、この線と config の loud 検査 (§8) の 2 段で塞ぐ。

## 6. 指示カタログ

CLI が導出する指示は 3 種。**カタログに無い事象は指示にならない**ので、分類できない観測を落とす受け皿として `anomaly` を必ず持つ (指示 0 件の沈黙と「観測したが分類できない」を区別する)。

| 指示 | 発火条件 (機械的に確定) | orchestrator の実行 |
|---|---|---|
| `start` | 候補があり、wip 枚数 < 上限 N (空き分だけ。候補と選定母集合の playbook を添える。母集合は §10) | 着手する issue を選び、wip を付けて worker の起動を決定ファイルに書く (選定・見送りは LLM) |
| `reenter` | 紐づく open CL がちょうど 1 本あり、その head が worker の branch 規約 (`worktree-issue-<issue>`) に従い、issue に wip も `ready-for-human` も無く、その CL に条件が立つ (立った条件を `conditions` に併記。語彙は下記の条件カタログ) | wip を付けて既存 branch への再入 worker の起動を決定ファイルに書く (条件ごとの対応手順は条件別 playbook が持ち、spawn prompt にはその path を `conditions` 順に載せる)。`start` より先に扱う (新規着手より既存 CL の完了が近い) |
| `anomaly` | 上記に分類できない観測 (wip 枚数が上限 N を超えている / wip と `ready-for-human` が同居している / 1 つの open issue に open CL が複数紐づく — 対象は label に依らず open issue 全件) | 見送りか人へ返す。どちらでも判断を決定ファイルに残す |

**条件カタログ** (条件名 → snapshot 上の述語 → 対応 playbook)。定義元は CLI の 1 箇所で、orchestrator の契約 file が持つ「条件ごとの読み直し方法」と条件名の並びが一致することをテストが検査する。

| 条件 | 述語 | 対応 playbook |
|---|---|---|
| `conflict` | CL が merge conflict (`mergeable = CONFLICTING`) | `playbook-conflict-resolution` |
| `review` | 未解決の review thread が 1 本以上 | `playbook-review-response` |
| `ci` | head commit の checks が `FAILURE` / `ERROR` | `playbook-ci-fix` |

- 指示の発火条件は **snapshot (§7) から導出できるものに限る** — 観測経路を持たない事実 (プロセスの生死等) を条件に使わない
- 同一 CL に複数条件が立つときは 1 指示にまとめ、カタログの順に併記する (worker は 1 回の再入で全部見る)
- **人が開いた CL には再入しない** (head の branch 名が worker の規約と違えば指示を出さない)。worker が他人の branch へ push する経路を持たないための線で、人の CL の conflict は人が解く。人の CL は統治対象外なので、この沈黙は anomaly にもしない
- **再入も wip を付けて起動し、wip の付いた issue には `reenter` を出さない**。dedup を持たないので、wip が無いと再入 worker が push するまで毎 tick 同じ CL へ worker が積み増される。並列上限 N も wip 枚数で共有し、CLI が機械的に守る: 空き slot の分だけ `reenter` を出し (余りは次 tick で再導出)、残りの空きを `start` の `free_slots` にする
- **`ready-for-human` の付いた issue には `start` も `reenter` も出さない**。人へ返した issue の指示が毎 tick 再導出されて orchestrator を起こし続けない
- **wip ∧ 紐づく open CL なし は anomaly にしない** — wip 付与から CL 作成までの正常状態と、worker が死んで残った stale wip は snapshot からは区別できない。anomaly にすると着手中の issue が毎 tick orchestrator を起こす
- 指示の dedup は持たない — 前 tick の指示が実行済みなら現実が変わっており、同じ指示は導出されない。実行されなかった指示は次 tick で再導出される (再送機構が要らない)。**orchestrator が見送った指示も再導出され、毎 tick 再判断するのが仕様** — 見送りを恒久化したいときは人が label で状態を変える

## 7. snapshot / 指示ファイル / 決定ファイル / log

各 file の形式は [`formats.md`](formats.md) が正本。本節は責務だけを持つ。

- **snapshot**: 1 tick の観測の正規化像 (候補・wip・人待ち・open issue と open CL の紐づき・open CL の状態)。merged は持たない — 完了は issue の close という外部事実で表れ、merged を条件に使う指示も無い。指示ファイルの一部として書かれ、正本ではない
- **指示ファイル**: snapshot と指示の列。orchestrator の入力。orchestrator は判断の裏取りに外部 store を読み直してよい — 指示は tick 時点の像で、人が手で付けた label 等の差分は読み直しでしか拾えない
- **決定ファイル**: orchestrator が書く唯一の出力。指示ごとの採否と理由、起動する worker の列 (spawn prompt の全文と、prompt に載せた playbook の絶対 path)。CLI は orchestrator の正常終了後にこれを読み、採否を log へ写してから決定どおり worker を起動する。**CLI は起動前に決定ファイルを検査し、判断不能の握り潰し (採否の欠け) と、worker が読めない prompt (未展開の変数・実在しない playbook) を起動前に落とす**。検査項目と、検査に落ちたときにどこまで起動するかは formats.md §5.2 が正本 (理由は ADR 0002)
- **log (`log.jsonl`)**: 毎 tick 1 行の実行記録 + orchestrator の判断記録 (決定ファイルから CLI が写す)。worker の判断は載らず、worker log・Claude Code の transcript・CL 説明文に残る。**tick 行は起動した orchestrator / worker ごとに CLI が発行した session id を持つ** — log の 1 行から Claude Code の transcript へ一意に辿る突合鍵。append-only だが**正本ではない** — 消えても運用は続き、判断履歴の欠損も許容する (人の対処が要る帰結は外部 store 側に残る)。最終行の時刻が死活検知を兼ね、機構の改善 (debrief) はこれを読む。**行の schema は外部の読み手を持つ公開契約**で、変えるときは互換を考える

## 8. 宣言 config

`${XDG_CONFIG_HOME:-~/.config}/claude-dispatcher/<project>/config.toml`。必須項目は 3 つ: **issue 置き場 / 着手可 label の綴り / 並列上限 N**。任意項目は CL 置き場 (省略すると issue 置き場を継ぐ)・triage label の綴り・cron へ認証を渡す token file (下記)。項目の書式は formats.md §2。

- 置き場は repo の外 (ホーム配下)。issue 置き場 ≠ 実装 repo の構成で「どの clone の宣言が効くか」を曖昧にしないため。clone の path は config に持たせず、crontab の `cd` が決める (§9)
- **config が新しいか変わったときに、置き場 repo の実在と、機構が付ける label (`dispatcher:wip` / `ready-for-human`) の実在を loud に検査する**。検査に失敗した config では観測を開始しない。検査済みの config は hash を state dir に残し、変わるまで再検査しない。label を後から消しても次の変更まで再検査しない — 付与の失敗は orchestrator の見送りとして log に現れる。label を作るのは導入者 (`setup`) で、`tick` は tracker に書かない
- 未知の table / key は名指しで失敗させる (綴り違いが「宣言していない」と同じ挙動になるのを防ぐ)
- **triage label (`[issue].triage_label`)** は worker が残タスクを起票するときに付ける唯一の label。省略すると label なしで起票する (§10)。**着手可 label と同じ綴りなら `config_error` にする** — 同じだと worker の起票が次 tick の候補になり、dispatcher が自分の作業を自己増殖させる。宣言の誤りで起きるこの経路だけは CLI が決定的に塞げる
- **token file (`[auth].token_file` / `[auth].claude_token_file`)** は cron へ gh と claude の認証を渡す任意の経路。gh は `GH_TOKEN` / `GITHUB_TOKEN` → `hosts.yml` → OS keyring の順で認証を解決し、macOS の Claude Code はログインを Keychain に置く。keyring は cron の非ログイン session から読めないことがある。CLI は env に該当の変数が無いときだけ file を読み、`GH_TOKEN` / `CLAUDE_CODE_OAUTH_TOKEN` として子プロセスへ渡す (env が勝つ)。file が無い / group・other から読める mode / 中身が空 / 相対 path のときは config 起因として止める。token はどの出力にも出さない。中身の空白は途中も除いて読む (端末で折り返された token には改行が入り、改行入りの token では認証 header を組めない)
- **gh の認証が通らない失敗は config 起因と分けて `auth_error` にする** — 綴りを直しても直らない失敗を綴りの失敗として報告すると、読み手が config を探し回る。判定は gh の終了 (exit 4 / `HTTP 401` / `gh auth login` の案内) を CLI が 1 箇所で読んで行う

## 9. 駆動

- **cron が `tick` を定期実行する**。claude の起動は tick 末尾の条件分岐 (指示 0 件なら起動しない) — 静止時の LLM コストはゼロ
- **crontab の 1 行は `cd <実装 repo の clone> && <claude-dispatcher の絶対 path> tick <project>`** に、stdout / stderr を state dir の `cron.log` へ append する形。cron の PATH には `~/go/bin` も `~/.local/bin` も無いので、binary は絶対 path で書く。`setup` は自分の実行 path を解決してこの行に埋める
- **orchestrator の起動**: 同梱の契約 file と指示ファイル・決定ファイルの path を渡して `claude -p … --permission-mode auto --session-id <CLI が発行した UUID>` を clone を cwd にして起動し、終了を待つ (lock は保持したまま。上限 15 分を超えたら kill して log に残す)。契約は skill として登録せず prompt として直接渡す (ADR 0003)
- **worker の起動**: 決定ファイルの spawn ごとに `claude -p "<spawn prompt>" --permission-mode auto --session-id <UUID>` を同じ cwd で**新しい process session (setsid) として detach 起動**し、stdout / stderr を worker log へ落として tick を終える。起動した pid・log path・session id は tick の log 行に載る
- **セッションの起動は差し替え可能な 1 つの部品に閉じる** (ADR 0005)。今の実装は `claude -p` だけ。`claude --bg` 等への差し替えが部品 1 つの追加で済む形に保つ
- **permission posture は `auto`** (orchestrator / worker とも)。`-p` では人の確認へ落ちる経路が無く、classifier が止めた操作は実行されずセッションは続くので、sandbox と permission 層を保ったまま無人で走る。permission 層を外す起動は採らない (ADR 0002 / 0005)。止められた worker は続行不能として人へ返す (§10)
- 定期起動は cron だけに頼る (取りこぼしを追い掛けない性質で足りる理由と、他の scheduler を採らない理由は ADR 0001)
- **依存 CLI (gh / claude / git) の path は CLI が自分で解決する** — cron の最小環境では PATH が通らない。よく使われる置き場 (`~/.local/bin`・mise の shims・Homebrew の prefix 等) を探す
- **crontab の出力先 `cron.log` が、CLI 自身が起動できなかった失敗の唯一の観測点**。CLI が出す行は、log.jsonl の行を指す前置を付けた 1 行 (想定外の失敗で止まった tick も log.jsonl に行を残し、cron.log の行が指す先を失敗の種類で変えない)。前置は crontab の行ではなく CLI が持つ (利用者に crontab の書式を増やさせない)。CLI の手前の失敗 (shell が出す行) だけは前置できない。state dir ごと無いと cron.log も書けないので、`setup` が dir を先に作る
- **死活は 2 段で読む**: log.jsonl 最終行の `ts` が周期の 2 倍より古ければ tick が走っていない。そのとき cron.log の更新時刻が log.jsonl 最終行より新しければ CLI の手前の問題 (末尾に起動失敗)、cron.log も古ければ cron 自体 (crontab が無い / マシンがスリープ) の問題。cron.log には失敗 tick の出力も溜まるので中身の有無では判定しない
- `flock` で単一実行を保証する (前 tick の orchestrator が長引いても tick は重ねない)
- **`tick --dry-run` は副作用の無い試運転** — config の検査 (検査済み hash を読まず毎回全部。書きもしない) → 観測 → 指示の導出までを通し、claude を起動する直前で止めて、指示の種別と件数を stdout に 1 行で出す。**state dir に何も書かない**ので、実 config のまま撃ってよく、走っている cron の tick とも衝突しない。claude と gh が最終的な PATH で解決できることも検査する
- **`tick --dry-run --cron-env` は CLI が自分を最小環境で撃ち直す** — `HOME` と `PATH=/usr/bin:/bin` だけを残した環境で起動し直すので、PATH の自己解決と、親 shell の環境変数 (`GH_TOKEN` 等) に頼った認証の両方を cron と同じ条件で落とせる。撃ち直しを CLI が持つのは、Claude Code の sandbox の除外指定が Bash 呼び出しの先頭 token だけで照合されるため — 呼び出し側が `env -i …` を前置すると CLI が sandbox 内に落ち、gh が credential を読めない偽の失敗になる。**cron を完全には再現しない**: keyring は環境変数でなくログイン session に従うので、対話 session から撃つと cron では読めない keyring を読めてしまう。keyring 保存の認証は登録後の死活 (log.jsonl 最終行の `ts`) でしか確かめられない
- crontab の登録は `setup` が cron 相当の試運転が通った後、導入者の承認を得て代行する (既存の tick 行は差分提示にとどめ、黙って置き換えない)

## 10. worker 契約 (spawn prompt)

spawn prompt の文面は同梱の契約 file が正本。本節は構造の決定だけを持つ。

- issue 番号・置き場・着手形態 (`start` / `reenter`)。新規着手の branch 名は `worktree-issue-<issue>` (issue 番号から機械的に決まる規約。CLI はこの綴りで worker 由来の CL を見分ける)。playbook と原則索引の**絶対 path を Read させる** — worker に原則を届ける経路は spawn prompt だけ
- **playbook は orchestrator が選ぶ**。`start` は issue 本文だけを信号に、新しい CL を作る playbook 群から 1 本選ぶ。**選定母集合と選定条件は playbook 側が宣言する** — playbook の frontmatter の `metadata` に `deliverable: cl` (母集合の印) と `dispatch-when` (どの issue に選ぶかの条件文) を書く。CLI は tick ごとに `playbook-*` を走査して母集合を集め、`start` 指示に path と `dispatch-when` を組にして添える。path の列挙をどこにも持たないので、印を付けた playbook は CLI を触らずに選定対象へ入る。印があるのに `dispatch-when` が無い / frontmatter が読めない playbook は tick を error にする (黙って母集合から外さない)。orchestrator は既定の `playbook-implementation` 以外を `dispatch-when` に明示的に当たるときだけ選び、迷ったら既定に倒す (狭い playbook を誤って渡すと無関係な step が worker の作業を占める)。`reenter` は条件カタログ (§6) の playbook を条件順に全部渡す
- CL 到達前に two-axis-review を invoke し、走らせたレビューの出力を CL へのコメントで載せる (worker = 作業した本人が自分の指摘を落とす構造の唯一の歯止め)
- CL 記述に issue への closing reference を必ず書く (merge で issue を閉じるため。cross-repo の CL 置き場でも紐づきを読めるように逐語で書く)
- **自律判断で進んだ点を CL 説明文の独立した節に書く** (往復を持たない代償を成果物側で払う)
- **続行不能なら引き渡しコメントを書き、`ready-for-human` を付け、wip を剥がして終了する**。順序はコメント → label (label を先に動かすと成果の所在を書く前に人が動く)。無言の終了は失敗扱い
- どの終わり方でも wip を剥がし、worktree を自分で消す。worktree は clone 配下 (`.claude/worktrees/issue-<issue>`) に作り、cwd は clone root のまま (cwd の中は消せない)
- **再入では新規 CL を作らず、CL の head branch から worktree を作り直して既存 branch へ push する**。conflict は CL の base branch を head へ merge して解消する (stack した CL の base は main でない)。head が別の worktree で checkout 済みなら続行不能として人へ返す。条件を 1 つも解消できずに終わる再入は続行不能として人へ返す (剥がした wip だけが残ると同じ再入が毎 tick 出る)
- `ci` 条件は原因を直した commit を push した時点で解消扱いとし、結果は待たない (合否は次 tick が持つ)。**手元で再現できない失敗は推測で push せず人へ返す** — 再入回数を機械が数えないので、推測 push を許すと直らない CL が毎 tick slot を取り続ける
- **review thread の書き手の信頼境界**: collaborator (`authorAssociation` が OWNER / MEMBER / COLLABORATOR) の指摘だけを反映の対象にし、それ以外は人へ返す — public repo では誰でも review comment を残せるので、書き手を見ずに push 権限を持つ worker の行動指示にしない。同意できない指摘は CL 上で反論せず人へ返す
- 作業ツリーの外にある実体 (settings の適用・label 作成・稼働 clone) を触る受け入れ条件は担当から外し、CL 本文に「user に残る作業」として書く (worker の sandbox が clone root の外への書き込みを拒む)
- **残タスクと範囲外の欠陥は issue にする** — CL 本文は merge 後に読まれず残りが散逸する。**付ける label は宣言 config の triage label だけ** (無ければ label なし)。着手可 label が付くと次 tick の `start` が worker 自身の起票を拾い、dispatcher が自分の作業を自己増殖させる。同じ title の open issue があれば新しく作らず発端をコメントで足す
  - **この規約は spawn prompt の文面でしか守らせられない (残るリスク)**。worker は tick が渡す利用者本人の gh 認証で書くので、label を付けたのが worker か人かを tracker 上の actor で区別できず、tick の側で決定的に弾く述語が無い。CLI が塞ぐのは宣言の誤り (triage label = 着手可 label、§8) だけ。破られたときの歯止めは、並列上限 N と、候補を選ぶ orchestrator の判断と、人の triage になる
- **issue 本文は着手可 label を付けた人の triage を信頼の根拠にする** — orchestrator は本文を選定の信号に、worker は仕様として読む。本文の書き手の検査は持たないので、着手可 label を付けた後に作者が本文を書き換えると、書き換えた内容が push 権限を持つ worker に届く (残るリスク)。public repo では、collaborator 以外が作者の issue に着手可 label を付けるときにこのリスクを負う。作者が collaborator でない issue を候補から外す述語は拡張候補
- **permission が止めた操作 (classifier の deny) を迂回しない** — 進めなくなったら続行不能として人へ返す
- gh は 1 呼び出しにつき shell の top-level 断片の先頭に置いて単体で実行させる (sandbox の除外指定が先頭 token だけで照合されるため、`N=$(gh …)` のような埋め込みは除外されず credential を読めない)

## 11. 配布と外部依存

- CLI は Go の単一 binary。`go install` と GitHub Releases (macOS / Linux) で配る (ADR 0003)
- **orchestrator の契約 file は binary に埋め込む**。契約と `tick` の版が必ず一致する
- **playbook・原則索引・two-axis-review は plugin `swat-skills` (marketplace `swat9013`) に実行時に依存する**。CLI は Claude Code の `~/.claude/plugins/installed_plugins.json` から plugin 名 `swat-skills` の `installPath` を引き、次の絶対 path を解決して LLM に渡す (plugin 内の配置に依存する interface)
  - playbook: `<installPath>/skills/procedure/playbook-*/SKILL.md`
  - 原則索引: `<installPath>/skills/knowledge/principle-index/SKILL.md`
  - scope の選択順: `projectPath` が cwd と一致する project scope の entry → user scope の entry → どちらも無ければ `~/.claude/skills/swat-skills` (Claude Code が in-place で読む skills ディレクトリ)
  - 別 marketplace 由来の `swat-skills` entry が複数あれば、どれを使うか決められないので loud に止める
  - 版の照合・互換検査はしない。常に install 済みの版を使う
- worker は plugin の skill (two-axis-review) と agent を名前で呼ぶので、worker が動く clone で plugin `swat-skills` が有効になっている必要がある。`doctor` はこれを検査する
- **Claude Code の settings は CLI が書かない**。`doctor` は要る entry を表示する: sandbox の `filesystem.allowWrite` に state dir (orchestrator が決定ファイルを書く先)、`excludedCommands` に `gh` (orchestrator / worker の tracker 操作) と `claude-dispatcher` (セッション内から `status` / `doctor` を撃つとき、中の gh が credential を読めるように)
- cron entry は `setup` が承認を得て登録する (§9)

## 12. スコープ外

- **merge 後の deploy** — merge 自体が人間ゲートのため範囲外
- **triage** — 着手可の付与・`ready-for-human` の解消・stale wip の回収は人間の領分
- **stale wip の自動解消 / orchestrator の常駐運用** — 拡張候補として認知だけしておく
- **gh 以外の tracker / CL host** (GitLab / Jira) — 拡張候補。置き場の宣言は tracker 種別と識別子の組で持つので、観測と LLM の gh 操作を種別ごとに足す形で広げられる
