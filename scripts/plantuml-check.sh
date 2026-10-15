#!/usr/bin/env bash
set -euo pipefail

# commit 時の hook。手元の plantuml が固定した版 (scripts/plantuml.version) かを確かめてから、
# docs/design の直下の .puml の構文を `plantuml --check-syntax` の exit code で確かめる
# (`-tsvg` は構文の誤りでも誤りの画像を出して exit 0 を返しうるので、描画の成否では見ない)。
# 版が違えば検査を撃たずに落とす (描いた svg の版の一致は repocheck diagrams が検査する)。repo の root で撃つ

want=$(tr -d '[:space:]' < scripts/plantuml.version)
if ! [[ "$want" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "plantuml の版を scripts/plantuml.version から読めない (読めた値: '${want}')" >&2
  exit 1
fi
install="https://github.com/plantuml/plantuml/releases/tag/v${want} から native-plantuml-<OS>-<arch>-${want}.zip (または plantuml-${want}.jar と Java) を入れる"
if ! command -v plantuml >/dev/null; then
  echo "plantuml が PATH に無い。固定した版 $want を入れる (CONTRIBUTING.md の「セットアップ」):" >&2
  echo "  $install" >&2
  exit 1
fi
# 1 行目は `PlantUML version <版> ...`
if ! out=$(plantuml -version 2>&1); then
  echo "plantuml -version が失敗した。入れ直す (CONTRIBUTING.md の「セットアップ」):" >&2
  echo "$out" >&2
  echo "  $install" >&2
  exit 1
fi
have=$(awk 'NR==1{print $3}' <<<"$out")
if [ "$have" != "$want" ]; then
  echo "手元の plantuml は '${have}' で、固定した版は ${want}。入れ直す (CONTRIBUTING.md の「セットアップ」):" >&2
  echo "  $install" >&2
  exit 1
fi
status=0
for f in docs/design/*.puml; do
  plantuml --check-syntax "$f" || { echo "構文の誤り: $f" >&2; status=1; }
done
exit "$status"
