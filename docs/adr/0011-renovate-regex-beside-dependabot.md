# ADR 0011: Dependabot の対象外の版を、custom.regex に限った Renovate で上げる

- Status: Accepted
- Date: 2026-10-10

## Context

依存の版は Dependabot ([.github/dependabot.yml](../../.github/dependabot.yml)) が週 1 回上げる。対象は GitHub Actions・Go の依存・pre-commit の hook の `rev` で、次の 5 つの版は Dependabot の対象外だった。

- go.mod の `toolchain` 行 (Dependabot の `gomod` が上げる対象として資料に書かれていない)
- goreleaser-action に渡す GoReleaser の版 (checks.yml と release.yml)
- golangci-lint-action に渡す golangci-lint の版 (checks.yml)
- checks.yml の `pre-commit` job で入れる pre-commit の版
- .pre-commit-config.yaml の actionlint の hook 環境に入れる shellcheck (go-shellcheck) の版

これらは CONTRIBUTING.md の手作業のチェックリストで、Dependabot の PR を merge するときに人が見て上げていた。新しい版 (セキュリティ修正を含む) が出ても、誰も気付けない。

古さの検知は PR の gate に置かない。network 上の版の一覧を引く検査は CL の変更と無関係に赤くなりうる (CONTRIBUTING.md の「gate」の 2 つ目の原則)。

## Decision

**Renovate を入れ、manager を `custom.regex` に限って、上の 5 つの版だけを regex で拾って上げる。Dependabot はそのまま残す。** 設定は [renovate.json](../../renovate.json)。

- `enabledManagers: ["custom.regex"]` にし、Renovate の組み込みの manager (gomod・github-actions・pre-commit 等) を動かさない。Dependabot が上げる版を Renovate も上げると、同じ版の PR が 2 本出る
- go.mod の `toolchain` 行も gomod manager でなく regex で拾う。gomod manager を有効にすると require の更新が Dependabot と二重になる
- schedule は週 1 回 (`schedule:weekly`)、auto-merge はしない、Dependency Dashboard の issue は作らない (`dependencyDashboard: false`)
- 版は workflow と設定 file にリテラルのまま書く (regex と、版を読むほかの箇所が同じ所を読めるように)
- Renovate の PR の branch (`renovate/...`) は、WORKFLOW.md の CL の trigger (head branch が `worktree-issue-<n>`) に当たらないので、worker は起動しない

## Consequences

### 良い影響

- 5 つの版の新しい版が、版を上げた PR として届く。人の作業は PR の CI を見て merge するだけになる
- 版を上げる PR が Dependabot と同じく週 1 回・人の merge で入るので、運用が揃う

### 悪い影響 / 制約

- 版を上げる bot が 2 つになる。どの版をどちらが上げるかは、CONTRIBUTING.md の「版の更新」と renovate.json を見ないと分からない
- Renovate の GitHub App の導入は repo の管理権限を持つ人の作業で、worker はできない
- regex は書式に依存する。版の行の書き方を変えると、Renovate が黙って拾わなくなる
- golangci-lint の版を上げる PR を merge すると、手元の binary を入れ直すまで push 前の hook が落ちる (CONTRIBUTING.md の「セットアップ」)

## Alternatives considered

### 古くなったら落ちる定期 job を自前で置く

- 却下理由: 取得先 4 種 (Go の版・GitHub Releases・PyPI・Go module proxy) を引く script を自前で持つことになり、検知しても版の書き換えは人に残る。Renovate は設定だけで済み、版を上げた PR まで出る

### Dependabot をやめて Renovate に一本化する

- 却下理由: Dependabot で足りている範囲 (Actions・Go の依存・pre-commit の hook) の設定と運用を移し替える費用に見合う利点が無い。足りない 5 つの版だけを Renovate に任せる

### Renovate の組み込み manager (gomod) で toolchain 行を拾う

- 却下理由: gomod manager は require の依存も上げるので、Dependabot の `gomod` と同じ PR が 2 本出る
