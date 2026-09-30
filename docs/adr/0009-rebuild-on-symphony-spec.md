# ADR 0009: Symphony の SPEC を土台に、宣言した trigger で機械が worker を直接起動する形へ作り直す

- Status: Accepted
- Date: 2026-09-30
- 次の決定を置き換える
  - ADR 0001: claim を label に置く部分、orchestrator を起こす部分、「実装済み」を紐づく CL の存在で表す部分とその Alternatives での却下、tick を跨いで何も記憶しない部分 (claim・attempt・打ち切りを loop の memory に持つ)
  - ADR 0002: 全体
  - ADR 0003: orchestrator の契約を埋め込む部分と、playbook を plugin `swat-skills` から解決する部分
  - ADR 0004: 宣言 config を XDG に置く部分、token file の path を config に書く部分、state dir の鍵を project 名にする部分 (scope key に変わる)、orchestrator が決定ファイルを書くための sandbox の許可
  - ADR 0005: worker を detach 起動する部分、orchestrator を起動する部分、起動引数を `--permission-mode auto` に固定し permission 層を外さないとする部分 (起動引数は workflow 定義が決める)
  - ADR 0006: 停止要求が worker を止めない部分、token file を保険に残す部分 (認証は環境変数で渡す)、`loop <project> <interval>` という起動の形、tick を跨いで状態を持ち越さない部分、project 単位の lock と単発の `tick`
  - ADR 0008: issue と CL の紐づけを CL 置き場の部品が返す部分、CL の状態の語彙を GitHub の綴りのまま定義する部分とその Alternatives での却下 (`cl.*` の語彙に変わる)、機構の label の検査、契約 file と playbook への言及

## Context

これまでの形には、次の 3 つの性質があった。

- issue を実装して CL にすることと、CL の conflict・review・CI 失敗を手直しすることの 2 つに特化していた
- 手順の資材は plugin `swat-skills` の playbook に固定されていた
- 何に着手させるかは orchestrator LLM が判断し、orchestrator は決定ファイルを経由して worker を起動していた

これを汎用の仕組みにし、「どの出来事にどの手順を当てるか」を project ごとに宣言できるようにしたい (#74)。

OpenAI の [Symphony](https://github.com/openai/symphony) は同じ種類の問題を解く仕様 (`SPEC.md`) を公開している。その上に乗る方法を 2 つ比べた。

- **Symphony を包む**: 自前の実装はほとんど減らない。理由は次の 3 つ
  - SPEC §10 は agent を Codex app-server の protocol に固定している
  - 上流は「prototype」であり、SPEC から自前の版を作ることを勧めている
  - SPEC は CL を見ない
- **SPEC の設計を借りて自前で作り直す**: こちらを採る

SPEC と今の形は、次の 3 点で正反対の選択をしている。論点ごとに比べて選んだ (grilling の決定表は #74)。

| 論点 | SPEC | これまで |
|---|---|---|
| claim | orchestrator process の memory | tracker の `dispatcher:wip` label |
| 起動の判断 | 機械が決定的に行う | orchestrator LLM が選ぶ |
| worker の寿命 | orchestrator の子。retry と stall 検知あり | detach した使い切り。retry なし |

## Decision

**project は、実装 repo の中の workflow 定義に trigger (作業対象に対する述語と action の組) を宣言する。loop は tick ごとに外部 store を読み、trigger に当たった作業対象へ worker を子 process として直接起動する。claim と retry は loop の memory に持ち、機械は tracker と CL host に何も書かない。**

- **orchestrator LLM と決定ファイルは廃止する**
  - 起動するかどうかは宣言で決まる
  - 見送るか、人へ返すかは action 自身が判断する
- **作業対象は issue と CL の 2 種類にする。機械は issue と CL を紐づけない**
  - CL 側の trigger は CL の状態だけを見る
  - どの branch で作業するかは worker が決める
- **完了は、action が作業対象を trigger から外すことで表す** (SPEC の handoff state と同じ)
  - dispatcher が持つ label (`dispatcher:wip`・`ready-for-human`) はなくなる
- **action は prompt そのものにする**
  - skill を使うときは、prompt の先頭に `/skill-name` を書く
  - 共通 prompt (workflow 定義の本文) は `--append-system-prompt-file` で渡す
  - plugin `swat-skills` への依存と、binary に埋め込んだ契約はなくなる
- **workflow 定義は実装 repo の中に置く** (SPEC の `WORKFLOW.md`)
  - LLM が書き換えるリスクは利用者が負い、dispatcher は関与しない
  - 秘密は `$VAR` で間接参照する
- **workspace は機械が作業対象ごとに作る** (SPEC §9)
  - 作り方は hooks で利用者が書く
  - attempt を跨いで残し、作業対象が終端になったら消す
- **lock の鍵は scope key にする**
  - scope key は tracker の adapter が返す、issue 置き場の識別子
  - 1 つの issue 置き場には 1 台のマシンから回す

### SPEC から意図して外れるところ

次の 7 点は、SPEC に合わせて「直す」対象ではない。

1. **agent の起動 (SPEC §10) を `claude -p` に置き換える**
   - `--output-format stream-json` を読んで、stall を検知する
   - attempt が 2 回目以降なら `--resume` で前の session を続ける
2. **CL 側の trigger を機械が評価する**
   - SPEC の機械は CL を見ず、手直しは人が tracker の state を `Rework` に戻すことで起きる
   - GitHub の issue には state が open / closed しかないので、CL の状態を機械が読んで trigger にする
3. **当たったままの正常終了は失敗と数える**: 正常に終わっても trigger に当たったままなら、失敗と同じく backoff して attempt を数え、上限に達したら打ち切る
   - SPEC は正常終了なら 1 秒後に続きを起動し、回数に上限を持たない
   - SPEC がそうするのは、Codex の worker が turn の上限で作業の途中に正常終了するため
   - `claude -p` は作業が終わるまで走るので、正常終了して当たったままなのは action の不具合とみなせる
   - 上限がないと、1 つの action の不具合が利用枠を使い続ける
   - 打ち切りは memory に持ち、tracker には書かない
4. **二重起動は scope key の lock で防ぐ**
   - SPEC は claim を memory に持つが、複数の process が同じ tracker に向くことを定めていない
5. **走っている worker は終端でだけ止める**: 作業対象が trigger から外れても止めず、作業対象が終端になったときだけ止める
   - SPEC は、実行中の issue が active でなくなると worker を止める
   - こちらでは、action 自身が作業の途中で作業対象を trigger から外す (label を外すなど)。外れた時点で止めると、後始末を打ち切ってしまう
6. **空きを待つ間は attempt を進めない**
   - SPEC §16.6 は、空きが無くて再起動できないときも attempt を進める
   - こちらは attempt の上限で打ち切るので、進めると空きを待つだけで打ち切りの回数を使ってしまう
7. **未知の key は失敗させる**: workflow 定義の未知の key は名指しで失敗させる
   - SPEC §5.3 は、前方互換のために未知の key を無視する
   - 綴り違いが「宣言していない」と同じ挙動になり、trigger が黙って効かなくなるのを防ぐほうを採る

## Consequences

### 良い影響

- 自前の実装から、次のものが消える
  - orchestrator の周辺: 契約・指示ファイル・決定ファイルとその検査・起動の迂回
  - plugin の install 先の解決
  - wip と stale wip の扱い
  - issue と CL の紐づけの判定
- 起動するかどうかが宣言で決まる。同じ snapshot からは同じ worker が起動する
- worker が loop の子になるので、次のことが 1 つの stream を読むだけで揃う
  - stall の検知
  - retry
  - 進行の表示
- trigger・action・hooks・起動の引数を利用者が書けるので、次の拡張が workflow 定義の範囲に収まる
  - 成果物が CL でない作業
  - plugin を持たない利用者
  - permission mode の選択 (#62)
  - worktree の作り方 (#26)

### 悪い影響 / 制約

- **loop を止めると、走っている worker も止まる**
  - これまでは、Ctrl+C の後も worker が最後まで進んだ
  - 1 回目の停止要求で worker の終了を待つことで、段階的な停止は保つ
- **claim はマシンを跨いで効かない**
  - 別のマシンで同じ issue 置き場に loop を立てると、二重に起動する
  - 「1 つの issue 置き場は 1 台から回す」は運用上の約束になる
- **「見送る」の横断的な判断を失う**
  - これまでの orchestrator は、同じ tick の候補を並べて比べてから選べた
  - 代わりに、trigger の宣言順と作成日時の古い順で起動する
- **CL が merge されずに close されても、issue は自動では候補に戻らない**
  - 実装済みであることを、紐づく CL の有無ではなく、action が label で表すため
- **打ち切りは loop を起動し直すと消える**。起動し直すたびに、上限までの attempt を再び使いうる
- **作り直しの間は、外から観測できる形式の互換を保たない**
  - log.jsonl の行と `paths --json` は公開契約だが、新しい形式が決まるまでは互換を壊してよい
  - 利用者がまだいないため (#74 Q32)。新しい形式が決まったら、公開契約の規則を戻す
- **prompt の途中に書いた `/skill` は Claude Code が展開しない**
  - 呼ばれるかどうかは model 次第になる
  - 事前の検査も、先頭の `/skill` にしか効かない
- **これまで spawn prompt の契約が持っていた規約は、dispatcher 本体から消える**。対象はレビューの通し方・引き渡しの書き方・起票の label など
  - この repo 自身の workflow 定義の共通 prompt に移す
- **この repo の運用は、利用者が守る約束に下がる**。対象は次の 2 つ
  - merge を人の最終 gate にすること
  - 人が開いた CL に worker を送らないこと
  - どちらも、workflow 定義で `cl.approved` に action を当てないことと、CL 側の述語で head branch を同じ repo の branch に絞ることで表す
  - 破ったときの歯止めとして、`doctor` がこの 2 つに当たる宣言を警告する

## Alternatives considered

### Symphony の Elixir 版を包む

- 却下理由: agent が Codex に固定されている
  - Claude Code を使うには、Codex app-server の protocol を模した shim か、fork した runner を自前で持つことになる
  - 上流は保守を約束していない
  - CL の手直しも SPEC の範囲外なので、自前で持ち続けることになる

### orchestrator LLM を残し、trigger を「LLM への指示」として使う

- 却下理由: trigger と action の対応を宣言で書く以上、LLM に残る仕事は「見送り」だけになる。そのために決定ファイル・契約・検査を保つのは割に合わない

### claim を tracker の label に残す (機械が wip を付け外しする)

- 却下理由: 機械が tracker に書く経路が復活する
  - worker を loop の子にしたので、loop が止まれば worker も止まる。memory の claim で不整合は残らない
  - マシンを跨いだ排他は失うが、1 台から回す運用で足りる

### issue 側の述語を GitHub の検索 query で書く

- 却下理由: 検索の index は即時には更新されない
  - action が label を外した直後の再評価が古い index を読み、完了を失敗と数えうる
  - rate limit も厳しい
  - 一覧の取得結果を手元で評価する小さな文法を持つ

### 述語を既製の式言語 (CEL・jq の式など) で書く

- 却下理由: 書ける条件が広すぎ、誤りの名指しが弱くなる
  - 述語は label・assignee・作者の立場など十数個の field の組み合わせで足り、式言語の表現力を要しない
  - 未知の key を名指しで失敗させる方針 (外れるところ「未知の key は失敗させる」) を、式の中の綴り違いにまで効かせにくい
  - 依存を 1 つ増やし、workflow 定義の公開形を式言語の版に縛る
