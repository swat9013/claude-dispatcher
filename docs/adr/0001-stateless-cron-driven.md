# ADR 0001: 台帳を持たず、cron 駆動の tick で毎回外部 store を読み直す

- Status: Accepted
- Date: 2026-09-26

## Context

issue から CL までを無人で回す機構に要るユースケースは 2 つに尽きる。着手可の issue を実装して CL にすることと、CL の状態 (conflict / review / CI) を検知して手直しすることである。

この種の機構は、状態を自前の台帳に持ち、常駐プロセスが台帳を進める形に向かいやすい。そうすると次の問題が恒常的に生まれる。

- 台帳 (意図) と tracker / CL host (現実) が食い違い、その解消規則と例外処理が際限なく増える
- 宣言の綴り誤りを根拠に機械が状態を書き換え、稼働中の作業を壊す事故が silent に起きる
- 常駐プロセスの crash 時の再起動・boot 時の起動・二重起動防止という監督問題を抱える
- orchestrator を常駐セッションにすると context が溜まり、人手の起こし直しが要る

## Decision

**専用の永続 store を持たない。状態は成果物 — tracker の label・紐づく open CL の存在・remote branch — に置き、cron が起動する tick が毎回外部 store を読み直して再構成する。**

- 状態を書くのは LLM (orchestrator / worker) と人間だけ。tick は観測と、機械的に確定する指示の導出までで、外部 store に書かない (導入時に導入者の承認を得て label を作るのは tick ではなく導入用の subcommand で、状態を書くことには当たらない)
- 指示があるときだけ使い切りの orchestrator セッションを起こす。指示 0 件なら LLM を起動しない
- 取りこぼした tick は補償しない。次 tick が現実を読み直すので、cron の弱点 (取りこぼしを追い掛けない / スリープ中は走らない) が実害にならない
- 並行実行は `flock` で 1 本に絞る

### cron で動かすための注意 (実運用で踏んだもの)

- **cron の PATH は最小** — `~/go/bin`・`~/.local/bin`・mise の shims・Homebrew の prefix はどれも入っていない。crontab の行は CLI を絶対 path で指し、CLI は依存 CLI (gh / claude / git) の path を自分で解決する
- **keyring に置いた認証は cron から読めないことがある** — gh は `GH_TOKEN` / `GITHUB_TOKEN` → `hosts.yml` → OS keyring の順で認証を引き、macOS の Claude Code はログインを Keychain に置く。keyring はログイン session に従うので、cron の非ログイン session からは読めないことがある (orchestrator が `Not logged in` で数秒で落ちる)。config に token file を任意で置き、CLI が子プロセスへ環境変数で渡す経路を持つ
- **対話 shell からの試運転は keyring の失敗を再現しない** — 環境変数を削っても keyring は読めてしまう。keyring の問題は登録後の死活 (log.jsonl 最終行の `ts`) でしか確かめられない
- **cron 相当の試運転は CLI 自身が最小環境で撃ち直す** — Claude Code の sandbox の除外指定は Bash 呼び出しの先頭 token だけで照合される。呼び出し側が `env -i …` を前置すると CLI が sandbox の中に落ち、gh が credential を読めない偽の失敗になる
- **死活は 2 段で読む** — log.jsonl 最終行の `ts` が周期の 2 倍より古ければ tick が走っていない。そのとき crontab の出力先 (cron.log) の更新時刻が新しければ CLI の手前の起動失敗、古ければ cron 自体の問題。CLI が起動できない失敗は log.jsonl に載らないので、cron.log を唯一の観測点として残す

## Consequences

### 良い影響

- drift という概念が消える。機械が状態を書かないので silent な機械遷移も起きない
- orchestrator は毎回新しい context で始まる。プロセスの生死や context の肥大が運用問題にならない
- 静止時の LLM コストはゼロ

### 悪い影響 / 制約

- 判断の系譜は tracker のコメントと CL 履歴からしか追えない。log.jsonl は正本ではなく欠損しうる
- worker が異常死すると wip が残り、issue が塞がる。自動回収は持たず、人が triage で剥がす
- cron の停止は機械では検知されない。人が log の最終行の時刻を見るまで気づかない
- push 前の成果は worker の死とともに失われる (worktree を残さない割り切り)
- issue と CL の紐づけは worker が CL 記述に書く closing reference に依存する

## Alternatives considered

### 台帳を持ち、常駐 daemon が状態を進める

- 却下理由: drift の解消と silent な機械遷移という、最も高くつく失敗クラスを構造として抱える

### 台帳を issue と CL の紐づけ専用に最小化して残す

- 却下理由: claim を tracker label に置いた時点で台帳の主用途が消え、残る紐づけは closing reference で足りる

### 「実装済み」を label で表す

- 却下理由: open CL の存在という外部事実のキャッシュにすぎない。CL が merge されずに close されると label だけが残り、issue が永久に候補から外れる。CL の存在で判定すれば、close で自動的に候補へ戻る

### 常駐 orchestrator (同一セッションでの繰り返し)

- 却下理由: context を作り直せず、静止時にもコストがかかる

### 自作 scheduler / systemd timer と launchd

- 却下理由: 自作ループは監督問題を持ち込む。2 系統の timer を保守する理由は、cron の弱点が stateless 設計で実害にならない以上、無い
