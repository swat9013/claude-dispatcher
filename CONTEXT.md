# claude-dispatcher

issue tracker の着手可 issue を Claude Code のセッションに実装させ、CL まで運ぶ自律オーケストレーションの文脈。専用の永続 store を持たず、状態は tracker と CL host の上の成果物に置く。

## 機構と実行単位

**dispatcher**:
issue から CL までを無人で進める機構の総称。実体は CLI `claude-dispatcher` と、それが起動する LLM セッション群。
_Avoid_: dispatch, agent runner

**project**:
dispatcher を回す単位。issue 置き場・CL 置き場・着手可 label・並列上限を 1 つの宣言 config で固定し、実装 repo の clone 1 つに対応する。
_Avoid_: workspace, repo (実装 repo と置き場を指す語と紛れる)

**tick**:
CLI の 1 回分の仕事。外部 store を読み直し、指示を導出し、指示があるときだけ LLM を起こす。loop が周期ごとに回すほか、単発でも撃てる。
_Avoid_: run, cycle, poll

**loop**:
人が起動してから人が止めるまで、1 つの project の tick を周期ごとに回し続ける CLI の process。落ちても自分では起き直さない。
_Avoid_: daemon, scheduler (監督を持つ常駐物と紛れる)

**snapshot**:
1 tick で読んだ外部 store の正規化像。tick を跨いで持ち越さない。
_Avoid_: state, cache

**外部 store**:
状態の正本が置かれる tracker (issue と label) と CL host (CL・review thread・checks・branch) の総称。
_Avoid_: 台帳, database

## 置き場

**issue 置き場**:
dispatcher が候補を探し、claim と人返しの label を付ける tracker 上の場所。
_Avoid_: issue repo (tracker が GitHub 以外でも通る語にする)

**CL 置き場**:
worker が CL を開く CL host 上の場所。省略すると issue 置き場と同じ。

**CL**:
PR / MR の中立語。紐づく open CL の存在そのものが「実装済み」を表す。
_Avoid_: PR, MR (特定 host の語)

## 状態

**着手可**:
triage を経て、AI に委ねてよい仕様が揃ったことを示す label の状態。綴りは project ごとに宣言する。
_Avoid_: ready, todo

**候補**:
着手可で、wip も人待ちも付かず、紐づく open CL が無い issue。
_Avoid_: queue, backlog

**wip (`dispatcher:wip`)**:
着手中を示す claim の label。枚数がそのまま並列実行数になる。人が手で付けて claim してもよい。
_Avoid_: in-progress, assignee (assignee は人の担当専用)

**人待ち (`ready-for-human`)**:
LLM が続行不能として人へ返した印の label。人が外すまで候補に戻らない。
_Avoid_: blocked, escalated

**stale wip**:
付けた LLM が剥がさずに止まって残った wip。worker の異常死のほか、wip を付けた後に orchestrator が止まったとき (timeout・loop への 2 回目の停止要求) にも残る。snapshot からは着手中と区別できず、回収は人が行う。

## 役とやり取り

**orchestrator**:
指示があるときだけ起動される使い切りの LLM セッション。指示ごとの採否を判断し、wip を付け、起動する worker を決定ファイルに書く。
_Avoid_: scheduler, manager

**worker**:
決定ファイルどおりに CLI が detach 起動する使い切りの LLM セッション。担当 issue を実装して CL に到達するか、人へ返して終わる。orchestrator との往復チャネルを持たない。
_Avoid_: agent, runner

**指示 (instruction)**:
snapshot から機械的に確定する行動候補。CLI が導出し、実行するかは orchestrator が決める。
_Avoid_: task, job, command

**kind**:
指示・採否・worker 起動が取る着手形態。`start` = 新規実装、`reenter` = 既存 CL への再入。

**再入 (reenter)**:
worker 由来の open CL に条件 (conflict / 未解決 review / CI 失敗) が立ったとき、その CL の branch へ worker を戻すこと。
_Avoid_: retry, resume

**anomaly**:
指示カタログのどれにも分類できない観測。指示 0 件の沈黙と区別して orchestrator へ上げる。
_Avoid_: error, alert

**指示ファイル**:
指示があった tick で CLI が書く、snapshot と指示の列。orchestrator の唯一の入力。

**決定ファイル**:
orchestrator が書く唯一の出力。指示ごとの採否と、起動する worker の spawn prompt を持つ。

**spawn prompt**:
worker に渡す prompt の全文。worker が自走して CL に着くまでに要る情報をすべて文面に持つ。

**引き渡し**:
LLM が人へ返すときに issue へ書くコメント。停止理由・ここまでの成果・人が次にやることの 3 節からなる。
_Avoid_: escalation

## 外部の資材

**playbook**:
worker が作業の手順として Read する手順書。plugin `swat-skills` が提供し、dispatcher は同梱しない。
_Avoid_: recipe, workflow

**原則索引**:
worker が作業に当たる設計原則を引くための索引。plugin `swat-skills` が提供する。
