# ADR 0032: workspace root は `.scope-key` で 1 つの scope key に専有させる

- Status: Accepted
- Date: 2026-11-07
- ADR 0010 の改名の手順を補う (置き換えない)。project key を改名したら `.scope-key` も消す

## Context

1 つの repo の作業を Jira project ごとの loop で回すと、同じ dir に置いた workflow 定義は既定の `workspace.root` が同じになる。掃除は root の下をすべて自分の project のものとして読むので、別の Jira project の `issue-<番号>` を終端と読み、worker が走っていても消す (#498)。

## Decision

**loop と単発起動は起動時に `<workspace root>/.scope-key` を読み、無ければ自分の scope key を書き、別の scope key が書いてあれば起動せずに失敗する。試運転と `doctor` は読むだけにする。** 検査は起動時だけで、走行中に `workspace.root` を書き換えた tick は検査しない。

## Alternatives considered

- 既定の root に scope dir を足す: 既定を使う全利用者の path が変わり、同じ root を明示した場合を防げない
- workspace の名前を key にする: `cl-<番号>` は両方の loop の掃除対象に残り、ADR 0010 を覆す

## Consequences

### 良い影響

- 設定の誤りで別の loop の workspace を消す経路が、起動時の失敗に変わる

### 悪い影響 / 制約

- `.scope-key` の無い root は最初に起動した loop のものになる。この決定の前から共有していた root では、この検査は相手の workspace を守らない
- project key を改名したら `.scope-key` を手で消す。別の issue 置き場へ移すなら `workspace.root` を別の dir に直す
- 見直し条件は置かない。起きるのは設定の誤りの 1 回ごとで、実行の記録から決定の良し悪しを言える量が無い
