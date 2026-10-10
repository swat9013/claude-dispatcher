# ADR 0012: workflow 定義の書き方の正本を、binary に埋め込む案内に置く

- Status: Accepted
- Date: 2026-10-11

## Context

利用者のセッション (利用者が対話で動かす LLM のセッション) が dispatcher を導入し、`WORKFLOW.md` を書くには、書ける項目・述語・template の変数・検査の規則を知る必要がある (#162)。

その正本は `docs/design/formats.md` §2 で、binary には入っていない。Homebrew・`go install`・GitHub Releases で入れた第三者の手元には無い。`setup` の雛形のコメントもこの path を指していて、利用者の repo からは開けない。

そこで、書き方の案内を binary に埋め込み、`claude-dispatcher guide` で出すことにした。すると、formats.md §2 と案内が同じ形式を書くことになる。

## Decision

**埋め込む案内を、workflow 定義の書き方の正本にする。formats.md §2 は案内を指すだけにする。**

- formats.md の §2.1〜§2.9 の見出しは残し、各節は案内の対応する節を指す。コードと文書の `formats.md §2.x` の参照を書き換えずに済ませる
- §2.8 (検査)・§2.9 (事前検査) のうち、CLI が何を出すか (誤りの行の書式・exit・いつ落ちるか) は formats.md に残す。外から観測できる形式で、black-box テストの正本だから。案内は「何が誤りになるか」を利用者の語で書く
- workflow 定義の読み込みが受ける key がすべて案内に出てくるかを、テストで検査する。埋め込んだ契約と formats.md の一致をテストで保った ADR 0003 に倣う (その契約は ADR 0009 で orchestrator と一緒になくなったが、埋め込みの是非とは別の理由による)
- 案内は、利用者の手元に無いもの (`docs/design/system.md`・issue 番号・`internal/` のコメント) を指さない

## Consequences

### 良い影響

- 案内は、入れた binary と同じ版の形式を必ず出す
- 書き方を 1 箇所で直せば、利用者のセッションにも、この repo の開発者と worker にも届く

### 悪い影響 / 制約

- workflow 定義の書き方の正本が `docs/design/` の外 (embed できる package の dir) に置かれる。formats.md だけを読む開発者は、リンクを辿る必要がある
- 案内は利用者向けの語で書くので、開発者向けの細部 (実装の根拠・関連 issue) は書けない。それらは formats.md の残りの節・system.md・ADR に置く

## Alternatives considered

### formats.md §2 をそのまま埋め込む

- 却下理由: §2 は開発者向けで、利用者の手元に無い `system.md`・issue 番号・`internal/` のコメントを指す。指された先を利用者のセッションは開けない

### 利用者向けの案内を別に書き、formats.md §2 も残す

- 却下理由: 同じ形式を 2 つの文章が書くと、いずれずれる。ずれは、利用者のセッションが誤った `WORKFLOW.md` を書くことで初めて表に出る。key の網羅はテストで検査できても、規則の文面の一致は検査できない

### JSON Schema を出す

- 却下理由: 制約の多くが他の項目の値で変わる (`tracker.kind` が `jira` なら `on: cl` を書けない等)。スキーマで書くと読み手のエージェントにとって読みにくく、検査は `loop --dry-run` と `doctor` が既に持つ
