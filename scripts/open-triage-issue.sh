#!/usr/bin/env bash
set -euo pipefail

# 定期の検査 (.github/workflows/govulncheck.yml・required-checks.yml) が検出を triage 待ちの issue にする。
# 題名の完全一致で open な issue があれば起こさず、無ければ前置きと報告 (先頭の 60000 byte) を本文に
# needs-triage の label で起こす。gh が失敗したら非 0 で終わる。run を落とすのは呼び出し側の workflow
#
# source すると triage_body だけを定義し、gh を撃たずに本文を確かめられる

# issue 本文の上限 (65536 文字) に収める
limit=60000

# 前置きと報告から issue 本文を組んで stdout に出す
triage_body() {
  local prelude=$1 report=$2
  local cut fence longest
  cut=$(mktemp)
  head -c "$limit" "$report" > "$cut"
  if [ "$(wc -c < "$report")" -gt "$limit" ]; then
    drop_split_char "$cut" "$report"
  fi
  # 開きより短い backtick の行は code block を閉じないので、報告中の最長の連なりより 1 つ長くする
  longest=$({ LC_ALL=C grep -aoE '`+' "$cut" || true; } | awk '{ if (length > m) m = length } END { print m + 0 }')
  fence=$(printf '%*s' "$((longest >= 3 ? longest + 1 : 3))" '' | tr ' ' '`')
  cat "$prelude"
  echo
  echo "$fence"
  cat "$cut"
  # 末尾に改行が無い報告 (切った報告を含む) でも、閉じの囲いを行頭に置く
  if [ -n "$(tail -c 1 "$cut")" ]; then
    echo
  fi
  echo "$fence"
  if [ "$(wc -c < "$report")" -gt "$limit" ]; then
    echo
    echo "出力が長いので先頭の $limit byte で切った。全文は run の log にある"
  fi
  rm -f "$cut"
}

# 切った報告の末尾が多 byte 文字の途中なら、その文字の先頭 byte から後を落とす。
# 切れ目の次の byte が継続 byte (0x80-0xBF) のときだけ途中と見なし、報告がもともと持つ不正な byte は変えない
drop_split_char() {
  local cut=$1 report=$2
  local next k=0 byte need
  next=$(head -c "$((limit + 1))" "$report" | tail -c 1 | od -An -tu1 | tr -d ' ')
  if [ "$next" -lt 128 ] || [ "$next" -ge 192 ]; then
    return
  fi
  for byte in $(tail -c 3 "$cut" | od -An -tu1 | awk '{ for (i = NF; i >= 1; i--) print $i }'); do
    k=$((k + 1))
    if [ "$byte" -ge 128 ] && [ "$byte" -lt 192 ]; then
      continue
    fi
    if [ "$byte" -ge 240 ] && [ "$byte" -lt 248 ]; then
      need=4
    elif [ "$byte" -ge 224 ] && [ "$byte" -lt 240 ]; then
      need=3
    elif [ "$byte" -ge 192 ] && [ "$byte" -lt 224 ]; then
      need=2
    else
      return
    fi
    if [ "$k" -lt "$need" ]; then
      head -c "$(($(wc -c < "$cut") - k))" "$cut" > "$cut.tmp"
      mv "$cut.tmp" "$cut"
    fi
    return
  done
}

if [ "${BASH_SOURCE[0]}" != "$0" ]; then
  return 0
fi

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

body=$(mktemp)
trap 'rm -f "$body"' EXIT
triage_body "$prelude" "$report" > "$body"
gh issue create --repo "$repo" --title "$title" --label needs-triage --body-file "$body"
