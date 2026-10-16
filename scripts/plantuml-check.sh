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
# 版は `PlantUML version <版> ...` の行から取る。-version の exit code は見ない (graphviz の dot が無いと、
# 版を出した上で 0 以外を返す。この gate は dot を使わない)。Java の wrapper の警告など、他の行は読み飛ばす
out=$(plantuml -version 2>&1) || true
have=$(awk '/^PlantUML version /{print $3; exit}' <<<"$out")
if [ -z "$have" ]; then
  echo "plantuml -version から版を読めない。入れ直す (CONTRIBUTING.md の「セットアップ」):" >&2
  echo "$out" >&2
  echo "  $install" >&2
  exit 1
fi
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
