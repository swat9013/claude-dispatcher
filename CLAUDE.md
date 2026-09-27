## 開発フロー

ファイルを変更する実装に入る前・branch を切る・commit する・PR を出す前に `CONTRIBUTING.md` を読み、そこに書かれた worktree・branch 規約・gate・commit / PR 規約に従う。

## Agent skills

### Issue tracker

issue は GitHub Issues (`swat9013/claude-dispatcher`) に置き、`gh` CLI で操作する。See `docs/agents/issue-tracker.md`.

### Triage labels

既定の 5 role をそのままの綴りで使う (`needs-triage` / `needs-info` / `ready-for-agent` / `ready-for-human` / `wontfix`)。See `docs/agents/triage-labels.md`.

### Domain docs

single-context (repo 直下の `CONTEXT.md` + `docs/adr/`)。See `docs/agents/domain.md`.
