## 開発フロー

ファイル変更・branch 作成・commit・PR 作成のいずれかに入る前に `CONTRIBUTING.md` を読み、「branch・worktree 運用」「gate」「commit・PR 規約」の各節に従う。

CLI のコードを変える前に `docs/design/README.md` の「設計を先に直す」を読み、そこが挙げる変更なら実装より先に `docs/design/` の正本を直す。

## Agent skills

### Issue tracker

issue は GitHub Issues (`swat9013/claude-dispatcher`) に置き、`gh` CLI で操作する。See `docs/agents/issue-tracker.md`.

### Triage labels

既定の 5 role をそのままの綴りで使う (`needs-triage` / `needs-info` / `ready-for-agent` / `ready-for-human` / `wontfix`)。加えて、深掘り待ちに `need-grilling` を使う。See `docs/agents/triage-labels.md`.

### Domain docs

single-context (repo 直下の `CONTEXT.md` + `docs/adr/`)。See `docs/agents/domain.md`.
