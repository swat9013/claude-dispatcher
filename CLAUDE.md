## 開発フロー

ファイル変更・branch 作成・commit・PR 作成のいずれかに入る前に `CONTRIBUTING.md` を読み、「文書の読み手」「branch・worktree 運用」「gate」「commit・PR 規約」の各節に従う。

CLI のコードを変える前に `docs/design/README.md` の「設計を先に直す」を読み、そこが挙げる変更なら実装より先に `docs/design/` の正本を直す。

## Worktree 方式

- git diff に差分が出る作業は git worktree 上で行う (読み取りだけの作業は除く)
- 差分の出る作業を subagent に任せるときは、Agent tool に `isolation: "worktree"` を渡す

## Agent skills

### Issue tracker

issue は GitHub Issues (`swat9013/claude-dispatcher`) に置き、`gh` CLI で操作する。See `docs/agents/issue-tracker.md`.

### Triage labels

role と label の対応の正本は `docs/agents/triage-labels.md`。label を付ける・外す前に読む。

### Domain docs

single-context (repo 直下の `CONTEXT.md` + `docs/adr/`)。See `docs/agents/domain.md`.
