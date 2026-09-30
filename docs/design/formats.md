# 外から観測できる形式

> **移行中 (ADR 0009、#74。この注記は #83 で外す)**: 本 file は、作り直しの slice (#77〜#82) が形式を決めるたびに節ごとに書き足す。まだ決まっていない節は「未定」と書き、その節を決める issue を示す。新しい設計と食い違うところでは、[`system.md`](system.md) が優先する。
>
> 作り直しの間は、互換を保たない (ADR 0009 の「悪い影響 / 制約」)。下の「公開契約」も、新しい形式が決まるまでは適用しない。

- 本 file が CLI の外から観測できる形式の正本。black-box テストはここを参照して書き、実装とテストが食い違ったら本 file に合わせる
- 各 file の責務は [`system.md`](system.md) §7 / §8 / §9、用語は [`CONTEXT.md`](../../CONTEXT.md)
- **公開契約**: log.jsonl の行 (§4) と `paths --json` (§7) は外部の読み手を持つ。key の削除・改名・意味の変更は互換を壊すので、足すだけにとどめるか、ADR を起こす

## 1. 置き場

| 変数 | 既定 |
|---|---|
| state root | `${XDG_STATE_HOME:-$HOME/.local/state}/claude-dispatcher` |

macOS でも XDG に揃える (`~/Library` は使わない)。scope key ごとに state dir を持つ。

```
<state root>/<scope dir>/
  loop.lock                           loop の生存期間の flock の対象 (§6)
  log.jsonl                           tick と worker の起動・終わり方の行 (§4)
  prompts/issue-<番号>.md             描画した共通 prompt。worker に --append-system-prompt-file で渡す
  workers/issue-<番号>.log            worker の stdout と stderr。attempt を跨いで追記する
```

workspace は state dir の外、workflow 定義の `workspace.root` の下に作業対象ごとに置く (§2.1)。

```
<workspace root>/issue-<番号>/        作業対象の workspace。作業対象が終端になるまで残す
```

- **scope key**: issue 置き場の部品が決める、issue 置き場の識別子 (system.md §13)
  - GitHub は `github.com/<owner>/<name>` を小文字にしたもの (GitHub の owner と repo の名前は大文字と小文字を区別しない)
- **`<scope dir>`**: scope key を無害化した綴りに、scope key の短い hash を足した名前
  - 無害化: 小文字にし、`[a-z0-9._-]` 以外の文字を `_` に置き換える
  - hash: scope key の sha256 の hex の先頭 8 文字
  - 例: scope key `github.com/acme/widgets` → `github.com_acme_widgets-<hash 8 文字>`
  - hash を足すのは、無害化で別の scope key が同じ綴りになっても、置き場を分けるため
- state dir は loop が起動時に作る。試運転 (§5) は作らない
- 時刻は RFC 3339 の UTC (`Z` 表記) で、秒まで

## 2. workflow 定義 (`WORKFLOW.md`)

実装 repo の中に置く。path は `loop` の引数で渡し、省けば cwd の `WORKFLOW.md` を読む (§6)。

file は YAML の front matter と本文からなる。

- 1 行目が `---` の行で、次の `---` の行までが front matter
- その後ろが本文。本文は共通 prompt (system.md §10)。空でもよい

```markdown
---
tracker:
  kind: github               # 必須。初版は github だけ
  repo: acme/widgets         # 必須。issue 置き場 (owner/name)。$VAR で書ける
  token: $WIDGETS_GH_TOKEN   # 任意。gh に GH_TOKEN として渡す。$VAR でだけ書ける
polling:
  interval: 5m               # 任意。周期。既定 5m
workspace:
  root: .claude-dispatcher/workspaces   # 任意。既定はこの値 (workflow 定義の dir からの相対)
hooks:                       # 任意。どれも省ける
  after_create: git -C "$CLAUDE_DISPATCHER_CLONE" worktree add "$CLAUDE_DISPATCHER_WORKSPACE"
  before_run: git fetch
  before_remove: git -C "$CLAUDE_DISPATCHER_CLONE" worktree remove --force "$CLAUDE_DISPATCHER_WORKSPACE"
  timeout: 60s
limits:
  max_concurrent: 1          # 任意。並列上限。既定 1
  max_attempts: 3            # 任意。作業対象 1 件の attempt の上限。既定 3
  max_retry_backoff: 5m      # 任意。backoff の上限。既定 5m
  stall_timeout: 15m         # 任意。worker の stream が途絶えてから止めるまで。既定 15m。0 で無効
  run_timeout: 1h            # 任意。worker 1 回分の上限時間。既定 1h。0 で無効
claude:
  command: claude            # 任意。既定 claude
  args: [--permission-mode, auto]   # 任意。既定は空
triggers:                    # 必須。1 つ以上
  - name: implement          # 必須
    on: issue                # 必須
    when:                    # 任意。省くと、open な issue のすべてに当たる
      labels:
        all: [ready-for-agent]
        none: [needs-info]
      unassigned: true
      author: collaborator
      blocked: false
    action: |                # 必須。worker に渡す prompt の template
      /swat-skills:playbook-implementation
---
この project の共通 prompt。
```

### 2.1 項目

| key | 型 | 中身 |
|---|---|---|
| `tracker.kind` | 文字列 | tracker の種類。`github` だけ |
| `tracker.repo` | 文字列 | issue 置き場。`<owner>/<name>` |
| `tracker.token` | 文字列 | gh に環境変数 `GH_TOKEN` として渡す token。`$VAR` でだけ書ける (値そのものを書かない)。省くと、loop を起動した環境の認証を gh がそのまま使う |
| `polling.interval` | 文字列 | 周期。Go の duration の綴り (`90s` / `5m` / `1h30m`) で、1s 以上 24h 以下。短い周期は tracker の rate limit を食う |
| `workspace.root` | 文字列 | workspace を置く dir。相対 path は workflow 定義の dir から、`~/` は HOME から (HOME が絶対 path でなければ失敗)。`$VAR` で書ける |
| `hooks.after_create` | 文字列 | workspace を作った直後に撃つ shell script。失敗したら workspace を消し、その attempt は失敗 |
| `hooks.before_run` | 文字列 | worker を起動する前に毎回撃つ。失敗したら、その attempt は失敗 |
| `hooks.after_run` | 文字列 | worker が終わった後 (止めたときも) に毎回撃つ。失敗は log に残して続ける |
| `hooks.before_remove` | 文字列 | workspace を消す前に撃つ。失敗は log に残して続ける |
| `hooks.timeout` | 文字列 | hook 1 回の上限時間。既定 60s。超えたら process group ごと止め、失敗として扱う |
| `limits.max_concurrent` | 整数 | 同時に走らせる worker の上限。1 以上。既定 1 |
| `limits.max_attempts` | 整数 | 作業対象 1 件の attempt の上限。1 以上。既定 3。上限の attempt が失敗したら打ち切る |
| `limits.max_retry_backoff` | 文字列 | backoff の上限。Go の duration の綴りで、0 より長い。既定 5m |
| `limits.stall_timeout` | 文字列 | worker の stream (stdout の stream-json) が途絶えてから、止めて失敗とするまでの時間。既定 15m。`0s` で無効。claude は 1 つの tool の実行中は stream に何も書かないので、長い build や test を走らせる repo では長めにする |
| `limits.run_timeout` | 文字列 | worker 1 回分の上限時間。起動からの経過が超えたら止めて失敗とする。既定 1h。`0s` で無効 |
| `claude.command` | 文字列 | worker として起動する command。PATH から探す。既定 `claude` |
| `claude.args` | 文字列の列 | command に、dispatcher の引数 (§6「worker の起動」) より前に渡す引数。既定は空 |
| `triggers` | 列 | trigger の宣言。宣言順が起動の優先順 (system.md §6) |
| `triggers[].name` | 文字列 | trigger の名前。`[A-Za-z0-9._-]+`。trigger の間で重複しない |
| `triggers[].on` | 文字列 | 作業対象の種類。`issue` |
| `triggers[].when` | 対応表 | 述語。書いた条件はすべて AND で評価する (§2.2) |
| `triggers[].action` | 文字列 | worker に渡す prompt の template (§2.6)。空にできない |

次の項目は、受け持つ slice が決めるまで書けない (書くと未知の key として失敗する)。

- CL 側の trigger (`on: cl`): #80

**YAML の読み方**: 値を書いていない key (`when:` だけの行) は、空の対応表として読む。anchor と alias は辿る。merge key (`<<: *base`) は持たない (`<<` は未知の key として失敗する)。

### 2.2 issue 側の述語 (`on: issue` の `when`)

| key | 型 | 当たる issue |
|---|---|---|
| `labels.all` | 文字列の列 | 列の label をすべて持つ |
| `labels.any` | 文字列の列 | 列の label のどれかを持つ。空の列は書けない |
| `labels.none` | 文字列の列 | 列の label をどれも持たない |
| `assignee` | 文字列 | その login の人が assignee に居る |
| `unassigned` | 真偽 | `true` なら assignee が居ない、`false` なら 1 人以上居る |
| `author` | 文字列 | `collaborator` なら作者が collaborator (repo の owner・organization の member・collaborator)、`non_collaborator` ならそれ以外 |
| `milestone` | 文字列 | その題名の milestone に入っている |
| `blocked` | 真偽 | `true` なら未解決 (open) の依存先 (blocked by) が 1 つ以上ある、`false` なら無い |

- `assignee` と `unassigned` は一緒に書けない
- label と login の綴りは、大文字と小文字を区別せずに比べる (GitHub と同じ)

### 2.3 評価の規則

- issue ごとに trigger を宣言順に評価し、最初に当たった 1 つだけを採る
- 候補は、trigger の宣言順を先に、同じ trigger の中では issue の作成日時の古い順に並べる。作成日時が同じなら番号の小さい順

### 2.4 `$VAR` による間接参照

- 「`$VAR` で書ける」とした項目は、値の全体を `$NAME` (`NAME` は `[A-Za-z_][A-Za-z0-9_]*`) にすると、loop を起動した環境の変数 `NAME` の値に置き換わる
- 変数が未設定か空なら、項目と変数を名指しして失敗する
- 値の一部だけを置き換える書き方 (`acme/$NAME`) は持たない。`$` を含む値は、そのままの綴りとして読む

### 2.5 hooks

- `sh -c` で撃つ。cwd は作業対象の workspace
- 環境変数は loop の環境に、次のものを足す

| 変数 | 中身 |
|---|---|
| `CLAUDE_DISPATCHER_WORKSPACE` | workspace の絶対 path |
| `CLAUDE_DISPATCHER_CLONE` | workflow 定義の dir の絶対 path |
| `CLAUDE_DISPATCHER_KIND` | 作業対象の種類 (`issue`) |
| `CLAUDE_DISPATCHER_NUMBER` | 作業対象の番号 |


### 2.6 template

action と本文 (共通 prompt) は、worker を起動するたびに Go の text/template で描画する。

| 変数 | 中身 |
|---|---|
| `.issue.number` | issue の番号 |
| `.issue.title` | 題名 |
| `.issue.url` | URL |
| `.issue.labels` | label の綴りの列 |
| `.trigger.name` | 当たった trigger の名前 |
| `.attempt` | 何回目の起動か (1 から) |
| `.workspace` | workspace の絶対 path |

- 未知の変数 (`.issue.body` など) と未知の関数は、描画の失敗にする。描画に失敗した attempt は失敗
- workflow 定義の検査 (§2.7) で、action と本文を見本の変数 (どれも空でない値) で描画してみる。描画できなければ検査で落とすので、作業対象を読んでから描画に失敗するのは、見本では通った分岐だけになる
- 例: `/swat-skills:playbook-implementation issue #{{ .issue.number }} ({{ .issue.url }})`

### 2.7 検査

次の誤りは、項目の位置 (`triggers[0].when.labels.all` の形) を名指しして失敗させる。誤りが複数あれば、すべてを 1 行ずつ出す。

- file が無い・読めない・front matter が無い・YAML として読めない (YAML の誤りの行番号も file の行番号で出す)
- 未知の key (ADR 0009「未知の key は失敗させる」)
- 型の誤り・必須の項目の欠落・未知の値 (`tracker.kind`・`triggers[].on`・`author`)
- 空の文字列 (空の `assignee` や label を「条件なし」と取り違えないため)
- 上の表の各項目の制約 (`polling.interval` の範囲・trigger の名前の綴りと重複・`assignee` と `unassigned` の併記・空の `labels.any`・空の `action`)
- `$VAR` の未設定と、`tracker.token` に値そのものを書いたこと
- 同じ key を 1 つの対応表に 2 回書いたこと
- action と本文の template を描画できないこと (§2.6。綴りの誤り・未知の変数・未知の関数)

## 3. exit code

| exit | 意味 |
|---|---|
| 0 | 成功 |
| 1 | 観測できなかった (gh を起動できない・rate limit・読み切れない・その他の gh の失敗) / 想定外の失敗 |
| 2 | 引数の誤り / workflow 定義の誤り (§2.7) / issue 置き場が見えない (綴りの誤りか、権限が無い) |
| 3 | 同じ scope key の loop が走っている (§6) |
| 4 | gh の認証が通らない |

- 観測の失敗は、issue 置き場の部品が 認証 / 見えない / 読み切れない / rate limit に分けて返す (system.md §13)
  - 読み切れない: 1 往復で読む件数の上限を超えた (issue の label・assignee・依存先が 100 件を超えた)。切り詰めた像から候補を出さない

## 4. log.jsonl

1 行 1 JSON object。append-only。どの行も `ts` (RFC 3339 の UTC、秒まで)・`scope` (scope key)・`event` を持つ。

| `event` | いつ | ほかの key |
|---|---|---|
| `tick` | tick の終わり | `result` (`ok` / `error`)・`candidates` (候補の数)・`launched` (起動した作業対象の列)・`error` (`result` が `error` のとき) |
| `start` | worker を起動した | `target`・`trigger`・`attempt`・`session_id`・`workspace`・`pid` |
| `end` | worker 1 回分の終わり方を決めた | `target`・`trigger`・`attempt`・`session_id`・`outcome`・`reason`・`exit_code` (process が自分で終わったときだけ。signal で止まったら載せない) |
| `retry` | `failed` の後、次の attempt を予定した | `target`・`trigger`・`attempt`・`next_attempt`・`session_id`・`backoff` (秒。小数を含む) |
| `release` | 再起動を待つ claim を、起動せずに解いた | `target`・`trigger`・`attempt`・`session_id`・`reason` (`終端` / `trigger から外れた` / `trigger が workflow 定義から消えた`) |
| `abandon` | attempt の上限で打ち切った | `target`・`trigger`・`attempt`・`session_id` |
| `unabandon` | 打ち切りを解いた (打ち切ったときの trigger から外れたのを観測した) | `target`・`trigger` |
| `error` | 処理は続けるが、運用者が知るべき失敗 | `target` (あれば)・`error` |

- `target` は `issue#<番号>`
- `outcome`

| 値 | 意味 |
|---|---|
| `completed` | worker が終わった後、作業対象が起動した trigger から外れていた。または終端になっていた。worker 自身が失敗していても completed とし、`reason` に失敗を添える |
| `failed` | worker が終わった後も作業対象が trigger に当たったままで、worker が失敗した (異常終了・hook の失敗・描画の失敗・起動できない・stall・上限時間の超過) か、正常に終わった。claim は解かず、`retry` か `abandon` の行が続く |
| `stopped` | loop が止めた (作業対象が終端になった・2 回目の停止要求) |

- `error` の行を書く場面: `after_run` と `before_remove` の失敗・workspace を消せない (次の tick の掃除で消し直す)・終わった worker の作業対象を読み直せない (claim を持ったまま次の tick で読み直す)・止める worker の process group に signal を送れない
- log.jsonl に行を書けなければ、そのことを stdout に出して続ける
- `attempt` は、その行が指す claim の最後に起動した (起動しようとした) attempt
- `session_id` は CLI が発行して最初の attempt に `--session-id` で渡した値。同じ claim の attempt を通して 1 つ。Claude Code の transcript へ辿る鍵
- claim を解くのは `completed` と `stopped` の `end`・`release`・`abandon` の行

## 5. 試運転 (`loop --dry-run`)

```
claude-dispatcher loop --dry-run [<workflow の path>]
```

workflow 定義を読んで検査し、snapshot を作って trigger を評価し、起動するはずの作業対象を示して終わる。**何も書かない** (state dir を作らず、lock も取らない)。

成功したら、候補を 1 件 1 行で、評価の順 (§2.3) に stdout へ出す。候補が無ければ何も出さない。

```
implement	issue	#42	ログインの失敗を記録する
implement	issue	#43	設定の読み込みを速くする
```

- 列はタブで区切る: trigger の名前・作業対象の種類・`#<番号>`・題名
- 題名の制御文字 (タブ・改行・ESC など) は空白に置き換える

失敗したら、stdout に何も出さず、stderr に理由を出して §3 の exit code で終わる。workflow 定義の誤りは 1 件 1 行で出す。

## 6. `loop`

```
claude-dispatcher loop [<workflow の path>]
```

実装 repo の clone を cwd にして撃つ。

**起動時の検査** (落ちたら stderr に理由を出して終わる):

| 検査 | exit |
|---|---|
| 引数の数と flag | 2 |
| workflow 定義を読めて、検査に通る (§2.7) | 2 |
| gh と `claude.command` を解決できる (PATH と、よく使われる置き場) | 1 |
| state dir を作れる | 1 |
| 同じ scope key の loop が走っていない (`loop.lock` を取れる) | 3 |

- 同じ issue 置き場の 2 本目の loop は、clone や workflow 定義の path が違っても起動時に止まる
- lock は loop の生存期間だけ持つ。loop が死ねば外れる

**tick**: 起動直後に 1 回撃ち、以後は tick の終了から `polling.interval` だけ待って次を撃つ。tick ごとに workflow 定義を読み直す。

- 読み直した workflow 定義が検査に落ちたら、その tick は何も起動しない (#74 Q38)。周期は、最後に検査に通った版のものを使う
- 読み直した workflow 定義の scope key が起動時と違えば、その tick は何も起動しない (lock は起動時の scope key で取っている)

**tick の手順** (system.md §7):

1. 突き合わせ: 走っている worker の作業対象を読み直し、終端になっていれば worker を止め、`after_run` と `before_remove` を撃って workspace を消す。trigger から外れただけでは止めない
2. 終わった worker のうち、作業対象を読み直せなかったものを読み直す
3. workflow 定義を読み直して snapshot を作る
4. 掃除: workspace root の下の `issue-<番号>` のうち、claim が無く open な issue の一覧にも無いものを読み直し、終端になっていれば `before_remove` を撃って消す。起動の直後の tick が起動時の掃除を兼ね、以後の tick が、claim を解いた後に終端になったものと消し損ねたものを拾う
5. 打ち切りを解く: 打ち切った作業対象のうち、snapshot に無いか、打ち切ったときの trigger の述語に当たらなくなったもの (その trigger が workflow 定義から消えたものを含む) の打ち切りを解く
6. 再起動: backoff の明けた再起動待ちの claim を、次の「再起動」の規則で起動する
7. trigger を評価し、候補のうち claim も打ち切りもされていないものを、`limits.max_concurrent` から走っている worker と再起動待ちの claim を引いた数だけ起動する (確かめ待ちの claim は数えない)

**再起動** (system.md §7「失敗の扱い」):

- `failed` の後、attempt が `limits.max_attempts` に達していれば打ち切る。達していなければ、`min(10s × 2^(attempt−1), limits.max_retry_backoff)` の後に再起動を予定する
- backoff が明けたら、tick を待たずに再起動を試みる (tick の途中なら、その tick の手順 6 で試みる)
  - 走っている worker が `limits.max_concurrent` に達していれば、attempt を進めずに待ち直す。worker が終わって空きが出たときに試み直す
  - 起動した trigger が workflow 定義から消えていれば、claim を解く (`release`)
  - 作業対象を読み直す。終端なら `before_remove` を撃って workspace を消し、trigger から外れていれば、claim を解く (`release`)。読み直せなければ次の tick で試み直す
  - 当たったままなら、attempt を 1 つ進め、同じ trigger・同じ session id・同じ workspace で起動する
- 再起動は、そのときの workflow 定義 (trigger・本文・hooks・`claude`・`limits`) で行う。workspace の root だけは、最初に起動したときのものを使う (session は workspace の path ごとに保存されるので)
- 前の attempt で claude を起動できていなければ (hook・描画・起動の失敗)、session はまだ無いので `--session-id` で始める
- 停止要求を受けた後に失敗した attempt は、再起動を予定しない
- 打ち切りは作業対象ごとに memory に持つ。打ち切った作業対象は、どの trigger に当たっても起動しない。loop を起動し直すと消える
- 打ち切りを解くには、label を外して打ち切ったときの trigger から外し、1 周期待ってから付け直す (外してから付け直すまでが 1 周期に収まると、外れたのを観測できない)。`status` (#81) の打ち切りの表示にこの手順を書く

- 作業対象の読み直しで issue が消えていたら (削除・移管。gh が `Could not resolve to an Issue` を返すか、応答に issue が無い)、終端と同じに扱う

**worker の 1 回分**:

1. workspace が無ければ作って `after_create` を撃ち、`before_run` を撃つ
2. action と本文を描画し、本文を `prompts/issue-<番号>.md` に書く
3. 次の argv で起動する。cwd は workspace、stdin は空。stdout (stream-json) は `workers/issue-<番号>.log`、stderr は `workers/issue-<番号>.stderr.log` に追記する (claude には file をそのまま渡す)

   ```
   <claude.command> <claude.args…> -p --output-format stream-json --verbose <session> --append-system-prompt-file <prompts の path> -- <描画した action>
   ```

   - `<session>` は、前の attempt で session を始めていれば同じ id で `--resume <uuid>` (止めた session の続きから始まる)、始めていなければ `--session-id <uuid>`
   - action は `--` の後ろに置く (`-` で始まる action を option と読ませない)
   - stream の file が `limits.stall_timeout` のあいだ伸びないか、起動からの経過が `limits.run_timeout` を超えたら、process group を止めて失敗とする (止め方は 2 回目の停止要求と同じ)。止める判断と停止要求が重なったら、停止要求で止めたことにする

4. 終わったら `after_run` を撃ち、作業対象を読み直す。終端か、起動した trigger から外れていれば `completed` として claim を解き、当たったままなら `failed` として再起動 (上) に回す
   - workspace を消すときは、worker を起動したときの workflow 定義の `workspace.root` と hooks を使う

**出力** (#81 で loop の画面に作り直す。今の形は仮): log.jsonl (§4) に書く行を、人が読む形で stdout にも 1 行ずつ追記する。

```
<時刻> loop を始めた: scope <scope key> · state dir <path> · workflow <path>
<時刻> tick ok · 候補 2: implement issue #42, implement issue #43
<時刻> tick error · <理由>
<時刻> 起動 issue#42 (implement, attempt 1, session <uuid>)
<時刻> 終了 issue#42 (implement): completed — trigger から外れた
<時刻> 再起動を予定 issue#42 (implement, attempt 2, 10s 後)
<時刻> 再起動せずに解いた issue#42 (implement): trigger から外れた
<時刻> 再起動を待つ issue#42 (implement, attempt 2): 並列上限 1 に空きが出るまで待つ
<時刻> 打ち切り issue#42 (implement, attempt 3 回): label を外して trigger から外すと解ける (外してから 1 周期待つ)
<時刻> 打ち切りを解いた issue#42 (implement)
<時刻> error issue#42: <理由>
<時刻> 停止待ち: 走っている worker 1 本の終了を待つ (もう一度で止める)
<時刻> loop を止めた (停止要求 SIGINT)
```

- tick の行の候補は、試運転 (§5) と同じ順。候補が無ければ `tick ok · 候補 0`

**停止**: SIGINT / SIGTERM / SIGHUP を同じに扱う。

| 受けたとき | 振る舞い |
|---|---|
| 1 回目 | 新しい tick と起動 (再起動を含む) をやめ、走っている worker の終了を待って (終わり方の処理を済ませて) 止まる。待つ間も周期ごとに突き合わせ (tick の手順 1) だけを撃ち、終端になった作業対象の worker は止める。走っていなければ直ちに止まる。再起動待ちの claim は error の行を残して捨てる |
| 2 回目 | 走っている worker の process group に SIGTERM を送り、5s で終わらなければ SIGKILL を送る。worker が終わったら group に残った process も SIGKILL で止め、`after_run` を撃ち、`stopped` の `end` 行を書いて止まる |

exit: 0 = 停止要求で止まった / 1・2・3 = 起動時の検査 (上表)。

## 7. `status` / `paths` / `setup` / `doctor`

未定。`status` と `paths` は #81、`setup` と `doctor` は #82 で作り直す。今の binary はこれらの subcommand を持たない。

## 8. `--version`

```
claude-dispatcher --version
```

binary の版と commit を stdout に 1 行で出す。workflow 定義も state dir も読まないので、導入が壊れていても撃てる (不具合の報告に貼る)。

```
claude-dispatcher v0.3.0 (<commit の hash>)
```

- 版: Releases の binary は GoReleaser が埋めた tag (`v0.3.0`)。`go install <module>@<版>` の binary は module の版 (同じ `v0.3.0`)。git の作業ツリーでの `go build` は Go が VCS から刻む pseudo-version (直近の tag の次の patch の `-0.<日時>-<hash>`。例: `v0.1.1-0.20260927163012-aac3e1c06160`、未 commit の変更があれば `+dirty`)。VCS の情報が無い build (`go run`・`-buildvcs=false` 等) は `(devel)`
- commit: Releases の binary は tag の commit。git の作業ツリーでの `go build` は build 情報の `vcs.revision`。`go install <module>@<版>` の binary は module cache から build するので commit を持たず `unknown`。VCS の情報が無い build も `unknown`

exit: 0。
