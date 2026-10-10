#!/usr/bin/env bash
set -euo pipefail

# main の ruleset の required status checks が、checks.yml の job から決まる check 名と一致するかを検査する。
# .github/workflows/required-checks.yml が撃ち、手元でも同じく撃てる (gh の認証が要る)。repo の root で撃つ。
# ずれは 1 件 1 行で標準エラーに出して exit 1、検査そのものの失敗 (API の失敗を含む) は exit 2 で終わる

repo=${GITHUB_REPOSITORY:-$(gh repo view --json nameWithOwner --jq .nameWithOwner)} || exit 2
rules=$(gh api "repos/$repo/rules/branches/main") || exit 2
# go run は子の exit code を 1 に潰すので、build してから撃つ
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
go build -o "$dir/repocheck" ./scripts/repocheck || exit 2
"$dir/repocheck" ruleset <<<"$rules"
