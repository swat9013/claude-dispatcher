---
# claude-dispatcher の workflow 定義 (書き方は `claude-dispatcher guide`)。setup が書いた雛形なので、project に合わせて直す。
tracker:
  kind: jira
  host: {{HOST}}
  repo: {{REPO}}
# CL 置き場は cwd の clone の origin から埋めた。CL を出す先が違えば直す
{{CL}}polling:
  interval: 5m
hooks:
  # workspace は clone の detach した worktree にする。branch は issue の worker だけが切る。同じ branch は 2 つの worktree
  # で checkout できないので、{{CL_NOUN}} の worker は head branch を detach で取り出して push する (action に書いてある)
  after_create: git -C "$CLAUDE_DISPATCHER_CLONE" worktree add --detach "$CLAUDE_DISPATCHER_WORKSPACE"
  before_remove: git -C "$CLAUDE_DISPATCHER_CLONE" worktree remove --force "$CLAUDE_DISPATCHER_WORKSPACE"
claude:
  # 再入の仕組みを塞ぐ (理由は `claude-dispatcher guide` の項目の表の下の注記「再入の仕組みを塞ぐ」)。
  # --disallowedTools は値を複数取るが、dispatcher が後ろに渡す -p で止まる
  args: [--permission-mode, auto, --disallowedTools, "ScheduleWakeup,CronCreate,CronList,CronDelete,Monitor"]
triggers:
  # status 名は project ごとに違う。project の status 名に合わせて書き換える (site に無い名前は loop の起動時に名指しされる)
  - name: implement
    on: issue
    when:
      status:
        any: [Ready for Agent]
    action: |
      Jira の issue {{ .issue.key }} ({{ .issue.url }}) を実装する。issue の内容は acli jira workitem view {{ .issue.key }} で読む。
      branch claude-dispatcher/{{ .issue.key }} を切って実装し、テストを通して push し、{{CL_NOUN}} を {{CL_CREATE}} で出す。
      {{CL_NOUN}} を出したら、issue の status を Ready for Agent から移す (acli jira workitem transition --key {{ .issue.key }} --status <次の status> --yes)。
      実装できないときは、理由と人に決めてほしいことを issue のコメントに書き (acli jira workitem comment create --key {{ .issue.key }} --body <本文>)、status を Ready for Agent から移す。
{{CL_TRIGGERS}}
  # {{CL_NOUN}} を merge しても Jira の issue は終端にならない。status を動かすのは人 (merge に当てる trigger は置かない)
---
あなたは claude-dispatcher が無人で起動した worker です。人は画面の前にいません。

- 作業は workspace ({{ .workspace }}) の中だけで行う
- この起動は {{ .trigger.name }} の {{ .attempt }} 回目。前の回の続きなら、前の回の成果を確かめてから続ける
- Jira の issue は acli で操作する。{{CL_NOUN}} は {{CL_CLI}} で操作する
- 人の判断が要るときや手に負えないときは、推測で進めずに、作業対象に理由と人に決めてほしいことを書いて終える
