# ADR 0025: 調停 remote の push の負けを、porcelain の拒否の印で判定する

- Status: Accepted
- Date: 2026-10-23
- ADR 0011 の決定を補う (置き換えない)。ADR 0011 は先着を `git push --force-with-lease` の条件付き更新で決めるとしたが、push が負けたことをどう見分けるかは決めていなかった

## Context

ADR 0011 の調停では、着手宣言と生存報告の push が通らなかったとき、それが「他の loop に先を越された (負け)」のか「network や認証の失敗」なのかを見分ける必要がある。負けなら worker を起動せずに退き、失敗なら調停できなかったとして扱う。

`git push --porcelain` の出力で拒否を表す行は、理由の文言 (`cannot lock ref ...`・`stale info` など) が server の実装と git の version で変わる。一方、拒否の印は次の 2 つに分かれる。

- `[remote rejected]`: server が old-oid の照合で拒んだとき
- `[rejected]`: push の前に読んだ remote の ref が期待値と違い、git が送らずに拒んだとき

この挙動は #169 の実機確認と、手元の bare repo での再現で確かめた。

## Decision

**push の exit code が非 0 で、porcelain の行が `!` で始まり `[remote rejected]` か `[rejected]` を含むときを負けと判定する。理由の文言は判定に使わない。**

判定の規則の正本は `docs/design/system.md` §4。

## Consequences

### 良い影響

- server の実装や git の version で変わる理由の文言に依存しないので、host を替えても判定が崩れない

### 悪い影響 / 制約

- 印の出し分けは実機と bare repo で確かめた範囲のもので、印そのものが git の version で変われば判定を見直す
