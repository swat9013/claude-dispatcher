# ADR 0011: マシンを跨いだ分担を、実装 repo の git remote に置いた独自 ref で調停する

- Status: Accepted
- Date: 2026-10-11
- 次の決定を置き換える
  - ADR 0009: 「claim はマシンを跨いで効かない」「1 つの issue 置き場は 1 台から回す」とした部分。調停 remote を指した project に限り、マシンを跨いだ分担を持つ
  - ADR 0001 / 0009: 機械が外部 store に何も書かないとした部分のうち、調停用の独自 ref を書くこと。tracker には引き続き書かず、CL・review・branch にも書かない

## Context

チームの各メンバーが実装 repo を clone し、各自のマシンで loop を起動して、同じ issue 置き場を分担したい (#167)。claim は loop の memory にしかないので、今は別のマシンで同じ置き場に loop を立てると二重に起動する。

調停の場の候補は次のとおり。dispatcher に新しい依存を足したくないので、チームが既に共有しているものに絞った。

- tracker 上の連絡 issue に、着手と生存をコメントで書き合う
- 実装 repo の git remote に、独自 ref を置き合う
- Slack のスレッドや、共有の store (Redis など)

## Decision

**workflow 定義で調停 remote を指した project では、loop は着手宣言と生存報告を、実装 repo の git remote の独自 ref (`refs/dispatcher/<scope key>/...`) に置く。先着は `git push --force-with-lease` の条件付き更新で決め、push が通った loop だけが worker を起動する。leader は持たない。**

- 着手宣言は作業対象ごとに 1 本、生存報告は loop ごとに 1 本の ref にする。どちらも空の tree の commit を指し、持ち主や間隔は commit message に書く
- 着手宣言は「ref が無いときだけ作る」で push する。同じ作業対象を 2 台が同時に取ろうとしても、通るのは 1 台だけになる
- 生存報告が「報告に書かれた間隔の 3 倍」新しくならない loop は死んだとみなす。経過は観測した側の時計だけで数え、マシン間の時計は比べない。死んだ loop の着手宣言は、旧 sha を期待値にした置き換えで乗っ取る
- 自分の着手宣言が乗っ取られていたら、その作業対象の worker を止める。network が切れた loop が、生きたまま死んだとみなされた場合に通る経路で、まれな二重実行は受け入れる (self-fencing は持たない)
- ref は claim を解いたときと loop を止めたときに消す。落ちた loop の ref は、気づいた loop が消す

## Consequences

### 良い影響

- 先着が server の条件付き更新で決まるので、「同時に書いた後で、負けた側が退く」処理が要らない。乗っ取りのときだけ worker を止める
- 調停が tracker の種類に依存しない。GitHub・GitLab・Jira のどの置き場でも、実装 repo は必ずある
- 独自 ref は branch ではないので、branch 一覧・CL の画面・通常の clone や fetch に出ない。同時に在る ref は「走っている loop + 持っている claim」の数に収まる
- tracker には今までどおり書かない。作業対象の label や本文を機械が書き換える事故 (ADR 0001 が避けたもの) は起きない

### 悪い影響 / 制約

- 機械が CL host に書く経路ができる。書くのは `refs/dispatcher/` の下だけに限る
- 経緯 (誰がいつ着手し、いつ乗っ取られたか) は、各マシンのログの行にしか残らない。チームでスレッドとして読むことはできない
- loop を起動するマシンには、実装 repo へ push できる権限が要る
- 乗っ取られた worker が止まる前に push した成果は残りうる。要対処に出し、人が見る
- 落ちた loop の着手宣言を他の loop が拾えるまで、生存報告の間隔の 3 倍かかる。既定の周期 5 分なら約 15 分
- 生存報告の push が repo の push の回数を増やす。間隔は「tick の間隔と 1 分の長いほう」にして、GitHub の推奨 (repo あたり 6 push/分) に収める
- server 側の条件付き更新の原子性は upstream git の receive-pack の性質に頼る。GitHub と GitLab がこれを明言した資料はない。GitHub が独自 ref の作成・条件付きの置き換え・削除を受け付けることは、2026-10-11 に実機で確かめた。GitLab は未確認

## Alternatives considered

### tracker の連絡 issue にコメントで書き合う

- 却下理由: コメントの書き込みは原子的でない。同時に書いた後に、コメントの順で勝敗を読み直し、負けた側が worker を止める処理が要る
  - 人がスレッドとして経緯を読める利点はある
  - tracker ごとに書き込みの adapter を作る必要があり、コメントの順序の付き方も tracker ごとに違う

### 1 本の ref に、着手・解放・生存の出来事を commit として積む

- 却下理由: 無関係な書き込み同士 (ある loop の生存報告と、別の loop の着手宣言) も同じ ref を奪い合い、そのたびに読み直して積み直すことになる
  - 履歴が増え続ける (10 台・5 分周期で 1 日約 2,900 commit)。切り詰める役を誰が担うかを決め直すことになる
  - commit の履歴が経緯の記録になる利点はある

### Slack のスレッドや共有の store に置く

- 却下理由: token・SDK・インフラという、dispatcher の外の要素が増える

### leader を選び、leader が割り当てる

- 却下理由: 先着は条件付きの push で決まり、死んだ loop の回収も各 loop が独立に判定できる。leader にしかできない仕事が無い
