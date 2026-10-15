# ADR 0020: glab へ渡す GITLAB_TOKEN を tracker.host で絞らない

- Status: Accepted
- Date: 2026-10-16

## Context

ADR 0014 は `tracker.token` を glab に `GITLAB_TOKEN` として渡し、worker と hooks にも同じ環境変数で渡すと決めた。その Consequences は「worker から token を読める。ただし露出は新たに増えない。」としている。

glab は `GITLAB_TOKEN` を host を問わず使う。worker と hooks が `tracker.host` と別の GitLab の host へ glab を撃つと、token がその host にも送られる (#196)。ADR 0014 はこの帰結を書いていない (#248)。

## Decision

**glab へ渡す `GITLAB_TOKEN` を `tracker.host` の host に絞らない。** ADR 0014 の決定はそのまま保ち、この ADR はその帰結と、絞らない理由を補う。

glab で token を host に絞る手段は、glab の設定 file に host ごとの token を書くことしかない。dispatcher が設定 file に token を書けば、ADR 0014 の Decision 3「dispatcher は秘密を持たない線」に反する。

## Consequences

### 良い影響

- dispatcher は token を環境変数で CLI に渡すだけで、file に書かない線を保つ
- 利用者自身の glab 設定 (別の host の認証など) は worker と hooks からもそのまま見える

### 悪い影響 / 制約

- worker と hooks が `tracker.host` と別の GitLab の host へ glab を撃つと、`tracker.token` の token がその host にも送られうる。ADR 0014 の「露出は新たに増えない」はこの送り先までは覆わない
- 回避は dispatcher が強制せず、案内 (`internal/guide/guide.md`) で利用者に告げる

## Alternatives considered

### glab の設定 file に token を書いて host を絞る

- 却下理由: dispatcher が秘密を file に持つことになり、ADR 0014 の Decision 3 に反する

### `GLAB_CONFIG_DIR` で dispatcher 用の設定 dir に差し替える

- 却下理由: 差し替えた dir にも token を書く必要があり、上の案と同じく秘密を持つ。加えて、worker と hooks から利用者自身の glab 設定も見えなくなる

### ADR を起こさず、理由と退けた案を `docs/design/system.md` §8 に残す

- 却下理由: system.md は最終形へ書き換えていく正本で、後で §8 を書き換えると理由と退けた案が記録から消える
