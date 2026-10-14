#!/usr/bin/env bash
set -euo pipefail

# 定期の検査 (.github/workflows/govulncheck.yml・required-checks.yml) が検出を triage 待ちの issue にする。
# 題名の完全一致で open な issue があれば起こさず、無ければ前置きと報告 (先頭の 60000 byte) を本文に
# needs-triage の label で起こす。gh が失敗したら非 0 で終わる。run を落とすのは呼び出し側の workflow
#
# source すると、set -euo pipefail・limit・下の関数を定義して戻り、gh を撃たない。
# triage_body で本文を確かめられる

# issue 本文の上限 (65536 文字) に収める
limit=60000

# 前置きと報告から issue 本文を組んで stdout に出す
triage_body() {
  local prelude=$1 report=$2
  local cut fence truncated=no
  if [ "$(wc -c < "$report")" -gt "$limit" ]; then
    truncated=yes
  fi
  cut=$(mktemp)
  head -c "$limit" "$report" > "$cut"
  if [ "$truncated" = yes ]; then
    drop_split_char "$cut" "$report"
  fi
  fence=$(fence_for "$cut")
  cat "$prelude"
  echo
  echo "$fence"
  cat "$cut"
  # 末尾に改行が無い報告 (切った報告を含む) でも、閉じの囲いを行頭に置く
  if [ -n "$(tail -c 1 "$cut")" ]; then
    echo
  fi
  echo "$fence"
  if [ "$truncated" = yes ]; then
    echo
    echo "出力が長いので先頭の $limit byte で切った。全文は run の log にある"
  fi
  rm -f "$cut"
}

# 報告を囲う backtick の列を stdout に出す。開きより短い backtick の行は code block を閉じないので、
# 報告中の最長の連なりより 1 つ長くする (最低 3)
fence_for() {
  local report=$1
  local longest
  longest=$({ LC_ALL=C grep -aoE '`+' "$report" || true; } | awk '{ if (length > m) m = length } END { print m + 0 }')
  printf '%*s\n' "$((longest >= 3 ? longest + 1 : 3))" '' | tr ' ' '`'
}

# 報告の先頭を切った file (cut) の末尾が多 byte 文字の途中なら、その文字の先頭 byte から後を落とす。
# 途中と見なすのは、文字の残りの byte が報告の切れ目の後に継続 byte として揃っているときだけ。
# 揃っていない並び (報告がもともと持つ不正な byte) は変えない
drop_split_char() {
  local cut=$1 report=$2
  local size i k=0 lead='' need
  local -a before after
  size=$(wc -c < "$cut")
  # 切れ目の前後 3 byte ずつを 10 進の値で並べる
  read -r -a before <<< "$(tail -c 3 "$cut" | od -An -tu1)"
  read -r -a after <<< "$(head -c "$((size + 3))" "$report" | tail -c +"$((size + 1))" | od -An -tu1)"
  for ((i = ${#before[@]} - 1; i >= 0; i--)); do
    k=$((k + 1))
    if ! is_continuation_byte "${before[i]}"; then
      lead=${before[i]}
      break
    fi
  done
  if [ -z "$lead" ]; then
    return 0
  fi
  if [ "$lead" -ge 240 ] && [ "$lead" -lt 248 ]; then
    need=4
  elif [ "$lead" -ge 224 ] && [ "$lead" -lt 240 ]; then
    need=3
  elif [ "$lead" -ge 192 ] && [ "$lead" -lt 224 ]; then
    need=2
  else
    return 0
  fi
  if [ "$need" -le "$k" ]; then
    return 0
  fi
  for ((i = 0; i < need - k; i++)); do
    if [ "$i" -ge "${#after[@]}" ] || ! is_continuation_byte "${after[i]}"; then
      return 0
    fi
  done
  head -c "$((size - k))" "$cut" > "$cut.tmp"
  mv "$cut.tmp" "$cut"
}

# UTF-8 の継続 byte (0x80-0xBF) か
is_continuation_byte() {
  [ "$1" -ge 128 ] && [ "$1" -lt 192 ]
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
