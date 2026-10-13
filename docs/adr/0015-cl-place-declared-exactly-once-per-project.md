# ADR 0015: CL 置き場は project ごとにちょうど 1 つ、workflow 定義の宣言で持つ

- Status: Accepted
- Date: 2026-10-13

## Context

dispatcher は実装 repo の clone を cwd にして動き、worker はその repo の branch を push して CL を出す。issue 置き場が Jira でも、CL を出す先 (GitHub か GitLab の repo) はかならずある。

#114 で、Jira を issue 置き場にする構成に、別の CL 置き場を宣言する `cl:` を足した。そのとき project を「1 つの issue 置き場と、多くとも 1 つの CL 置き場」と定義し、jira の `cl:` を任意にした。任意にすると、実際には起こらない「CL 置き場の無い project」を、workflow 定義の検査・部品の組み立て・起動時の確認・`doctor` が状態として抱える (`cl:` の無い jira の `on: cl` を断る文言、CL 置き場の部品が無い Store、CL 置き場の確認を飛ばす分岐)。

あわせて、CL 置き場をどこから決めるかを記録しておく必要がある。github・gitlab では `tracker.repo` が issue 置き場と CL 置き場を兼ねる。そのため「github のように origin から推せば `cl:` は要らないのでは」という問いが出るが、github・gitlab も実行時に origin を読んではいない。

## Decision

**project の CL 置き場はちょうど 1 つで、workflow 定義の宣言で持つ。実行時に clone の origin から推さない。**

- github・gitlab: `tracker.repo` (と `tracker.host`) の宣言した場所が、issue 置き場と CL 置き場を兼ねる (system.md §12)
- jira: `tracker.repo` は Jira の project key で CL 置き場を兼ねられないので、`cl:` (`kind` / `host` / `repo`) を必須にする。書かなければ workflow 定義の検査で `cl` の欠落として名指しする
  - 決め方の規則は「issue 置き場の provider が CL 置き場の役割を持たなければ `cl:` が要る」で、種類の名前で分岐しない (system.md §13)
- loop・試運転・`doctor` は宣言だけを読む。origin を読むのは `setup` だけで、`setup` は読んだ値を雛形の宣言 (`tracker.repo` か `cl:`) に書き出す。以後は宣言が正本
- tick が読み直した workflow 定義の CL 置き場が起動時と違えば、scope key が違うときと同じく、その tick は何も起動しない (formats.md §6)

## Consequences

### 良い影響

- 「CL 置き場の無い project」が状態から消える。CL 側の trigger は、どの種類でも CL 置き場の provider が支える宣言の範囲で書ける
- jira でも、CL 置き場の CLI (gh / glab) の解決と認証が、loop の起動時・試運転・`doctor` でかならず確かめられる。CL 側の作業に入ってから認証の不足に気付くことが無い
- どの種類でも「置き場は宣言で持ち、`setup` が origin から埋める」で揃う。CL を出す先は WORKFLOW.md を読めば分かる

### 悪い影響 / 制約

- CL 側の trigger を使わない jira の利用者も、`cl:` を書き、gh か glab を入れて認証する必要がある
- #114 より前の jira の workflow 定義 (`cl:` が無い) は検査で落ちる。`cl:` を足すまで loop が起動しない
- `setup --kind jira` は flag だけでは完結せず、origin を持つ clone と gh / glab の認証が要る

## Alternatives considered

### `cl:` を任意にする (project の CL 置き場は多くとも 1 つ)

- 却下理由: 実行環境の前提 (実装 repo の clone 上で動く) では起こらない状態を表せてしまう。その状態のための分岐 (`on: cl` を断る文言・CL 置き場の部品の無い Store・確認の省略) を、検査・組み立て・`doctor` が持ち続ける

### 実行時に clone の origin から CL 置き場を推す

- 却下理由: origin と別の remote に CL を出す構成 (fork の運用・mirror) で、黙って違う repo を読む。CL を出す先が WORKFLOW.md に現れず、`doctor` の出力でしか分からない

### `cl:` を省いたら origin から推し、書けば上書きする

- 却下理由: 上の案と同じく、省いたときに黙って誤る。置き場の正本が宣言と origin の 2 つになり、どちらが効いているかを読み手が判定することになる
