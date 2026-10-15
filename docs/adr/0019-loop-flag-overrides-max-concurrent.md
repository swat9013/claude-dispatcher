# ADR 0019: 並列上限は loop の引数で上書きでき、上書きした値は読み直しで変わらない

- Status: Accepted
- Date: 2026-10-15

## Context

並列上限は workflow 定義の `limits.max_concurrent` だけで決まっていた。workflow 定義は実装 repo に commit され、tick ごとに読み直される (`docs/design/system.md` §8)。

並列上限は、loop を回すマシンが同時に捌ける worker の数で決まる値で、project の宣言ではない。調停 remote (ADR 0011) で別々のマシンの loop が同じ issue 置き場を分担すると、どのマシンも同じ版の workflow 定義を読むので、マシンごとに違う上限を置けない。一時的に絞る・広げるだけでも、repo の file を書き換えて commit する必要があった (#262)。

## Decision

**`loop` は並列上限を引数 (`--max-concurrent <N>`) で受け、渡されたら workflow 定義の `limits.max_concurrent` より優先する。引数で渡した値は loop が止まるまで固定し、tick ごとに workflow 定義を読み直しても変わらない。引数を省いた loop は、これまでどおり workflow 定義の値を tick ごとに読み直して使う。**

引数で上書きできる値は、マシンの資源で決まる値に限る。この線引きの正本は `docs/design/system.md` §8 に置く。

## Consequences

### 良い影響

- 同じ workflow 定義を読むマシンごとに、捌ける数に合わせた並列上限を置ける
- 一時的に上限を変えるときに、repo への commit が要らない

### 悪い影響 / 制約

- 「workflow 定義を直せば、loop を起動し直さずに効く」(`system.md` §8) に例外ができる。引数を渡した loop では、workflow 定義の `limits.max_concurrent` を直しても効かない。どの値で判定したかは tick の行の `max_concurrent` で確かめる
- 利用者が起動 script に書く CLI の契約になるので、後から外すと、その script が引数の誤りで止まる

## Alternatives considered

### `limits.max_concurrent` を廃止し、引数だけにする

- 却下理由: 既存の workflow 定義を壊す。引数を省いた loop がすべて既定の 1 に落ちる

### 引数と workflow 定義の小さいほうを使う

- 却下理由: 引数で上限を広げられず、捌ける数の多いマシンに合わせられない

### 引数の値も tick ごとの読み直しに混ぜ、workflow 定義が変わったらそちらを採る

- 却下理由: 引数を渡した人の意図が、別の人の commit で黙って覆る。どちらが効いているかを読み手が追えない
