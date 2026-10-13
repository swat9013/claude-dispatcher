# ADR 0014: tracker.token を gitlab でも書け、置き場の CLI・worker・hooks のすべてに渡す

- Status: Accepted
- Date: 2026-10-13

## Context

`tracker.token` は `kind: github` でだけ書け、loop の観測で撃つ gh にだけ `GH_TOKEN` として渡していた (ADR 0010 の Context の括弧書き)。`kind: gitlab` では、loop も worker も glab 自身の認証 (`glab auth login` の結果) を使い、`tracker.token` は検査で落としていた (#24 の決定)。

`tracker.token` は、ssh 越しの shell などで CLI の認証を取れない環境のためにある。その環境では、次の 2 つの穴があった (#108)。

- worker と hooks には token を渡していなかった。loop の観測は通るが、worker が撃つ `gh pr create` などは認証で落ちる
- glab の認証を取れない環境も gh と同じ形で起きるのに、`kind: gitlab` では token を渡す手段が無かった

## Decision

1. **`tracker.token` を `kind: gitlab` でも書けるようにし、glab に `GITLAB_TOKEN` として渡す**。`kind: jira` では書けないまま残す。acli は token を環境変数で受けないため
2. **`tracker.token` は、loop が撃つ置き場の CLI・worker・hooks のすべてに渡す** (system.md §8「秘密は `$VAR` で間接参照する」)
   - loop の環境に `GH_TOKEN` / `GITLAB_TOKEN` が既にあっても、`tracker.token` を書いたらその値で上書きする。観測と worker が別の token で動くずれを作らない
3. **dispatcher は秘密を持たない線を保つ**。token は環境変数で CLI に渡すだけで、dispatcher が token を使って API を撃つことはない

ADR 0010 の Context の「`tracker.token` は gh に環境変数で渡すだけ」は、この ADR が置き換える。CL 置き場の宣言 (`cl:`。#114) には token を足さない。

## Consequences

### 良い影響

- CLI の認証を取れない環境でも、worker が CL を開け、hook の中で gh / glab を撃てる
- gh と glab で `tracker.token` の扱いが対称になる

### 悪い影響 / 制約

- worker から token を読める。ただし露出は新たに増えない。`$VAR` の変数は既に loop の環境にあり、worker と hooks はその環境をそのまま継ぐ。CLI 自身の認証で動く構成でも、worker は `gh auth token` などで token を読める
- loop を起動した shell の `GH_TOKEN` / `GITLAB_TOKEN` は、`tracker.token` を書くと worker からも見えなくなる (上書きされる)

## Alternatives considered

### gitlab では glab 自身の認証だけを使い続ける (#24 の決定)

- 却下理由: glab の認証を取れない環境 (ssh 越しの shell など) は gh と同じ形で起きる。その環境では、gitlab の構成だけが loop を動かせない

### token を loop の観測にだけ渡し、worker と hooks には渡さない

- 却下理由: 観測は通るのに、worker が CL を開く段で認証に落ちる。worker から token を隠すことにもならない。`$VAR` の変数は既に worker の環境にある

### hooks には渡さない

- 却下理由: hooks は token を要しない操作がほとんどだが、渡す先を分けると規則が 2 本になる。hook の中で gh / glab を撃つ利用者だけが、ssh 越しで落ちる。渡しても露出は増えない
