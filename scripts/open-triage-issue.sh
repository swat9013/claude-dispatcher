#!/usr/bin/env bash
set -euo pipefail

# 定期の検査 (.github/workflows/govulncheck.yml・required-checks.yml) が検出を triage 待ちの issue にする。
# 題名の完全一致で open な issue があれば起こさず、無ければ前置きと報告 (先頭の 60000 byte) を本文に
# needs-triage の label で起こす。gh が失敗したら非 0 で終わる。run を落とすのは呼び出し側の workflow

title=${1:?usage: $0 <題名> <前置きの file> <報告の file>}
prelude=${2:?usage: $0 <題名> <前置きの file> <報告の file>}
report=${3:?usage: $0 <題名> <前置きの file> <報告の file>}

repo=${GITHUB_REPOSITORY:-$(gh repo view --json nameWithOwner --jq .nameWithOwner)}

# --search は曖昧に一致するので、題名の完全一致で同じ issue が open かを見る
existing=$(TITLE=$title gh issue list --repo "$repo" --state open --search "\"$title\" in:title" --json number,title --jq 'map(select(.title == env.TITLE)) | .[0].number // empty')
if [ -n "$existing" ]; then
  echo "open な #$existing があるので issue を起こさない"
  exit 0
fi

# issue 本文の上限 (65536 文字) に収める
limit=60000
body=$(mktemp)
trap 'rm -f "$body"' EXIT
{
  cat "$prelude"
  echo
  echo '```'
  # 末尾に改行が無い報告でも、閉じの ``` を行頭に置く
  printf '%s\n' "$(head -c "$limit" "$report")"
  echo '```'
  if [ "$(wc -c < "$report")" -gt "$limit" ]; then
    echo
    echo "出力が長いので先頭の $limit byte で切った。全文は run の log にある"
  fi
} > "$body"
gh issue create --repo "$repo" --title "$title" --label needs-triage --body-file "$body"
