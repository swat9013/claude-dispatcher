# システム構成

- 本 file が構造の正本。ユースケースは [`usecases.md`](usecases.md)、外から観測できる形式 (config / 指示ファイル / 決定ファイル / log / 出力) は [`formats.md`](formats.md) が正本
- 用語は [`CONTEXT.md`](../../CONTEXT.md)、決定の理由は [`docs/adr/`](../adr/)
- orchestrator の手順と spawn prompt の文面は CLI に同梱する契約 file が正本 (本 file は構造の決定だけを持つ)

## 1. システム境界

機械システムは **CLI `claude-dispatcher` 1 つと、その入出力 (宣言 config / snapshot / 指示ファイル / 決定ファイル / log)** だけ。LLM (orchestrator / worker)・人間・外部 store はすべて境界の外のアクター。

CLI の subcommand は役割で 3 群に分かれる。

| 群 | subcommand | 書くもの |
|---|---|---|
| 駆動 | `loop` / `tick` | state dir (log / 指示ファイル / lock / worker log)。外部 store には書かない。`loop` は `tick` を周期ごとに回す (§9) |
| 観測 | `status` / `paths` | 何も書かない (読み取り専用) |
| 導入 | `setup` / `doctor` | `setup` だけが書く: config の雛形と state dir (config が無いときだけ作り、既存は上書きしない)・tracker label (導入者の承認後)。Claude Code の settings はどちらも書かない |

`status` は log の `spawned` (起動記録) を起点に、wip の付いた issue と生きている worker process を 1 行ずつ並べ、worker の生死 (起動部が答える — §13)・wip・紐づく CL を毎回読み直して出す。**process が死んでいるのに wip が残っている行が stale wip の手掛かり**になる (snapshot からは区別できないが、起動記録と process の生死を突き合わせる `status` からは見える)。起動記録を持たない wip (orchestrator が付けた後に止まり、worker が起動されなかったもの) は `status` にも出ない。見出しには project の loop が生きているか (process の一覧で見る) と最終 tick を出す。

| アクター | 方向 | 関わり |
|---|---|---|
| tracker | ← 観測 / LLM が書く | issue・label の置き場。`tick` は**読むだけ**で、状態を表す label を書かない。例外は導入時の `setup` が導入者の承認を得て label を作ることだけ |
| CL host | ← 観測 / LLM が書く | CL の置き場。CLI は**読むだけ** |
| orchestrator (LLM) | ← 起動される / → 決定ファイル | 指示ファイルを読み、採否判断と実行 (wip 付与 / 見送り / 人へ返す) を行う。worker の起動は**決定ファイルに書く**だけで、プロセスは起こさない (§9) |
| worker (LLM) | — | 機械システムと直接やり取りしない。実装・CL 作成・終端信号 (label / issue コメント)・残タスクの issue 起票はすべて外部 store へ書く |
| plugin `swat-skills` | ← 読む | playbook と原則索引の置き場。CLI は path を解決して LLM に渡すだけで、中身を解釈しない (§11) |
| 人間 | → | triage (着手可の付与・人待ちの解消・stale wip の回収) と CL の merge。機械システムとの接点は `loop` の起動と停止、loop の画面・log・`status`・`doctor` の読みだけ |

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
 loop ──▶ tick           │           │               │
         │ config読み     │           │               │
         │ snapshot+指示  │           │               │
         │ log 1行append  │           │               │
         ├─ 指示0件 → 終了 │           │               │
         └─ 指示あり ──▶ orchestrator ──決定ファイル──▶ tick ──spawn──▶ worker ─┘
            (claude -p)   採否判断+wip付与   (決定どおり起動) (headless)  実装→CL→worktree削除
```

loop は人が clone で起動し、止めるまで tick を周期ごとに回す (§9)。

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
- **issue に紐づく CL** は、CL 置き場の open CL のうち、(a) CL 記述の closing reference がその issue を指すもの、または (b) CL 置き場の repo 自身の branch (fork でない) で、名前が worker の規約 `worktree-issue-<issue>` のもの。(b) は CLI が branch 名から機械的に判定するので、worker が closing reference を書き忘れても「実装済み」を見落として二重着手にならない。fork の branch を (b) から外すのは、worker は fork を作らず、第三者が fork から同じ名前の branch で開いた CL に issue を塞がせず、再入もさせないため (fork の CL も closing reference があれば (a) で紐づく)
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
| `start` | 候補があり、wip 枚数 < 上限 N で、選定母集合が空でない (空き分だけ。候補と選定母集合の playbook を添える。母集合は §10) | 着手する issue を選び、wip を付けて worker の起動を決定ファイルに書く (選定・見送りは LLM) |
| `reenter` | 紐づく open CL がちょうど 1 本あり、その head が CL 置き場の repo 自身の branch で worker の branch 規約 (`worktree-issue-<issue>`) に従い、issue に wip も `ready-for-human` も無く、その CL に条件が立つ (立った条件を `conditions` に併記。語彙は下記の条件カタログ) | wip を付けて既存 branch への再入 worker の起動を決定ファイルに書く (条件ごとの対応手順は条件別 playbook が持ち、spawn prompt にはその path を `conditions` 順に載せる)。`start` より先に扱う (新規着手より既存 CL の完了が近い) |
| `anomaly` | 上記に分類できない観測 (wip 枚数が上限 N を超えている / wip と `ready-for-human` が同居している / 1 つの open issue に open CL が複数紐づく — 対象は label に依らず open issue 全件) | 見送りか人へ返す。どちらでも判断を決定ファイルに残す |

**条件カタログ** (条件名 → snapshot 上の述語 → 対応 playbook)。定義元は CLI の 1 箇所で、orchestrator の契約 file が持つ「条件ごとの読み直し方法」と条件名の並びが一致することをテストが検査する。

| 条件 | 述語 | 対応 playbook |
|---|---|---|
| `conflict` | CL が merge conflict (`mergeable = CONFLICTING`) | `playbook-conflict-resolution` |
| `review` | 未解決の review thread が 1 本以上 | `playbook-review-response` |
| `ci` | head commit の checks が `FAILURE` / `ERROR` | `playbook-ci-fix` |

- 述語が比べる `mergeable` / `checks` の値は指示ファイルの語彙 (formats.md §5.1) で、CL host の綴りではない。CL 置き場の部品が host の応答をこの語彙へ写す (§13)
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
- **log (`log.jsonl`)**: 毎 tick 1 行の実行記録 + orchestrator の判断記録 (決定ファイルから CLI が写す)。worker の判断は載らず、worker log・Claude Code の transcript・CL 説明文に残る。**tick 行は起動した orchestrator / worker ごとに CLI が発行した session id を持つ** — log の 1 行から Claude Code の transcript へ一意に辿る突合鍵。append-only だが**正本ではない** — 消えても運用は続き、判断履歴の欠損も許容する (人の対処が要る帰結は外部 store 側に残る)。最終行の時刻が最後に回った tick の時刻で、機構の改善 (debrief) はこれを読む (loop が生きているかは process の一覧で見る — §9)。**行の schema は外部の読み手を持つ公開契約**で、変えるときは互換を考える

## 8. 宣言 config

`${XDG_CONFIG_HOME:-~/.config}/claude-dispatcher/<project>/config.toml`。必須項目は 3 つ: **issue 置き場 / 着手可 label の綴り / 並列上限 N**。任意項目は CL 置き場 (省略すると issue 置き場を継ぐ)・triage label の綴り・認証を渡す token file (下記)。項目の書式は formats.md §2。

- 置き場は repo の外 (ホーム配下)。issue 置き場 ≠ 実装 repo の構成で「どの clone の宣言が効くか」を曖昧にしないため。clone の path は config に持たせず、loop を撃つ cwd が決める (§9)
- **config が新しいか変わったときに、置き場 repo の実在と、機構が付ける label (`dispatcher:wip` / `ready-for-human`) の実在を loud に検査する**。検査に失敗した config では観測を開始しない。検査済みの config は hash を state dir に残し、変わるまで再検査しない。label を後から消しても次の変更まで再検査しない — 付与の失敗は orchestrator の見送りとして log に現れる。label を作るのは導入者 (`setup`) で、`tick` は tracker に書かない
- 未知の table / key は名指しで失敗させる (綴り違いが「宣言していない」と同じ挙動になるのを防ぐ)
- **triage label (`[issue].triage_label`)** は worker が残タスクを起票するときに付ける唯一の label。省略すると label なしで起票する (§10)。**着手可 label と同じ綴りなら `config_error` にする** — 同じだと worker の起票が次 tick の候補になり、dispatcher が自分の作業を自己増殖させる。宣言の誤りで起きるこの経路だけは CLI が決定的に塞げる
- **token file (`[auth].token_file` / `[auth].claude_token_file`)** は、起動した環境から認証を取れないとき (ssh 越しの session 等) に gh と claude の認証を渡す任意の経路。gh は `GH_TOKEN` / `GITHUB_TOKEN` → `hosts.yml` → OS keyring の順で認証を解決し、macOS の Claude Code はログインを Keychain に置く。keyring はログインしていない session から読めないことがある。CLI は env に該当の変数が無いときだけ file を読み、`GH_TOKEN` / `CLAUDE_CODE_OAUTH_TOKEN` として子プロセスへ渡す (env が勝つ)。file が無い / group・other から読める mode / 中身が空 / 相対 path のときは config 起因として止める。token はどの出力にも出さない。中身の空白は途中も除いて読む (端末で折り返された token には改行が入り、改行入りの token では認証 header を組めない)
- **gh の認証が通らない失敗は config 起因と分けて `auth_error` にする** — 綴りを直しても直らない失敗を綴りの失敗として報告すると、読み手が config を探し回る。判定は gh の終了 (exit 4 / `HTTP 401` / `gh auth login` の案内) を CLI が 1 箇所で読んで行う

## 9. 駆動

- **loop が `tick` を周期ごとに回す** (ADR 0006)。人が実装 repo の clone を cwd にして `loop <project> <interval>` を撃ち、止めるまで回り続ける。claude の起動は tick 末尾の条件分岐 (指示 0 件なら起動しない) — 静止時の LLM コストはゼロ。単発の `tick` は 1 回分を撃って終わる (手で 1 回回すとき)
- **周期は tick の終了から数える**。起動直後に 1 回撃ち、以後は tick が終わってから interval だけ待つ。tick は loop の process の中で同期に回るので重ならない。次の tick の時刻は壁時計で判定する (スリープ中に monotonic clock が進まない OS がある)。取りこぼした周期とスリープ中の周期は追い掛けない — 次の tick が現実を読み直すので実害にならない (ADR 0001)
- **tick の result が何であっても次の周期へ進む**。config と token は tick ごとに読み直すので、直せば撃ち直さずに戻る。loop が起動時に止まるのは前提の欠け (引数・project 名・config の実在・state dir の実在・loop の lock) だけ
- **loop は自分では起き直さない**。端末を閉じる・マシンを再起動する・loop が落ちると tick は止まり、人が同じコマンドで撃ち直すまで回らない。boot 時の起動や crash 時の再起動という監督は持たない (ADR 0006)
- **停止は段階的にする**。1 回目の SIGINT / SIGTERM / SIGHUP は、sleep 中なら即、tick の実行中ならその tick を最後まで進めて (orchestrator の終了を待ち、決定どおり worker を起動し、tick 行を書いて) から止まる。2 回目は、orchestrator を起動する前ならそれを起動せず、起動中ならその process group を止め、どちらも決定ファイルを読まずに `result: error` の tick 行を書いて止まる (timeout と同じ扱い)。orchestrator が正常終了した後に届いた 2 回目は 1 回目と同じに扱い、決定どおりの worker の起動を終えてから止まる (起動を途中で打ち切ると、orchestrator が付けた wip が worker の無いまま残る)。orchestrator は新しい process session で起動しているので端末の signal が届かず、loop だけが死ぬと、孤児の orchestrator が付けた wip を決定ファイルごと読む者がいないまま stale wip になる。worker はどちらの停止でも止めない。stdout への書き込みが失敗しても停止処理は続ける (読み手が端末ごと消えた pipe の SIGPIPE で倒れない)
- **二重起動は loop の生存期間の lock で拒む**。同じ project の 2 本目の loop は起動時に止まる。tick 単位の `flock` は残し、単発の `tick` と loop の tick を直列化する。tick の lock は tick 行を log.jsonl に書き終えるまで持つ — 先に外すと、次の tick の行が前の tick の行より先に書かれうる
- **tick は loop の process の中で回す**。binary と同梱の契約は loop を起動した時点の版に固定され、更新は撃ち直しで拾う
- **起動した worker の終了は起動部が回収する** (§13) — loop は長く生きるので、回収しないと終わった worker が zombie として残る
- **loop の画面が第一の観測点**。`status` の表に loop の見出し (状態・次の tick の時刻・直近の tick の result と error) と停止の操作案内を足し、一定の間隔と tick の直後に描き直す。止まったら画面を残して停止の理由を 1 行足す。stdout が端末でなければ画面を消さず、tick の直後に追記する。loop の中の tick は失敗行 (formats.md §6) を stderr に出さず、見出しに error を出す。形式は formats.md §13
- **別の端末から死活を見るのは `status`** — process の一覧から loop を探して「loop 稼働中 / loop なし」と最終 tick を出す (§1)。lock を覗いて確かめることはしない (flock に覗くだけの操作は無く、取ると、その一瞬に重なった tick が `locked` で流れる)
- **orchestrator の起動**: 同梱の契約 file と指示ファイル・決定ファイルの path を渡して `claude -p … --permission-mode auto --session-id <CLI が発行した UUID>` を clone を cwd にして起動し、終了を待つ (tick の lock は保持したまま。上限 15 分を超えたら kill して log に残す)。契約は skill として登録せず prompt として直接渡す (ADR 0003)
- **worker の起動**: 決定ファイルの spawn ごとに `claude -p "<spawn prompt>" --permission-mode auto --session-id <UUID>` を同じ cwd で**新しい process session (setsid) として detach 起動**し、stdout / stderr を worker log へ落として tick を終える。起動した pid・log path・session id は tick の log 行に載る
- **セッションの起動・待ち・停止・回収・生死の判定は差し替え可能な 1 つの部品 (起動部) に閉じる** (ADR 0005、§13)。今の実装は `claude -p` だけ。`claude --bg` 等への差し替えが adapter 1 つの追加で済む形に保つ
- **permission posture は `auto`** (orchestrator / worker とも)。`-p` では人の確認へ落ちる経路が無く、classifier が止めた操作は実行されずセッションは続くので、sandbox と permission 層を保ったまま無人で走る。permission 層を外す起動は採らない (ADR 0002 / 0005)。止められた worker は続行不能として人へ返す (§10)
- **依存 CLI (gh / claude / git) の path は CLI が自分でも解決する** — loop は起動した shell の PATH を継ぐが、最小の PATH の shell (ssh 越し等) から撃たれても動くように、よく使われる置き場 (`~/.local/bin`・mise の shims・Homebrew の prefix 等) を探す
- **`tick --dry-run` は副作用の無い試運転** — config の検査 (検査済み hash を読まず毎回全部。書きもしない) → 観測 → 指示の導出までを通し、claude を起動する直前で止めて、指示の種別と件数を stdout に 1 行で出す。**state dir に何も書かない**ので、実 config のまま撃ってよく、走っている loop とも衝突しない。claude と gh が最終的な PATH で解決できることも検査する

## 10. worker 契約 (spawn prompt)

spawn prompt の文面は同梱の契約 file が正本。本節は構造の決定だけを持つ。

- issue 番号・置き場・着手形態 (`start` / `reenter`)。新規着手の branch 名は `worktree-issue-<issue>` (issue 番号から機械的に決まる規約。CLI はこの綴りで worker 由来の CL を見分ける)。playbook と原則索引の**絶対 path を Read させる** — worker に原則を届ける経路は spawn prompt だけ
- **playbook は orchestrator が選ぶ**。`start` は issue 本文だけを信号に、新しい CL を作る playbook 群から 1 本選ぶ。**選定母集合と選定条件は playbook 側が宣言する** — playbook の frontmatter の `metadata` に `deliverable: cl` (母集合の印) と `dispatch-when` (どの issue に選ぶかの条件文) を書く。CLI は tick ごとに `playbook-*` を走査して母集合を集め、`start` 指示に path と `dispatch-when` を組にして添える。path の列挙をどこにも持たないので、印を付けた playbook は CLI を触らずに選定対象へ入る。印があるのに `dispatch-when` が無い / frontmatter が読めない playbook は tick を error にする (黙って母集合から外さない)。**母集合が空なら `start` 指示を出さない** (orchestrator は選べる playbook が無く全候補を見送るしかないので、起動しても判断が無い)。`reenter` / `anomaly` は今までどおり出し、指示が `start` だけなら orchestrator を起動しない。`doctor` は空の母集合を情報として示す (formats.md §12)。orchestrator は既定の `playbook-implementation` 以外を `dispatch-when` に明示的に当たるときだけ選び、迷ったら既定に倒す (狭い playbook を誤って渡すと無関係な step が worker の作業を占める)。`reenter` は条件カタログ (§6) の playbook を条件順に全部渡す
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
  - **続行不能で終えるとき、担当 issue 自身の残りは issue にせず、引き渡しコメントの「人が次にやること」(formats.md §8) が持つ**。issue にするのは担当範囲の外で見つけた欠陥と直さなかったレビュー指摘だけ。担当 issue の残りを別の issue にすると、`ready-for-human` の付いた担当 issue とその残りが分かれて散る
- **issue 本文は着手可 label を付けた人の triage を信頼の根拠にする** — orchestrator は本文を選定の信号に、worker は仕様として読む。本文の書き手の検査は持たないので、着手可 label を付けた後に作者が本文を書き換えると、書き換えた内容が push 権限を持つ worker に届く (残るリスク)。public repo では、collaborator 以外が作者の issue に着手可 label を付けるときにこのリスクを負う。作者が collaborator でない issue を候補から外す述語は拡張候補
- **permission が止めた操作 (classifier の deny) を迂回しない** — 進めなくなったら続行不能として人へ返す
- gh は 1 呼び出しにつき shell の top-level 断片の先頭に置いて単体で実行させる (sandbox の除外指定が先頭 token だけで照合されるため、`N=$(gh …)` のような埋め込みは除外されず credential を読めない)

## 11. 配布と外部依存

- CLI は Go の単一 binary。`go install`・GitHub Releases・Homebrew tap `swat9013/tap` の cask の 3 経路で、macOS / Linux に配る (ADR 0003 / 0007)
  - tag の push で、GoReleaser が Releases の binary と tap の cask を同時に更新する。cask は `homebrew_casks` で生成する (`brews` の formula は使わない)
  - tap は Homebrew の tap trust の対象なので、install の手順は完全修飾名 (`swat9013/tap/claude-dispatcher`) で書く
  - cask は `depends_on` を持たない。依存 (gh / claude / plugin `swat-skills`) の充足は、経路によらず README の「前提」と `doctor` が見る
  - binary は署名しないので、macOS では cask の `postflight_steps` が quarantine を外す。GoReleaser には専用の設定が無いので `custom_block` で書く (ADR 0007)
- **orchestrator の契約 file は binary に埋め込む**。契約と `tick` の版が必ず一致する
- **playbook・原則索引・two-axis-review は plugin `swat-skills` (marketplace `swat9013`) に実行時に依存する**。CLI は Claude Code の `~/.claude/plugins/installed_plugins.json` から plugin 名 `swat-skills` の `installPath` を引き、次の絶対 path を解決して LLM に渡す (plugin 内の配置に依存する interface)
  - playbook: `<installPath>/skills/procedure/playbook-*/SKILL.md`
  - 原則索引: `<installPath>/skills/knowledge/principle-index/SKILL.md`。**orchestrator を起動する tick (指示が 1 件以上残った tick) で file が無ければ、tick を error にする** (`result: error`、「指示を導出できなかった」。formats.md §3)。無いまま起動すると、worker は原則を 1 つも受け取らずに黙って走る。orchestrator を起動しない tick (静止した tick と、空の母集合で `start` を落として指示が残らなかった tick) は索引を prompt に載せないので確かめない
  - scope の選択順: `projectPath` が cwd と一致する project scope の entry → user scope の entry → どちらも無ければ `~/.claude/skills/swat-skills` (Claude Code が in-place で読む skills ディレクトリ)
  - 別 marketplace 由来の `swat-skills` entry が複数あれば、どれを使うか決められないので loud に止める
  - 版の照合・互換検査はしない。常に install 済みの版を使う
- worker は plugin の skill (two-axis-review) と agent を名前で呼ぶので、worker が動く clone で plugin `swat-skills` が有効になっている必要がある。`doctor` はこれを検査する
- **Claude Code の settings は CLI が書かない**。`doctor` は要る entry を表示する: sandbox の `filesystem.allowWrite` に state dir (orchestrator が決定ファイルを書く先)、`excludedCommands` に `gh` (orchestrator / worker の tracker 操作) と `claude-dispatcher` (セッション内から `status` / `doctor` を撃つとき、中の gh が credential を読めるように)

## 12. スコープ外

- **merge 後の deploy** — merge 自体が人間ゲートのため範囲外
- **triage** — 着手可の付与・`ready-for-human` の解消・stale wip の回収は人間の領分
- **着手可 issue の本文の規範整合性レビュー** (plugin `swat-skills` の `ready-for-agent-review`) — 着手可 label を付ける経路 (triage) の責務で、orchestrator の `start` の採否では掛けない (§10 の信頼の根拠からの帰結。理由: #29)。採否で orchestrator が見る「本文が自己完結しているか」(契約 file) はこれに含まない
- **stale wip の自動解消 / orchestrator の常駐運用** — 拡張候補として認知だけしておく
- **loop の監督** (boot 時の起動・落ちた loop の起こし直し) — 人が同じコマンドで撃ち直す (ADR 0006)
- **gh 以外の tracker / CL host** (GitLab / Jira) — 拡張候補。置き場の宣言は tracker 種別と識別子の組で持つので、CLI の観測は置き場の部品に adapter を足し (§13、ADR 0008)、LLM の gh 操作は契約 file と playbook に種別ごとに足す形で広げられる。issue の同一性 (今は番号) を key に広げるときは、公開形式 (formats.md) を合わせて直す

## 13. CLI の seam

CLI の中で呼び出し側から中身を隠す部品と、差し替えの口 (seam) を置く位置。部品の中の分け方は実装に任せ、本節は部品の責務と seam の位置だけを持つ。**seam は本番とテストで 2 つ以上の adapter を持つところにだけ置く**。

| 部品 | 呼び出し側 | 呼び出し側から隠すもの | adapter |
|---|---|---|---|
| tick の 1 回分 | `loop` / `tick` / `setup` と `doctor` の試運転 | 観測から worker の起動までの段取りと、停止要求の段階の解釈 | (seam を置かない) |
| 起動部 | tick の 1 回分 / status の現況 | セッションの起動の形 (argv・process session・pid・生死の見分け方) | `claude -p` / テストの fake。`claude --bg` はここに足す |
| issue 置き場の部品 | tick の 1 回分 / status の現況 / `setup` / `doctor` | tracker の呼び方・応答の綴り・失敗の見分け方 | gh / テストの in-memory |
| CL 置き場の部品 | tick の 1 回分 / status の現況 / `doctor` | CL host の呼び方・応答の綴り・失敗の見分け方 | gh / テストの in-memory |
| status の現況 | `status` / loop の画面 | 機械の観測 (process 一覧・claude のセッション一覧) と、載せる worker の選び方 | 機械の観測: `ps` と `claude` / テストの fake |

- **tick の 1 回分は停止要求を 1 つだけ受け、段階を自分で解釈する**。呼び出し側が渡せるのは「orchestrator を止めよ」だけで、orchestrator の起動前に届けば起動せず、起動中なら起動部に止めさせて決定ファイルを読まず、正常終了の後なら無視して決定どおりの worker の起動を終える (§9 の 2 回目の停止要求)。1 回目の停止要求は次の tick を始めないことなので loop だけが扱う。観測の途中に届いた要求で観測は打ち切らない — 観測は gh の上限時間で終わり、打ち切っても orchestrator の前で止まる結果は変わらない
- **tick の 1 回分は確定した tick 行を返す**。中身は log.jsonl に書いたものと同じで、書けなかったときはその理由を含む。exit code と失敗行 (formats.md §6) を stderr に出すかは呼び出し側が行から決める — 単発の `tick` は出し、loop は出さずに見出しへ載せる。loop の見出しの最終 tick はこの行から描き、log.jsonl を読み直さない
- **`setup` / `doctor` の試運転は tick の 1 回分の試運転を process 内で呼ぶ**。自分の binary を撃ち直さない
- **起動部はセッションの起動・待ち・停止・回収・生死の判定を 1 つの部品に閉じる** (ADR 0005)。orchestrator は上限時間か停止要求で process group ごと止め、どちらで止まったかを区別して返す。worker は detach 起動し、その終了を起動した process の中で回収する。「今生きている worker の一覧」を 1 回で読む口を持ち、status の現況は起動記録をこれと突き合わせるだけにする — 生死の見分け方 (`claude -p` では pid の command 行に session id が在るか、formats.md §10) は起動の形ごとに違うので、部品の外に出さない
- **issue 置き場の部品と CL 置き場の部品は、config から組み立てて中立の事実と分類済みの失敗を返す** (ADR 0008)
  - issue 置き場: open issue と label / wip の付いた issue / 置き場の検査 / label の作成 (`setup` だけが使う)
  - CL 置き場: open CL (紐づく issue・`mergeable`・`checks`・未解決の review thread の数) / branch ごとの最新の CL (merge 済みを含む) / 置き場の検査。組み立てのときに issue 置き場を受け取り、紐づく issue をその置き場のものに絞って返す
  - CL の状態の語彙 (`mergeable` / `checks` / CL の state) は formats.md §5.1 / §10 が正本。adapter が host の応答をその語彙へ写す (gh の応答は同じ綴り)。条件カタログ (§6) の述語はこの語彙を比べる
  - 失敗は 認証 (`auth_error` へ写す — §8) / 見えない / 読み切れない に分けて返す
  - 置き場の検査は、置き場 repo の実在と、呼び出し側が渡す label の集合の実在を、見えない repo と無い label のデータで返す。集合は tick が機構の label (§8)、`setup` / `doctor` が着手可 label を含む全部。文言は呼び出し側が組み、人が自分で label を作るときの手順の文面だけは adapter が出す
  - 範囲は CLI だけ。orchestrator の契約 file と playbook は gh を直接撃ち、この部品を通らない
  - issue の同一性は番号 (整数)。log.jsonl・決定ファイル・worker の branch 名が同じ前提に立つ
- **status の現況は 1 project の今を組み、描画を見出し・注記・表に分けて返す**。今とは loop の生死・最終 tick・載せる worker の行・注記のこと。`status ps` / `status watch` / loop の画面は見出しを選んで組み立てる (formats.md §10 / §13.1)。機械の観測は部品の中で行う。loop の終了行の「止めずに走っている worker」の数 (formats.md §13.2) も同じ組み立てから数える
