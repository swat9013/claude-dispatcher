# Contributing

この repo で作業する agent (worker) と人間に向けた開発フローの宣言。

## セットアップ

- 前提ツールは [README の「前提」](README.md#前提) のとおり
- この repo の [WORKFLOW.md](WORKFLOW.md) の action は Claude Code plugin `swat-skills@swat9013` ([swat9013/claude-skills](https://github.com/swat9013/claude-skills)) の playbook を呼ぶので、この repo で loop を回すなら入れる (user scope)
  ```
  /plugin marketplace add swat9013/claude-skills
  /plugin install swat-skills@swat9013
  ```
- 加えて `golangci-lint` を CI と同じ版 (.github/workflows/checks.yml の `lint` job の `version:`) で入れる。公式の install script に版を渡して binary を入れる (手順は <https://golangci-lint.run/docs/welcome/install/>)。brew などの package manager は版を選べず、CI と版がずれるので使わない。`go install` と go.mod の `tool` directive は golangci-lint の公式が動作を保証していない
  - push 前の hook は、手元の版が checks.yml の `version:` と違えば lint を撃たずに落とし、入れ直しのコマンドを出す。Renovate が golangci-lint の版を上げた PR を merge した後は、入れ直すまで push が止まる (worker の workspace を含む)
- 加えて `pre-commit` (4.4.0 以上) を入れ、clone ごとに 1 回 `pre-commit install` を撃つ (commit 時と push 前の hook が両方入る)。gitleaks・actionlint・shellcheck は pre-commit が hook 環境として build するので別途の導入は要らない (初回の build に network が要る)
  - Go は手元の版が [go.mod](go.mod) の `toolchain` 行より古くても、`go` コマンドがその版を取ってきて使う (`GOTOOLCHAIN` の既定の `auto`)。hook も `go` コマンド経由で撃つので、手元と CI で同じ版の Go が動く

## 開発中の claude-dispatcher を試す

`scripts/claude-dispatcher-dev.sh` は、script がある checkout の claude-dispatcher を build し直してから、渡した引数で実行する。リリースを待たずに、手元の変更を実際の project で試せる。

```sh
alias claude-dispatcher-dev=~/src/claude-dispatcher/scripts/claude-dispatcher-dev.sh   # worktree で試すときは、その worktree の script を指す
claude-dispatcher-dev --version   # 版に checkout の commit が出る (未 commit の変更があれば +dirty)
claude-dispatcher-dev loop --dry-run   # cwd の WORKFLOW.md を読み、起動するはずの作業対象を示す
claude-dispatcher-dev loop
```

- **Homebrew 版と区別する**: 素の `claude-dispatcher` は PATH 上の版 (Homebrew 等) を呼ぶ。build した binary は、その checkout の `dist/dev/claude-dispatcher` に置く
- **走っている dev の loop は止まらない**: build は一時 file に書いてから rename で入れ替えるので、dev の loop を回したまま別の端末で撃ち直せる。loop は起動した時点の binary で回り続けるので、変更を効かせるには loop を撃ち直す
- **状態は Homebrew 版と共有する**: state dir ([docs/design/formats.md](docs/design/formats.md) §1) は Homebrew 版と同じものを読み書きする。同じ issue 置き場の loop は 1 本しか動かせない (`loop.lock`) ので、Homebrew 版の loop を止めてから dev の loop を撃つ。分けたいときは `XDG_STATE_HOME` を上書きして撃つ
- **tracker と CL host は本物**: 試運転でない loop は、実際の issue 置き場を読んで worker を起動する。試すなら使い捨ての issue に trigger の label を付けるか、`loop --dry-run` で止める

## branch・worktree 運用

- 作業は issue 単位で行い、main から `worktree-issue-<n>` (`<n>` は issue 番号) の branch を切る
  - この repo の workflow 定義 ([WORKFLOW.md](WORKFLOW.md)) は、CL 側の trigger (conflict・review・CI の手直し) をこの綴りの、この repo 自身の head branch に絞る。綴りを崩した CL には手直しの worker が起動しない。fork の CL にも worker を送らない ([docs/design/system.md](docs/design/system.md) §6)
  - 人が自分で開いた CL もこの綴りなので、draft でなく `ready-for-human` の label も無ければ、手直しの worker がその head branch へ push しうる。自分で進める CL は draft にしておくか、`ready-for-human` の label を付ける
- ファイルの変更 (コード・docs を問わない) は、main の checkout で直接行わず、worktree を作ってその中で行う。main の checkout に未 commit の変更を残すと、別の作業の差分と混ざって PR に切り出せなくなる
  - 対話で動かす Claude Code では `EnterWorktree` に name `issue-<n>` を渡す。branch 名は `worktree-` が前置されて `worktree-issue-<n>` になる
  - dispatcher が起動する worker は、WORKFLOW.md の hooks が clone から作った workspace (worktree。`.claude-dispatcher/workspaces/` の下) の中で作業する ([docs/design/system.md](docs/design/system.md) §7)
- 対話の session が issue の作業を始め、途中から worker に引き継いでよい。`worktree-issue-<n>` を push し、worktree を消してから `ready-for-agent` を付ける。worker は remote の同じ branch を取り込んで続ける ([WORKFLOW.md](WORKFLOW.md) の `implement`)
- 実装が終わったら、worktree の branch を push して PR を作る
- main へは PR 経由でだけ入れる (ruleset が強制する。下の「gate」の「main の保護」)

## gate (品質チェック)

検査の置き場は次の 2 点で決めている。

- **CI に置く検査は、同じものを手元 (pre-commit) でも撃てるようにする**。dispatcher は CL の CI が落ちると `fix-ci` の trigger で worker を起動し、worker は手元で再現できない失敗を推測で直さず人へ返す ([WORKFLOW.md](WORKFLOW.md) の共通 prompt)。CI にしか無い検査が落ちると、worker は毎回人へ返すことになる
- **CL の変更と無関係に赤くなりうる検査は、PR の gate にしない**。network 上の脆弱性 DB を引く govulncheck がこれに当たる。gate にすると、新しい脆弱性が公開されたときに、開いている worker の CL に一斉に `fix-ci` の trigger が当たる

検査と撃たれる場所:

- commit 時: `pre-commit install` 済みなら、次の hook ([.pre-commit-config.yaml](.pre-commit-config.yaml)) が自動で走り、落ちると commit を止める。全 file に手で撃つなら `pre-commit run --all-files` (対象は tracked の file だけ。新しい file は stage してから撃つ)
  - gitleaks: staged の内容に秘匿情報があれば落ちる
  - gofmt (`go fmt ./...`): 整形し直した file があれば落ちる。file は書き換わった後なので、見直して stage し直す
  - go mod tidy (`go mod tidy -diff`): go.mod / go.sum に差分が出れば落ちる
  - actionlint: workflow の静的検査。`run:` の script は、hook 環境に版を固定して入れた shellcheck で検査する
  - repocheck (`go run ./scripts/repocheck files`): 同じ値を 2 箇所以上に書いている設定の一致を検査する。GoReleaser の版 (checks.yml と release.yml) と、下の「PR に付く check 名」の列挙 (checks.yml の job 名と matrix から組み立てた名前) がずれていれば落ちる。network は使わない
- push 前: `go vet ./...`・`golangci-lint run ./...` (設定は [.golangci.yml](.golangci.yml)。binary の入れ方は上の「セットアップ」) ・`go test -race ./...` (データ競合の検出付き) が通ること。加えて gitleaks が、push する commit のうち remote に無いもの (merge commit を除く) を 1 つずつ検査する。CI の `pre-commit` job は同じ hook を PR の commit 範囲に撃つ。`pre-commit install` 済みなら push 時に hook が走り、落ちると push を止める
  - golangci-lint の既定セットも govet を含むが、`go vet ./...` も撃つ (理由は .pre-commit-config.yaml の go-vet の hook のコメント)
  - `test/blackbox` は binary を build して外から撃つ black-box テスト (外から観測できる契約の検査。正本は [docs/design/formats.md](docs/design/formats.md))。外部 CLI は stub に差し替えるので、network も認証も要らず、マシンに在る実物にも届かない。例外: Homebrew の prefix 等に gh / claude の実物があるマシン (macOS の runner の gh 等) では、「解決できない」を確かめるテストが skip される
- PR と main への push: [.github/workflows/ci.yml](.github/workflows/ci.yml) が検査の本体 [.github/workflows/checks.yml](.github/workflows/checks.yml) を呼ぶ。Go は setup-go が go.mod の `toolchain` 行の版を入れる
  - `test`: `go vet ./...` と `go test -race ./...` を ubuntu と macOS の runner (配布対象の OS) で撃つ。skip したテストは理由とともに job の log の最後に並ぶ
  - `lint`: `golangci-lint run ./...` を ubuntu の runner だけで撃つ (理由は checks.yml の `lint` job のコメント)
  - `goreleaser-check`: `goreleaser check` で [.goreleaser.yaml](.goreleaser.yaml) を検査する
  - `pre-commit`: commit 時の hook を `pre-commit run --all-files` で撃つ。PR では加えて、push 前の gitleaks の hook を PR の commit 範囲 (base..head。merge commit を除く) に撃つ。merge commit を許しているので、途中の commit で入れて後の commit で消した秘匿情報も main の履歴に残るため。merge commit で解いた conflict の中身は検査されない。手元での再現は `pre-commit run --hook-stage pre-push --from-ref origin/main --to-ref HEAD gitleaks` (stack した CL なら `origin/main` を前段の branch にする)
  - 同じ PR に push が重なると、古い run を中止する。main への push の run は中止しない
  - PR に付く check 名: `checks / test (ubuntu-latest)`・`checks / test (macos-latest)`・`checks / lint`・`checks / goreleaser-check`・`checks / pre-commit`。名前は checks.yml の job 名と matrix から決まる。変えたら、この列挙と、下の ruleset の required status checks を一緒に直す。列挙のずれは commit 時の repocheck が、ruleset のずれは下の「定期」の required-checks が拾う。この列挙は repocheck が機械で読む (読み方は scripts/repocheck の listedCheckNames)
- main の保護: repo の ruleset `main` (対象は既定 branch) が、「main へは PR 経由でだけ入れる」と「CI が通った PR だけを merge する」を機械で強制する。設定は repo の管理権限が要るので、worker は変えられない
  - PR を必須にする。review の承認は必須にしない (一人運用で、自分の PR を merge できなくなるため)
  - required status checks は上の 5 つの check 名。報告元を GitHub Actions に限る (同じ名前の check を別の app が報告しても通らない)。govulncheck は PR で走らないので含めない
  - 「merge 前に branch を最新にする」(strict) は無効にする。有効にすると main が進むたびに開いている全 PR の branch 更新が要り、worker の CL ではそれが手直しの trigger の起動や人への返却を招く。main への push で走る CI が事後に検知する
  - bypass は置かない (管理者も main へ直接 push できない)
- 定期: [.github/workflows/govulncheck.yml](.github/workflows/govulncheck.yml) が、週 1 回 (月曜 0:00 UTC)・main への push・手動 (`workflow_dispatch`) で、依存と標準ライブラリの脆弱性を `go tool govulncheck ./...` で検査する。検出したら `needs-triage` の issue を 1 件起こし (同じ題名の open な issue があれば起こさない)、run を落とす。run を落とすのは、issue を起こせなかったとき (権限・API の失敗) にも検出を見落とさないため。手元での再現は同じコマンド (版は go.mod の `tool` 行で固定している)
  - [.github/workflows/required-checks.yml](.github/workflows/required-checks.yml) が、同じ契機 (週 1 回・main への push・手動) で、main の ruleset の required status checks が checks.yml の job から決まる check 名と一致するかを検査する。ruleset は API でしか読めず CL と無関係に変わりうるので、PR では撃たない。ずれていれば govulncheck と同じく `needs-triage` の issue を 1 件起こし、run を落とす。手元での再現は `scripts/check-required-checks.sh` (`gh` の認証が要る)
- 版の更新: 2 つの bot が週 1 回、版を上げる PR を出す。auto-merge はしない。分担の理由は [ADR 0011](docs/adr/0011-renovate-regex-beside-dependabot.md)
  - [.github/dependabot.yml](.github/dependabot.yml): GitHub Actions・Go の依存 (indirect と、govulncheck を含む)・pre-commit の hook の `rev` を、それぞれ 1 本の PR で上げる
  - [renovate.json](renovate.json) (Renovate。manager は `custom.regex` だけ): Dependabot の対象外の次の版を、regex で拾って上げる。版の行の書き方を変えたら、renovate.json の regex も直す
    - go.mod の `toolchain` 行 (build に使う版)
    - goreleaser-action に渡す `version:` (checks.yml と release.yml の 2 箇所。一致は commit 時の repocheck が検査する)
    - checks.yml の `pre-commit` job で入れる pre-commit の版
    - checks.yml の `lint` job で golangci-lint-action に渡す `version:` (上げたら手元の binary も同じ版にする。上の「セットアップ」)
    - .pre-commit-config.yaml の actionlint の `additional_dependencies` に固定した shellcheck (go-shellcheck) の版 (Dependabot が上げるのは hook の `rev`)
  - Renovate は GitHub App [Renovate](https://github.com/apps/renovate) を repo に入れて初めて動く。導入は repo の管理権限を持つ人の作業で、worker はできない。入っているかは repo の Settings の「GitHub Apps」で確かめ、動いているかは `renovate/` で始まる branch の PR が出るかで確かめる
  - 手で見るもの: go.mod の `go` 行 (利用者に求める最低の版。サポート中の最も古い Go の版に合わせる)。依存の go.mod の `go` 行より下げられないので、Dependabot が上げた依存 (govulncheck の `golang.org/x/vuln` とその依存を含む) に引き上げられることがある。Dependabot の PR で `go` 行が動いたら、利用者に求める版が上がってよいかを見てから merge する
- release: tag (`v*`) は main の commit に打つ。push すると [.github/workflows/release.yml](.github/workflows/release.yml) が、上の CI と同じ検査の本体 (checks.yml) を tag の commit で通し、tag の commit が main に在ることを確かめてから GoReleaser で GitHub Releases に binary を出し、tap `swat9013/homebrew-tap` の cask を更新する ([.goreleaser.yaml](.goreleaser.yaml))
  - prerelease の tag (`v1.2.0-rc.1` 等) は、Release を prerelease として出し、tap を更新しない
  - tap への push には Actions secret `HOMEBREW_TAP_GITHUB_TOKEN` を使う。Actions の `GITHUB_TOKEN` は別 repo に push できないので、`swat9013/homebrew-tap` の Contents だけに read/write を持つ fine-grained token を人が発行して登録する
  - token には失効日がある。失効したら同じ権限で発行し直し、同じ名前の secret を上書きする。secret が未登録か失効していれば、release workflow は publish の前に止まる

## commit・PR 規約

- commit message と PR 本文は日本語で書く。1 行目は変更の要約 (「〜する」の形)、本文に変更点を箇条書きする
- PR 本文には、対応する issue への closing reference (`Closes #<n>`) を書く
- PR 本文には、issue の確定事項に無く自律判断で決めたことと、残るリスクを節を分けて書く
