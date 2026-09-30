# 外から観測できる形式

> **移行中 (ADR 0009、#74。この注記は #83 で外す)**: 本 file は、作り直しの slice (#77〜#82) が形式を決めるたびに節ごとに書き足す。まだ決まっていない節は「未定」と書き、その節を決める issue を示す。新しい設計と食い違うところでは、[`system.md`](system.md) が優先する。
>
> 作り直しの間は、互換を保たない (ADR 0009 の「悪い影響 / 制約」)。下の「公開契約」も、新しい形式が決まるまでは適用しない。

- 本 file が CLI の外から観測できる形式の正本。black-box テストはここを参照して書き、実装とテストが食い違ったら本 file に合わせる
- 各 file の責務は [`system.md`](system.md) §7 / §8 / §9、用語は [`CONTEXT.md`](../../CONTEXT.md)
- **公開契約**: log.jsonl の行 (§4) と `paths --json` (§7) は外部の読み手を持つ。key の削除・改名・意味の変更は互換を壊すので、足すだけにとどめるか、ADR を起こす

## 1. 置き場

| 変数 | 既定 |
|---|---|
| state root | `${XDG_STATE_HOME:-$HOME/.local/state}/claude-dispatcher` |

macOS でも XDG に揃える (`~/Library` は使わない)。scope key ごとに state dir を持つ。

```
<state root>/<scope dir>/
  loop.lock                           loop の生存期間の flock の対象 (§6)
```

- **scope key**: issue 置き場の部品が決める、issue 置き場の識別子 (system.md §13)
  - GitHub は `github.com/<owner>/<name>` を小文字にしたもの (GitHub の owner と repo の名前は大文字と小文字を区別しない)
- **`<scope dir>`**: scope key を無害化した綴りに、scope key の短い hash を足した名前
  - 無害化: 小文字にし、`[a-z0-9._-]` 以外の文字を `_` に置き換える
  - hash: scope key の sha256 の hex の先頭 8 文字
  - 例: scope key `github.com/acme/widgets` → `github.com_acme_widgets-<hash 8 文字>`
  - hash を足すのは、無害化で別の scope key が同じ綴りになっても、置き場を分けるため
- state dir は loop が起動時に作る。試運転 (§5) は作らない
- 時刻は RFC 3339 の UTC (`Z` 表記) で、秒まで

## 2. workflow 定義 (`WORKFLOW.md`)

実装 repo の中に置く。path は `loop` の引数で渡し、省けば cwd の `WORKFLOW.md` を読む (§6)。

file は YAML の front matter と本文からなる。

- 1 行目が `---` の行で、次の `---` の行までが front matter
- その後ろが本文。本文は共通 prompt (system.md §10)。空でもよい

```markdown
---
tracker:
  kind: github               # 必須。初版は github だけ
  repo: acme/widgets         # 必須。issue 置き場 (owner/name)。$VAR で書ける
  token: $WIDGETS_GH_TOKEN   # 任意。gh に GH_TOKEN として渡す。$VAR でだけ書ける
polling:
  interval: 5m               # 任意。周期。既定 5m
triggers:                    # 必須。1 つ以上
  - name: implement          # 必須
    on: issue                # 必須
    when:                    # 任意。省くと、open な issue のすべてに当たる
      labels:
        all: [ready-for-agent]
        none: [needs-info]
      unassigned: true
      author: collaborator
      blocked: false
    action: |                # 必須。worker に渡す prompt の template
      /swat-skills:playbook-implementation
---
この project の共通 prompt。
```

### 2.1 項目

| key | 型 | 中身 |
|---|---|---|
| `tracker.kind` | 文字列 | tracker の種類。`github` だけ |
| `tracker.repo` | 文字列 | issue 置き場。`<owner>/<name>` |
| `tracker.token` | 文字列 | gh に環境変数 `GH_TOKEN` として渡す token。`$VAR` でだけ書ける (値そのものを書かない)。省くと、loop を起動した環境の認証を gh がそのまま使う |
| `polling.interval` | 文字列 | 周期。Go の duration の綴り (`90s` / `5m` / `1h30m`) で、1m 以上 24h 以下 |
| `triggers` | 列 | trigger の宣言。宣言順が起動の優先順 (system.md §6) |
| `triggers[].name` | 文字列 | trigger の名前。`[A-Za-z0-9._-]+`。trigger の間で重複しない |
| `triggers[].on` | 文字列 | 作業対象の種類。`issue` |
| `triggers[].when` | 対応表 | 述語。書いた条件はすべて AND で評価する (§2.2) |
| `triggers[].action` | 文字列 | worker に渡す prompt の template。空にできない |

次の項目は、受け持つ slice が決めるまで書けない (書くと未知の key として失敗する)。

- hooks・並列上限・`claude` の起動 command と引数: #78
- attempt の上限・backoff の上限・stall の上限・worker 1 回分の上限時間: #79
- CL 側の trigger (`on: cl`): #80

**YAML の読み方**: 値を書いていない key (`when:` だけの行) は、空の対応表として読む。anchor と alias は辿る。merge key (`<<: *base`) は持たない (`<<` は未知の key として失敗する)。

### 2.2 issue 側の述語 (`on: issue` の `when`)

| key | 型 | 当たる issue |
|---|---|---|
| `labels.all` | 文字列の列 | 列の label をすべて持つ |
| `labels.any` | 文字列の列 | 列の label のどれかを持つ。空の列は書けない |
| `labels.none` | 文字列の列 | 列の label をどれも持たない |
| `assignee` | 文字列 | その login の人が assignee に居る |
| `unassigned` | 真偽 | `true` なら assignee が居ない、`false` なら 1 人以上居る |
| `author` | 文字列 | `collaborator` なら作者が collaborator (repo の owner・organization の member・collaborator)、`non_collaborator` ならそれ以外 |
| `milestone` | 文字列 | その題名の milestone に入っている |
| `blocked` | 真偽 | `true` なら未解決 (open) の依存先 (blocked by) が 1 つ以上ある、`false` なら無い |

- `assignee` と `unassigned` は一緒に書けない
- label と login の綴りは、大文字と小文字を区別せずに比べる (GitHub と同じ)

### 2.3 評価の規則

- issue ごとに trigger を宣言順に評価し、最初に当たった 1 つだけを採る
- 候補は、trigger の宣言順を先に、同じ trigger の中では issue の作成日時の古い順に並べる。作成日時が同じなら番号の小さい順

### 2.4 `$VAR` による間接参照

- 「`$VAR` で書ける」とした項目は、値の全体を `$NAME` (`NAME` は `[A-Za-z_][A-Za-z0-9_]*`) にすると、loop を起動した環境の変数 `NAME` の値に置き換わる
- 変数が未設定か空なら、項目と変数を名指しして失敗する
- 値の一部だけを置き換える書き方 (`acme/$NAME`) は持たない。`$` を含む値は、そのままの綴りとして読む

### 2.5 検査

次の誤りは、項目の位置 (`triggers[0].when.labels.all` の形) を名指しして失敗させる。誤りが複数あれば、すべてを 1 行ずつ出す。

- file が無い・読めない・front matter が無い・YAML として読めない (YAML の誤りの行番号も file の行番号で出す)
- 未知の key (ADR 0009「未知の key は失敗させる」)
- 型の誤り・必須の項目の欠落・未知の値 (`tracker.kind`・`triggers[].on`・`author`)
- 空の文字列 (空の `assignee` や label を「条件なし」と取り違えないため)
- 上の表の各項目の制約 (`polling.interval` の範囲・trigger の名前の綴りと重複・`assignee` と `unassigned` の併記・空の `labels.any`・空の `action`)
- `$VAR` の未設定と、`tracker.token` に値そのものを書いたこと
- 同じ key を 1 つの対応表に 2 回書いたこと

## 3. exit code

| exit | 意味 |
|---|---|
| 0 | 成功 |
| 1 | 観測できなかった (gh を起動できない・rate limit・読み切れない・その他の gh の失敗) / 想定外の失敗 |
| 2 | 引数の誤り / workflow 定義の誤り (§2.5) / issue 置き場が見えない (綴りの誤りか、権限が無い) |
| 3 | 同じ scope key の loop が走っている (§6) |
| 4 | gh の認証が通らない |

- 観測の失敗は、issue 置き場の部品が 認証 / 見えない / 読み切れない / rate limit に分けて返す (system.md §13)
  - 読み切れない: 1 往復で読む件数の上限を超えた (issue の label・assignee・依存先が 100 件を超えた)。切り詰めた像から候補を出さない

## 4. log.jsonl

未定 (#78 で決める)。今の loop は log を書かない。

## 5. 試運転 (`loop --dry-run`)

```
claude-dispatcher loop --dry-run [<workflow の path>]
```

workflow 定義を読んで検査し、snapshot を作って trigger を評価し、起動するはずの作業対象を示して終わる。**何も書かない** (state dir を作らず、lock も取らない)。

成功したら、候補を 1 件 1 行で、評価の順 (§2.3) に stdout へ出す。候補が無ければ何も出さない。

```
implement	issue	#42	ログインの失敗を記録する
implement	issue	#43	設定の読み込みを速くする
```

- 列はタブで区切る: trigger の名前・作業対象の種類・`#<番号>`・題名
- 題名の制御文字 (タブ・改行・ESC など) は空白に置き換える

失敗したら、stdout に何も出さず、stderr に理由を出して §3 の exit code で終わる。workflow 定義の誤りは 1 件 1 行で出す。

## 6. `loop`

```
claude-dispatcher loop [<workflow の path>]
```

実装 repo の clone を cwd にして撃つ。

**起動時の検査** (落ちたら stderr に理由を出して終わる):

| 検査 | exit |
|---|---|
| 引数の数と flag | 2 |
| workflow 定義を読めて、検査に通る (§2.5) | 2 |
| gh を解決できる (PATH と、よく使われる置き場) | 1 |
| state dir を作れる | 1 |
| 同じ scope key の loop が走っていない (`loop.lock` を取れる) | 3 |

- 同じ issue 置き場の 2 本目の loop は、clone や workflow 定義の path が違っても起動時に止まる
- lock は loop の生存期間だけ持つ。loop が死ねば外れる

**tick**: 起動直後に 1 回撃ち、以後は tick の終了から `polling.interval` だけ待って次を撃つ。tick ごとに workflow 定義を読み直す。

- 読み直した workflow 定義が検査に落ちたら、その tick は何も起動しない (#74 Q38)。周期は、最後に検査に通った版のものを使う
- 読み直した workflow 定義の scope key が起動時と違えば、その tick は何も起動しない (lock は起動時の scope key で取っている)

**出力** (#81 で loop の画面に作り直す。今の形は仮): stdout に 1 行ずつ追記する。log.jsonl (§4) を #78 で入れるまでは、これが唯一の出力で、stdout の読み手が消えた後の tick は事後に読めない。

```
<時刻> loop を始めた: scope <scope key> · state dir <path> · workflow <path>
<時刻> tick ok · 候補 2: implement issue #42, implement issue #43
<時刻> tick error · <理由>
<時刻> loop を止めた (停止要求 SIGINT)
```

- tick の行の候補は、試運転 (§5) と同じ順。候補が無ければ `tick ok · 候補 0`
- 今の loop は、候補に worker を起動しない (#78 で入れる)

**停止**: SIGINT / SIGTERM / SIGHUP を同じに扱う。tick の合間なら直ちに、tick の実行中ならその tick を終えてから止まる。

exit: 0 = 停止要求で止まった / 1・2・3 = 起動時の検査 (上表)。

## 7. `status` / `paths` / `setup` / `doctor`

未定。`status` と `paths` は #81、`setup` と `doctor` は #82 で作り直す。今の binary はこれらの subcommand を持たない。

## 8. `--version`

```
claude-dispatcher --version
```

binary の版と commit を stdout に 1 行で出す。workflow 定義も state dir も読まないので、導入が壊れていても撃てる (不具合の報告に貼る)。

```
claude-dispatcher v0.3.0 (<commit の hash>)
```

- 版: Releases の binary は GoReleaser が埋めた tag (`v0.3.0`)。`go install <module>@<版>` の binary は module の版 (同じ `v0.3.0`)。git の作業ツリーでの `go build` は Go が VCS から刻む pseudo-version (直近の tag の次の patch の `-0.<日時>-<hash>`。例: `v0.1.1-0.20260927163012-aac3e1c06160`、未 commit の変更があれば `+dirty`)。VCS の情報が無い build (`go run`・`-buildvcs=false` 等) は `(devel)`
- commit: Releases の binary は tag の commit。git の作業ツリーでの `go build` は build 情報の `vcs.revision`。`go install <module>@<版>` の binary は module cache から build するので commit を持たず `unknown`。VCS の情報が無い build も `unknown`

exit: 0。
