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
  cron.log                            crontab の行が append する stdout / stderr (§6)
  tick.lock                           flock の対象
  config-verified                     実在検査に通った config.toml の sha256 (hex 1 行)
  instructions/<stem>.json            指示ファイル (§5.1)。指示があった tick だけ
  decisions/<stem>.json               決定ファイル (§5.2)
  decisions/<stem>.orchestrator.log   orchestrator の stdout / stderr
  decisions/<stem>.handoff-<issue>.md orchestrator が人へ返したときの引き渡し本文
  workers/<issue>-<stem>.log          worker の stdout / stderr
```

- `<project>` は `[A-Za-z0-9._-]+`。crontab の行と subcommand の引数で同じ綴りを使う
- `<stem>` は tick の開始時刻 (UTC) の `YYYYMMDDTHHMMSS.ffffffZ`。マイクロ秒まで入れるのは、同じ秒に 2 tick 走ったときに前の file を上書きしないため
- 時刻は RFC 3339 の UTC (`Z` 表記) で、log.jsonl の `ts` はマイクロ秒まで、cron.log の前置は秒まで

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

[auth]                         # 任意。cron から認証が読めない環境だけ書く。書くなら 2 key の少なくとも 1 つ
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
| 1 | `error` | 観測できなかった / 指示を導出できなかった / orchestrator が正常終了しなかった / claude を起動できなかった / 決定ファイルの検査に落ちた / 想定外の失敗 |
| 2 | `config_error` | config 起因で観測していない (無い / 読めない / 未知 key / 置き場や label が実在しない / token file の不備) |
| 3 | `locked` | 前 tick が走っていたので見送った |
| 4 | `auth_error` | gh の認証が通らず観測していない (綴りを直しても直らない) |

`tick` 以外の subcommand も 0 = 成功、1 = 失敗、2 = 引数・config 起因を使う。

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
| `ts` / `project` / `cwd` / `result` | 常に。`ts` は tick の開始時刻。最終行の `ts` が死活の手掛かり |
| `error` | `result` が `ok` 以外のとき。複数行の error は ` / ` で 1 行に畳む |
| `observed` / `candidates` / `wip` / `instructions` | 観測に至った tick。`instructions` は指示の種別 → 件数 (0 件なら `{}`) |
| `instruction_file` | 観測に至った tick。指示 0 件なら `null` |
| `orchestrator` | claude を起動した tick だけ (この key の有無で起動したかを読む) |
| `spawned` | `orchestrator` があるとき。`result: error` でも起動済みの worker は載る |

- 想定外の失敗 (panic 等) で止まった tick も `result: error` の行を残し、**それまでに確定した key (`instruction_file` / `orchestrator` / 起動済みの `spawned`) を載せる**。tick は段階ごとに行の中身を積み、最後に 1 行で書き出すので、どこで止まっても claude と worker を起動済みかが行から読める
- `session_id` は CLI が起動ごとに発行して `--session-id` で渡した値。Claude Code の transcript `~/.claude/projects/<cwd から Claude Code が決める dir 名>/<session_id>.jsonl` へ辿る鍵で、`cwd` と組で引く

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
- `linked_cls` と `cls[].issues` の紐づきは、closing reference が指す issue と、head branch `worktree-issue-<issue>` が示す issue の和 (system.md §4)
- `checks` は head commit の checks の集約 (`SUCCESS` / `PENDING` / `FAILURE` / `ERROR` / `null`)
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
| `spawn[].prompt` に未展開の変数 (`${`) が残っていない | 1 件も起動せず `error` |
| `spawn[].playbooks` (prompt に載せた playbook の絶対 path の列) の各 path が prompt 本文に含まれ、file として実在する。`start` は指示の選定母集合の 1 本、`reenter` は指示の条件の playbook を条件順に並べた列の部分列 (読み直しで外れた条件は落としてよい) | 1 件も起動せず `error` |
| 網羅: 上の表の issue 1 件ごとに採否がある | 書かれた `spawn` を起動してから `error` |

## 6. cron.log の行

CLI が失敗 tick で stderr に出す 1 行:

```
<時刻 (UTC, 秒まで)> [<project>] tick=<log.jsonl の ts か -> result=<result> <error>
```

- `tick=-` は log.jsonl を書けなかった tick (state dir が無い等)
- 想定外の失敗 (panic 等) で止まった tick は、stack trace 等の後ろにこの 1 行を置く (`<error>` は `想定外の失敗で止まった: <内容>`)。末尾の行だけで読めるようにするため
- 正常な tick は何も書かない。前置の無い行は CLI 以外 (shell) が出したもの

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
claude-dispatcher status ps [<project>] [--json]
claude-dispatcher status watch [<project>] [--interval <秒>]
```

project を省略すると、config root の下の全 project (§9 の `projects`) を並べる。`watch` は `ps` の表を `--interval` 秒 (既定 5、1 以上 86400 以下) ごとに描き直し、Ctrl-C で終わる。実装 repo の clone を cwd にして撃つ (BRANCH 列は cwd の clone の作業ツリーを読む)。

- **読み取り専用**: state dir にも外部 store にも書かない。lock file も作らず、lock も取らない (取ると、その一瞬に重なった tick が `locked` の行を残す)。tick が走っているかは process の一覧 (`claude-dispatcher … tick <project>` の process) で見る
- **載せる worker**: log.jsonl の tick 行の `spawned` のうち、issue ごとの最新の起動記録で issue に `dispatcher:wip` が付いているか process が生きているもの、と、それより古い起動記録で process が生きているもの (wip は issue の今の worker にだけ掛ける。古い起動記録に掛けると、再入で起こし直した issue の前回の worker が stale wip に見える)。process の生死は pid の command 行に `session_id` が在るかで見る (pid は再利用される)。process が死んでいて wip が残っている行が stale wip の手掛かり (system.md §1)
- 外部 process (gh / git / claude / ps) が失敗した列は `?` にして表は出し、何を読めなかったかを `! <理由>` の注記行に残す

表は project ごとに 1 段:

```
myproj  待機  最終 tick 2026-09-26T03:00:00Z ok
  ! <注記>
ISSUE  KIND   STATE    ELAPSED  SESSION  BRANCH  WIP  CL        TICK
#42    start  running  12m      -        +3      yes  #57 OPEN  2026-09-26T02:48:00Z
```

| 列 | 中身 |
|---|---|
| ISSUE / KIND / TICK | 起動記録の issue・kind・起動した tick の `ts` (秒まで) |
| STATE | `running` / `exited` |
| ELAPSED | 起動した tick からの経過 (`45s` / `12m` / `3h05m` / `2d04h`) |
| SESSION | `claude agents --json` に同じ session が居れば `<id> <status>/<state>`、居なければ `-` |
| BRANCH | cwd の clone に `worktree-issue-<issue>` の作業ツリーがあれば `origin/HEAD` からの ahead 数 (`+3`)、無ければ `-` |
| WIP | issue に `dispatcher:wip` が付いているか (`yes` / `no`) |
| CL | head branch `worktree-issue-<issue>` の最新 CL (`#<番号> <state>`)、無ければ `-` |

`--json` は同じ内容を次の形で出す (読めなかった値は `"?"`)。

```json
{"projects": [{"project": "myproj",
  "tick": {"running": false, "last_ts": "2026-09-26T03:00:00.123456Z", "last_result": "ok"},
  "workers": [{"issue": 42, "kind": "start", "pid": 12345, "state": "running", "elapsed_sec": 720, "wip": true,
               "tick_ts": "2026-09-26T02:48:00.000000Z", "session": null, "branch": {"name": "worktree-issue-42", "ahead": 3},
               "cl": {"number": 57, "state": "OPEN"}}],
  "notes": []}]}
```

exit: 0。指定した project が無い / project が 1 つも無いときは 2。

## 11. `setup`

```
claude-dispatcher setup <project>
```

実装 repo の clone を cwd にして撃つ。次の段を順に進め、段が済んでいれば何もせず次へ進む (再実行で同じ終状態に収束する)。

1. **宣言 config の雛形と state dir**: config.toml が無ければ、雛形 (`[issue].repo` は cwd の clone の origin、`ready_label = "ready-for-agent"`、`max_wip = 1`) と state dir を作り、「埋めてから撃ち直す」と示して止まる。config.toml があれば上書きしない (state dir が無ければ作る)
2. **config の検査**: `tick` と同じ検査。落ちたら名指しで止まる
3. **label**: issue 置き場に `dispatcher:wip` / `ready-for-human` / 着手可 label / (書いてあれば) triage label が無ければ、作る label を示して承認を尋ね、承認されたら作る
4. **試運転**: `tick <project> --dry-run` と `tick <project> --dry-run --cron-env` を順に撃ち、出力をそのまま示す
5. **crontab**: 次の 1 行を組み、`crontab -l` と突き合わせる

   ```
   */5 * * * * cd <clone> && <claude-dispatcher の絶対 path> tick <project> >> <state dir>/cron.log 2>&1
   ```

   同じ行があれば済み。この project の tick 行 (`tick <project>` を含む行) が別の形であれば、現行の行と組んだ行を並べて示すだけで**置き換えない**。無ければ足す行を示して承認を尋ね、承認されたら現行の表に 1 行足して登録し、`crontab -l` で登録を確かめる

- **承認は stdin から `y` / `yes` を受けたときだけ**。それ以外 (空行・EOF・端末の無い実行) は承認なしとして書かず、自分で撃つコマンドを示して止まる
- Claude Code の settings は書かない (`doctor` が要る entry を示す)

exit: 0 = crontab に tick 行がある状態で終わった / 1 = 途中で止まった (雛形を書いた・承認されなかった・gh の失敗・既存の tick 行と食い違う) / 2 = 引数か config の誤り。試運転が落ちたときは試運転の exit code (§3) をそのまま返す。

## 12. `doctor`

```
claude-dispatcher doctor <project>
```

導入の充足を検査して 1 項目 1 行で示す。**何も書かない** (state dir・config・settings・crontab・外部 store のどれにも)。

```
ok  config        <config.toml の path>
NG  label         置き場 acme/widgets に dispatcher:wip が無い — `claude-dispatcher setup myproj` で作る
--  最終 tick     2026-09-26T03:00:00Z ok
```

| 項目 | 見るもの |
|---|---|
| `config` | config.toml が在り、`tick` と同じ検査に通る |
| `state dir` | state dir が在る |
| `依存 CLI` | gh / claude / git が (PATH の自己解決の後で) 見つかる |
| `置き場` | issue 置き場 (と CL 置き場) が gh から見える |
| `label` | `dispatcher:wip` / `ready-for-human` / 着手可 label / (書いてあれば) triage label が在る |
| `plugin` | plugin `swat-skills` が system.md §11 の選択順で 1 つに決まる (別 marketplace の重複は NG) |
| `playbook` | start の選定母集合が 1 本以上あり、条件カタログ (system.md §6) の playbook が全部在る |
| `原則索引` | 原則索引の file が在る |
| `crontab` | この project の tick 行がある |
| `最終 tick` | log.jsonl の最後の tick 行の `ts` と `result` (情報。判定しない) |

先頭の印は `ok` (充足) / `NG` (不足。理由と直し方を添える) / `--` (情報)。項目の後に、Claude Code の settings に要る entry (sandbox の `filesystem.allowWrite` に state dir、`excludedCommands` に `gh` と `claude-dispatcher`) を示す。

exit: 0 = `NG` が無い / 1 = `NG` がある / 2 = 引数の誤り。
