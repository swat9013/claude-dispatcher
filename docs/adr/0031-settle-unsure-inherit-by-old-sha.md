# ADR 0031: 結果の分からない引き継ぎは旧 sha を覚え、ref が旧 sha のままなら届いていないと決着させる

- Status: Accepted
- Date: 2026-10-26
- ADR 0030 の決定を補う (置き換えない)。ADR 0030 は「今の実装が旧 sha を覚えていない」ことを前提に、旧 sha を覚えて ref の sha と比べる案を別 issue (#477) に回した。この ADR はその案を採り、ADR 0030 の前提を変える

## Context

同じマシンで起動し直した loop は、生きている orphan の作業対象に残った死んだ loop の着手宣言を、旧 sha を期待値にした置き換えで自分の名前に引き継ぐ (ADR 0027)。置き換えの push が結果の分からない失敗をすると、loop はその作業対象を覚えておき、次の tick で ref と着手宣言の commit message を読んで、届いていたかを決める (formats.md §12 の 8)。

ADR 0030 の時点の実装は、覚えるときに期待値の旧 sha を捨て、届いたかを着手宣言の commit message を読めたかだけで決めていた。この決め方では、ref の sha が旧 sha のまま (push は届いていない) でも、その commit message を読めなければ決着しない。そのまま loop が止まると、自分の名前の着手宣言が remote に無いのに `orphan のために引き継いだかもしれない着手宣言を残して止まる (…)` の `error` の行が出る。

ただし今の実装は、coordination board から読んだ commit message を sha ごとに覚え、ref に載り続ける sha の commit message は読み直さずに返す (`coord.Party`)。引き継ぎは持ち主の loop 名を読めた着手宣言にしか撃たないので、旧 sha の commit message は引き継いだ時点で読めている。ref が旧 sha のままなら次の tick でも読めるので、今の実装では上の組み合わせに届かない。届かないことは commit message の覚え方に頼っており、旧い決着の判定はそれを自分では確かめていなかった。

置き換えの push は旧 sha を期待値にするので、届いていれば ref は旧 sha でなくなる。ref の sha が旧 sha のままなら、push は届いていない。

## Decision

**結果の分からない引き継ぎを覚えるときは、push の期待値にした旧 sha も覚える。次の tick の突き合わせで ref の sha が覚えた旧 sha のままなら、commit message を読まずに「届いていない」と決めて忘れる。**

- ref の sha が旧 sha と違うとき (自分の push が届いたか、他の loop が書き換えた) は、今までどおり commit message を読んで決める。読めない間は覚えたままにする
- ADR 0030 の決定 (届いたかを決める前に止まるときは、届いたかによらず同じ文言の `error` の行を同じ水準で書く) は見直さない。届いていなくてもこの行が出る場合は残る (下の悪い影響)

## Consequences

### 良い影響

- ref の sha が旧 sha のままなら、commit message を読めなくても次の tick で決着し、止まるときに偽の `error` の行が出ない。今の実装では届かない組み合わせだが、ref が旧 sha のままなら届いていないという判定は ref の sha だけで決まり、commit message の覚え方に結び付かなくなる (覚え方を変えても、この判定は変わらない)

### 悪い影響 / 制約

- 次の tick の突き合わせで決着させる前に止まるとき (突き合わせで ref を読めなかった tick の後を含む) と、ref の sha が旧 sha と違い、その commit message を読めないまま止まるときは、届いていなくても同じ `error` の行が出る (ADR 0030 が受け入れた範囲に残る)

## Alternatives considered

### ADR を書かず formats.md の記述だけを直す

- 却下理由: ADR 0030 が、旧 sha を覚えていないことを前提にし、この案を別 issue で扱うと明記している。ADR を書かずに実装と formats.md だけを変えると、決定の記録が実装とずれる
