# 外から観測できる形式

- 本 file が CLI の外から観測できる形式の正本。black-box テストはここを参照して書き、実装とテストが食い違ったら本 file に合わせる
- 各 file の責務は [`system.md`](system.md) §7 / §8 / §9、用語は [`CONTEXT.md`](../../CONTEXT.md)
- **公開契約**: log.jsonl の行 (§4) と `paths --json` (§9) は外部の読み手を持つ。key の削除・改名・意味の変更は互換を壊すので、足すだけにとどめるか、ADR を起こす

## 1. 置き場

| 変数 | 既定 |
|---|---|
| config root | `${XDG_CONFIG_HOME:-$HOME/.config}/claude-dispatcher` |
| state root | `${XDG_STATE_HOME:-$HOME/.local/state}/claude-dispatcher` |

macOS でも XDG に揃える (`~/Library` は使わない)。project ごとに次を持つ。

```
<config root>/<project>/
  config.toml                         宣言 config (人が書く。CLI は setup の雛形作成以外で書かない)

<state root>/<project>/
  log.jsonl                           tick 行と orchestrator 行 (§4)
  tick.lock                           tick の flock の対象
  tick.now                            走っている tick の状態 (§10)。tick が lock を持つ間だけ在る
  loop.lock                           loop の生存期間の flock の対象 (§13)
  config-verified                     実在検査に通った config.toml の sha256 (hex 1 行)
  instructions/<stem>.json            指示ファイル (§5.1)。指示があった tick だけ
  decisions/<stem>.json               決定ファイル (§5.2)
  decisions/<stem>.orchestrator.log   orchestrator の stdout / stderr
  decisions/<stem>.handoff-<issue>.md orchestrator が人へ返したときの引き渡し本文
  workers/<issue>-<stem>.log          worker の stdout / stderr
```

- `<project>` は `[A-Za-z0-9._-]+`。subcommand の引数で使う綴り
- `<stem>` は tick の開始時刻 (UTC) の `YYYYMMDDTHHMMSS.ffffffZ`。マイクロ秒まで入れるのは、同じ秒に 2 tick 走ったときに前の file を上書きしないため
- 時刻は RFC 3339 の UTC (`Z` 表記) で、log.jsonl の `ts` はマイクロ秒まで、tick の失敗行 (§6) の前置と画面 (§10 / §13) は秒まで

## 2. config.toml

```toml
[issue]
repo = "owner/name"            # 必須。issue 置き場
ready_label = "ready-for-agent" # 必須。着手可 label の綴り
triage_label = "needs-triage"  # 任意。worker が残タスクを起票するときに付ける唯一の label。省略すると label なし
tracker = "gh"                 # 任意。既定 "gh"。初版は "gh" だけ (他は名指しで失敗)

[cl]                           # 任意。CL 置き場が issue 置き場と別のときだけ書く
repo = "owner/other"           # [cl] を書くなら必須 (空の [cl] は誤り)。tracker は issue 側と同じ

[limits]
max_wip = 2                    # 必須。1 以上の整数。並列上限 N

[auth]                         # 任意。起動した環境から認証を取れないとき (ssh 越し等) だけ書く。書くなら 2 key の少なくとも 1 つ
token_file = "~/.config/claude-dispatcher/<project>/gh-token"
claude_token_file = "~/.config/claude-dispatcher/<project>/claude-token"
```

- 未知の table / key、型の誤り、必須 key の欠落は、名指しで `config_error` にする
- `triage_label` が `ready_label` と同じ綴りなら `config_error` (worker の起票が候補になり自己増殖する)
- token file の path は `~` 始まりか絶対 path。mode が group / other から読める・file が無い・中身が空なら `config_error`
- 実在検査 (config が新しいか `config-verified` の hash と違うとき): `[issue].repo` と `[cl].repo` が存在し、issue 置き場に `dispatcher:wip` と `ready-for-human` の label があること。`triage_label` を書いたらそれもあること

## 3. exit code と result

| exit | `result` | 意味 |
|---|---|---|
| 0 | `ok` | 観測して指示を導出した (orchestrator を起動したなら、正常終了して決定どおり worker を起動した) |
| 1 | `error` | 観測できなかった / 指示を導出できなかった (plugin を解決できない・原則索引の file が無い・選定母集合の playbook が読めない) / orchestrator が正常終了しなかった / claude を起動できなかった / 決定ファイルの検査に落ちた / 想定外の失敗 |
| 2 | `config_error` | config 起因で観測していない (無い / 読めない / 未知 key / 置き場や label が実在しない / token file の不備) |
| 3 | `locked` | 前 tick が走っていたので見送った |
| 4 | `auth_error` | gh の認証が通らず観測していない (綴りを直しても直らない) |

`tick` 以外の subcommand も 0 = 成功、1 = 失敗、2 = 引数・config 起因を使う。`loop` は起動時に同じ project の loop が走っていれば 3 で終わる (§13)。

## 4. log.jsonl

1 行 1 JSON object。append-only。2 種類の行がある。

### 4.1 tick 行

```json
{
  "ts": "2026-09-26T03:00:00.123456Z",
  "project": "myproj",
  "cwd": "/home/me/src/myproj",
  "result": "ok",
  "error": "…",
  "observed": {"issues": 12, "cls": 3},
  "candidates": 2,
  "wip": 1,
  "instructions": {"reenter": 1, "start": 1},
  "instruction_file": "<state root>/myproj/instructions/20260926T030000.123456Z.json",
  "orchestrator": {"exit_code": 0, "seconds": 41.2, "timed_out": false, "session_id": "<uuid>"},
  "spawned": [
    {"issue": 42, "kind": "start", "pid": 12345, "log": "<state root>/myproj/workers/42-20260926T030000.123456Z.log", "session_id": "<uuid>"}
  ]
}
```

| key | いつ載るか |
|---|---|
| `ts` / `project` / `cwd` / `result` | 常に。`ts` は tick の開始時刻。最終行の `ts` が最後に回った tick の時刻 |
| `error` | `result` が `ok` 以外のとき。複数行の error は ` / ` で 1 行に畳む |
| `observed` / `candidates` / `wip` / `instructions` | 観測に至った tick。`instructions` は指示の種別 → 件数 (0 件なら `{}`) |
| `instruction_file` | 観測に至った tick。指示 0 件なら `null` |
| `orchestrator` | claude を起動した tick だけ (この key の有無で起動したかを読む) |
| `spawned` | `orchestrator` があるとき。`result: error` でも起動済みの worker は載る |

- 想定外の失敗 (panic 等) で止まった tick も `result: error` の行を残し、**それまでに確定した key (`instruction_file` / `orchestrator` / 起動済みの `spawned`) を載せる**。tick は段階ごとに行の中身を積み、最後に 1 行で書き出すので、どこで止まっても claude と worker を起動済みかが行から読める
- `session_id` は CLI が起動ごとに発行して `--session-id` で渡した値。Claude Code の transcript `<Claude Code の設定 dir>/projects/*/<session_id>.jsonl` へ辿る鍵 (dir 名は Claude Code が cwd から決めるが、その規則は文書化されていないので glob で探す — §10 の ACTIVITY)

### 4.2 orchestrator 行

```json
{
  "ts": "2026-09-26T03:00:00.123456Z",
  "project": "myproj",
  "actor": "orchestrator",
  "instruction_file": "<state root>/myproj/instructions/20260926T030000.123456Z.json",
  "decisions": [{"issue": 42, "action": "start", "reason": "…"}]
}
```

`actor` の有無で tick 行と区別する。`ts` は同じ tick の tick 行と同じ値。決定ファイルの `decisions` をそのまま写す。

## 5. 指示ファイルと決定ファイル

### 5.1 指示ファイル (`instructions/<stem>.json`)

```json
{
  "snapshot": {
    "observed_at": "…", "issue_repo": "owner/name", "cl_repo": "owner/name",
    "limits": {"max_wip": 2, "wip_count": 1},
    "observed": {"issues": 12, "cls": 3},
    "issues": {
      "candidates": [{"number": 42, "title": "…", "url": "…", "body": "…"}],
      "wip": [{"number": 40, "title": "…", "url": "…"}],
      "ready_for_human": [38]
    },
    "linked_cls": {"40": [101]},
    "cls": [{"number": 101, "url": "…", "branch": "worktree-issue-40", "base": "main", "draft": false,
             "issues": [40], "mergeable": "MERGEABLE", "checks": "SUCCESS", "unresolved_threads": 0}]
  },
  "instructions": [
    {"kind": "reenter", "issue": 39, "cl": {"number": 100, "url": "…", "branch": "worktree-issue-39", "base": "main"},
     "conditions": [{"name": "conflict", "playbook": "/abs/…/playbook-conflict-resolution/SKILL.md"}]},
    {"kind": "start", "free_slots": 1, "candidates": [{"number": 42, "title": "…", "url": "…", "body": "…"}],
     "playbooks": [{"path": "/abs/…/playbook-implementation/SKILL.md", "dispatch_when": "…"}]},
    {"kind": "anomaly", "reason": "multiple_open_cls", "issues": [37], "cls": [98, 99]}
  ]
}
```

- 指示の並びは `reenter` → `start` → `anomaly`
- 候補だけが issue 本文 `body` を持つ (orchestrator の playbook 選定の信号)
- `linked_cls` と `cls[].issues` の紐づきは、closing reference が指す issue と、CL 置き場の repo 自身の head branch (fork でない) `worktree-issue-<issue>` が示す issue の和 (system.md §4)
- `mergeable` は CL を base へ merge できるか (`MERGEABLE` / `CONFLICTING` / `UNKNOWN`)
- `checks` は head commit の checks の集約 (`SUCCESS` / `PENDING` / `EXPECTED` / `FAILURE` / `ERROR` / `null`。`EXPECTED` は必須の checks がまだ報告されていない状態)
- `mergeable` / `checks` の語彙は本 file が正本で、CL host の綴りではない。CL 置き場の部品が host の応答をこの語彙へ写す (gh の応答は同じ綴り。system.md §13)
- `anomaly.reason` は `wip_over_limit` / `wip_and_ready_for_human` / `multiple_open_cls`。`multiple_open_cls` だけが `cls` を持つ
- 取得上限に達した tick は指示ファイルを書かず `result: error` にする — 切り詰めた像から指示を出すと、窓の外の open CL を持つ issue が候補へ戻って二重着手になる

### 5.2 決定ファイル (`decisions/<stem>.json`)

orchestrator が書く。path は起動時に渡される (指示ファイルと同じ `<stem>`)。

```json
{
  "decisions": [{"issue": 42, "action": "start", "reason": "<1 文>"}],
  "spawn": [{"issue": 42, "kind": "start", "prompt": "<spawn prompt の全文>", "playbooks": ["/abs/…/playbook-implementation/SKILL.md"]}]
}
```

| 指示 | 1 件ごとに採否が要る issue | 許される `action` |
|---|---|---|
| `start` | `candidates` の全件 | `start` / `skip` |
| `reenter` | `issue` | `reenter` / `skip` |
| `anomaly` | `issues` の全件 | `skip` / `ready-for-human` |

CLI は orchestrator の正常終了後に次を検査する (timeout / 異常終了なら file を読まない)。

| 検査 | 落ちたとき |
|---|---|
| file があり JSON として読める (spawn 0 件でも file は要る) | 1 件も起動せず `error` |
| `action` と `spawn[].kind` が上の語彙に入り、`spawn[].kind` が同じ issue の `action` と一致する | 1 件も起動せず `error` |
| `spawn[].issue` が重複しない (同じ issue の worker は worker log の path と作業ツリーの branch を取り合う) | 1 件も起動せず `error` |
| `kind: start` の spawn が start 指示の `free_slots` 件以下 (並列上限 N を起動前に守る) | 1 件も起動せず `error` |
| `spawn[].prompt` に未展開の変数 (`${`) が残っていない | 1 件も起動せず `error` |
| `spawn[].playbooks` (prompt に載せた playbook の絶対 path の列) の各 path が prompt 本文に含まれ、file として実在する。`start` は指示の選定母集合の 1 本、`reenter` は指示の条件の playbook を条件順に並べた列の部分列 (読み直しで外れた条件は落としてよい) | 1 件も起動せず `error` |
| 網羅: 上の表の issue 1 件ごとに採否がある | 書かれた `spawn` を起動してから `error` |

## 6. tick の失敗行

単発の `tick` が失敗したときに stderr へ出す 1 行 (loop の中の tick は出さず、loop の見出しに出す — §13):

```
<時刻 (UTC, 秒まで)> [<project>] tick=<log.jsonl の ts か -> result=<result> <error>
```

- `tick=-` は log.jsonl を書けなかった tick (state dir が無い等)
- 想定外の失敗 (panic 等) で止まった tick は、stack trace 等の後ろにこの 1 行を置く (`<error>` は `想定外の失敗で止まった: <内容>`)。末尾の行だけで読めるようにするため
- 正常な tick は何も書かない

## 7. `tick --dry-run` の stdout

成功時に stdout へ JSON 1 行:

```json
{"ts": "…", "project": "myproj", "dry_run": true, "result": "ok", "observed": {"issues": 12, "cls": 3}, "candidates": 2, "wip": 1, "instructions": {"start": 1}}
```

失敗時は stdout に何も出さず、stderr に §6 の書式で `tick=-` の 1 行を出して §3 の exit code で終わる。state dir には何も書かない。

## 8. 引き渡しコメント

orchestrator と worker が人へ返すときに issue へ書く本文。3 節で固定する。

```
## 停止理由
<何が分からない / 何に止められたか。permission に止められたなら止められた操作>

## ここまでの成果
<push 済み branch / draft CL の URL / 無ければ「なし」>

## 人が次にやること
- [ ] <解決に要る入力・判断・作業を 1 項目ずつ>
- [ ] 解決したら `ready-for-human` label を外す。open CL が無ければ次の tick から候補に戻る。open CL (draft 含む) が残っていれば候補には戻らない — CL を閉じるか人が引き継ぐ
```

## 9. `paths --json`

```
claude-dispatcher paths --json [<project>]
```

project を渡すと 1 project の置き場を、省略すると root と既存 project の一覧を返す。

```json
{"project": "myproj", "config_file": "/home/me/.config/claude-dispatcher/myproj/config.toml",
 "state_dir": "/home/me/.local/state/claude-dispatcher/myproj",
 "log_file": "/home/me/.local/state/claude-dispatcher/myproj/log.jsonl"}
```

```json
{"config_root": "/home/me/.config/claude-dispatcher", "state_root": "/home/me/.local/state/claude-dispatcher",
 "projects": ["myproj"]}
```

`projects` は config root の下で `config.toml` を持つ dir 名の昇順。

## 10. `status`

```
claude-dispatcher status ps [<project>]
claude-dispatcher status watch [<project>] [--interval <秒>]
```

project を省略すると、config root の下の全 project (§9 の `projects`) を並べる。`watch` は `ps` の表を `--interval` 秒 (既定 5、1 以上 86400 以下) ごとに描き直し、Ctrl-C で終わる。loop の画面 (§13) も同じ表を使う。実装 repo の clone を cwd にして撃つ (BRANCH 列は cwd の clone の作業ツリーを読む)。

- **読み取り専用**: state dir にも外部 store にも書かない。lock file も作らず、lock も取らない (取ると、その一瞬に重なった tick が `locked` の行を残し、起動しようとした loop が拒まれる)。loop が生きているかは process の一覧 (`claude-dispatcher … loop <project>` の process) で見る
- **走っている tick**: tick が lock を持つ間だけ置く `tick.now` を読む (下の「`tick.now`」)。file の `pid` の process が居ないか、その command 行が `claude-dispatcher tick <project>` / `claude-dispatcher loop <project> …` でなければ (pid の再利用)、異常終了で残った file として無視し、注記に残す。command 行は先頭 (argv[0]) から照合する (worker の command 行には spawn prompt の本文が載り、途中の綴りに当たりうる)。ただし process の一覧を読んだ後に書かれた file は、一覧に pid が写っていないだけなので、注記せずに出さない (次の描き直しで出る)。process の一覧を読めなければ確かめられないので出さない
- **載せる worker**: log.jsonl の tick 行の `spawned` のうち、次のどれかに当たるもの
  - issue ごとの最新の起動記録で、issue に `dispatcher:wip` が付いているか、process が生きているか、起動 (起動した tick の `ts`) から 24 時間以内のもの (24 時間は固定の値。起動記録に終了時刻は無いので起動から数える)
  - それより古い起動記録で、process が生きているもの

  wip は issue の今の worker にだけ掛ける (古い起動記録に掛けると、再入で起こし直した issue の前回の worker が stale に見える)。`running` と `stale` は時間と関係なく載り、終わった worker の結末 (`cl` / `human` / `silent`) は起動から 24 時間だけ載る。process の生死は起動部が見分ける。`claude -p` では pid の command 行に `session_id` が在るかで見る (pid は再利用される。system.md §13)。STATE が `stale` の行が stale wip の手掛かり (system.md §1)
- 外部 process (gh / git / claude / ps) が失敗した列は `?` にして表は出し、何を読めなかったかを `! <理由>` の注記行に残す
- **判断の行**: log.jsonl の最も新しい orchestrator 行 (§4.2) の `decisions` のうち、`action` が `skip` と `ready-for-human` のものを 1 件 1 行 `  #<issue> <action>: <reason>` で出し、その前に判断した時刻の行 `判断 <その行の ts (秒まで)>` を置く。`start` / `reenter` は出さない (採った issue は worker として表に出る)。その後の tick で orchestrator が起動されなくても、同じ判断を出し続ける。該当する decision が 0 件なら、時刻の行も出さない。`reason` の改行と制御文字 (ESC など) は空白にする (1 件 1 行を保ち、端末に制御文字を撃ち込ませない)。stdout が端末なら、行が端末の幅に収まるように reason を切り詰める (`#<issue> <action>:` は切らない)。端末でなければ切り詰めない。最も新しい orchestrator 行が、`actor` は読めるのに `ts` か `decisions` を読めなければ (読めない行として注記に数える)、それより古い判断は出さない。JSON として読めない行は orchestrator 行かどうかが分からないので、「最も新しい orchestrator 行」に数えない。端末が `#<issue> <action>:` より狭いと、行は端末の幅を超える

表は project ごとに 1 段 (見出し → 判断の行 → 注記行 → 表):

```
myproj  loop 稼働中  tick 実行中 · orchestrator 3m (上限 15m)  最終 tick 2026-09-26T03:00:00Z ok
判断 2026-09-26T03:00:00Z
  #43 skip: 仕様に受け入れ条件が無い
  ! <注記>
ISSUE  KIND   STATE    ELAPSED  SESSION  BRANCH  WIP  CL        TICK                  ACTIVITY
-      orch   running  3m       -        -       -    -         2026-09-26T03:05:00Z  4s Bash gh issue list
#42    start  running  12m      -        +3      yes  #57 OPEN  2026-09-26T02:48:00Z  12s Bash go test ./...
```

見出しの 2 欄目は、この project の loop の process が在れば `loop 稼働中`、無ければ `loop なし`、process の一覧を読めなければ `loop ?`。その後に、tick が走っている間だけ、loop の画面の 1 行目 (§13.1) と同じ綴りの状態欄を置く: `tick 実行中 · <tick の開始からの経過>`、orchestrator の実行中は `tick 実行中 · orchestrator <orchestrator の起動からの経過> (上限 15m)`。

orchestrator の実行中は、表の先頭に orchestrator の行を 1 行置く: ISSUE `-`・KIND `orch`・STATE `running`・ELAPSED は orchestrator の起動からの経過・SESSION は worker の行と同じく `claude agents --json` を orchestrator の session id で引く・BRANCH / WIP / CL `-`・TICK は tick の開始時刻・ACTIVITY は worker の行と同じく orchestrator の session id の transcript から。worker の行が無くても、この行があれば表を出す。

| 列 | 中身 |
|---|---|
| ISSUE / KIND / TICK | 起動記録の issue・kind・起動した tick の `ts` (秒まで) |
| STATE | worker の結末。下の順に判定し、最初に当たった値を出す: `running` (process が生きている) → `stale` (process は死んでいるが issue に wip が残っている。CL があっても `stale` — 人が回収すべきものを先に見せる) → `cl` (CL 列の CL が `OPEN` か `MERGED`) → `human` (issue に `ready-for-human` が付いている) → `silent` (どれでもない。無言の終了 — CONTEXT.md)。判定に要る値 (生死・wip・CL・`ready-for-human`) を読めなければ `?` にして注記を残す。`ready-for-human` は判定がそこまで進む行があるときだけ読む。綴りは機構の定数で config では変えられない |
| ELAPSED | 起動した tick からの経過 (`45s` / `12m` / `3h05m` / `2d04h`) |
| SESSION | `claude agents --json` に同じ session が居れば `<id> <status>/<state>`、居なければ `-` |
| BRANCH | cwd の clone に `worktree-issue-<issue>` の作業ツリーがあれば `origin/HEAD` からの ahead 数 (`+3`)、無ければ `-` |
| WIP | issue に `dispatcher:wip` が付いているか (`yes` / `no`) |
| CL | CL 置き場の repo 自身の head branch (fork でない) `worktree-issue-<issue>` の最新 CL (`#<番号> <state>`。state は `OPEN` / `CLOSED` / `MERGED`)、無ければ `-`。同じ名前の branch の CL を新しい順に 10 本まで読み、fork の CL を読み飛ばす。10 本とも fork の CL でまだ続きがあれば、その issue だけ `?` にして注記を残す |
| ACTIVITY | 最新の活動 `<最後の event からの経過> <内容>` (`12s Bash go test ./...` / `3m Edit internal/x.go`)。STATE が `running` の行と orchestrator の行だけに出し、それ以外は `-`。下の「ACTIVITY」 |

**ACTIVITY**: Claude Code の transcript から読む。worker log は `claude -p` の text 出力で終了まで 0 byte のことがあるが、transcript は走っている間も書き足される。

- 探し方: `<Claude Code の設定 dir>/projects/*/<session_id>.jsonl` を glob で探す。設定 dir は `CLAUDE_CONFIG_DIR` があればそれ、無ければ `~/.claude`。cwd から dir 名を作る Claude Code の規則 (文書化されていない) は再現しない。`session_id` は起動記録 (tick 行の `spawned[].session_id`)、orchestrator の行は `tick.now` の `orchestrator.session_id`
- 当たった file が複数あれば、最後に書かれたもの (mtime が最新) を読む
- file の末尾 (256KiB) だけを読み、最後の完全な行から後ろ向きに読む (transcript は大きくなるため)。末尾の行が大きく (画像の tool_result 等) その範囲に時刻を持つ行が無ければ、4MiB で読み直す
- 経過は、時刻 (`timestamp`) を持つ最後の行からの経過。綴りは ELAPSED と同じ
- 内容は、最後の tool 呼び出し (tool 名と主な引数: `Bash` は command の 1 行目、`Edit` / `Write` / `Read` は file の path、それ以外は tool 名だけ)。最後の tool 呼び出しの後に assistant の発話があれば、発話の 1 行目。読んだ範囲にどちらも無ければ経過だけ (tool の引数を読めなければ tool 名だけ)。端末へ出すので、制御文字 (ESC・tab・CR 等) は空白に置き換える
- stdout が端末なら、行が端末の幅に収まるように切り詰める。端末でなければ、内容を 60 文字で切り詰める
- transcript の中身の形は Claude Code の内部の仕様で、それへの依存はこの列 1 つに閉じる。file が見つからないか、形が読めない (時刻を持つ行が 1 つも読めない) か、起動記録に session id が無ければ、その行の ACTIVITY を `?` にして注記を残す。ほかの列には影響させない

`tick.now` は JSON 1 行 (key は下表)。tick の 1 回分が lock を取った直後に書き、段階が変わるたびに書き直し (同じ dir の一時 file から rename する。読み手に書きかけを見せない)、tick 行を書いた後、lock を外す前に消す。error・panic・loop の停止要求で終わる経路でも消す。単発の `tick` を signal で止めたときと、process が kill されたときは残る — status は pid の照合でそれを無視する。`tick --dry-run` と、`setup` / `doctor` の試運転は書かない (state dir に何も書かない — §7 / §12)。tick は読まない — 指示の導出にも lock の判定にも使わない、表示のための痕跡。書けないか消せなければ、tick は止めずに (result も変えずに) 理由を 1 行ずつ残す。単発の `tick` は stderr に、loop は画面の `!` 行 (§13.1) に出す。

| key | 中身 |
|---|---|
| `pid` | tick を走らせている process (単発の `tick` か `loop`) |
| `ts` | tick の開始時刻 (tick 行の `ts` と同じ値) |
| `stage` | `observe` (観測中) / `orchestrator` (orchestrator 実行中) / `spawn` (orchestrator の終了の後、決定の検査と worker の起動中。正常終了でなければ、tick 行を書いて終わるまでの間) |
| `orchestrator` | `stage` が `orchestrator` のときだけ在る。`started` (起動時刻。`ts` と同じ綴り)・`session_id` |

exit: 0。指定した project が無い / project が 1 つも無いときは 2。

## 11. `setup`

```
claude-dispatcher setup <project>
```

実装 repo の clone を cwd にして撃つ。次の段を順に進め、段が済んでいれば何もせず次へ進む (再実行で同じ終状態に収束する)。

1. **宣言 config の雛形と state dir**: config.toml が無ければ、雛形 (`[issue].repo` は cwd の clone の origin、`ready_label = "ready-for-agent"`、`max_wip = 1`) と state dir を作り、「埋めてから撃ち直す」と示して止まる。config.toml があれば上書きしない (state dir が無ければ作る)
2. **config の検査**: `tick` と同じ検査。落ちたら名指しで止まる
3. **label**: issue 置き場に `dispatcher:wip` / `ready-for-human` / 着手可 label / (書いてあれば) triage label が無ければ、作る label を示して承認を尋ね、承認されたら作る
4. **試運転**: `tick <project> --dry-run` と同じ試運転を行い、出力をそのまま示す
5. **loop の起動コマンド**: 試運転が通ったら `cd <clone> && claude-dispatcher loop <project> 5m` を示して終わる。setup は loop を起動しない

- **承認は stdin から `y` / `yes` を受けたときだけ**。それ以外 (空行・EOF・端末の無い実行) は承認なしとして書かず、自分で撃つコマンドを示して止まる
- Claude Code の settings は書かない (`doctor` が要る entry を示す)

exit: 0 = 試運転が通り、loop の起動コマンドを示して終わった / 1 = 途中で止まった (雛形を書いた・承認されなかった・gh の失敗) / 2 = 引数か config の誤り。試運転が落ちたときは試運転の exit code (§3) をそのまま返す。

## 12. `doctor`

```
claude-dispatcher doctor <project>
```

導入の充足を検査して 1 項目 1 行で示す。**何も書かない** (state dir・config・settings・外部 store のどれにも)。

```
--  版            v0.3.0 (<commit>)
ok  config        <config.toml の path>
NG  label         置き場 acme/widgets に dispatcher:wip が無い — `claude-dispatcher setup myproj` で作る
--  最終 tick     2026-09-26T03:00:00Z ok
```

| 項目 | 見るもの |
|---|---|
| `版` | この binary の版と commit (情報。判定しない)。綴りは §14 |
| `config` | config.toml が在り、`tick` と同じ検査に通る |
| `state dir` | state dir が在る |
| `依存 CLI` | gh / claude / git が (PATH の自己解決の後で) 見つかる |
| `置き場` | issue 置き場 (と CL 置き場) が gh から見える |
| `label` | `dispatcher:wip` / `ready-for-human` / 着手可 label / (書いてあれば) triage label が在る |
| `plugin` | plugin `swat-skills` が system.md §11 の選択順で 1 つに決まる (別 marketplace の重複は NG) |
| `playbook` | 条件カタログ (system.md §6) の playbook が全部在る。start の選定母集合の本数も示す (0 本は `--`。tick は start を出さないだけで動く) |
| `原則索引` | 原則索引の file が在る |
| `試運転` | `tick <project> --dry-run` と同じ試運転 (state dir に何も書かない) が exit 0 で終わる。NG なら出力を添える |
| `最終 tick` | log.jsonl の最後の tick 行の `ts` と `result`、読めない行があればその件数 (情報。判定しない) |

先頭の印は `ok` (充足) / `NG` (不足。理由と直し方を添える) / `--` (情報)。項目の後に、Claude Code の settings に要る entry (sandbox の `filesystem.allowWrite` に state dir、`excludedCommands` に `gh` と `claude-dispatcher`) を示す。

exit: 0 = `NG` が無い / 1 = `NG` がある / 2 = 引数の誤り。

## 13. `loop`

```
claude-dispatcher loop <project> <interval>
```

実装 repo の clone を cwd にして撃つ。`<interval>` は Go の duration の綴り (`90s` / `5m` / `1h30m`) で、1m 以上 24h 以下。起動直後に tick を 1 回撃ち、以後は tick の終了から `<interval>` 待って次を撃つ。tick の `result` が何であっても次の周期へ進む。tick は単発の `tick` と同じ 1 回分で、log.jsonl に同じ形の行を残す (§4)。

**起動時の検査** (落ちたら画面を出さず、stderr に理由を 1 行出して終わる):

| 検査 | exit |
|---|---|
| 引数の数・project 名の綴り・`<interval>` の綴りと範囲 | 2 |
| config.toml が在る | 2 |
| state dir が在る (`setup` が作る) | 1 |
| 同じ project の loop が走っていない (`loop.lock` を取れる) | 3 |

config の中身は起動時に検査しない。tick ごとに読み直し、落ちた tick は `config_error` として見出しに出る (直せば撃ち直さずに戻る)。

### 13.1 画面

stdout が端末のとき、`status ps <project>` の表 (§10) の見出し行を loop の見出しに差し替え、末尾に操作案内を足した画面を描く。15 秒ごと・tick の始まりと直後・停止を求められたときに、画面を消して (`\033[H\033[2J`) 描き直す。

```
myproj  loop 5m  待機 · 次の tick 2026-09-26T03:05:00Z (あと 3m)
最終 tick 2026-09-26T03:00:00Z ok · 指示 start 1 · 起動 #42
判断 2026-09-26T03:00:00Z
  #43 skip: 仕様に受け入れ条件が無い
  ! <注記>
ISSUE  KIND   STATE    ELAPSED  SESSION  BRANCH  WIP  CL        TICK                  ACTIVITY
#42    start  running  12m      -        +3      yes  #57 OPEN  2026-09-26T02:48:00Z  12s Bash go test ./...

Ctrl+C で停止
```

| 行 | 中身 |
|---|---|
| 1 行目 | project・`loop <interval>`・状態 (下表) |
| `最終 tick` | loop が回した直近の tick の `ts`・`result`・指示の種別と件数 (0 件なら `指示 0`)・起動した worker の issue (無ければ省く)。まだ 1 回も終えていなければ `最終 tick なし` |
| 判断の行 | `status` と同じ (§10)。出す判断が無ければ出さない |
| `!` 行 | 直近の tick の `error` (`result` が `ok` 以外のとき。tick 行を log.jsonl に書けなかったときは `result` に関わらず、書けなかった理由を含めて出す)、直近の tick が `tick.now` を書けなかった・消せなかった理由 (§10)、`status` の注記 (§10) |
| 表 | `status` と同じ (§10。ACTIVITY 列を含み、端末なら幅に収める)。orchestrator の実行中はその行も出る。載せる行が無ければ出さない |
| 最終行 | 操作案内 (下表) |

| 状態 | 1 行目の状態欄 | 操作案内 |
|---|---|---|
| 待機 | `待機 · 次の tick <時刻> (あと <残り>)` | `Ctrl+C で停止` |
| tick 実行中 | `tick 実行中 · <経過>`。orchestrator を待つ間は `tick 実行中 · orchestrator <経過> (上限 15m)` | `Ctrl+C: この tick を終えてから停止` |
| 停止待ち | `停止待ち · ` に tick 実行中と同じ経過 | `停止待ち: この tick を終えたら止まる。もう一度 Ctrl+C で orchestrator を止めて止まる (付いた wip は残りうる)` |
| 停止 (止まった後に残す画面) | `停止` | (出さない) |

`<残り>` と `<経過>` は §10 の ELAPSED と同じ綴り。

stdout が端末でないときは画面を消さず、tick の直後と止まったときにだけ、操作案内を除いた同じ中身を空行で区切って追記する。

### 13.2 停止

SIGINT / SIGTERM / SIGHUP を同じに扱う。

| 受けたとき | 振る舞い |
|---|---|
| 1 回目・tick の合間 | tick を始めずに止まる |
| 1 回目・tick の実行中 | 状態を停止待ちにし、その tick を最後まで進めて (orchestrator の終了を待ち、決定どおり worker を起動し、tick 行を書いて) から止まる |
| 2 回目・orchestrator の起動前か起動中 | 起動前ならそれを起動せず、起動中ならその process group を止め、決定ファイルを読まずに `result: error` の tick 行を書いて止まる |
| 2 回目・orchestrator の正常終了後 | 1 回目と同じ。決定ファイルの検査と決定どおりの worker の起動を終え、tick 行を書いてから止まる (起動を途中で打ち切ると、付いた wip が worker の無いまま残る) |

- 起動済みの worker はどちらの停止でも止めない
- 止まったら操作案内を除いた画面を描いて残し (1 行目の状態欄は `停止`)、終了行を 1 行足す

  ```
  <時刻 (UTC, 秒まで)> [<project>] loop を止めた (<理由>)。止めずに走っている worker: <n> 本
  ```

  - `<理由>` は `停止要求 <signal 名>` (例 `停止要求 SIGINT`)。2 回目の停止要求で止めたときは `2 回目の停止要求で orchestrator を止めた — 経過は <orchestrator log の path>。wip を付けたまま残った issue が無いか確かめる` (orchestrator を起動する前なら `2 回目の停止要求で orchestrator を起動せずに止めた`)
  - `<n>` は `status` の worker の行のうち STATE が `running` の数 (orchestrator の行は数えない)。process の一覧か log.jsonl を読めなければ `?`。止まる前の最後の現況を組んでいる間に次の停止要求が来たら、組むのを待たずに直前の現況から数える
- 2 回目の停止要求で止めた tick 行の `error` は `停止要求で orchestrator を止めた` か `停止要求で orchestrator を起動しなかった`
- stdout への書き込みの失敗 (読み手の消えた pipe 等) では止まらない。描画を捨てて続け、停止要求で止まる。終了行を stdout に書けなければ stderr に出す
- 走っている間の想定外の失敗 (panic) は stack trace を stderr に出し、tick なら `result: error` の最終 tick として見出しに、status の現況なら注記に載せて続ける (端末では次の描き直しで stack trace が画面から消えうるので、見出しと注記が残る観測点)

exit: 0 = 停止要求で止まった / 1・2・3 = 起動時の検査 (上表)。想定外の失敗で loop 自身が止まったときは 1。

## 14. `--version`

```
claude-dispatcher --version
```

binary の版と commit を stdout に 1 行で出す。config も state dir も読まないので、導入が壊れていても撃てる (不具合の報告に貼る)。

```
claude-dispatcher v0.3.0 (<commit の hash>)
```

- 版: Releases の binary は GoReleaser が埋めた tag (`v0.3.0`)。`go install <module>@<版>` の binary は module の版 (同じ `v0.3.0`)。git の作業ツリーでの `go build` は Go が VCS から刻む pseudo-version (直近の tag の次の patch の `-0.<日時>-<hash>`。例: `v0.1.1-0.20260927163012-aac3e1c06160`、未 commit の変更があれば `+dirty`)。VCS の情報が無い build (`go run`・`-buildvcs=false` 等) は `(devel)`
- commit: Releases の binary は tag の commit。git の作業ツリーでの `go build` は build 情報の `vcs.revision`。`go install <module>@<版>` の binary は module cache から build するので commit を持たず `unknown`。VCS の情報が無い build も `unknown`
- doctor の `版` の行 (§12) も同じ綴りを出す

exit: 0。
