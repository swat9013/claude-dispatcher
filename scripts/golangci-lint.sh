#!/usr/bin/env bash
set -euo pipefail

# push 前の hook。手元の golangci-lint が CI (checks.yml の lint job の version:) と同じ版かを確かめてから撃つ。
# 版が違えば lint を撃たずに落とす (版がずれると、手元と CI で結果が食い違う)。repo の root で撃つ

# 版を取り出す式は、checks.yml から golangci-lint の版を読むほかの箇所と揃える
want=$(awk '/golangci-lint-action@/{f=1} f && /version:/{print $2; exit}' .github/workflows/checks.yml)
if [ -z "$want" ]; then
  echo "golangci-lint の版を .github/workflows/checks.yml の lint job から読めない" >&2
  exit 1
fi
install="curl -sSfL -o /tmp/golangci-lint-install.sh https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh && sh /tmp/golangci-lint-install.sh -b \"\$(go env GOPATH)/bin\" $want"
if ! command -v golangci-lint >/dev/null; then
  echo "golangci-lint が PATH に無い。CI と同じ $want を入れる (CONTRIBUTING.md の「セットアップ」):" >&2
  echo "  $install" >&2
  exit 1
fi
# --short は v を付けずに版だけを出す
have=$(golangci-lint version --short)
if [ "v${have#v}" != "v${want#v}" ]; then
  echo "手元の golangci-lint は $have で、CI (checks.yml の lint job) は ${want}。入れ直す (CONTRIBUTING.md の「セットアップ」):" >&2
  echo "  $install" >&2
  exit 1
fi
exec golangci-lint run ./...
