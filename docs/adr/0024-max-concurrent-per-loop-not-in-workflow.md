# ADR 0024: 並列上限を workflow 定義から外し、loop の引数と `max-concurrent` で loop ごとに渡す

- Status: Accepted
- Date: 2026-10-22
- ADR 0019 のうち、`limits.max_concurrent` を残す部分と、引数で渡した値を loop が止まるまで固定する部分を置き換える

## Context

ADR 0019 は、並列上限を loop を回すマシンの資源で決まる値で project の宣言ではないと線引きし、`loop --max-concurrent` で上書きできるようにした。引数で渡した値は loop が止まるまで固定し、`limits.max_concurrent` は既存の workflow 定義を壊さないために残した。

この形では、loop を回しながら並列上限を変えられない。変えるには loop を起動し直すが、停止要求は走っている worker の終わりを待つか (1 回目)、worker を止める (2 回目)。引数を省いた loop は `WORKFLOW.md` を直せば次の tick から効くが、直すのは repo に commit する file で、調停 remote で分担すると全マシンに同じ値が効く。

並列上限を workflow 定義に置く理由は、既存の workflow 定義との互換だけだった。

## Decision

**並列上限は workflow 定義に置かない (`limits.max_concurrent` を撤去する)。loop は起動時の値を引数 `--max-concurrent <N>` で受け (省けば 1)、走っている間は `claude-dispatcher max-concurrent <N> [WORKFLOW]` で変えられる。subcommand で渡した値が引数より優先し、その loop が止まるまで効く。**

- subcommand は、workflow 定義から scope key を引き、その state dir の `max_concurrent` に値を書く。loop が立っていなければ書かずに失敗する
- loop は tick ごとに workflow 定義と一緒にこの file を読み、次の tick から新しい値で空きを判定する。loop を起こして tick を早めることはしない
- 走っている worker の数より下げても、走っている worker は止めない。空きが出るまで新しい起動と再起動を待つ
- loop は起動時にこの file を消す。前の loop の値を次の loop に継がない
- 元の値へ戻す操作は持たない。戻すなら戻したい値を撃つ
- workflow 定義に置かない値の線引きの正本は `docs/design/system.md` §8

## Consequences

### 良い影響

- loop を止めずに、走っている worker を中断せずに並列上限を変えられる
- 並列上限が「workflow 定義を読み直すたびに効く値」と「引数で固定した値」に分かれていた例外 (`system.md` §8) が消え、loop ごとに人が渡す値だけになる

### 悪い影響 / 制約

- `limits.max_concurrent` を書いた既存の workflow 定義は、未知の key として検査に落ちる。項目を消し、値を `--max-concurrent` で渡し直す。互換は持たない
- 引数を省いた loop は 1 で回る。1 より多く回すマシンは、起動 script に `--max-concurrent` を書く
- 利用者が script に書く CLI の契約が 1 つ増える。後から外すと、その script が止まる
- 新しい値が効くのは次の tick からで、`polling.interval` が長いと待つ
- subcommand を撃った直後に loop が起動し直すと、値が新しい loop に効くことがある。まれなので扱わない

## Alternatives considered

### 引数の値を固定したまま、`WORKFLOW.md` の編集で変える (ADR 0019 のまま)

- 却下理由: 引数を渡した loop では変えられず、引数を省いた loop では repo の file を書き換える。調停 remote で分担すると、マシンごとに違う値を置けない

### signal (SIGUSR1 / SIGUSR2) で 1 ずつ増減する

- 却下理由: 値を指定できず、撃つ側から今の値を確かめられない。停止要求のほかに signal の経路が増える

### loop の端末のキー操作で変える

- 却下理由: ADR 0006 が保留した raw mode の問題 (Ctrl+C がキー入力として届き、停止の経路が 2 つになる) を抱える

### git 管理外の上書き file を workflow 定義の隣に置く

- 却下理由: loop が止まっても残り、次の loop が黙って拾う

### `limits.max_concurrent` を残し、3 つの値に優先の順を付ける

- 却下理由: 並列上限はマシンの資源で決まる値で、repo の宣言に置く理由は互換だけだった。出どころが 3 つになると、どれが効いているかを読み手が追う手間が増える

### `--max-concurrent` を必須にする

- 却下理由: 引数を書いていない起動 script がすべて止まる。既定 1 は安全な側の値で、途中で広げられる
