---
# claude-dispatcher の workflow 定義 (形式は docs/design/formats.md §2)。setup が書いた雛形なので、project に合わせて直す。
tracker:
  kind: github
  repo: {{REPO}}
polling:
  interval: 5m
hooks:
  # workspace は clone の worktree にする。branch は worker が切る (action に書いてある)
  after_create: git -C "$CLAUDE_DISPATCHER_CLONE" worktree add --detach "$CLAUDE_DISPATCHER_WORKSPACE"
  before_remove: git -C "$CLAUDE_DISPATCHER_CLONE" worktree remove --force "$CLAUDE_DISPATCHER_WORKSPACE"
limits:
  max_concurrent: 1
claude:
  args: [--permission-mode, auto]
triggers:
  - name: implement
    on: issue
    when:
      labels:
        all: [ready-for-agent]
      blocked: false
    action: |
      issue #{{ .issue.number }} ({{ .issue.url }}) を実装する。
      branch claude-dispatcher/issue-{{ .issue.number }} を切って実装し、テストを通して push し、issue を閉じる参照 (Closes #{{ .issue.number }}) を付けた pull request を出す。
      pull request を出したら、issue から ready-for-agent の label を外す。
      実装できないときは、理由と人に決めてほしいことを issue に書き、ready-for-agent の label を外す。
  - name: resolve-conflict
    on: cl
    when:
      conflict: true
      head: claude-dispatcher/*
      draft: false
    action: |
      pull request #{{ .cl.number }} ({{ .cl.url }}) の conflict を解く。
      head branch {{ .cl.head }} を checkout し、base branch を取り込んで conflict を解き、テストを通して push する。
  - name: address-review
    on: cl
    when:
      review_unresolved: true
      head: claude-dispatcher/*
      draft: false
    action: |
      pull request #{{ .cl.number }} ({{ .cl.url }}) の未解決の review に応える。
      head branch {{ .cl.head }} を checkout し、未解決の review thread ごとに、直して push するか、直さない理由を返信する。応えた thread は resolve する。
  - name: fix-ci
    on: cl
    when:
      ci_failed: true
      head: claude-dispatcher/*
      draft: false
    action: |
      pull request #{{ .cl.number }} ({{ .cl.url }}) の失敗している CI を直す。
      head branch {{ .cl.head }} を checkout し、失敗した check の log を読んで原因を直し、テストを通して push する。
  # 承認済みの pull request に当てる trigger (approved: true) は置かない。merge を worker に任せるなら、自分で足す
---
あなたは claude-dispatcher が無人で起動した worker です。人は画面の前にいません。

- 作業は workspace ({{ .workspace }}) の中だけで行う
- この起動は {{ .trigger.name }} の {{ .attempt }} 回目。前の回の続きなら、前の回の成果を確かめてから続ける
- 人の判断が要るときや手に負えないときは、推測で進めずに、作業対象に理由と人に決めてほしいことを書いて終える
