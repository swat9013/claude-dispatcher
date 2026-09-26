# ADR 0005: LLM セッションは `claude -p` で起動し、起動部を差し替え可能な部品に閉じる

- Status: Accepted
- Date: 2026-09-26

## Context

tick は orchestrator と worker の 2 種類の Claude Code セッションを無人で起動する。起動の形には次の選択肢がある。

- `claude -p` (print / headless): TTY が要らず、終わると exit する。cron から起動でき、tick は終了を待てる
- 対話セッション (`claude "<prompt>"`): TTY が要るので cron からは起動できない。セッションが自分で終わる手段も無い
- background セッション (`claude --bg`): 対話セッションを背景で走らせ、人が後から `claude attach` で覗いたり介入したりできる。すぐに戻るので、tick が終了を待つ形にはならない

permission posture は auto mode を使う。classifier が deny した操作は、`-p` でも対話でも実行されない (対話にしても、人が deny を承認で覆すことはできない)。

加えて、`-p` の利用が将来、定額の利用枠の外に置かれる可能性がある。そうなれば起動の形を変える必要が出る。

## Decision

**orchestrator も worker も `claude -p --permission-mode auto --session-id <UUID>` で起動する。起動は CLI の中の 1 つの部品 (interface) に閉じ、今の実装は `-p` の 1 つだけにする。**

- orchestrator は tick が終了を待ち、worker は新しい process session として detach する
- session id は CLI が発行して渡す。log の行から Claude Code の transcript へ一意に辿るため
- `--dangerously-skip-permissions` は使わない。止められた worker は続行不能として人へ返す
- 起動の形を変えるとき (例: `claude --bg` + `claude agents` による観測) は、部品の実装を 1 つ足し、影響を受ける駆動の記述 (lock の持ち方・終了の待ち方) を設計 doc で直してから切り替える

## Consequences

### 良い影響

- TTY の無い cron から、追加の器 (terminal multiplexer 等) なしで起動できる
- tick は orchestrator の終了を待てるので、lock の意味が単純 (tick 1 本 = orchestrator 1 本)
- 起動の形の変更が部品 1 つに閉じる

### 悪い影響 / 制約

- 走っている worker を人が対話的に覗く手段は持たない。観測は worker log・`status`・transcript で行う
- `-p` の扱いが利用枠の上で変われば、起動部を差し替える作業が要る (未確定のリスク)

## Alternatives considered

### 対話セッションで起動する

- 却下理由: TTY が要るので cron から起動できず、terminal multiplexer の pane 等の器を持ち込むことになる。セッションが自分で終わらないので、tick が終了を待てない

### `claude --bg` で起動する

- 保留: 人が attach して介入できる利点があるが、TTY の無い cron から起動できるか、作業を終えたセッションが自分で終わるか、は未確認。すぐに戻るので lock の意味も変わる。`-p` を変える必要が出たときの第一候補として、起動部の差し替えで入れる

### `--dangerously-skip-permissions` で起動する

- 却下理由: tracker に書く権限と push 権限を持つセッションから permission 層を外すことになる
