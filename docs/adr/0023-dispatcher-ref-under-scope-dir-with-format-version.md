# ADR 0023: 調停 remote の ref は scope dir の 1 段の下に置き、commit message に形式の版を書く

- Status: Accepted
- Date: 2026-10-22
- ADR 0011 を補う (`refs/dispatcher/<scope key>/...` の scope key の段の綴りと、版の違う loop の扱いを決める。ADR 0011 の他の決定は変えない)

## Context

ADR 0011 は、着手宣言と生存報告を `refs/dispatcher/<scope key>/...` に置くとした。scope key をそのまま ref の path にすると、次の 2 つが起きる。

- GitLab の scope key は `<host>/<group>/…/<name>` で、段の数が置き場で変わる。`gitlab.com/a/b` の ref と同じ prefix の下に、`gitlab.com/a/b/claims` という project の ref が並びうる。`a/b` の loop が自分の ref を prefix で列挙すると、別の置き場の ref まで拾う
- git は、`.` で始まる段や `.lock` で終わる段などを ref 名として受け付けない。GitLab の path の文字種 (`[A-Za-z0-9._-]`) はこれらを通す

また、ref は別々のマシンの loop が読み合うので、dispatcher の版が揃っているとは限らない。古い loop が新しい形の生存報告を読めないと、生きている loop を死んだとみなして乗っ取る。

## Decision

**scope key の段には `<scope dir>` (formats.md §1 の、無害化した scope key に sha256 の先頭 8 文字を足した名前) を 1 段で置く。着手宣言と生存報告の commit message には形式の版を書く。loop は、自分の知る版より新しい版の ref を見たら、その scope では worker を起動せず、dispatcher を上げることを要対処に出す。**

- 例: `refs/dispatcher/github.com_acme_widgets-1a2b3c4d/...`
- 新しい版の loop は、古い版の ref も読む

## Consequences

### 良い影響

- 1 つの scope の ref が 1 段の prefix に収まり、列挙で他の置き場の ref を拾わない
- ref 名が、git の綴りの規則をいつも満たす
- マシンの state dir と remote の ref を、同じ名前で突き合わせられる
- 版が混ざっても、黙って乗っ取ることが起きない。古い loop は止まって知らせる

### 悪い影響 / 制約

- ref の path から scope key をそのまま読めない
- チームの 1 人が先に版を上げ、新しい形の ref を書いた時点で、古い版の loop は起動を止める。版はチームで揃えて上げる

## Alternatives considered

### scope key をそのまま段に置く (ADR 0011 の書き方のまま)

- 却下理由: Context の 2 つが起きる

### 版を ref の path に入れる (`refs/dispatcher/v1/...`)

- 却下理由: 版の違う loop は互いの ref が見えず、同じ作業対象を黙って二重に起動する

### 版を扱わず、版を揃えるよう案内に書く

- 却下理由: 揃っていないことを誰も検知できず、乗っ取りが黙って起きる
