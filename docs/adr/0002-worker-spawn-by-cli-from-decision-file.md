# ADR 0002: worker は orchestrator ではなく CLI が決定 file どおりに起動する

- Status: Accepted
- Date: 2026-09-26

## Context

orchestrator (`claude -p --permission-mode auto` の使い切りセッション) は、どの issue に着手させるかを決める。自然な形は orchestrator が Bash から worker を `nohup claude -p … &` で detach 起動することだが、これは動かない。

- auto mode の classifier は、background で別のセッションを起動する操作を **permission の bypass として deny する** (対話セッションでも同じく deny される)
- `-p` のセッションには人の確認へ落ちる経路が無いので、deny された起動は実行されないまま orchestrator が正常に終わる。**着手が silent に欠ける**

permission posture の代替は 2 つしか無い。`dontAsk` は許可 rule に無い操作を全部 deny するので、orchestrator の全操作 (gh / Write) を rule で網羅する運用になる。`--dangerously-skip-permissions` は、tracker に書く権限を持つセッションから permission 層を外すことになる。

## Decision

**orchestrator は worker を起動しない。起動する worker (spawn prompt の全文) を決定 file に書いて終わり、CLI (LLM なし・sandbox の外) がその決定どおり worker を `claude -p --permission-mode auto` で detach 起動する。**

| 主体 | 起動に関して持つもの |
|---|---|
| orchestrator (LLM) | 指示の採否・wip 付与・決定 file (`decisions` = 採否と理由、`spawn` = issue / 着手形態 / prompt / playbook の path) |
| CLI | orchestrator の正常終了後に決定 file を検査し、採否を log へ写し、`spawn` の各項を新しい process session (`setsid`) として起動する。起動した pid / log path / session id を tick の log 行へ載せる |

- 決定 file は spawn 0 件でも書かせる。無い / 読めない / 検査に落ちたら、1 件も起動せず tick を error にする
- 例外は網羅の欠け (指示の issue に採否が無い) で、書かれた spawn を起動してから error にする — wip を付けた issue を起動せずに残すと、次 tick では普通の wip に見えて誰も拾わない
- orchestrator が timeout / 異常終了したら決定 file を読まない (wip を付けた後に書き切れていない可能性がある)。付いた wip は機械が剥がさず、stale wip として人が回収する
- 「機械は決めない・LLM は起動しない」の線を保つ。CLI が起動するのは LLM が逐語で書いた prompt だけで、選定も起動の要否も CLI は判断しない

## Consequences

### 良い影響

- auto mode と sandbox を保ったまま無人で回る。classifier が止めるのは worker の個々の操作だけになる
- 判断の記録が構造化される。採否と理由は CLI が log へ写すので、LLM に JSON の手組みを頼らない
- 起動失敗が loud になる。claude が無い / exec の失敗 / 決定 file の不備は tick の error として残る

### 悪い影響 / 制約

- tick は orchestrator の終了を待つ (lock を保持したまま最大 15 分)。cron の周期より長引けば次 tick は `locked` で流れる
- wip 付与から worker 起動までに時間差がある。その間に orchestrator が死ぬと wip だけが残る
- 決定 file という中間物が 1 つ増え、その形を orchestrator の契約と CLI の両方が知っている。形の正本は `docs/design/formats.md` に置き、同梱の契約 file に写した例との一致をテストで検査する

## Alternatives considered

### orchestrator を `--dangerously-skip-permissions` で起動する

- 却下理由: tracker に書く権限を持つセッションは、permission 層を外す対象として最も不適

### orchestrator を `dontAsk` で起動し、起動 command を allow rule に書く

- 却下理由: orchestrator の全操作を rule で網羅する運用になり、rule と mode の組み合わせが環境依存になる

### orchestrator の Bash の `run_in_background` で起動する

- 却下理由: harness が管理する background process はセッション終了後の生存が保証されず、使い切りセッションでは worker が道連れになりうる

### CLI が指示から直接 worker を起動する (orchestrator を省く)

- 却下理由: 候補の選定・見送り・怪しい状況の判断が機械に染み、「状態遷移と判断は LLM」の線を壊す
