# Contributing

この repo で作業する agent (worker) と人間に向けた開発フローの宣言。

## セットアップ

- 前提ツールは [README の「前提」](README.md#前提) のとおり (Claude Code / plugin `swat-skills@swat9013` / `gh`)
- 加えて `pre-commit` (4.4.0 以上) と `shellcheck` を入れ、clone ごとに 1 回 `pre-commit install` を撃つ (commit 時と push 前の hook が両方入る)。gitleaks と actionlint は pre-commit が hook 環境として build するので別途の導入は要らない (初回の build に network が要る)
  - Go は手元の版が [go.mod](go.mod) の `toolchain` 行より古くても、`go` コマンドがその版を取ってきて使う (`GOTOOLCHAIN` の既定の `auto`)。hook も `go` コマンド経由で撃つので、手元と CI で同じ版の Go が動く

## branch・worktree 運用

- 作業は issue 単位で行い、main から `worktree-issue-<n>` (`<n>` は issue 番号) の branch を切る
  - dispatcher はこの綴りの head branch (fork でない、この repo 自身の branch) で worker 由来の CL を見分け、issue と紐づける ([docs/design/system.md](docs/design/system.md) §4)。綴りを崩すと二重着手の防止が効かない
- ファイルの変更 (コード・docs を問わない) は、main の checkout で直接行わず、worktree を作ってその中で行う。main の checkout に未 commit の変更を残すと、別の作業の差分と混ざって PR に切り出せなくなる
  - 対話で動かす Claude Code では `EnterWorktree` に name `issue-<n>` を渡す。branch 名は `worktree-` が前置されて `worktree-issue-<n>` になる
  - dispatcher が spawn した worker は、spawn prompt の「作業ツリー」の手順に従う (正本は [internal/contract/orchestrator.md](internal/contract/orchestrator.md))。cwd を clone root に置いたまま `git worktree add` で作る点が上と違う
- 実装が終わったら、worktree の branch を push して PR を作る
- main へは PR 経由でだけ入れる

## gate (品質チェック)

検査の置き場は次の 2 点で決めている。

- **CI に置く検査は、同じものを手元 (pre-commit) でも撃てるようにする**。dispatcher は CL の CI が落ちると worker を再入させ、worker は手元で再現できない失敗を推測で直さず人へ返す ([docs/design/system.md](docs/design/system.md) §6 の `ci` 条件と §10)。CI にしか無い検査が落ちると、worker は毎回人へ返すことになる
- **CL の変更と無関係に赤くなりうる検査は、PR の gate にしない**。network 上の脆弱性 DB を引く govulncheck がこれに当たる。gate にすると、新しい脆弱性が公開されたときに、開いている worker の CL が一斉に `ci` 再入を起こす

検査と撃たれる場所:

- commit 時: `pre-commit install` 済みなら、次の hook ([.pre-commit-config.yaml](.pre-commit-config.yaml)) が自動で走り、落ちると commit を止める。全 file に手で撃つなら `pre-commit run --all-files` (対象は tracked の file だけ。新しい file は stage してから撃つ)
  - gitleaks: staged の内容に秘匿情報があれば落ちる
  - gofmt (`go fmt ./...`): 整形し直した file があれば落ちる。file は書き換わった後なので、見直して stage し直す
  - go mod tidy (`go mod tidy -diff`): go.mod / go.sum に差分が出れば落ちる
  - actionlint: workflow の静的検査。PATH に `shellcheck` があれば `run:` の script も検査する。CI の ubuntu の runner には `shellcheck` があるので、手元に無いと CI でだけ落ちる
- push 前: `go vet ./...` と `go test -race ./...` (データ競合の検出付き) が通ること。`pre-commit install` 済みなら push 時に hook が走り、落ちると push を止める。`-race` を付けても全体で 30 秒ほど (付けないと 27 秒ほど) なので、push 前にも付けている
  - `test/blackbox` は binary を build して外から撃つ black-box テスト (外から観測できる契約の検査。正本は [docs/design/formats.md](docs/design/formats.md))。外部 CLI は stub に差し替えるので、network も認証も要らず、マシンに在る実物にも届かない。例外は 2 つ: Homebrew の prefix 等に gh / claude の実物があるマシン (macOS の runner の gh 等) では、「解決できない」を確かめるテストが skip される。`--cron-env` の試運転は cron と同じ `/usr/bin:/bin` から始まり、そこに在る実物 (ubuntu の runner の gh 等) に届きうる。テストが stub を引けるのは、claude が `/usr/bin:/bin` に無いので自己解決が働き、stub の置き場 (`~/.local/bin`) を前に足すから
- PR と main への push: [.github/workflows/ci.yml](.github/workflows/ci.yml) が検査の本体 [.github/workflows/checks.yml](.github/workflows/checks.yml) を呼ぶ。Go は setup-go が go.mod の `toolchain` 行の版を入れる
  - `test`: `go vet ./...` と `go test -race ./...` を ubuntu と macOS の runner (配布対象の OS) で撃つ。skip したテストは理由とともに job の log の最後に並ぶ
  - `goreleaser-check`: `goreleaser check` で [.goreleaser.yaml](.goreleaser.yaml) を検査する
  - `pre-commit`: commit 時の hook を `pre-commit run --all-files` で撃つ。PR では加えて、PR の commit (base..head。merge commit と、merge で入った main 側の commit を除く) を gitleaks で 1 つずつ検査する。merge commit を許しているので、途中の commit で入れて後の commit で消した秘匿情報も main の履歴に残るため。手元での再現は `gitleaks git --redact --log-opts="--no-merges --first-parent origin/main..HEAD"`
  - 同じ PR に push が重なると、古い run を中止する。main への push の run は中止しない
  - PR に付く check 名: `checks / test (ubuntu-latest)`・`checks / test (macos-latest)`・`checks / goreleaser-check`・`checks / pre-commit`
- 定期: [.github/workflows/govulncheck.yml](.github/workflows/govulncheck.yml) が、週 1 回 (月曜 0:00 UTC)・main への push・手動 (`workflow_dispatch`) で、依存と標準ライブラリの脆弱性を `go tool govulncheck ./...` で検査する。検出したら `needs-triage` の issue を 1 件起こし (同じ題名の open な issue があれば起こさない)、run を落とす。手元での再現は同じコマンド (版は go.mod の `tool` 行で固定している)
- 版の更新: [.github/dependabot.yml](.github/dependabot.yml) が週 1 回、GitHub Actions・Go の依存 (govulncheck を含む)・pre-commit の hook の `rev` を、それぞれ 1 本の PR で上げる。auto-merge はしない。CI の gitleaks の版は .pre-commit-config.yaml の `rev` から読むので、hook と一緒に上がる。Dependabot の対象外で、手で上げるもの:
  - go.mod の `go` 行 (サポート中の最も古い Go の版) と `toolchain` 行 (build に使う版。Dependabot が上げるかは確かめていない)
  - goreleaser-action に渡す `version:` (checks.yml と release.yml の 2 箇所)
  - checks.yml の `pre-commit` job で入れる pre-commit の版
- release: tag (`v*`) は main の commit に打つ。push すると [.github/workflows/release.yml](.github/workflows/release.yml) が、上の CI と同じ検査の本体 (checks.yml) を tag の commit で通し、tag の commit が main に在ることを確かめてから GoReleaser で GitHub Releases に binary を出し、tap `swat9013/homebrew-tap` の cask を更新する ([.goreleaser.yaml](.goreleaser.yaml))
  - prerelease の tag (`v1.2.0-rc.1` 等) は、Release を prerelease として出し、tap を更新しない
  - tap への push には Actions secret `HOMEBREW_TAP_GITHUB_TOKEN` を使う。Actions の `GITHUB_TOKEN` は別 repo に push できないので、`swat9013/homebrew-tap` の Contents だけに read/write を持つ fine-grained token を人が発行して登録する
  - token には失効日がある。失効したら同じ権限で発行し直し、同じ名前の secret を上書きする。secret が未登録か失効していれば、release workflow は publish の前に止まる

## commit・PR 規約

- commit message と PR 本文は日本語で書く。1 行目は変更の要約 (「〜する」の形)、本文に変更点を箇条書きする
- PR 本文には、対応する issue への closing reference (`Closes #<n>`) を書く
- PR 本文には、issue の確定事項に無く自律判断で決めたことと、残るリスクを節を分けて書く
