# Contributing

この repo で作業する agent (worker) と人間に向けた開発フローの宣言。

## セットアップ

- 前提ツールは [README の「前提」](README.md#前提) のとおり (Claude Code / plugin `swat-skills@swat9013` / `gh`)
- 加えて `pre-commit` を入れ、clone ごとに 1 回 `pre-commit install` を撃つ。gitleaks は pre-commit が hook 環境として build するので別途の導入は要らない (初回の build に network が要る)

## branch・worktree 運用

- 作業は issue 単位で行い、main から `worktree-issue-<n>` (`<n>` は issue 番号) の branch を切る
  - dispatcher はこの綴りの head branch で worker 由来の CL を見分け、issue と紐づける ([docs/design/system.md](docs/design/system.md) §4)。綴りを崩すと二重着手の防止が効かない
- main へは PR 経由でだけ入れる

## gate (品質チェック)

- commit 時: `pre-commit install` 済みなら、gitleaks が staged の内容を自動で検査し、秘匿情報を検出すると commit を止める (検査対象は staged だけなので、手で走らせるなら stage した後に `pre-commit run`)
- CLI の実装が入るまで、build / test / lint の gate はまだ無い。実装 issue で gate コマンドを足した時点でこの節に追記する

## commit・PR 規約

- commit message と PR 本文は日本語で書く。1 行目は変更の要約 (「〜する」の形)、本文に変更点を箇条書きする
- PR 本文には、対応する issue への closing reference (`Closes #<n>`) を書く
- PR 本文には、issue の確定事項に無く自律判断で決めたことと、残るリスクを節を分けて書く
