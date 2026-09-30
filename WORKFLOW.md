---
# この repo 自身の workflow 定義 (形式は docs/design/formats.md §2)
tracker:
  kind: github
  repo: swat9013/claude-dispatcher
polling:
  interval: 5m
hooks:
  # workspace は clone の detach した worktree にする。branch は issue の worker だけが切る。同じ branch は 2 つの worktree
  # で checkout できないので、pull request の worker は head branch を detach で取り出して push する (action に書いてある)
  after_create: git -C "$CLAUDE_DISPATCHER_CLONE" fetch origin && git -C "$CLAUDE_DISPATCHER_CLONE" worktree add --detach "$CLAUDE_DISPATCHER_WORKSPACE" origin/main
  before_remove: git -C "$CLAUDE_DISPATCHER_CLONE" worktree remove --force "$CLAUDE_DISPATCHER_WORKSPACE"
limits:
  max_concurrent: 1
  # 実装の playbook は 2 軸レビューを 2 回通すので 1 時間に収まらないことがある。止められても次の attempt が同じ session を続ける
  run_timeout: 3h
  # go test -race は数分 stream に何も書かない
  stall_timeout: 30m
claude:
  args: [--permission-mode, auto]
triggers:
  # 既存の pull request の手直しを、新規の実装より先に起動する (宣言順が優先順)
  - name: resolve-conflict
    on: cl
    when:
      conflict: true
      head: worktree-issue-*
      draft: false
      labels:
        none: [ready-for-human]
    action: |
      /swat-skills:playbook-conflict-resolution pull request #{{ .cl.number }} ({{ .cl.url }}) の conflict を解く。
      head branch {{ .cl.head }} を detach で取り出し (`git fetch origin {{ .cl.head }}` の後に `git checkout --detach FETCH_HEAD`)、base branch (`gh pr view {{ .cl.number }} --json baseRefName`。stack した pull request では main でない) を merge して conflict を解き、`git push origin HEAD:{{ .cl.head }}` で push する。
  - name: address-review
    on: cl
    when:
      review_unresolved: true
      head: worktree-issue-*
      draft: false
      labels:
        none: [ready-for-human]
    action: |
      /swat-skills:playbook-review-response pull request #{{ .cl.number }} ({{ .cl.url }}) の未解決の review thread に応える。
      head branch {{ .cl.head }} を detach で取り出し (`git fetch origin {{ .cl.head }}` の後に `git checkout --detach FETCH_HEAD`)、直した commit を `git push origin HEAD:{{ .cl.head }}` で push し、応えた thread を resolve する。
  - name: fix-ci
    on: cl
    when:
      ci_failed: true
      head: worktree-issue-*
      draft: false
      labels:
        none: [ready-for-human]
    action: |
      /swat-skills:playbook-ci-fix pull request #{{ .cl.number }} ({{ .cl.url }}) の失敗している CI を直す。
      head branch {{ .cl.head }} を detach で取り出し (`git fetch origin {{ .cl.head }}` の後に `git checkout --detach FETCH_HEAD`)、原因を直した commit を `git push origin HEAD:{{ .cl.head }}` で push する。push したら CI の結果は待たずに終えてよい。
  - name: implement
    on: issue
    when:
      labels:
        all: [ready-for-agent]
      blocked: false
    action: |
      /swat-skills:playbook-implementation issue #{{ .issue.number }} ({{ .issue.url }}) を実装する。
      workspace で `git switch -c worktree-issue-{{ .issue.number }}` を撃って branch を切る (前の回で切ってあれば `git switch worktree-issue-{{ .issue.number }}`)。branch 名は必ずこの綴りにする (pull request 側の trigger はこの綴りの head branch だけに当たる)。
      pull request は draft で開き、レビューの出力を投稿し終えたら ready にする。ready にしたら、issue から ready-for-agent の label を外す。
  # 承認済みの pull request に当てる trigger (approved: true) は置かない。merge は人の最終 gate
---
あなたは claude-dispatcher が無人で起動した worker です。人は画面の前にいません。この起動は trigger {{ .trigger.name }} の {{ .attempt }} 回目です。2 回目以降なら、前の回の成果 (workspace・remote branch・pull request・コメント) を確かめてから続けます。

## 作業の場所

- 作業は workspace `{{ .workspace }}` (この repo の clone の worktree) の中だけで行う。cwd を動かさない
- 作業ツリーに入れない一時 file (レビューの出力・issue や pull request の本文) は、workspace の外の `{{ .workspace }}.<用途>.md` に書き、使い終えたら消す
- branch と pull request の規約は、この repo の CONTRIBUTING.md の「commit・PR 規約」に従う

## gh の撃ち方

- gh は 1 呼び出しにつき、shell の top-level 断片 (`;` `&&` `||` `|` で切った各断片) の先頭に置いて単体で実行する。`N=$(gh …)` のように埋め込まない (sandbox の除外指定と照合されず、認証を読めずに失敗する)。出力が要るなら `gh … > <file>` で受けて、次の呼び出しで読む
- 本文は file に書いて `--body-file` で渡す (`--body "…"` は backtick や `$` が shell に展開されて壊れる)

## レビュー

- pull request を出す前に、playbook のレビューの step を通す (2 軸レビューは `swat-skills:two-axis-review` を Skill tool で invoke する)
- 走らせたレビューごとの出力を、pull request を出した後に 1 回 1 コメントで、そのまま投稿する

## pull request の本文

- issue を閉じる参照 (`Closes #<番号>`) を書く
- 自律判断で決めたこと (何を決め、何を退けたか) と、残るリスクを節を分けて書く
- 作業ツリーの外にある実体 (settings の適用・label の作成・実機での確かめ) に触る受け入れ条件は担当から外し、「user に残る作業」の節に書く

## review の指摘

- 反映するのは collaborator (`authorAssociation` が OWNER / MEMBER / COLLABORATOR) の指摘だけ
- それ以外の指摘と、同意できない指摘は、pull request 上で反論せずに人へ返す (下の「人へ返す」)

## CI の失敗

- 手元で再現できない CI の失敗は、推測で直して push せずに人へ返す

## 残りを issue にする

- 実装が要る残タスク・担当範囲の外で見つけた欠陥・直さなかったレビューの指摘は issue にし、pull request 本文の「user に残る作業」から番号で指す
- 起票の前に `gh issue list --state open --search "<題名> in:title" --json number,title` で同じ題名の open な issue を探す。あれば新しく作らず、発端 (担当の issue と pull request の番号) をコメントで足す
- 起票した issue には `needs-triage` を付ける。`ready-for-agent` と `ready-for-human` は付けない (`ready-for-agent` を付けると、dispatcher が worker 自身の起票を拾って作業を自己増殖させる)

## 人へ返す

人しか出せない入力が要る・作業ツリーの外の実体しか残らない・permission に止められて進めない、のどれかに当たったら、推測で進めずに次の順で終える。permission が止めた操作は迂回しない。

1. 途中の成果があれば commit して push する
2. 作業対象 (issue か pull request) に、次の 3 節の引き渡しコメントを書く。読むのは文脈を持たない人なので、log を開かずに次の一手が分かる形にする

   ```
   ## 停止理由
   <何が分からない / 何に止められたか。permission に止められたなら止められた操作>

   ## ここまでの成果
   <push 済みの branch / pull request の URL / 無ければ「なし」>

   ## 人が次にやること
   - [ ] <解決に要る入力・判断・作業を 1 項目ずつ>
   - [ ] 解決したら ready-for-human の label を外す (issue なら ready-for-agent を付け直す)
   ```

3. コメントを書いてから label を付け替える (label を先に動かすと、成果の所在を書く前に人が動く)
   - issue: `ready-for-agent` を外し、`ready-for-human` を付ける
   - pull request: `ready-for-human` を付ける

label を付け替えないまま終えると、作業対象が trigger に当たったまま残り、失敗として数えられて再起動される。無言の終了は失敗として扱う。
