# ADR 0017: 調停 remote への ref の push は、pre-push hook を通さない

- Status: Accepted
- Date: 2026-10-14
- ADR 0011 を補う (ADR 0011 の決定は変えない)

## Context

ADR 0011 では、調停 remote を指した project の loop が、着手宣言と生存報告を実装 repo の git remote の独自 ref (`refs/dispatcher/<scope key>/...`) へ push する。push するのは空の tree の commit で、利用者のコードを含まず、HEAD とも無関係になる。

自前運用の GitLab (19.3.2) での実機確認 (#170) で、この push が実装 repo の pre-push hook に止められた。hook (`pnpm check`) は「push する commit が HEAD と違う」として push を落とし、`git push --no-verify` を付けると通った。husky・lefthook・pre-commit などの pre-push hook が入った実装 repo では、host を問わず同じことが起きうる。

- 着手宣言の push が落ちると、その loop は作業対象を取れない
- 生存報告の push が落ちると、その loop は他のマシンから死んだとみなされ、worker が乗っ取られる

## Decision

**調停 remote への `refs/dispatcher/*` の push は、すべて `git push --no-verify` で撃ち、実装 repo の pre-push hook を通さない。**

pre-push hook は利用者のコードの変更を検査するためのもので、dispatcher が push する空の tree の commit には検査する対象が無い。検査を飛ばしても守るものを失わない。

## Consequences

### 良い影響

- 実装 repo にどんな pre-push hook が入っていても、着手宣言と生存報告が hook に落とされない

### 悪い影響 / 制約

- 実装 repo の pre-push hook が、利用者のコードと無関係な目的 (push の監査・記録など) を持っていても、dispatcher の push にはそれが効かない
- server 側の hook (pre-receive) と remote の push の制限は `--no-verify` では避けられない。ADR 0011 の「loop を起動するマシンには、実装 repo へ push できる権限が要る」はそのまま残る

## Alternatives considered

### hook の効かない別の場所 (bare clone など) から push する

- 却下理由: clone を持つ費用と同期の手間が増える。加えて、`core.hooksPath` を global に設定した環境では bare clone でも hook が効くので、`--no-verify` 無しでは hook を確実に避けられない
