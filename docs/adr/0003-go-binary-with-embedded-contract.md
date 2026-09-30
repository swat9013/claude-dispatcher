# ADR 0003: Go の単一 binary に orchestrator の契約を埋め込み、playbook は plugin `swat-skills` から実行時に解決する

- Status: Accepted (配布経路に Homebrew tap の cask を足すのは ADR 0007。契約の埋め込みと plugin `swat-skills` からの playbook の解決は ADR 0009 が置き換えた)
- Date: 2026-09-26

## Context

dispatcher は、自分の repo で回したい第三者に配る汎用ツールとして作る。成立に要る部品は 3 つある。

1. cron から起動される CLI (観測・指示の導出・LLM の起動)
2. orchestrator の手順と spawn prompt の文面 (契約)
3. worker が Read する playbook と原則索引、worker が invoke するレビュー skill と agent

3 は Claude Code plugin `swat-skills` (marketplace `swat9013`) が既に提供している。worker はレビュー skill と agent を名前で呼ぶので、3 は「file がある」だけでは足りず、worker のセッションで plugin として有効になっている必要がある。

配り方には次の制約がある。

- cron の最小 PATH からも確実に起動できる、安定した path が要る
- 契約 (2) と CLI (1) の版が食い違うと、決定ファイルの形・条件カタログ・検査が噛み合わず、黙って壊れる
- 利用者の環境に余分な runtime を要求したくない

## Decision

**CLI は Go の単一 binary にし、orchestrator の契約 file を `embed` で binary に埋め込む。playbook・原則索引・レビュー skill は plugin `swat-skills` に実行時に依存し、CLI が install 先を解決して絶対 path を LLM に渡す。**

- 配布は `go install github.com/swat9013/claude-dispatcher/cmd/claude-dispatcher@<tag>` と、GoReleaser で GitHub Releases に置く macOS / Linux の binary
- orchestrator には契約を skill としてではなく prompt として直接渡す。plugin の skill 名に依存しないので、契約と CLI の版が必ず一致する
- 契約の中で原則索引を指す path は、CLI が解決した絶対 path を埋め込む
- plugin の install 先は、Claude Code が記録する install 情報から実行時に解決する (手順の正本は `docs/design/system.md` §11)。crontab にも config にも plugin の path を書かせない
- 版の照合・互換検査はしない。常に install 済みの版を使い、playbook の frontmatter や名前が変わって壊れたら、そのとき対処する

## Consequences

### 良い影響

- 利用者は binary 1 つと plugin を入れれば動く。Python や uv を要求しない
- cron の行は binary の絶対 path を指すだけで済む。plugin の install 先 (版ごとに変わる cache の path) を crontab に書かない
- 契約と CLI の版が 1 つの成果物に固定される

### 悪い影響 / 制約

- plugin `swat-skills` の変更で CLI が実行時に壊れうる。互換検査を持たないので、検知は tick の error か worker の失敗になる
- 決定ファイルの形を、埋め込んだ契約 (orchestrator が読む例) と `docs/design/formats.md` の両方が持つ。一致をテストで検査する
- `installed_plugins.json` は Claude Code の内部 file で、形が変わると解決が壊れる。解決は 1 箇所に閉じ、失敗は loud にする

## Alternatives considered

### Python の package (`uv tool install`) にする

- 却下理由: frontmatter や TOML の読み込みは標準的な library で済み書きやすいが、利用者に uv と Python を要求する。第三者への配布が主目的なので、単一 binary の価値が上回る

### CLI を plugin に同梱したまま、固定 path の launcher が plugin の install 先を引いて起動する

- 却下理由: dispatcher を plugin の一部として配る限り、第三者は dispatcher のために plugin 一式の構成と更新経路を受け入れることになる。独立したツールとして版と配布を持てない

### 契約を plugin の skill に残し、CLI だけを分離する

- 却下理由: 契約と CLI の版が 2 系統に割れ、決定ファイルの形や条件カタログが食い違っても検知できない

### playbook を本 repo に取り込む

- 却下理由: playbook はレビュー skill と agent を名前で呼ぶので、playbook だけを持ち出しても動かない。plugin 側で育つ手順を二重に保守することにもなる

### crontab に plugin の版付き path を直接書く

- 却下理由: plugin を更新すると旧版の cache はいずれ消え、tick が黙って止まる
