---
# claude-dispatcher の workflow 定義 (形式は docs/design/formats.md §2)。setup が書いた雛形なので、project に合わせて直す。
tracker:
  kind: gitlab
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
  # GitLab の CE は issue の依存を API で返さないので、blocked の述語は書けない
  - name: implement
    on: issue
    when:
      labels:
        all: [ready-for-agent]
    action: |
      issue #{{ .issue.number }} ({{ .issue.url }}) を実装する。
      branch claude-dispatcher/issue-{{ .issue.number }} を切って実装し、テストを通して push し、issue を閉じる参照 (Closes #{{ .issue.number }}) を付けた merge request を glab mr create で出す。
      merge request を出したら、issue から ready-for-agent の label を外す (glab issue update {{ .issue.number }} --unlabel ready-for-agent)。
      実装できないときは、理由と人に決めてほしいことを issue に書き (glab issue note)、ready-for-agent の label を外す。
  # merge request に当てる trigger (conflict・review・CI の手直し) は、GitLab ではまだ書けない
---
あなたは claude-dispatcher が無人で起動した worker です。人は画面の前にいません。

- 作業は workspace ({{ .workspace }}) の中だけで行う
- この起動は {{ .trigger.name }} の {{ .attempt }} 回目。前の回の続きなら、前の回の成果を確かめてから続ける
- issue と merge request は glab で操作する
- 人の判断が要るときや手に負えないときは、推測で進めずに、作業対象に理由と人に決めてほしいことを書いて終える
