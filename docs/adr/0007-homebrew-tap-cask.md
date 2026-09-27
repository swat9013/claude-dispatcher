# ADR 0007: Homebrew tap `swat9013/tap` の cask を配布経路に加える

- Status: Proposed (quarantine と依存の 2 論点が未決。人が CL で確定したら、merge より前に本文の「未決」を決定へ書き換えて Accepted にする。Proposed のまま merge しない)
- Date: 2026-09-27
- ADR 0003 のうち、配布を `go install` と GitHub Releases に限っていた部分に経路を 1 つ足す (binary に契約を埋め込む決定と plugin の解決は変えない)

## Context

ADR 0003 は CLI を Go の単一 binary にし、`go install` と GoReleaser で GitHub Releases に置く binary で配ると決めた。macOS の利用者の多くは CLI を Homebrew で入れて `brew upgrade` で更新する。Homebrew 以外の経路では、更新のたびに Releases から取り直すか `go install` を撃ち直すことになる。

tap (Homebrew の第三者 repository) として `swat9013/homebrew-tap` を用意した。tag の push で GitHub Actions 上の GoReleaser が tap の定義を更新する形にすれば、release の手順を増やさずに Homebrew からも入れられる。

決める論点は 4 つある。

1. formula と cask のどちらで配るか
2. Linux を対象に含めるか
3. 署名していない binary に macOS が付ける quarantine (Gatekeeper の検査対象にする拡張属性 `com.apple.quarantine`) をどう扱うか
4. 依存 CLI (gh / claude) を `depends_on` に載せるか

各論点は GoReleaser と Homebrew の一次情報で確かめた。出典は末尾に置く (GoReleaser は v2.18.2、Homebrew は 7.0.x の時点)。

## Decision

**tap `swat9013/tap` に cask `claude-dispatcher` を置き、GoReleaser の `homebrew_casks` で tag ごとに更新する。macOS と Linux の両方を対象にする。** quarantine の扱いと依存の宣言は一次情報だけでは一方に決まらないので、下の「未決」に選択肢を並べ、人が確定する。

### 1. formula ではなく cask で配る

- GoReleaser は formula を生成する `brews` を v2.10 で deprecated にし、v2.16 で正式に deprecated とした。代わりが `homebrew_casks` である [G1][G2]
- deprecation の理由は、ビルド済みの binary を入れる formula は Linuxbrew のために取っていた回避策で、今は cask を使うべきだから [G2]
- 新しく足す経路を deprecated な仕組みの上に作る理由は無い

### 2. Linux を対象に含める

- Homebrew 5.0.0 は cask の Linux 対応を改善した [H4]。Cask Cookbook は `binary` の artifact を macOS と Linux の両方で使えるとし、Linux 向けの checksum の書き方 (`arm64_linux:` / `x86_64_linux:`) も定めている [H1]
- GoReleaser v2.18.2 の `homebrew_casks` は、archive から `on_macos` と `on_linux` の両方の節を生成する [G3]
- 既存の配布は macOS と Linux の両方に binary を出している (ADR 0003)。Homebrew の経路だけ Linux を外す理由は無い
- quarantine は macOS の機構なので、論点 3 の決め方は Linux に効かない

### 3. quarantine の扱い: 未決

事実:

- Homebrew は cask の download に quarantine 属性を付け、Gatekeeper の検査をすり抜けさせない [H2]
- 本 repo の binary は署名していない (README)。GoReleaser の docs は、署名していない binary の cask は `xattr` で quarantine を外さないと「damaged and cannot be opened」で起動できないことがあるとしている [G1]
- Homebrew は Gatekeeper の迂回を容易に提供しない方針で、`--no-quarantine` を 5.0.0 で deprecated にした [H4]。Gatekeeper の検査に落ちる cask の disable (2026 年 9 月) は `Homebrew/homebrew-cask` が対象で [H4][H5]、第三者 tap は対象外

選択肢:

| 案 | 中身 | 利点 | 欠点 |
|---|---|---|---|
| A. GoReleaser の hook で外す | `homebrew_casks.hooks.post.install` に `xattr -dr com.apple.quarantine` を書く (GoReleaser docs の例そのまま) [G1] | 設定が docs どおりで、利用者は何もしなくてよい | v2.18.2 はこれを Ruby の `postflight do` として生成する [G3]。Homebrew 7.0.0 は第三者 tap の legacy flight block を deprecated にし、警告を出すのは 2027-12-11 まで [H3]。それ以降に動く保証が無い。GoReleaser 自身も、Apple が予告なくこの迂回を塞ぎうると注意している [G1] |
| B. `custom_block` で `postflight_steps` を書く | Homebrew 7 系の宣言的な steps DSL の `run "/usr/bin/xattr", …` を `on_macos` で囲む [H1] | 非推奨でない形で、A と同じく利用者は何もしなくてよい | **未検証**: steps が使う `{{staged_path}}` と GoReleaser の template の区切りが衝突する。`custom_block` で意図どおりに出力できるかは、GoReleaser の snapshot で確かめる必要がある。Apple が迂回を塞ぎうる点は A と同じ |
| C. 署名と公証 | GoReleaser の sign / notarize で Developer ID 署名と公証を行う [G1] | 迂回が要らず、Homebrew の方針とも Apple の推奨とも合う。Releases の binary も同時に直る | Apple Developer Program の年額が掛かり、証明書と credential を Actions の secret として人が管理する |
| D. caveats で案内する | cask の `caveats` に、利用者が撃つ `xattr` のコマンドを書く [G1] | cask 側は迂回をしない | 初回の起動が失敗し、利用者が手で直す。`brew upgrade` のたびに付き直すかは**未検証** |

### 4. 依存の宣言: 未決

事実:

- cask は `depends_on formula:` と `depends_on cask:` の両方を宣言できる [H1]。GoReleaser の `homebrew_casks.dependencies` も両方を出力する [G1][G3]
- gh は `homebrew-core` の formula `gh` として在る [H6]
- claude は `Homebrew/homebrew-cask` の cask `claude-code` として在る [H7]。この cask は `conflicts_with cask: "claude-code@latest"` を宣言している [H7]
- plugin `swat-skills` は Homebrew で配られていないので、どの案でも依存に載らない。README の「前提」と `doctor` の検査は、どの案でも残る

選択肢:

| 案 | 利点 | 欠点 |
|---|---|---|
| a. どちらも載せない | 3 つの配布経路で前提が揃う。Homebrew 以外で入れた gh / claude と二重にならない | 利用者が gh と claude を自分で入れる。欠けていれば `doctor` で気づく |
| b. gh だけ載せる | Homebrew の利用者は gh を別に入れなくてよい | 前提が経路ごとに食い違う。apt や mise で入れた gh と二重になり、どちらが PATH で先に引かれるかが環境次第になる |
| c. gh と claude を載せる | 前提のうち CLI が揃う | b の欠点に加え、`claude-code@latest` を入れている利用者は conflict で install できない可能性がある (**未検証**: conflict 時の実際の挙動)。Homebrew 以外で入れた claude と二重になる |

## Consequences

### 良い影響

- Homebrew の利用者は `brew install` と `brew upgrade` で入れて更新できる
- tag の push だけで Releases と tap が同時に更新され、release の手順は増えない

### 悪い影響 / 制約

- tag から tap へ push するには、Actions の `GITHUB_TOKEN` とは別に、tap に書ける token が要る [G1]。人が発行して secret に登録し、失効前に更新する
- Homebrew 6.0.0 から、第三者 tap の中身を読むには明示の trust が要る [H3][H8]。完全修飾名 (`brew install --cask swat9013/tap/claude-dispatcher`) で入れると、その item だけが trust される [H8]。install の手順はこの形で書く。`--cask` を付けずに完全修飾名で cask に解決されるかは**未検証**
- Homebrew は第三者 tap を support の対象外にしている [H2]。tap の不具合は本 repo が引き受ける
- loop は起動した時点の binary で回り続ける (ADR 0006)。`brew upgrade` の後は撃ち直すまで旧版のまま動く。これは他の経路と変わらない

## Alternatives considered

### formula (`brews`) で配る

- 却下理由: GoReleaser が deprecated とした仕組みで、deprecated な option は次の major 版で消えうる [G2]。Homebrew が quarantine を付けると明記しているのは cask の download だけ [H2] なので、formula なら論点 3 が起きないかもしれない (**未検証**)。それでも、なくなる予定の仕組みに新しい経路を載せる理由にはならない

### macOS だけを対象にする

- 却下理由: Homebrew と GoReleaser のどちらも Linux の cask を扱える (論点 2)。既存の配布が Linux を含むので、経路ごとに対象 OS を変える理由が無い

## 出典

一次情報は GitHub 上のソースで読んだ。公開 URL と、読んだ ref を並べる。

- [G1] GoReleaser「Homebrew Casks」 <https://goreleaser.com/customization/publish/homebrew_casks/> — `goreleaser/goreleaser@v2.18.2:www/content/customization/publish/homebrew_casks.md` (Signing and Notarizing / GitHub Actions / dependencies / hooks の節)
- [G2] GoReleaser「Deprecation notices」の `brews` <https://goreleaser.com/resources/deprecations/#brews> — `goreleaser/goreleaser@v2.18.2:www/content/resources/deprecations.md` (`brews` の項と、冒頭の「Deprecated options are only removed on major versions of GoReleaser.」)
- [G3] GoReleaser の cask 生成の golden file — `goreleaser/goreleaser@v2.18.2:internal/pipe/cask/testdata/TestFullCask.rb.golden` と `TestFullPipe/hooks_templated.rb.golden` (`on_linux` の節・`depends_on` の出力・hook の `postflight do` への変換)
- [H1] Homebrew「Cask Cookbook」 <https://docs.brew.sh/Cask-Cookbook> — `Homebrew/brew@ce46735f:docs/Cask-Cookbook.md` (OS ごとの artifact・`depends_on`・`*flight_steps` の節)
- [H2] Homebrew「Homebrew Security and Supply Chain」 <https://docs.brew.sh/Homebrew-Security-and-Supply-Chain> — `Homebrew/brew@ce46735f:docs/Homebrew-Security-and-Supply-Chain.md` (cask の信頼モデルと quarantine、第三者 tap が unsupported であること)
- [H3] Homebrew 6.0.0 / 7.0.0 の release note <https://brew.sh/2026/06/11/homebrew-6.0.0/> <https://brew.sh/2026/09/13/homebrew-7.0.0/> — `Homebrew/brew.sh@bad4278f:_posts/2026-06-11-homebrew-6.0.0.md`・`_posts/2026-09-13-homebrew-7.0.0.md` (tap trust の導入、第三者 tap の legacy flight block が 2027-12-11 まで警告であること)
- [H4] Homebrew 5.0.0 の release note <https://brew.sh/2025/11/12/homebrew-5.0.0/> — `Homebrew/brew.sh@bad4278f:_posts/2025-11-12-homebrew-5.0.0.md` (`--no-quarantine` の deprecation、Gatekeeper に落ちる `Homebrew/homebrew-cask` の cask の disable 予定、cask の Linux 対応)
- [H5] Homebrew「Acceptable Casks」 <https://docs.brew.sh/Acceptable-Casks> — `Homebrew/brew@ce46735f:docs/Acceptable-Casks.md` (`Homebrew/homebrew-cask` に受け入れる条件としての Gatekeeper の要件。Cask Cookbook [H1] も「Official macOS casks must also meet the Gatekeeper requirement」とする)
- [H6] `Homebrew/homebrew-core:Formula/g/gh.rb`
- [H7] `Homebrew/homebrew-cask@71d2392a:Casks/c/claude-code.rb`
- [H8] Homebrew「Tap Trust」 <https://docs.brew.sh/Tap-Trust> — `Homebrew/brew@ce46735f:docs/Tap-Trust.md`
