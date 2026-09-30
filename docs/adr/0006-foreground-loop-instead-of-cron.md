# ADR 0006: cron をやめ、人が起こして人が止める loop で tick を回す

- Status: Accepted (停止要求が worker を止めない部分・token file を保険に残す部分・`loop <project> <interval>` という起動の形・tick を跨いで状態を持ち越さない部分・project 単位の lock と単発の `tick` は ADR 0009 が置き換えた。人が起こして人が止める loop で回す部分は残る)
- Date: 2026-09-27
- ADR 0001 のうち、定期起動を cron に頼る決定と、自作 scheduler の却下を置き換える

## Context

ADR 0001 は定期起動を cron に頼り、自作のループを「監督問題 (crash 時の再起動・boot 時の起動・二重起動防止) を持ち込む」として却下した。

cron に頼ったことで、設計は次の代償を引き受けていた (`docs/design/system.md` §8 / §9)。

- cron の最小の PATH で依存 CLI を引けないので、CLI が PATH を自分で解決する
- cron の非ログイン session から keyring の認証を読めないことがあるので、token file を config に書かせる
- 対話 shell からの試運転では cron を再現できないので、`tick --dry-run --cron-env` で自分を最小環境で撃ち直す
- CLI 自身が起動できなかった失敗を見るために cron.log を置き、死活を log.jsonl と cron.log の 2 段で読む
- crontab の 1 行を `setup` が承認を得て代行する

加えて、成功した tick は何も出さないので、回っているかは log を読みに行かないと分からない。

## Decision

**定期起動を cron から外し、CLI の `loop <project> <interval>` が 1 つの project の tick を周期ごとに回す。loop は人が clone を cwd にした端末で起動し、Ctrl+C で止め、落ちたら人が同じコマンドで撃ち直す。** 監督問題のうち、再起動と boot 時の起動は人に割り当て、二重起動は lock で塞ぐ。

- stateless は保つ。loop は tick を跨いで状態を持ち越さず、各 tick は今までどおり外部 store を読み直す。tick は loop の process の中で回す
- 周期は tick の終了から数える (起動直後に 1 回撃つ)。tick の result が何であっても次の周期へ進む — config と token は tick ごとに読み直すので、直せば撃ち直さずに戻る
- **停止は段階的にする**。1 回目の SIGINT / SIGTERM / SIGHUP は今の tick を最後まで進めてから止まり、2 回目は orchestrator を起動する前ならそれを起動せず、起動中ならその process group を止める (決定ファイルは読まない — timeout と同じ扱い)。orchestrator は新しい process session で起動しているので端末の signal が届かず、loop だけが死ぬと、孤児になった orchestrator が wip を付けても決定ファイルを読む者がおらず、worker が起動されないまま wip が stale になる。worker も別の process session なので、どちらの停止でも巻き込まない
- loop が生きている間持つ lock で、同じ project の 2 本目の loop を拒む。tick 単位の lock は単発の `tick` との直列化のために残す
- 端末の画面を観測点にする。`status` の表に loop の見出し (待機 / tick 実行中 / 停止待ち・次の tick の時刻・直近の tick の result と error) と停止の操作案内を足して描き直す。log.jsonl は `status` の起点と判断の記録のために残し、cron.log は撤去する
- cron の経路 (crontab の登録・`--cron-env`・cron.log・2 段の死活判定) は撤去し、併存させない

## Consequences

### 良い影響

- loop は起動した shell の PATH と認証をそのまま継ぐ。PATH の自己解決と token file は、起動した環境から取れないとき (ssh 越し等) の保険に下がる
- 回っているか・今何をしているかは、loop の画面を見れば分かる
- 利用者の crontab に外部ツールが書かなくなる

### 悪い影響 / 制約

- 端末を閉じる・マシンを再起動する・loop が落ちると止まり、自分では起き直さない。人が気づいて撃ち直すまで tick は回らない (ADR 0001 が却下した監督問題のうち、再起動と boot 時の起動を人が引き受ける)
- loop は長く生きるので、起動した worker の終了を自分で回収する (tick がすぐ終わる cron では init が回収していた)
- binary と同梱の契約は loop を起動した時点の版に固定される。更新したら撃ち直す
- 取りこぼした周期を追い掛けない・スリープ中に走らない性質は cron と変わらない。次の tick が読み直すので実害にならない (ADR 0001 の議論がそのまま効く)

## Alternatives considered

### cron と loop を併存させる

- 却下理由: 駆動が 2 系統になり、lock の相互作用と 2 通りの死活判定を保守する。ADR 0001 自身が、2 系統の timer を保守する理由は無いとしている

### 1 本の loop で全 project を回す

- 却下理由: tick は clone を cwd にして動き、clone の path は config に持たない (`docs/design/system.md` §8)。全 project を回すにはその決定を覆す

### tick ごとに自分を子 process で撃ち直す

- 却下理由: 更新が次の tick から効く利点はあるが、端末の signal を子に届かせない分離が要る。process 内で回し、更新は撃ち直しで拾う

### TUI library で alternate screen とキー操作を持つ

- 保留: raw mode では Ctrl+C がキー入力として届き、段階停止の経路が signal と 2 つになる。終了すると画面が消えて止まった理由が残らない。キー操作が要るようになったら検討する
