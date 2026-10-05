---
# claude-dispatcher の workflow 定義 (形式は docs/design/formats.md §2)。setup が書いた雛形なので、project に合わせて直す。
tracker:
  kind: gitlab
  host: {{HOST}}
  repo: {{REPO}}
polling:
  interval: 5m
hooks:
  # workspace は clone の detach した worktree にする。branch は issue の worker だけが切る。同じ branch は 2 つの worktree
  # で checkout できないので、merge request の worker は source branch を detach で取り出して push する (action に書いてある)
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
  - name: resolve-conflict
    on: cl
    when:
      conflict: true
      head: claude-dispatcher/*
      draft: false
    action: |
      merge request !{{ .cl.number }} ({{ .cl.url }}) の conflict を解く。
      source branch {{ .cl.head }} を detach で取り出し (`git fetch origin {{ .cl.head }} && git checkout --detach FETCH_HEAD`)、target branch を取り込んで conflict を解き、テストを通して `git push origin HEAD:{{ .cl.head }}` で push する。
  - name: address-review
    on: cl
    when:
      review_unresolved: true
      head: claude-dispatcher/*
      draft: false
    action: |
      merge request !{{ .cl.number }} ({{ .cl.url }}) の未解決の discussion に応える。
      source branch {{ .cl.head }} を detach で取り出し (`git fetch origin {{ .cl.head }} && git checkout --detach FETCH_HEAD`)、未解決の discussion ごとに、直して `git push origin HEAD:{{ .cl.head }}` で push するか、直さない理由を返信する。応えた discussion は resolve する。
  - name: fix-ci
    on: cl
    when:
      ci_failed: true
      head: claude-dispatcher/*
      draft: false
    action: |
      merge request !{{ .cl.number }} ({{ .cl.url }}) の失敗している pipeline を直す。
      source branch {{ .cl.head }} を detach で取り出し (`git fetch origin {{ .cl.head }} && git checkout --detach FETCH_HEAD`)、失敗した job の log を読んで原因を直し、テストを通して `git push origin HEAD:{{ .cl.head }}` で push する。
  # 承認済みの merge request に当てる trigger (approved: true) は置かない。merge を worker に任せるなら、自分で足す
---
あなたは claude-dispatcher が無人で起動した worker です。人は画面の前にいません。

- 作業は workspace ({{ .workspace }}) の中だけで行う
- この起動は {{ .trigger.name }} の {{ .attempt }} 回目。前の回の続きなら、前の回の成果を確かめてから続ける
- issue と merge request は glab で操作する
- 人の判断が要るときや手に負えないときは、推測で進めずに、作業対象に理由と人に決めてほしいことを書いて終える
