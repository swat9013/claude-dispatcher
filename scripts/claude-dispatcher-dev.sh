#!/usr/bin/env bash
set -euo pipefail

# この script がある checkout の claude-dispatcher を build し直してから、渡した引数で実行する。
# 開発中のコードを、実際の project を相手にリリース前に試すためのもの。
#
# binary の basename は claude-dispatcher のままにし、置き場の dir で Homebrew 版と区別する。status は argv[0] の
# basename で loop / tick の process を見分ける (internal/status/status.go の dispatcherRun) ので、名前を変えると
# 走っている dev の loop を数えない。
# 走っている dev の loop の binary を書き換えないよう、一時 file に build してから rename で入れ替える。

root=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
bin="$root/dist/dev/claude-dispatcher"
mkdir -p "$(dirname "$bin")"
go build -C "$root" -o "$bin.tmp.$$" ./cmd/claude-dispatcher
mv -f "$bin.tmp.$$" "$bin"
exec "$bin" "$@"
