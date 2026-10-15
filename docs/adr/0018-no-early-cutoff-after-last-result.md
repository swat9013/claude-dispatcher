# ADR 0018: 最後の `result` の後に行が途絶えた worker を、stall より早く見切らない

- Status: Accepted
- Date: 2026-10-15
- #185 で決めた振る舞いの理由を記録する (振る舞いは変えない)

## Context

worker の `claude -p` は、stream-json の `result` の行を turn ごとに書く。background の作業 (subagent の完了・pending な wakeup の発火) が新しい turn を始めると、その turn も `result` を 1 回書く。`-p` は終わりに background の作業を待つので、`result` を書いた後も process が終わらないことがある。

#117 の worker (2026-10-12) は、最後の報告を `result` で出した後も終わらず、並列の枠を塞いだ (#185)。最後の `result` を worker の終わりとみなせば、枠を早く空けられる。

## Decision

**worker の終わりは process の終了だけで決める。`result` の行は終わりとして扱わない。最後の `result` の後に行が途絶えた区間も、他の行の書かれない区間と同じく `limits.stall_timeout` で止める** (formats.md §6 の「**progress**」)。

最後の `result` の後の沈黙と、background の作業を待つ区間は、stream の行からは見分けられない。どちらも `result` の後に行が書かれない区間で、後者は後で新しい turn を始めて仕事を続ける。

## Consequences

### 良い影響

- background の作業を待っている worker を、仕事の途中で止めない
- stall の判定が、`result` の有無で分かれない 1 つの規則のままで済む

### 悪い影響 / 制約

- 仕事を終えたのに終わらない worker は、`limits.stall_timeout` が過ぎるまで並列の枠を塞ぐ。枠を早く空けるには、worker が終わらなくなる原因 (再入の仕組みなど) を `claude.args` で塞ぐ (guide.md の項目の表の `claude.args` の行)

## Alternatives considered

### 最後の `result` の後、一定時間行が無ければ worker を終わったとみなす

- 却下理由: 最後の `result` の後の沈黙と background の作業を待つ区間を stream の行から見分けられないので、background の作業を待っている worker も止めてしまう
