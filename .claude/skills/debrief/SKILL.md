---
name: debrief
description: claude-dispatcher を回している project の workflow 定義と痕跡を読み、本体の改善点を提案して、承認された分をこの repo に起票する
disable-model-invocation: true
argument-hint: "<WORKFLOW.md か project の dir の path> [--since <日時>]"
allowed-tools: Read, Bash(jq:*), Bash(scripts/claude-dispatcher-dev.sh paths:*), Bash(scripts/claude-dispatcher-dev.sh status:*), Bash(gh issue list:*), Bash(gh repo view:*)
---

# debrief

claude-dispatcher を実際に回している project から、**claude-dispatcher 本体**の改善点を拾う。dispatch (派遣) から帰った痕跡を吸い上げるので debrief と呼ぶ。

- 提案の宛先は本体だけ。project 側 (WORKFLOW.md・worker が使う playbook) が原因の症状は、「本体の既定値・事前検査・status 表示・log の行・doc で防げたか、早く気づけたか」に言い換えて扱う。言い換えられないものは捨てる。この repo の issue にしか持ち込み先が無いため
- 証拠源は state dir と WORKFLOW.md (その変更履歴を含む) に限る。Claude Code の transcript は撃った人の環境にしか無く、起票した証拠を他の人が引けないため
- 判定を script にしていないのは、痕跡がまだ少なく、どの判定を繰り返すかが分かっていないため。同じ jq の繰り返しに気づいたら、script は作らず、提示の末尾に 1 行で挙げる
- 形式の正本は [docs/design/formats.md](../../../docs/design/formats.md)。log.jsonl (§4) と `paths --json` (§7.3) は公開契約で、status.json・`prompts/`・`workers/` は契約外 (形が変わりうる)
- **読むだけ**: state dir にも workspace にも書かず、loop に停止要求も signal も送らない。loop が走っている最中にも撃つため

## 手順

### 1. 置き場を引く

引数の path が dir なら、直下の `WORKFLOW.md` を補う。path が無ければ、どの project を読むかを user に聞く (`paths` は path を省くと cwd の WORKFLOW.md、つまりこの repo 自身を読んでしまう)。

```sh
scripts/claude-dispatcher-dev.sh paths --json <WORKFLOW.md の path>
```

CLI はこの checkout から build した版 (`scripts/claude-dispatcher-dev.sh`) で撃つ。読み方の正本 (この checkout の formats.md) と版を揃えるため。PATH 上の `claude-dispatcher` は Homebrew の古い版のことがあり、引数の形から違いうる。出力の `state_dir`・`log`・`status_file` を以降で使い、worker の痕跡は `<state_dir>/workers/`、描画した共通 prompt は `<state_dir>/prompts/` にある。

次のどれかに当たったら「**読めなかった**」として理由を告げて終える。

- dev の script が build に失敗する
- `paths` が 0 以外で終わる (workflow 定義が無い・誤っている)
- `log` の file が無い (その project で loop を一度も回していない)

以降の jq の `F` は `log` の path を指す。`--since` があれば、日時を UTC の RFC 3339 (`2026-10-01T00:00:00Z`) に直し (offset の無い日時は端末の local time、日付だけなら local の 0 時として読む。`status` の表示と同じ)、各 jq の `F` を外して前に次の絞り込みを置き、stdin で渡す。log の `ts` は UTC の `Z` 表記なので、文字列の比較で絞れる。一時 file は作らない (作ると、どの終わり方でも消す後始末が要る)。

```sh
jq -c --arg since "<--since の UTC>" 'select(.ts >= $since)' <log> | jq ...
```

### 2. log.jsonl から数える (観点 1〜4)

log.jsonl から決定的に数える観点。どれも件数と、該当する行の `ts`・`target` を控える。件数と、起動しなかった tick が並列上限に達していたか (tick 行の値だけで決まる) は確度「決定的」で出す。一方、件数を status・WORKFLOW.md (とその履歴) と突き合わせて下す判定 (閉じていない claim の読み分け・上限に達していないのに起動しなかった理由・`running` 等の欄が無い古い行の正否・上限時間との比べ合わせ) は「解釈」とする。

```sh
jq -s -r '[length, .[0].ts, .[-1].ts] | @tsv' F     # 読んだ行数と期間
jq -r .event F | sort | uniq -c                     # event の数
jq '{last_tick, abandoned, ambiguous, blocked}' <status_file>   # 最後に書いた状態。loop が止まった後も残る
```

**1. 終わり方**

```sh
# outcome と reason の組ごとの件数。completed なのに reason に失敗が付いた行もここに出る
jq -r 'select(.event=="end") | [.trigger, .outcome, (.reason // "")] | @tsv' F | sort | uniq -c | sort -rn
# 作業対象ごとの経過 (retry の連鎖・abandon / unabandon の往復を見る)
jq -r 'select(.target) | [.target, .ts, .event, (.attempt // .next_attempt // ""), (.outcome // .reason // .error // "")] | @tsv' F | sort -s -k1,1
# attempt 1 回分の所要時間 (分)
jq -s -r 'map(select(.event=="start" or .event=="end")) | group_by([.session_id, .attempt])[] | select(length==2) | [.[0].target, .[0].trigger, .[0].attempt, (((.[1].ts|fromdateiso8601) - (.[0].ts|fromdateiso8601))/60 | floor), .[1].outcome] | @tsv' F
# 閉じていない claim (最後の行が claim を持ったままの event である作業対象)
jq -s -r 'map(select(.target)) | group_by(.target)[] | last | select(.event=="start" or .event=="retry" or .event=="wait_slot" or .event=="verify_wait" or (.event=="end" and .outcome=="failed")) | [.target, .event, .ts] | @tsv' F
```

claim を解く行は formats.md §4 (`completed` と `stopped` の `end`・`release`・`abandon`) が正本。停止要求で捨てた再起動待ちと確かめ待ちの claim は、最後の行が `error` になる (§6 の停止要求)。そのため上の jq は `error` の行も含めて最後の行を見る。

閉じていない claim は、`scripts/claude-dispatcher-dev.sh status <WORKFLOW.md の path>` が示す今の loop の worker と突き合わせる。claim は loop の memory にしか無いので、loop が居なければすべて消えている。本体の記録の欠けの候補は次の 2 つ。

- loop が走っているのに worker に居ないもの
- loop が居ないのに最後の行が `start`・`retry`・`wait_slot`・`verify_wait` のもの (end や停止の行を書く前に loop が落ちた。`verify_wait` なら確かめ待ちのまま落ちた)

**2. tick**

```sh
jq -r 'select(.event=="tick" and .result=="error") | .error' F | sort | uniq -c | sort -rn
jq -r 'select(.event=="tick") | .blocked[]? | [.trigger, .error] | @tsv' F | sort | uniq -c | sort -rn
jq -r 'select(.event=="tick") | .ambiguous[]? | [.head, (.targets | join(","))] | @tsv' F | sort | uniq -c
# 候補があるのに何も起動しなかった tick と、起動の判定に使った値
jq -r 'select(.event=="tick" and .candidates > 0 and (.launched | length) == 0) | [.ts, .candidates, .running, .waiting_retry, .max_concurrent] | @tsv' F
# 起動の直前の読み直しで起動しなかった候補。作業対象・trigger・理由の組ごとに、件数と最初・最後の ts
jq -s -r 'map(select(.event=="recheck_skip")) | group_by([.target, .trigger, .reason])[] | [.[0].target, .[0].trigger, .[0].reason, length, .[0].ts, .[-1].ts] | @tsv' F
# 起動しなかった tick の理由を tick ごとに読む (recheck_skip は、同じ tick の tick の行より前に書かれる)
jq -r 'select(.event=="recheck_skip" or .event=="tick") | [.ts, .event, (.target // .candidates), (.reason // ((.launched // []) | length))] | @tsv' F
```

候補があるのに起動しなかった tick は、`running` と `waiting_retry` の和が `max_concurrent` に達していれば正常 (起動をやめる規則と 3 つの値の意味は formats.md §4 の tick 行の項)。達していないのに起動していなければ、その tick の行より前に書かれた `recheck_skip` の行 (起動の直前の読み直しで見送った候補と理由。`reason` の値は formats.md §4)、同じ tick の `blocked`・`ambiguous`、その候補の claim・打ち切りの状態で理由を読む。読み直せなかった候補は `recheck_skip` ではなく、観点 4 の `error` の行 (`起動しようとした作業対象を読み直せない`) に出る。

`recheck_skip` は tick ごとに書かれる。同じ組の件数が多く、最初と最後の `ts` の間が周期の何倍にも及べば、その見送りが続いている。続く原因の推定は確度「解釈」とする。`終端`・`trigger から外れた` が続けば open な一覧の検索が古い結果を返し続けている、`当たるかをまだ決められない` が続けば CL の conflict の計算が終わらない、`宣言順で先の trigger に当たる` が続けば先の trigger でも起動されていない (その作業対象の claim・打ち切り・`blocked` を見る) 候補になる。

`running`・`waiting_retry`・`max_concurrent` の欄が空の行は、この 3 つの key を書かない古い版の log。その行に限り、その時刻に走っていた attempt (start と end の間) を数え、並列上限は `git -C <WORKFLOW.md の dir> log -p -- WORKFLOW.md` の変更時刻と照らして決める (並列上限は tick ごとに workflow 定義を読み直して決まる。formats.md §6)。

**3. 時間**

```sh
jq -r 'select(.event=="retry") | [.target, .next_attempt, .backoff] | @tsv' F
jq -r 'select(.event=="wait_slot") | .target' F | sort | uniq -c | sort -rn
# worker が終わった後の確かめ待ち。attempt 1 回分 (session_id と attempt の組) ごとに、件数と最初・最後の ts
jq -s -r 'map(select(.event=="verify_wait")) | group_by([.session_id, .attempt])[] | [.[0].target, .[0].trigger, .[0].attempt, length, .[0].ts, .[-1].ts] | @tsv' F
```

`verify_wait` は確かめ直すたびに (tick ごとに) 書かれる。最初と最後の `ts` の間が周期の何倍にも及べば、CL の conflict の計算が終わらず確かめ待ちが続いている (確度「解釈」)。

stall と上限時間の超過は、観点 1 の `reason` に文で載る。WORKFLOW.md の `limits.stall_timeout`・`limits.run_timeout` (省けば formats.md §2.1 の既定) と、観点 1 の所要時間を並べて読む。

**4. error の行**

```sh
jq -r 'select(.event=="error") | .error' F | sort | uniq -c | sort -rn
```

### 3. 解釈の材料を集める (観点 5・6)

LLM の解釈が要る観点。ここから出す提案は確度を「解釈」とし、観点 1〜4 の「決定的」と分けて示す。

**5. worker の出力**

読むのは F に現れる作業対象の分だけ。log の `target` の `issue#42` / `cl#7` は、file 名では `issue-42` / `cl-7` になる (formats.md §1)。worker の file は attempt を跨いで追記され、`--since` では絞れないので、F の期間より前の attempt の分も混ざる。

`workers/<作業対象>.log` は stream-json で、数 MB になる。全文を読まず、`result` の event だけを抜く。`result` は turn の終わりごとに出るので、attempt 1 回に 1 つとは限らない (background の task の通知で turn が続くと増える)。

```sh
jq -c 'select(.type=="result") | {subtype, is_error, num_turns, duration_ms, denied: [.permission_denials[]?.tool_name]}' <state_dir>/workers/<作業対象>.log
```

- 走っていない作業対象の log の最後の行が `result` でなければ、stall・上限時間・signal で止められた候補。観点 1 の end 行と照らす
- `workers/<作業対象>.stderr.log` は `sort | uniq -c | sort -rn | head` で繰り返す行を見る
- 描画の崩れを `grep -l -E '<no value>|\{\{' <state_dir>/prompts/<作業対象>.md` で探す (F に現れる作業対象の分だけ)

**6. 設定の回避策**

WORKFLOW.md の front matter と本文を読み、利用者が本体の不足を補っている箇所を探す。

- 既定値 (formats.md §2.1) から外した値と、外した理由の注記
- hooks・`claude.args` の凝った書き方
- 本文 (共通 prompt) が worker に言い聞かせている、本体の挙動の注意 (例: 「loop を起動し直すと回数は 1 に戻る」は attempt の持ち越しが無いことの回避策)
- YAML の書き方の注意 (anchor の置き場など)

### 4. 本体の改善に言い換える

観点ごとの所見を、本体の改善 1 件ずつに言い換える。言い換える前に、本体がすでにその機能を持っていないか、意図してスコープ外にしていないかを確かめる。入口は [docs/design/](../../../docs/design/) の該当の § (スコープ外は system.md §12) で、`internal/` はその § が名指す package だけを読む。持っているのに使われていないなら、改善は「doc か事前検査で気づかせる」側になる。

言い換えられずに捨てた所見は、1 行の理由を添えて提示の末尾に並べる。

### 5. 既存の issue と照合する

改善ごとに、要点の語で open と closed の両方を引く。

```sh
gh issue list --state all --search "<要点の語>" --json number,title,state,stateReason,labels --jq '.[] | [.number, .state, .stateReason, .title, ([.labels[].name] | join(","))] | @tsv'
```

| 当たった issue | 扱い |
|---|---|
| open | 新しく起票せず、新しい証拠を足すコメントの案にする |
| closed で `wontfix` か not planned | 既定では提示しない。提示の末尾に「過去に却下済み: #n」と 1 行ずつ並べるだけにする |
| closed で completed | 直したはずの症状の再発として起票し、本文で元の issue を指す |
| closed で duplicate | 重複先の issue を辿り、その issue でこの表を引き直す |
| 当たらない | 新しく起票する |

複数に当たったら、open → 却下済み → 再発の順で先に当たった 1 つで扱う。

### 6. 提示して承認をもらう

改善ごとに次を並べ、起票する番号を user に選んでもらう。承認されるまで起票もコメントもしない (この repo の他の人に見える操作のため)。

- 観点と確度 (決定的 / 解釈)
- 症状
- 証拠 (`ts` と `target`、WORKFLOW.md の項目名)
- 本体の該当箇所 (system.md・formats.md の §、`internal/` の package)
- 提案
- 照合の結果 (新規 / #n へコメント / #n の再発)

`gh repo view <WORKFLOW.md の tracker.repo> --json visibility` が public でなければ、出典の scope key を本文に載せるかも同じ問いで確かめる。

改善が 0 件なら「**提案 0 件**」とし、読んだ範囲 (行数・期間・読んだ worker log の数) を添えて、手順 1 の「読めなかった」と区別する。

### 7. 起票する

承認された分だけ撃つ。repo は clone の remote から gh が推定する (docs/agents/issue-tracker.md)。本文は heredoc で stdin に渡す。

```sh
gh issue create --title "<改善を「〜する」の形で>" --label needs-triage --body-file - <<'EOF'
<本文>
EOF
gh issue comment <n> --body-file - <<'EOF'
<本文>
EOF
```

- 起票の本文は「症状 / 証拠 / 本体の該当箇所 / 提案 / 出典」の節で書く。コメントは「証拠 / 出典」だけにし、既存の本文と重なる症状と提案は繰り返さない
- 出典は project の scope key (手順 6 で伏せると決めたなら伏せる) と分析した期間
- 証拠は `ts` と `target` で引ける形にとどめる。home 以下の絶対 path・作業対象の issue や CL の本文・worker の出力の中身は写さない (この repo は public で、痕跡は他の repo のもの)
- `target` は code span (`` `issue#42` ``) で書く。素の `#42` はこの repo の issue への参照に、`owner/repo#42` は相手の repo への backlink になる

起票とコメントの URL を並べて終える。
