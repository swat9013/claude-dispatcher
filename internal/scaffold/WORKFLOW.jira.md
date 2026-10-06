---
# claude-dispatcher の workflow 定義 (形式は docs/design/formats.md §2)。setup が書いた雛形なので、project に合わせて直す。
tracker:
  kind: jira
  host: {{HOST}}
  repo: {{REPO}}
polling:
  interval: 5m
hooks:
  # workspace は clone の detach した worktree にする。branch は issue の worker が切る
  after_create: git -C "$CLAUDE_DISPATCHER_CLONE" worktree add --detach "$CLAUDE_DISPATCHER_WORKSPACE"
  before_remove: git -C "$CLAUDE_DISPATCHER_CLONE" worktree remove --force "$CLAUDE_DISPATCHER_WORKSPACE"
limits:
  max_concurrent: 1
claude:
  args: [--permission-mode, auto]
triggers:
  # status 名は project ごとに違う。project の status 名に合わせて書き換える (site に無い名前は loop の起動時に名指しされる)。
  # Jira の CL 置き場 (GitHub / GitLab) の手直しの trigger (on: cl) はまだ書けない
  - name: implement
    on: issue
    when:
      status:
        any: [Ready for Agent]
    action: |
      Jira の issue {{ .issue.key }} ({{ .issue.url }}) を実装する。issue の内容は acli jira workitem view {{ .issue.key }} で読む。
      branch claude-dispatcher/{{ .issue.key }} を切って実装し、テストを通して push し、題名に {{ .issue.key }} を入れた CL を、この repo の host の CLI (gh pr create か glab mr create) で出す。
      CL を出したら、issue の status を Ready for Agent から移す (acli jira workitem transition --key {{ .issue.key }} --status <次の status> --yes)。
      実装できないときは、理由と人に決めてほしいことを issue のコメントに書き (acli jira workitem comment create --key {{ .issue.key }} --body <本文>)、status を Ready for Agent から移す。
---
あなたは claude-dispatcher が無人で起動した worker です。人は画面の前にいません。

- 作業は workspace ({{ .workspace }}) の中だけで行う
- この起動は {{ .trigger.name }} の {{ .attempt }} 回目。前の回の続きなら、前の回の成果を確かめてから続ける
- Jira の issue は acli で操作する。CL はこの repo の host の CLI (gh か glab) で操作する
- 人の判断が要るときや手に負えないときは、推測で進めずに、issue に理由と人に決めてほしいことを書いて終える
