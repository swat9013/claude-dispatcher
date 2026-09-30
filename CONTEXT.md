# claude-dispatcher

tracker 上の issue と CL を見張り、project が宣言した trigger に当たったものへ Claude Code のセッションを無人で起動する仕組みの文脈。機械は tracker を読むだけで、状態を書くのは起動されたセッションと人である。

## 機構と実行単位

**dispatcher**:
trigger に当たった作業対象へ無人で worker を起動する機構の総称。実体は CLI と、それが起動する LLM セッション群。
_Avoid_: agent runner, orchestrator

**project**:
dispatcher を回す単位。1 つの issue 置き場と、1 つの workflow 定義、1 つの実装 repo の clone に対応する。
_Avoid_: workspace, repo (実装 repo と置き場を指す語と紛れる)

**workflow 定義**:
project の trigger・共通 prompt・並列上限・起動の仕方をまとめた宣言。実装 repo の中に置き、repo と一緒に版を管理する。
_Avoid_: config, 宣言 config

**loop**:
人が起動してから人が止めるまで、1 つの project の tick を周期ごとに回し続ける CLI の process。claim を持ち、worker の親になる。落ちても自分では起き直さない。
_Avoid_: daemon, scheduler (監督を持つ常駐物と紛れる)

**tick**:
loop の 1 周期分の仕事。走っている worker を外部 store と突き合わせ、作業対象を読み直し、trigger を評価して、空いている分だけ worker を起動する。
_Avoid_: run, cycle, poll

**停止要求**:
人が loop に送る停止の合図。1 回目は走っている worker の終了を待ってから loop を止め、2 回目は worker も止めてから loop を止める。
_Avoid_: kill, shutdown

**snapshot**:
1 tick で読んだ外部 store の正規化像。tick を跨いで持ち越さない。
_Avoid_: state, cache

**外部 store**:
状態の正本が置かれる tracker (issue と label) と CL host (CL・review thread・checks・branch) の総称。
_Avoid_: 台帳, database

## 置き場

**issue 置き場**:
dispatcher が issue を読む tracker 上の場所。
_Avoid_: issue repo (tracker が GitHub 以外でも通る語にする)

**CL 置き場**:
dispatcher が CL を読む CL host 上の場所。

**scope key**:
issue 置き場を tracker の種類を問わずに一意に指す識別子。どの粒度で指すかは tracker ごとに決まり、Jira では site と project の組になる。同じマシンの中では、同じ scope key の loop は 1 本しか立たない。
_Avoid_: project 名, repo 名

**CL**:
PR / MR の中立語。
_Avoid_: PR, MR (特定 host の語)

## 作業対象と trigger

**作業対象**:
worker を起動する単位。open な issue か open な CL のどちらか。claim・workspace・attempt はこの単位で数える。
_Avoid_: work item, task, job

**終端**:
作業対象がもう起動の対象にならない状態。issue なら close、CL なら merge か close。

**trigger**:
workflow 定義が宣言する、作業対象に対する述語と action の組。issue 側の述語は label などで書き、CL 側の述語は dispatcher が定める CL の状態の語彙と絞り込みの条件で書く。
_Avoid_: event, 指示, 条件, rule

**CL の状態の語彙**:
CL 側の trigger が参照できる、dispatcher が定めた CL の状態。conflict・未解決の review・CI の失敗・承認済みの 4 つ。
_Avoid_: CL event

**action**:
trigger に当たった作業対象について、worker に渡す prompt。先頭に skill の呼び出しを書ける。
_Avoid_: playbook, command, spawn prompt

**共通 prompt**:
workflow 定義の本文。どの action にも添えて worker に渡す、project 共通の文面。
_Avoid_: 契約, system prompt

**当たる / 外れる**:
作業対象が trigger の述語を満たすことを「当たる」、満たさなくなることを「外れる」と言う。
_Avoid_: 述語が真 / 偽になる, match, 解消

**候補**:
いずれかの trigger に当たり、claim も打ち切りもされていない作業対象。
_Avoid_: queue, backlog

**曖昧な CL**:
同じ head branch から複数の open CL が開いている状態。CL 側の trigger の対象から外し、観測できる形で残す。
_Avoid_: anomaly, error

## 起動と終わり方

**worker**:
loop が 1 つの作業対象について子 process として起動する、使い切りの LLM セッション。action を実行し、作業対象を trigger から外して終わる。
_Avoid_: agent, runner

**claim**:
loop が作業対象に worker を起動中または再起動待ちであることを記憶に持つこと。tracker には書かず、loop が止まれば消える。
_Avoid_: wip, lock, assignee

**workspace**:
作業対象ごとに機械が用意する作業場所。attempt を跨いで残し、作業対象が終端になったら消す。
_Avoid_: worktree (作り方の 1 つにすぎない), sandbox

**完了**:
worker が終わった後に、作業対象が起動した trigger から外れていること。外すのは action の責務である。
_Avoid_: done, success

**attempt**:
同じ作業対象について、完了するまでに worker を起動した回数。worker が失敗するか、終わっても trigger に当たったままなら 1 つ進む。

**打ち切り**:
attempt が上限に達した作業対象を、loop がそれ以上起動しないこと。作業対象が一度 trigger から外れたのを観測すると解ける。
_Avoid_: give up, dead letter
