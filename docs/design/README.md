# 設計 doc

本ディレクトリが claude-dispatcher の設計の正本。実装の進行に合わせて最終形で書き換える (決定の理由は [`docs/adr/`](../adr/) に不変の記録として残す)。

## 索引

| file | 中身 |
|---|---|
| [`system.md`](system.md) | システム境界 / 全体図 / 解く問題 / 状態の表現 / 機械と LLM の線 / trigger / 起動・retry・打ち切り / workflow 定義 / 駆動 / worker に渡すもの / 配布と外部依存 / スコープ外 / CLI の seam |
| [`usecases.md`](usecases.md) | Cockburn 縮約テンプレートによるユースケース。冒頭に記述規約 |
| [`formats.md`](formats.md) | 外から観測できる形式。black-box テストの参照先 |

用語は [`CONTEXT.md`](../../CONTEXT.md)。

## 設計を先に直す

構造を変えるときは、実装 (CLI のコード) より先に上の正本を直す。実装が先に着地すると、設計 doc が実装の後追い要約に落ちて正本でなくなる。

| 変えるもの | 正本 |
|---|---|
| 状態の表現と書き手、候補の定義式、機械と LLM の線、trigger の文法と評価の規則、起動・retry・打ち切り、workflow 定義の項目と検査の姿勢、駆動、worker に渡すもの、配布と外部依存、CLI の seam (部品の責務と seam の位置) | `system.md` |
| ユースケース | `usecases.md` |
| 外から観測できる形式 | `formats.md` |

振る舞いの変わらないリファクタリング・bug fix・実装内部だけの変更は先に直さなくてよい。迷ったら先に直す。決定を覆すときは ADR を新しく起こす (既存 ADR の本文は書き換えない)。

## ドメインモデル図を持たない理由

dispatcher は専用の永続 store を持たない。状態は、外部 store の実体 (tracker の label / CL / remote branch) と、loop の process と寿命を共にする memory (claim・attempt・打ち切り) に置く。dispatcher 固有の永続 entity が無いので、集約・多重度として描く対象が無い。状態の表現は `system.md` §4 の表と候補の定義式で足りる。

dispatcher 固有の永続実体を導入する決定が出たときに図を足す。例: 打ち切りを loop の起動を跨いで持つと決めたとき、issue と CL の紐づけを記録すると決めたとき。
