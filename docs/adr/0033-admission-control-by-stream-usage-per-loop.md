# ADR 0033: admission control は worker の stream の使用率で loop ごとに判定し、起動だけを止める

- Status: Accepted
- Date: 2026-11-08

## Context

loop は候補がある限り worker を起動するので、人の知らない間に Claude の usage limit を消費する。2026-11-07 には週の使用率が 97% に達した後も起動が続き、リセットの直前に 100% に達して overage に入った (#499)。

使用率は、走っている worker の stream の `rate_limit_event` で読める。閉じている間は worker が走らないので、新しい event が来ない。event の形式が変わると、どの window も読めない `rate_limit_event` になる。そこで閉じると、使用率が 0% でも無人の loop の起動が止まる。手元の worker log の 1,516 件に、読めない行は 0 件だった (#502)。

## Decision

**loop は worker の stream の `rate_limit_event` から使用率を読み、閾値以上の window があれば新しい worker の起動と再起動を止める。走っている worker も loop の process も止めず、閾値以上の window のリセット時刻を過ぎると解ける。閾値は loop の引数で渡し、省けば 100% にする。観測は loop の間で共有しない。**

**どの window も読めない event を見たら、閉じて要対処に出す。解けるのは、読める event が来るか、人が loop を起動し直すときだけにする。** 形式が変われば dispatcher を上げる作業が要り、起動し直しはそこに含まれる。

## Alternatives considered

- `/usage` の文章を tick ごとに読む: 文言や locale の変更で黙って壊れる
- 非公開の `/api/oauth/usage` を読む: 公式の支持が無い
- 閾値で loop の process を止める: loop は自分では起き直さず (system.md §9)、5 時間の window がリセットしても再開しない
- 観測を state root に置いて loop の間で共有する: 減らせる超過は worker 1 本分で、scope key の外に状態を持つ重さに見合わない
- 閾値の既定を置かない: 引数を付け忘れた loop が、上限に達しても起動を続ける
- 読めない event で閉じた loop を、一定時間ごとに worker を 1 本だけ通して確かめる: limit の近くでも worker を起動しうる
- 読めない event では閉じない: admission control が黙って効かなくなる

## Consequences

### 良い影響

- 使用率が閾値に達すると、人の操作なしに起動が止まり、リセット時刻を過ぎると再開する

### 悪い影響 / 制約

- 使用率は、走っている worker の分だけ閾値を超えうる
- `rate_limit_event` の形式が変わると、使用率が低くても起動が止まる。走っている worker が読める event を届けなければ、人が dispatcher を上げて loop を起動し直すまで解けない
- 見直し条件: loop の `admission_close` の行の、使用率と閾値の差。5 ポイントを超える行が出たら、stream だけで観測し loop の間で共有しない決定を見直す
- 見直し条件: 閾値を 95 以下で渡した loop の worker log の、`status` が `rejected` の `rate_limit_event` の件数。1 件でも出たら、走っている worker を止めない決定を見直す
