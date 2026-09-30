# ADR 0004: 置き場を XDG の config と state に分け、Claude Code の settings は利用者に任せる

- Status: Accepted (宣言 config を XDG に置く部分・token file の部分・state dir の鍵を project 名にする部分・orchestrator のための sandbox の許可は ADR 0009 が置き換えた。workflow 定義は実装 repo の中に置き、state dir の鍵は scope key になる。state を XDG に置く部分と settings を利用者に任せる部分は残る)
- Date: 2026-09-26

## Context

dispatcher は project ごとに、人が書く宣言 config と、tick が書く痕跡 (log / 指示ファイル / 決定ファイル / worker log / lock) を持つ。

- orchestrator は Claude Code の sandbox の中で決定ファイルと引き渡し本文を書く。sandbox で書き込みを許す範囲は狭いほどよい
- 宣言 config には置き場 repo の綴りと、任意で認証 token の file の path が入る。LLM のセッションが書き換えてよい対象ではない
- 動かすには利用者の Claude Code の settings に entry が要る (sandbox の書き込み許可、gh を sandbox の外で実行させる指定)。settings の運用は利用者ごとに違う

## Decision

**宣言 config は `${XDG_CONFIG_HOME:-~/.config}/claude-dispatcher/<project>/`、tick の痕跡は `${XDG_STATE_HOME:-~/.local/state}/claude-dispatcher/<project>/` に置く。macOS でも XDG に揃える。Claude Code の settings は CLI が書かず、`doctor` が要る entry を表示する。**

- sandbox の書き込み許可は state dir だけで足りる。config は sandbox から書けないまま保てる
- `doctor` が表示する entry の一覧の正本は `docs/design/system.md` §11
- 置き場の場所は `paths --json` で引ける (外部の読み手が log を読むため)

## Consequences

### 良い影響

- sandbox に開ける穴が state dir 1 つに絞られる
- CLI の慣例 (XDG) に沿うので、利用者が置き場を推測できる。`XDG_*` で移せる
- 利用者の settings を外部ツールが書き換えない

### 悪い影響 / 制約

- settings の entry は利用者が自分で足す。足し忘れは `doctor` を撃つまで見えない (orchestrator の書き込み失敗や gh の失敗として tick の error に現れる)

## Alternatives considered

### `~/.claude/` 配下に置く

- 却下理由: Claude Code 本体の置き場に外部ツールの状態を混ぜることになり、config と state の区別も付かない。sandbox の許可も広くなる

### macOS では `~/Library/Application Support` に置く

- 却下理由: OS ごとに置き場が分かれ、crontab の行・doc・`doctor` の表示が OS で割れる

### `setup` が承認を得て settings に書き込む

- 却下理由: settings の置き場 (user / project / local) と運用は利用者ごとに違い、外部ツールが選んで書くのは踏み込みすぎ
