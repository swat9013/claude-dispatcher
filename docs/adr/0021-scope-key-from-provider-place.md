# ADR 0021: scope key は issue 置き場の provider が置き場から返す

- Status: Accepted
- Date: 2026-10-17
- 次の決定を置き換える
  - ADR 0009: 「lock の鍵は scope key にする」の子項目「scope key は tracker の adapter が返す、issue 置き場の識別子」

## Context

ADR 0009 は、lock の鍵にする scope key を tracker の adapter (issue 置き場の部品) が返すとした。

#274 (PR #307) で、scope key を返す主体を issue 置き場の役割を担う provider に移し、`docs/design/system.md` §13 もそう書き換えた。一方で ADR 0009 の本文は古い主体のまま残り、ADR 0009 を読んだ人は adapter が返すと読む (#309)。

scope key は lock の鍵と state dir の鍵に使う。どちらも tracker を撃つ前に要るので、issue 置き場の部品 (CLI の Runner を含む) を組まずに得られるほうがよい。

## Decision

**scope key は、issue 置き場の役割を担う provider が、読んだ置き場 (Place) から返す。** issue 置き場の部品を組まずに得る。scope key が issue 置き場の識別子であることは変えない。

- tracker ごとの粒度 (GitHub は owner と repo、GitLab は host と repo の path、Jira は site と project key) は provider の外に出さない (system.md §13)
- ADR 0009 の残りの決定 (lock の鍵を scope key にすること、1 つの issue 置き場を 1 台から回すことを ADR 0011 が置き換えたことを含む) はそのまま保つ

## Consequences

### 良い影響

- scope key を得るために tracker の CLI を組まずに済み、lock と state dir の鍵を部品の組み立てより前に決められる
- tracker の種類の知識が provider に閉じる線 (system.md §13) と揃う

### 悪い影響 / 制約

- provider を足すときは、置き場の読み方とともに scope key の返し方も書く

## Alternatives considered

### ADR を起こさず system.md §13 だけを直す

- 却下理由: ADR 0009 の本文と §13 の食い違いが残り、ADR 0009 を読んだ人が古い主体を正と読む

### ADR 0009 の本文を書き換える

- 却下理由: docs/design/README.md「設計を先に直す」は、決定を覆すときに ADR を新しく起こし、既存 ADR の本文を書き換えないと定める
