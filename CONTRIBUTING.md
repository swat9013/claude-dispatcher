# Contributing

この repo で作業する agent (worker) と人間に向けた開発フローの宣言。

## セットアップ

- 前提ツールは [README の「前提」](README.md#前提) のとおり (Claude Code / plugin `swat-skills@swat9013` / `gh`)
- 加えて `pre-commit` を入れ、clone ごとに 1 回 `pre-commit install` を撃つ (commit 時と push 前の hook が両方入る)。gitleaks は pre-commit が hook 環境として build するので別途の導入は要らない (初回の build に network が要る)

## branch・worktree 運用

- 作業は issue 単位で行い、main から `worktree-issue-<n>` (`<n>` は issue 番号) の branch を切る
  - dispatcher はこの綴りの head branch で worker 由来の CL を見分け、issue と紐づける ([docs/design/system.md](docs/design/system.md) §4)。綴りを崩すと二重着手の防止が効かない
- ファイルの変更 (コード・docs を問わない) は、main の checkout で直接行わず、worktree を作ってその中で行う。main の checkout に未 commit の変更を残すと、別の作業の差分と混ざって PR に切り出せなくなる
  - 対話で動かす Claude Code では `EnterWorktree` に name `issue-<n>` を渡す。branch 名は `worktree-` が前置されて `worktree-issue-<n>` になる
  - dispatcher が spawn した worker は、spawn prompt の「作業ツリー」の手順に従う (正本は [internal/contract/orchestrator.md](internal/contract/orchestrator.md))。cwd を clone root に置いたまま `git worktree add` で作る点が上と違う
- 実装が終わったら、worktree の branch を push して PR を作る
- main へは PR 経由でだけ入れる

## gate (品質チェック)

- commit 時: `pre-commit install` 済みなら、gitleaks が staged の内容を自動で検査し、秘匿情報を検出すると commit を止める (検査対象は staged だけなので、手で走らせるなら stage した後に `pre-commit run`)
- push 前: `go vet ./...` と `go test ./...` が通ること。`pre-commit install` 済みなら push 時に hook が走り、落ちると push を止める
  - `test/blackbox` は binary を build して外から撃つ black-box テスト (外から観測できる契約の検査。正本は [docs/design/formats.md](docs/design/formats.md))。外部 CLI (gh / claude) は stub に差し替えるので、network も認証も要らない
- release: tag (`v*`) は main の commit に打つ。push すると [.github/workflows/release.yml](.github/workflows/release.yml) が、tag の commit が main に在ることと `go vet` を確かめてから GoReleaser で GitHub Releases に binary を出し、tap `swat9013/homebrew-tap` の cask を更新する ([.goreleaser.yaml](.goreleaser.yaml))。テストは release では走らせず、上の push 前の gate が担う (Linux の runner で通すまで。#18)
  - prerelease の tag (`v1.2.0-rc.1` 等) は、Release を prerelease として出し、tap を更新しない
  - tap への push には Actions secret `HOMEBREW_TAP_GITHUB_TOKEN` を使う。Actions の `GITHUB_TOKEN` は別 repo に push できないので、`swat9013/homebrew-tap` の Contents だけに read/write を持つ fine-grained token を人が発行して登録する
  - token には失効日がある。失効したら同じ権限で発行し直し、同じ名前の secret を上書きする。secret が未登録か失効していれば、release workflow は publish の前に止まる

## commit・PR 規約

- commit message と PR 本文は日本語で書く。1 行目は変更の要約 (「〜する」の形)、本文に変更点を箇条書きする
- PR 本文には、対応する issue への closing reference (`Closes #<n>`) を書く
- PR 本文には、issue の確定事項に無く自律判断で決めたことと、残るリスクを節を分けて書く
