# システム構成

- 本 file が構造の正本
  - ユースケースは [`usecases.md`](usecases.md)
  - 外から観測できる形式 (workflow 定義・状態 file・log・出力) は [`formats.md`](formats.md)
- 用語は [`CONTEXT.md`](../../CONTEXT.md)、決定の理由は [`docs/adr/`](../adr/)
- 構造は [openai/symphony の `SPEC.md`](https://github.com/openai/symphony/blob/main/SPEC.md) を土台にする (ADR 0009)
  - 本 file の「SPEC §n」はその節を指す
  - SPEC から意図して外れるところは ADR 0009 に理由とともに置く

## 1. システム境界

機械システムは次の 2 つだけ。worker (LLM)・人間・外部 store はすべて境界の外のアクター。

- CLI `claude-dispatcher` 1 つ
- その入出力: workflow 定義・snapshot・状態 file・log・worker log・workspace

CLI の subcommand は、役割で 3 群に分かれる。

| 群 | subcommand | 書くもの |
|---|---|---|
| 駆動 | `loop` | state dir (状態 file・log・worker log・lock・共通 prompt の描画結果) と workspace。外部 store には書かない |
| 観測 | `status` / `paths` | 何も書かない (読み取り専用) |
| 導入 | `setup` / `doctor` | `setup` だけが書く。書くのは workflow 定義の雛形で、無いときだけ作り、既存は上書きしない。Claude Code の settings はどちらも書かない |

| アクター | 方向 | 関わり |
|---|---|---|
| tracker | ← 観測 / worker と人が書く | issue と label の置き場。CLI は**読むだけ** |
| CL host | ← 観測 / worker と人が書く | CL の置き場。CLI は**読むだけ** |
| worker (LLM) | ← 起動される / → stream | loop の子 process。action を実行し、結果は外部 store と workspace に残す。loop へは stream (進行の event) を流すだけで、問い合わせの往復は持たない |
| 人間 | → | workflow 定義を書き、triage と、自分の手に残した merge を行う。機械システムとの接点は次のものだけ<br>- `loop` の起動と停止<br>- loop の画面・`status`・log の読み<br>- `doctor` |

## 2. 全体図

```
 人間 ── workflow 定義 (実装 repo の中) / triage / merge
   │                                     ▲
   │ label・CL                           │ label・コメント・CL (action の結果)
   ▼                                     │
 ┌──────────────────── 外部 store (現実) ──────────────────────┐
 │  tracker (issue / label)      CL host (CL / thread / CI)    │
 └──────▲─────────────────────────────────────────▲────────────┘
        │観測 (読むだけ)                             │書く
        │                                           │
 loop ──┤ tick (周期ごと)                           │
        │  1. 突き合わせ: stall・終端の検知          │
        │  2. workflow 定義の読み直しと事前検査      │
        │  3. snapshot: 作業対象 (issue / CL)        │
        │  4. trigger の評価 → 候補                  │
        │  5. 空いている分だけ起動 ──▶ workspace ──▶ worker (claude -p、loop の子)
        │                                (hooks)     stream-json ──▶ loop
        │  worker の終了 → 外れたか確かめる → 完了 / attempt+1 → backoff / 打ち切り
        └─ 状態 file・log を書く ──▶ status / loop の画面
```

## 3. 解く問題と解き方

issue や CL を起点に無人で作業を回す機構は、次の 4 つの失敗に陥りやすい。

1. **要素過剰**: 台帳・dashboard・pane 管理を持つと、目的に対して構成が重くなる。改善のたびに、全要素との整合を払うことになる
2. **往復プロトコル**: 判断する役と実行する役の間で質問を中継すると、返信先の受け渡しと再起動後の送り直しが複雑さの大半を占める
3. **drift**: 台帳 (意図) と外部 store (現実) が食い違い、その解消を恒常的に管理し続ける
4. **silent な機械遷移**: 宣言の綴り誤りを根拠に機械が状態を書き換え、稼働中の作業を壊す

解き方は「**状態を外部 store に置き、機械は読むだけにする。何を起動するかは宣言で決める**」。

- **機械は tracker と CL host に書かない**。silent な機械遷移は起きない
- **loop の memory に持つのは claim と retry だけ**
  - どちらも loop の process と寿命を共にする。loop が止まれば worker も止まる
  - 起動し直した loop は外部 store を読み直すので、drift は tick を跨いで残らない
- **起動は trigger の宣言で決定的に決める**。判断する役を別に置かないので、往復は生まれない
- **判断が要ること (見送る・人へ返す) は action が行う**。判断の根拠は成果物 (CL 説明文・コメント) に残す

## 4. 状態の表現

dispatcher は、状態の意味を定めない。issue の label が何を表すか (着手可・人待ち・レビュー待ち) は、project の workflow 定義が trigger の述語で決める。dispatcher が扱う状態は次の表だけ。

| 状態 | 表現 | 書き手 | 寿命 |
|---|---|---|---|
| 作業対象 | open な issue と open な CL | 人 / worker | 外部 store |
| 終端 | issue の close、CL の merge か close | 人 / worker | 外部 store |
| claim (起動中・再起動待ち) | loop の memory | loop | loop の process |
| attempt・打ち切り | loop の memory | loop | loop の process |
| workspace | workflow 定義が決める root の下の作業場所 (既定は clone 配下) | loop (hooks) | 作業対象が終端になるまで |

- **候補 = いずれかの trigger に当たり ∧ claim されておらず ∧ 打ち切られていない作業対象**
- **完了 = worker が終わった後に、作業対象が起動した trigger から外れていること**
  - 外すのは action の責務 (SPEC の handoff state と同じ)
  - 例: 実装の action は、CL を開いたら着手可の label を外す
- **途中成果は remote branch と workspace に残る**。workspace は attempt を跨いで残すので、2 回目以降の worker は前の状態から続ける
- **issue と CL を機械は紐づけない**
  - 「実装済み」は、紐づく CL の有無ではなく、action が label で表す
  - CL が merge されずに close されても、issue は自動では候補に戻らない。戻すなら、人が label を付け直す
- **claim は tracker に書かない**。そのため、同じ issue 置き場に 2 本の loop が立つと二重に起動する
  - 同じマシンの中では、scope key の lock で 2 本目の loop を拒む (§9)
  - マシンを跨いだ排他は持たない。1 つの issue 置き場は 1 台から回す

## 5. 機械と LLM の線

| 主体 | やってよいこと | やらないこと |
|---|---|---|
| CLI (`loop`) | 外部 store の観測と正規化<br>trigger の評価<br>claim・attempt・打ち切りの記憶<br>workspace の作成と削除 (hooks を撃つ)<br>worker の起動・監視・停止<br>状態 file と log を書くこと | tracker・CL への書き込み<br>宣言にない判断 (見送り・優先順位の変更) |
| worker | action の実行 (実装・CL 作成・コメント・label の付け替えなど)<br>作業対象を trigger から外すこと<br>見送ること・人へ返すこと | loop への問い合わせ<br>permission が止めた操作の迂回 |
| 人間 | workflow 定義を書くこと・triage・merge (workflow 定義で worker に任せない限り) | — |

宣言の誤りによる事故は、次の 2 段で塞ぐ。

- **機械が書かない線**: 宣言が誤っても、機械が tracker の状態を壊すことはない
- **workflow 定義の loud な検査** (§8): 誤りを黙って通さない

誤った trigger が起こしうる最悪の事態は「当たるべきでない作業対象へ worker を起動すること」で、その先で何をするかは action の文面が決める。

## 6. trigger

trigger は、作業対象に対する述語と action の組。workflow 定義に並べて宣言する。

| 項目 | 中身 |
|---|---|
| 名前 | log・状態 file・template 変数で trigger を指す |
| 対象 | `issue` か `cl` |
| 述語 | 対象ごとの文法で書く条件。すべて AND で評価する |
| action | worker に渡す prompt の template |

**issue 側の述語**。SPEC §5.3.1 の `required_labels` / `active_states` を広げた形。

- label の all / any / none
- assignee (未割当・特定の人)
- 作者の立場 (collaborator か否か)
- milestone
- blocked by (未解決の依存先があるか / 無いか)
- status と issue type (`jira` でだけ書ける)
  - Jira の project は、triage の役割を label でなく status で表すことが多い。label の述語だけでは trigger を書けない
  - status 名と issue type 名が実在するかは、loop の起動時・試運転・`doctor` で確かめる (tick の中では確かめない)

**CL 側の述語**。CL の状態の語彙と、絞り込みを書く。

| 語彙 | 当たる状態 |
|---|---|
| `cl.conflict` | CL が merge conflict を持つ |
| `cl.review_unresolved` | collaborator が書いた未解決の review thread が 1 本以上ある |
| `cl.ci_failed` | head commit の checks が失敗している |
| `cl.approved` | CL が承認済みである |

- 絞り込み: label の all / any / none・head branch の pattern・head が同じ repo の branch か (fork でないか)・作者の立場・draft か否か
- **語彙の値は dispatcher の語彙で、CL host の綴りではない**。CL 置き場の adapter が host の応答をこの語彙へ写す (§13)
- **`cl.review_unresolved` は collaborator の thread だけを数える**
  - public repo では誰でも review comment を残せる
  - 書き手を見ずに数えると、第三者の書き込みが push 権限を持つ worker を起動する
- **作者では、人の CL と worker の CL を区別できない**
  - worker は利用者本人の認証で CL を開くため
  - 区別したい project は、head branch の pattern か label で絞る
- **head branch の pattern で絞るときは、同じ repo の branch に限る**
  - fork からは、誰でも同じ綴りの head branch を作れる
  - 同じ repo に限らないと、第三者が fork から開いた CL が、push 権限を持つ worker を起動する
- **`cl.approved` に action を当てるかは project が決める**
  - merge を人の最終 gate にする運用では、当てない

**評価の規則**。

- **作業対象ごとに trigger を宣言順に評価し、最初に当たった 1 つだけを起動する**
  - 1 つの作業対象で同時に走る worker は 1 本
  - 残りの trigger は、その worker が終わった後の tick で改めて評価する
- **起動の順序は trigger の宣言順を先に、同じ trigger の中では作成日時の古い順**
  - 宣言の先頭に置いた trigger ほど優先される (既存の CL の手直しを新規の実装より先にする、など)
  - SPEC §8.2 の priority は使わない。GitHub の issue に priority の field は無い
  - `jira` では、作成日時の代わりに番号の小さい順にする。acli の一覧は作成日時を返さない (ADR 0010)
- **claim されている作業対象 (走っている・再起動待ち) の workspace で checkout されている branch を head に持つ CL には、CL 側の trigger を当てない**
  - issue の worker が CL を開いた後も作業を続けている間や、その issue が再起動を待っている間に、同じ branch へ CL の worker を重ねないため
  - 雛形の規約との 2 段で防ぐ。雛形の CL 側の trigger は draft でない CL だけに当て、実装の action は CL を draft で開いて、終わる直前に ready にする
- **曖昧な CL (同じ head branch から複数の open CL がある) は CL 側の trigger の対象から外し、log と `status` に出す**
  - どの CL を作業対象にするか決まらないため
  - SPEC §11.3 の「正規化できない記録は外して log に出す」と同じ扱い
- **dedup は持たない**
  - 完了した作業対象は trigger から外れているので、再び当たらない
  - 当たったまま残った作業対象は、retry (§7) が扱う

## 7. 起動・retry・打ち切り

SPEC §7・§8・§16 の状態機械を土台にする。

**tick の手順**。

1. **突き合わせ**
   - 走っている worker ごとに、活動 (formats.md §6) として数える stream の行が最後に書かれてからの経過を見る。heartbeat (`tool_progress`) など活動として数えない行では経過は戻らない
   - stall の上限を超えた worker は止め、失敗として扱う
   - 起動からの経過が worker 1 回分の上限時間 (既定 1 時間。0 で無効) を超えた worker も止め、失敗として扱う。活動として数える行を書き続けて stall にかからない worker が、並列の枠を塞ぎ続けないようにする
   - 走っている作業対象を外部 store で読み直し、終端になっていれば worker を止めて workspace を消す
   - trigger から外れただけでは止めない (ADR 0009「走っている worker は終端でだけ止める」)
2. **workflow 定義の読み直しと事前検査** (§8)
   - 読めない・文法に合わないときは、この tick は何も起動しない。突き合わせは続ける
   - action の template の先頭の `/名前` が見つからない trigger だけは、その trigger を評価から外し、再起動もしない。他の trigger は評価する
3. **snapshot を作る**: open な issue と open な CL を読み、正規化する
4. **trigger を評価し、候補を並べる** (§6)
5. **空いている分だけ起動する**。並列上限から、走っている worker と再起動待ちの数を引いた分 (事前検査に落ちた trigger の再起動待ちは数えない)
   - 起動の直前に、候補を 1 件ずつ外部 store で読み直す。終端になっているか、起動しようとした trigger から外れていれば起動しない。open な一覧の検索は書き込みの直後に古い結果を返しうる (Jira の JQL 検索など) ので、完了した直後の作業対象を起動し直さないようにする
   - 読み直せなかった候補 (error として log に残す) と、trigger に当たるかをまだ決められない候補も、その tick では起動しない。次の tick で候補になれば試み直す
   - 読み直すと宣言順で先の trigger に当たるようになっていれば、その tick では起動しない。次の tick の評価が先の trigger で拾う
   - 起動しなかった分の空きは、次の候補に回す

**worker の 1 回分**。

1. workspace を用意する。無ければ作って `after_create` を撃ち、run の前に `before_run` を撃つ
2. action と共通 prompt を描画する (§10)
3. `claude -p` を子 process として起動し、stream を読む
   - 最初の attempt は CLI が発行した session id を `--session-id` で渡す。attempt が 2 回目以降なら、同じ id を `--resume` に渡して前の session を続ける。session id は同じ作業対象の attempt を通して 1 つで、log と状態 file にも同じ id を載せる
   - 上限時間・stall・停止要求で止めた session も続けられる。Claude Code は session を進むごとに保存しており、process group ごと止めても、止めた時点までの会話が `--resume` で戻る
   - Claude Code は session を cwd ごとに保存するので、続きの worker は同じ workspace の path で起動する
   - `--append-system-prompt-file` は `--resume` のときも毎回渡す
4. 終了後に `after_run` を撃つ
5. 作業対象を読み直し、起動した trigger から外れたかを確かめる
   - 読み直しに失敗したら、完了とも失敗とも数えない。error として log に残し、claim を持ったまま次の tick で確かめ直す
   - 外れていれば **完了**。claim を解く
   - 当たったままなら、worker が正常終了していても **失敗と同じに扱う** (ADR 0009「当たったままの正常終了は失敗と数える」)

**失敗の扱い**。

- **attempt を 1 つ進め、backoff して再起動を待つ**
  - backoff の式は SPEC §8.4 のまま: `min(10s × 2^(attempt−1), 上限)`。上限の既定は 5 分
  - 再起動の前に作業対象を読み直す。終端になっていれば claim を解き、trigger から外れていれば完了として claim を解く
  - 空きが無ければ、attempt を進めずに待ち直す (ADR 0009「空きを待つ間は attempt を進めない」)
  - 再起動は同じ trigger で続ける。宣言順で先の trigger に当たるようになっていても、その評価は claim が解けた後の tick で行う
- **attempt が上限に達したら打ち切る**
  - 打ち切りは memory に持ち、log と `status` に出す。tracker には書かない
  - 打ち切りは作業対象ごとに持つ。打ち切った作業対象は、どの trigger に当たっても起動しない
  - 打ち切ったときの trigger から作業対象が一度外れたのを tick で観測すると解ける。人が label を外して付け直せば、再び候補になる
  - 外してから付け直すまでが 1 周期に収まると、外れたのを観測できない。`status` の打ち切りの表示に、label を外して 1 周期待つよう書く
  - loop を起動し直すと消える

**workspace**。

- SPEC §9 の安全条件に従う: 作業対象ごとに決まった path に置き、root の外へ出ない
- **作業対象が終端になったら、`before_remove` を撃ってから消す**
  - loop の起動時にも、終端になった作業対象の workspace を掃除する
  - `after_run` と `before_remove` の失敗は error として log に残し、処理は続ける (SPEC §5.3.4)
  - workspace を消せなかったときは error として log に残し、次の tick で消し直す
- **作り方は hooks に任せる**
  - issue なら branch を切った worktree、CL なら head を detach で checkout した worktree、など
  - 雛形は `git worktree add` の例を置く

## 8. workflow 定義

実装 repo の中に置く (SPEC §5)。path は loop の引数で渡せ、省けば loop を起動した cwd の `WORKFLOW.md` を読む。項目の書式は formats.md が正本。

- **front matter が持つもの**
  - tracker の種類 (`github` / `gitlab` / `jira`) と adapter の設定
    - 書ける設定は種類ごとに決まる。種類が支えない設定は、検査で名指しして失敗させる。黙って通すと、条件が全件に当たるか、渡したつもりの設定が効かない
      - `gitlab`: `blocked` の述語・`tracker.token`
      - `jira`: `author` と `milestone` の述語・`tracker.token`・`on: cl` の trigger (issue 置き場と別の CL 置き場の宣言は #114)
      - `github` と `gitlab`: `status` と `type` の述語
  - trigger の列
  - hooks
  - 並列上限・attempt の上限・backoff の上限・stall の上限・worker 1 回分の上限時間 (既定 1 時間。0 で無効)
  - 周期
  - `claude` の起動 command と引数
- **本文は共通 prompt**
- **秘密は `$VAR` で間接参照する**。値そのものは書かない (SPEC §5.3.1)
  - loop は、起動した shell の環境変数を引き継ぐ
- **tick ごとに読み直す** (SPEC §6.2)
  - 直せば、loop を起動し直さずに効く
  - 読めない版・文法に合わない版では、新しい起動を止める。走っている worker の突き合わせは続ける
- **未知の key は名指しで失敗させる** (ADR 0009「未知の key は失敗させる」)
- **事前検査** (SPEC §6.3)
  - loop の起動時は、どの検査が落ちても起動を失敗させる。起動時は人が画面の前にいるので、全部を直させる
  - tick の中では、落ちた検査の範囲だけを止める (§7)
  - tracker の設定が組み立てられること
    - `jira` では、loop の起動時・試運転・`doctor` で、acli の認証の site が workflow 定義の site と同じこと・trigger が書いた status 名と issue type 名が実在することも確かめる。acli は呼び出しごとに site を選べず、active な account の site を読むため。tick の中では確かめない (途中で acli の account を切り替えると、別の site を読みうる)
  - trigger の述語が文法に合うこと
  - 各 trigger の action の template の**先頭の** `/名前` が、呼べる skill か command であること。見つからなければ、その trigger だけを起動しない (§7)
    - 作業対象に依らずに検査できるよう、描画の前の template を見る。先頭を template 変数で始める action は、先頭の `/名前` を確かめられないので、事前検査で名指しして失敗させる
- **workflow 定義を LLM が書き換えるリスクは利用者が負う**
  - worker は repo を編集できるので、自分が次に起動される trigger を書き換えうる
  - dispatcher はこれを防がない (ADR 0009)

## 9. 駆動

- **loop が tick を周期ごとに回す**
  - 人が実装 repo の clone を cwd にして `loop` を起動し、止めるまで回り続ける
  - 候補が無い tick は LLM を起動しない。静止時の LLM コストはゼロ
- **周期は tick の終了から数える**
  - 起動直後に 1 回回し、以後は tick が終わってから周期だけ待つ
  - 取りこぼした周期とスリープ中の周期は追い掛けない
- **二重起動は scope key の lock で拒む**
  - state dir も scope key で分ける (無害化した scope key に短い hash を足した名前)
  - lock は loop の生存期間だけ持つ
  - 同じ issue 置き場の 2 本目の loop は、clone や workflow 定義が違っても起動時に止まる
  - 走っている間に workflow 定義の issue 置き場を書き換えて scope key が変わったら、その tick は何も起動しない。lock は起動時の scope key で取っているので、別の置き場を回すには loop を起動し直す
- **loop は自分では起き直さない** (ADR 0006)
  - 起動し直した loop は、外部 store と workspace から続きを拾う
- **停止は 2 段にする**
  - 1 回目の SIGINT / SIGTERM / SIGHUP
    - 新しい起動と再起動待ちをやめる
    - 走っている worker の終了を待って止まる
  - 2 回目: 走っている worker の process group を止め、`after_run` を撃ってから止まる
  - worker は loop の子なので、loop が死ねば worker も止まる。止まった worker の作業対象は、trigger に当たったままなら、次に起動した loop が再び拾う
- **loop の画面が第一の観測点**
  - 見出し・`workers`・`要対処`・`ログ` のセクションに分けて描き直す
  - 見出しは loop の状態 (稼働中・停止待ち) と scope key と loop の binary の版、次の tick の時刻、直近の tick の結果と error
  - `workers` は claim の表 (作業対象・trigger・attempt・段階・経過・stream の最新の活動)。最新の活動は表の列で見るので、`ログ` には活動の行を出さない
  - `要対処` は人の手当てを待つもの (打ち切り・曖昧な CL・起動しない trigger)。無ければ出さない
  - stdout が端末のときだけ色を付ける。`status` も同じ描画を使う
- **別の端末からは `status` で見る**。loop が state dir に書き出す状態 file を読む
  - 状態 file は表示のためだけの痕跡で、loop は読み戻さない
- **permission posture は workflow 定義の起動引数が決める**
  - 雛形は `--permission-mode auto` を置く
  - permission 層を外すかどうかは利用者が決める
- **依存 CLI (tracker の CLI・claude・git) の path は CLI が自分でも解決する**。最小の PATH の shell (ssh 越しなど) から起動されても動くようにする
  - 解決するのは workflow 定義が撃つ CLI だけ。tracker の CLI は `tracker.kind` で決まる (`github` は gh、`gitlab` は glab、`jira` は acli)。使わない tracker の CLI が無いことは、PATH を書き換える理由にしない
- **試運転**
  - 1 tick 分の読み直し・事前検査・trigger の評価までを通し、起動する直前で止めて、起動するはずの作業対象と trigger を示す
  - state dir に何も書かない

## 10. worker に渡すもの

- **user prompt は、当たった trigger の action を描画したもの**
  - 先頭に `/skill-name` を書けば、Claude Code が起動前に skill を展開する
  - 途中に書いた `/skill-name` は展開されず、呼ばれるかどうかは model 次第になる
- **共通 prompt は、workflow 定義の本文を描画し、`--append-system-prompt-file` で渡す**
  - Claude Code の既定の system prompt の後ろに足す。user prompt の先頭を skill の呼び出しのために空けておける
- **template の変数**: `issue` か `cl`・`trigger`・`attempt`・`workspace`
  - 未知の変数と未知の filter は、描画の失敗にする (SPEC §5.4)
  - 描画に失敗した attempt は、失敗として扱う
- **session id は CLI が発行して渡す**。log と状態 file の 1 行から、Claude Code の transcript へ一意に辿るため
- **dispatcher 本体は、worker の作業規約を持たない**
  - 作業規約は、各 project の workflow 定義の共通 prompt が持つ
  - 例: レビューの通し方・人への引き渡しの書き方・残タスクを起票するときの label・gh の撃ち方

## 11. 配布と外部依存

- **CLI は Go の単一 binary**。`go install`・GitHub Releases・Homebrew tap `swat9013/tap` の cask の 3 経路で、macOS / Linux に配る (ADR 0003 / 0007)
  - tag の push で、GoReleaser が Releases の binary と tap の cask を同時に更新する
  - cask は `depends_on` を持たない。依存 (tracker の CLI (gh・glab・acli のどれか) / claude) の充足は README の「前提」と `doctor` が見る
  - binary は署名しないので、macOS では cask の `postflight_steps` が quarantine を外す (ADR 0007)
- **特定の plugin には依存しない**
  - action が呼ぶ skill と command は、利用者が入れた plugin・repo の `.claude/`・`~/.claude/` のどれに置いてもよい
  - 事前検査 (§8) は、この 3 か所の skill と command から先頭の `/名前` を探す。plugin は、user scope で入れたものと、この repo に project scope で入れたものを数える (local scope の plugin は commit しない settings で有効になり、worker の workspace では読まれない)
- **Claude Code の settings は CLI が書かない** (ADR 0004)。`doctor` は、worker が tracker を操作するのに要りそうな entry を、`tracker.kind` の CLI (gh・glab・acli) の綴りで表示する
  - `jira` は CL を開く CLI を workflow 定義から決められない (CL 置き場の宣言は #114) ので、その entry も足すよう 1 行で示す
- **`doctor` は、利用者の約束に頼る宣言を警告する**
  - `cl.approved` に action を当てている (merge を worker に任せうる)
  - CL 側の trigger に、head branch の pattern・label・同じ repo の branch のどの絞り込みも無い (人の CL や fork の CL に worker を送りうる)

## 12. スコープ外

- **merge 後の deploy**
- **triage**: label を付けて作業対象を trigger に当てるのは人の領分
- **マシンを跨いだ二重起動の防止**: 1 つの issue 置き場は 1 台から回す (§4)
- **loop の監督** (boot 時の起動・落ちた loop の起こし直し): 人が同じコマンドで起動し直す (ADR 0006)
- **tracker 以外の起点** (定期実行・外部の webhook): SPEC §2.2 の「汎用の workflow engine にしない」に従う
- **SPEC §13.7 の HTTP API と dashboard**: 観測は loop の画面・状態 file・`status` で足りる
- **GitHub・GitLab・Jira 以外の tracker / CL host**: tracker の adapter を足す形で広げる (§13)。scope key の粒度は adapter が決める
  - GitLab は、issue 置き場と CL 置き場が同じ host の同じ project にある構成だけを扱う
  - Jira は Jira Cloud の issue 置き場だけを扱う。Jira に CL 置き場は無く、issue 置き場と別の CL 置き場 (GitHub / GitLab) を宣言して束ねるのは #114 まで持たない。それまでの `jira` の workflow 定義は issue 側の trigger だけを書け、CL は worker が action の中で開く
  - Jira Data Center / Server は扱わない (acli が Jira Cloud だけを扱う)
- **1 つの Jira project を複数の repo で分けて回す区分** (component・JQL・board): 1 つの Jira project は 1 本の loop と 1 つの実装 repo で回す。同じ project の 2 本目の loop は scope key の lock で止まる (§9)

## 13. CLI の seam

CLI の中で、呼び出し側から中身を隠す部品と、差し替えの口 (seam) を置く位置。部品の中の分け方は実装に任せ、本節は部品の責務と seam の位置だけを持つ。**seam は、本番とテストで 2 つ以上の adapter を持つところにだけ置く**。

| 部品 | 呼び出し側 | 呼び出し側から隠すもの | adapter |
|---|---|---|---|
| issue 置き場の部品 | tick / 試運転 / `setup` / `doctor` | tracker の呼び方・応答の綴り・失敗の見分け方・scope key の決め方 | gh / glab / acli / テストの in-memory |
| CL 置き場の部品 | tick / 試運転 / `doctor` | CL host の呼び方・応答から CL の状態の語彙への写し方・失敗の見分け方 | gh / glab / テストの in-memory |
| 起動部 | tick | セッションの起動の形 (argv・process group・stream の読み方・停止・回収) | `claude -p` / テストの fake |
| hooks の実行 | tick | shell の撃ち方・timeout | (seam を置かない。テストは一時 dir で本物を撃つ) |

- **issue 置き場の部品と CL 置き場の部品は、workflow 定義から組み立てて、正規化した作業対象と分類済みの失敗を返す** (ADR 0008)
  - 正規化は SPEC §4.1.1 の Issue を土台にする。CL は、CL の状態の語彙と絞り込みに要る field を持つ
  - issue と CL の紐づけは返さない (ADR 0009)
  - 失敗は 認証 / 見えない / 読み切れない / rate limit に分けて返す (SPEC §11.4)
  - scope key は issue 置き場の部品が返す。tracker ごとの粒度 (GitHub は owner と repo、GitLab は host と repo の path、Jira は site と project key) を部品の外に出さない
  - Jira の作業対象の番号は issue の key (`WIDGETS-123`) の数字の部分。scope key が project key を含むので、scope の中で一意になる (ADR 0010)。project key を改名したら scope key が変わるので、workflow 定義を書き換えて loop を起動し直す
  - どの adapter で組み立てるかは、workflow 定義の `tracker.kind` で決める
- **起動部は、1 つの worker の起動・stream の読み取り・停止・回収を 1 つの部品に閉じる** (ADR 0005)
  - stream の event (起動・活動・終了) は中立の形で返す
  - 停止は process group ごと行い、上限時間か停止要求のどちらで止まったかを区別して返す
- **trigger の評価は、正規化した作業対象だけを見る**。adapter の生の応答を覗かない (SPEC §11.2)
