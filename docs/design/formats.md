# 外から観測できる形式

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
  loop.lock                           loop の生存期間の flock の対象。2 本目の loop を止める (§6)
  alive.lock                          loop の生死を status に見せる flock の対象 (§6、§7.2)
  log.jsonl                           tick と worker の起動・終わり方の行 (§4)
  status.json                         loop の今の状態 (状態 file、§7)。loop が書き換え、status が読む
  prompts/<作業対象>.md               描画した共通 prompt。worker に --append-system-prompt-file で渡す
  workers/<作業対象>.log              worker の stdout (stream-json)。attempt を跨いで追記する
  workers/<作業対象>.stderr.log       worker の stderr。attempt を跨いで追記する
```

`<作業対象>` は、issue なら `issue-<番号>`、CL なら `cl-<番号>`。

workspace は state dir の外、workflow 定義の `workspace.root` の下に作業対象ごとに置く (§2.1)。

```
<workspace root>/<作業対象>/          作業対象の workspace。作業対象が終端になるまで残す
```

- **scope key**: issue 置き場の部品が決める、issue 置き場の識別子 (system.md §13)
  - GitHub は `github.com/<owner>/<name>` を小文字にしたもの (GitHub の owner と repo の名前は大文字と小文字を区別しない)
  - GitLab は `<host>/<path>` を小文字にしたもの (例: `gitlab.example.com/acme/sub/widgets`。GitLab の path は大文字と小文字を区別しない)
  - Jira は `<site>/<project key>` を小文字にしたもの (例: `acme.atlassian.net/widgets`)。1 つの Jira project は 1 本の loop で回す (同じ project の 2 本目の loop は lock で止まる)。project key を改名したら scope key が変わるので、workflow 定義を書き換えて loop を起動し直す
- `jira` の作業対象の番号は、issue の key (`WIDGETS-123`) の数字の部分 (`123`)。`<作業対象>` の綴り・log.jsonl の `target` (§4)・hooks の `CLAUDE_DISPATCHER_NUMBER` (§2.6) は他の kind と同じく番号で書く (ADR 0010)
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
  kind: github               # 必須。github・gitlab・jira のどれか
  repo: acme/widgets         # 必須。issue 置き場 (github は owner/name、gitlab は group/…/name、jira は project key)。$VAR で書ける
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
  stall_timeout: 15m         # 任意。worker の活動が途絶えてから止めるまで。既定 15m。0 で無効
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
  - name: fix-ci
    on: cl
    when:
      ci_failed: true
      head: worktree-issue-*
      draft: false
    action: |
      CL #{{ .cl.number }} の CI の失敗を直す
---
この project の共通 prompt。
```

### 2.1 項目

| key | 型 | 中身 |
|---|---|---|
| `tracker.kind` | 文字列 | tracker の種類。`github` (gh で読む)・`gitlab` (glab で読む)・`jira` (acli で読む。Jira Cloud) のどれか |
| `tracker.host` | 文字列 | `gitlab` と `jira` でだけ書ける。`$VAR` で書ける。`github` で書くと未知の key<br>- `gitlab`: GitLab の host 名 (`gitlab.example.com`)。既定 `gitlab.com`<br>- `jira`: Jira Cloud の site (`acme.atlassian.net`)。既定を持たず必須。綴りは `[A-Za-z0-9.-]+` |
| `tracker.repo` | 文字列 | issue 置き場。`github` は `<owner>/<name>`、`gitlab` は `<group>/<name>` の段数を問わない path (`acme/sub/widgets`。どの段も `[A-Za-z0-9._-]+`)、`jira` は Jira の project key (`WIDGETS`。`[A-Za-z][A-Za-z0-9_]*`。大文字に揃えて読む) |
| `tracker.token` | 文字列 | `github` でだけ書ける。gh に環境変数 `GH_TOKEN` として渡す token。`$VAR` でだけ書ける (値そのものを書かない)。省くと、loop を起動した環境の認証を gh がそのまま使う。`gitlab` は glab 自身の認証 (`glab auth login` の結果)、`jira` は acli 自身の認証 (`acli jira auth login` の結果) を使い、書くと名指しで失敗する |
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
| `limits.stall_timeout` | 文字列 | worker の stream (stdout の stream-json) に活動 (§6) として数える行が書かれなくなってから、止めて失敗とするまでの時間。既定 15m。`0s` で無効。claude は tool の実行中は heartbeat (`tool_progress`) しか書かず、heartbeat は活動として数えないので、長い build や test を走らせる repo では長めにする |
| `limits.run_timeout` | 文字列 | worker 1 回分の上限時間。起動からの経過が超えたら止めて失敗とする。既定 1h。`0s` で無効 |
| `claude.command` | 文字列 | worker として起動する command。PATH から探す。既定 `claude` |
| `claude.args` | 文字列の列 | command に、dispatcher の引数 (§6「worker の起動」) より前に渡す引数。既定は空 |
| `triggers` | 列 | trigger の宣言。宣言順が起動の優先順 (system.md §6) |
| `triggers[].name` | 文字列 | trigger の名前。`[A-Za-z0-9._-]+`。trigger の間で重複しない |
| `triggers[].on` | 文字列 | 作業対象の種類。`issue` か `cl` (`gitlab` では merge request)。`jira` では `issue` だけを書ける (`cl` は名指しで失敗する。issue 置き場と別の CL 置き場の宣言は #114) |
| `triggers[].when` | 対応表 | 述語。書いた条件はすべて AND で評価する。書ける key は `on` で変わる (issue は §2.2、CL は §2.3) |
| `triggers[].action` | 文字列 | worker に渡す prompt の template (§2.7)。空にできない |

**YAML の読み方**: 値を書いていない key (`when:` だけの行) は、空の対応表として読む。anchor と alias は辿る。merge key (`<<: *base`) は持たない (`<<` は未知の key として失敗する)。

### 2.2 issue 側の述語 (`on: issue` の `when`)

| key | 型 | 当たる issue |
|---|---|---|
| `labels.all` | 文字列の列 | 列の label をすべて持つ |
| `labels.any` | 文字列の列 | 列の label のどれかを持つ。空の列は書けない |
| `labels.none` | 文字列の列 | 列の label をどれも持たない |
| `assignee` | 文字列 | その login (GitLab は username) の人が assignee に居る |
| `unassigned` | 真偽 | `true` なら assignee が居ない、`false` なら 1 人以上居る |
| `author` | 文字列 | `collaborator` なら作者が collaborator、`non_collaborator` ならそれ以外。collaborator の線は tracker ごとに下の表 |
| `milestone` | 文字列 | その題名の milestone に入っている |
| `blocked` | 真偽 | `true` なら未解決 (open) の依存先 (blocked by) が 1 つ以上ある、`false` なら無い。`tracker.kind` が `gitlab` なら書けない (下) |
| `status.any` / `status.none` | 文字列の列 | `jira` でだけ書ける。status 名が列のどれかである / どれでもない。`any` の空の列は書けない |
| `type.any` / `type.none` | 文字列の列 | `jira` でだけ書ける。issue type 名 (subtask・epic を含む) が列のどれかである / どれでもない。`any` の空の列は書けない |

| `tracker.kind` | collaborator と数える作者 |
|---|---|
| `github` | repo の owner・organization の member・collaborator (GitHub の `authorAssociation` が `OWNER` / `MEMBER` / `COLLABORATOR`) |
| `gitlab` | project での access level (group から継承したものを含む) が Developer (30) 以上の member。push できる層に揃える。worker は push 権限を持つので、誰の書き込みで起動してよいかの線を push できる人に引く |
| `jira` | `author` は書けない (下) |

- `assignee` と `unassigned` は一緒に書けない
- label と login の綴りは、tracker によらず大文字と小文字を区別せずに比べる
  - GitLab の label は大文字と小文字を区別するが、dispatcher の規則として揃える。trigger の評価は正規化した作業対象だけを見る (system.md §13) ので、tracker ごとに比べ方を変えない
  - 誤って当たるのは、大文字と小文字だけが違う label を 2 つ持つ project に限られる
- `tracker.kind` が `gitlab` なら、作業対象は種類が issue のものだけ (`issue_type` が `issue`)。同じ一覧に出る task・incident・test case は読まない
- `tracker.kind` が `gitlab` なら `blocked` は書けない (名指しで失敗する)。GitLab の issue の依存 (blocks / is blocked by) は有償の tier の機能で、CE の API は依存を返さない。読むと常に 0 件になり、`blocked: false` が黙って全件に当たる
- `status` と `type` は `jira` でだけ書ける (他の kind で書くと名指しで失敗する)

`tracker.kind` が `jira` のときの述語の意味:

| key | `jira` での意味 |
|---|---|
| `labels` | Jira の label。他の kind と同じく大文字と小文字を区別せずに比べる |
| `assignee` | 担当者の accountId (Jira Cloud の user は accountId でしか一意に識別できない) |
| `unassigned` | 担当者が居ない / 居る |
| `author` | 書けない (名指しで失敗する)。worker を誰の書き込みで起動してよいかの線を、Jira の権限 (repo の push 権限と別の線) に引けない |
| `milestone` | 書けない (名指しで失敗する)。sprint は無いことがあり、fixVersions は acli の一覧で読めない |
| `blocked` | link type `Blocks` の inward ("is blocked by") の link のうち、相手の status の区分 (`statusCategory.key`) が `done` でないものが 1 つ以上あれば `true` |
| `status` | status 名 (`fields.status.name`)。大文字と小文字を区別せずに比べる |
| `type` | issue type 名 (`fields.issuetype.name`)。大文字と小文字を区別せずに比べる |

- `jira` の作業対象は、project の全種類の issue (subtask・epic を含む)。絞るなら `type` を書く
- `jira` の終端は、status の区分 (`status.statusCategory.key`) が `done` であること。Wontfix のような done 区分の status も終端
- `status` と `type` の名前が実在するかは、loop の起動時・試運転・`doctor` で確かめる (§6・§5・§7.4)。tick の中では確かめない
  - status 名は `project = "<project key>" AND status = "<名前>"` の検索の件数 (`--count`) を撃ち、JQL の失敗で見分ける (認証の失敗は §3 の分類で認証として返す)。acli は project の status の一覧を返さないので、JQL の値の検証による site の単位の検査にとどまる。他の project にだけある status 名は検査を通る
  - JQL には project key と名前を `"` で囲んで書く (project key が JQL の予約語と重なっても読めるように)
  - issue type 名は project の issue type (`acli jira project view --key <project key> --json` の `issueTypes`) と比べる
- `blocked` を書いた trigger があると、tick ごとに open な一覧に加えて、依存を持つ open な issue を検索し (`issueLinkType = "is blocked by"`)、当たった issue を 1 件ずつ読み直して依存先の status の区分を見る

### 2.3 CL 側の述語 (`on: cl` の `when`)

CL の状態の語彙 (system.md §6) は、真偽の key で書く。`true` なら その状態の CL に、`false` なら その状態でない CL に当たる。

| key | 語彙 | 当たる CL (`true` のとき) |
|---|---|---|
| `conflict` | `cl.conflict` | merge conflict を持つ。GitHub が conflict を計算し終えていない CL (`mergeable` が `UNKNOWN`) は、`true` にも `false` にも当たらない (次の tick で見直す) |
| `review_unresolved` | `cl.review_unresolved` | collaborator が書いた未解決の review thread が 1 本以上ある。thread の書き手は、thread の最初の comment の作者 |
| `ci_failed` | `cl.ci_failed` | head commit の checks の集計が失敗 (`FAILURE` か `ERROR`) している。checks が無いか、走っている途中なら失敗ではない |
| `approved` | `cl.approved` | review で承認済み (`reviewDecision` が `APPROVED`) |

絞り込み:

| key | 型 | 当たる CL |
|---|---|---|
| `labels.all` / `labels.any` / `labels.none` | 文字列の列 | issue 側 (§2.2) と同じ |
| `head` | 文字列 | head branch の名前が pattern に当たり、かつ head が同じ repo の branch である。pattern は Go の `path.Match` の綴り (`*` は `/` 以外の文字の列、`?` は `/` 以外の 1 文字、`[...]` は文字の組) |
| `same_repo` | 真偽 | `true` なら head が同じ repo の branch (fork でない)、`false` なら fork の branch |
| `author` | 文字列 | issue 側 (§2.2) と同じ |
| `draft` | 真偽 | `true` なら draft、`false` なら draft でない |

- `head` を書いたら、fork の CL には当たらない。fork からは誰でも同じ綴りの head branch を作れるため (system.md §6)。`head` と `same_repo: false` は一緒に書けない
- head branch の名前は、大文字と小文字を区別して比べる (git と同じ)

上の表は `tracker.kind` が `github` のときの GitHub の field で書いた。`gitlab` では、語彙と絞り込みを merge request の field に次のように写す。

| 語彙 / 絞り込み | GitLab の field |
|---|---|
| `conflict` | `has_conflicts` が `true` なら conflict を持つ。そうでなく `detailed_merge_status` が `checking` / `unchecked` / `preparing` なら、計算し終えていないので `true` にも `false` にも当たらない (次の tick で見直す)。どちらでもなければ conflict を持たない。`detailed_merge_status` が無い応答は、対象の版より古い GitLab として読めない失敗にする |
| `review_unresolved` | 解決できる (resolvable な note を持つ) discussion で、解決されていない (resolvable な note のどれかが未解決) もののうち、最初の note の作者が collaborator のものが 1 本以上ある。最初の note が system note の discussion は数えない |
| `ci_failed` | head の pipeline (`head_pipeline`) の `status` が `failed`。`canceled` は失敗に数えない (人が意図して止めた pipeline に worker を送らない)。pipeline が無ければ失敗ではない |
| `approved` | merge request の承認 (`/merge_requests/:iid/approvals`) の `approved`。CE では 1 人以上の承認、有償の tier では承認ルールの充足で、どちらも host の判断に従う |
| `draft` | `draft` |
| `same_repo` | `source_project_id` と `target_project_id` が同じ。source の fork が消えて `source_project_id` が null なら `false` (§2.4) |
| `head` | `source_branch` |
| `author` | issue 側 (§2.2) と同じ線 (access level が Developer 以上) |

- `gitlab` の merge request は、open な一覧を読んだ後に 1 本ずつ、merge request 本体・承認・discussion を読み直す (`head_pipeline` は一覧の応答に無く、承認と discussion は別の endpoint にあるため)。CL 側の trigger を置くと、open な merge request 1 本につき 3 往復の API を毎 tick 撃つ
- `gitlab` の merge request の終端は `state` が `merged` か `closed`。`locked` (merge の処理中) は終端にしない (merge に失敗すると `opened` に戻る)

### 2.4 評価の規則

- 作業対象ごとに trigger を宣言順に評価し、最初に当たった 1 つだけを採る。issue には `on: issue` の trigger だけを、CL には `on: cl` の trigger だけを当てる
- 候補は、trigger の宣言順を先に、同じ trigger の中では作業対象の作成日時の古い順に並べる。作成日時が同じなら番号の小さい順
  - `jira` では、作成日時の代わりに番号の小さい順に並べる。acli の一覧は作成日時を返さない (ADR 0010)
- **曖昧な CL** (同じ repo の同じ head branch から、open な CL が 2 本以上ある) には、CL 側の trigger を当てない。fork の head branch は、fork ごとに別の branch として数える (fork は GitHub では head の repo の名前、GitLab では `source_project_id` で見分ける)
- loop は、claim している作業対象 (走っている・止めている・確かめ待ち・再起動待ち) の workspace で checkout されている branch を head に持つ、同じ repo の CL にも CL 側の trigger を当てない (§6 の tick の手順)。試運転 (§5) は claim を持たないので、この除外は掛からない
- open な一覧は、trigger に現れる種類のものだけを読む (`on: cl` の trigger が 1 つも無ければ CL の一覧を読まず、`on: issue` の trigger が無ければ issue の一覧を読まない)
- head の repo が消えた fork の CL (GitHub の `headRepository` が null、GitLab の `source_project_id` が null) は、どの fork の branch か分からないので、曖昧さを数えるときに数えない

### 2.5 `$VAR` による間接参照

- 「`$VAR` で書ける」とした項目は、値の全体を `$NAME` (`NAME` は `[A-Za-z_][A-Za-z0-9_]*`) にすると、loop を起動した環境の変数 `NAME` の値に置き換わる
- 変数が未設定か空なら、項目と変数を名指しして失敗する
- 値の一部だけを置き換える書き方 (`acme/$NAME`) は持たない。`$` を含む値は、そのままの綴りとして読む

### 2.6 hooks

- `sh -c` で撃つ。cwd は作業対象の workspace
- 環境変数は loop の環境に、次のものを足す

| 変数 | 中身 |
|---|---|
| `CLAUDE_DISPATCHER_WORKSPACE` | workspace の絶対 path |
| `CLAUDE_DISPATCHER_CLONE` | workflow 定義の dir の絶対 path |
| `CLAUDE_DISPATCHER_KIND` | 作業対象の種類 (`issue` か `cl`) |
| `CLAUDE_DISPATCHER_NUMBER` | 作業対象の番号 |


### 2.7 template

action と本文 (共通 prompt) は、worker を起動するたびに Go の text/template で描画する。

| 変数 | 中身 |
|---|---|
| `.issue.number` | issue の番号 |
| `.issue.title` | 題名 |
| `.issue.url` | URL |
| `.issue.labels` | label の綴りの列 |
| `.issue.key` | issue の key (`WIDGETS-123`)。`tracker.kind` が `jira` のときだけある |
| `.cl.number` | CL の番号 |
| `.cl.title` | 題名 |
| `.cl.url` | URL |
| `.cl.labels` | label の綴りの列 |
| `.cl.head` | head branch の名前 |
| `.trigger.name` | 当たった trigger の名前 |
| `.attempt` | 何回目の起動か (1 から) |
| `.workspace` | workspace の絶対 path |

- `.issue` は issue の worker にだけ、`.cl` は CL の worker にだけある
- `jira` の `.issue.url` は `https://<site>/browse/<key>`、`.issue.labels` は Jira の label の綴りのまま。`.issue.key` を他の kind で書くと、未知の変数として検査 (§2.8) で落ちる
- 未知の変数 (`.issue.body` など、CL の worker の `.issue`) と未知の関数は、描画の失敗にする。描画に失敗した attempt は失敗
- workflow 定義の検査 (§2.8) で、action と本文を見本の変数 (どれも空でない値) で描画してみる。action はその trigger の種類の見本で、本文はどの worker にも渡るので、trigger に現れる種類すべての見本で描画する。描画できなければ検査で落とすので、作業対象を読んでから描画に失敗するのは、見本では通った分岐だけになる
- 例: `/swat-skills:playbook-implementation issue #{{ .issue.number }} ({{ .issue.url }})`

### 2.8 検査

次の誤りは、項目の位置 (`triggers[0].when.labels.all` の形) を名指しして失敗させる。誤りが複数あれば、すべてを 1 行ずつ出す。

- file が無い・読めない・front matter が無い・YAML として読めない (YAML の誤りの行番号も file の行番号で出す)
- 未知の key (ADR 0009「未知の key は失敗させる」)
- 型の誤り・必須の項目の欠落・未知の値 (`tracker.kind`・`triggers[].on`・`author`)
- 空の文字列 (空の `assignee` や label を「条件なし」と取り違えないため)
- 上の表の各項目の制約 (`polling.interval` の範囲・trigger の名前の綴りと重複・`assignee` と `unassigned` の併記・空の `labels.any` / `status.any` / `type.any`・`head` の pattern の綴り・`head` と `same_repo: false` の併記・空の `action`)
- `$VAR` の未設定と、`tracker.token` に値そのものを書いたこと
- `tracker.kind` が支えない宣言。key を書く順 (`triggers` を `tracker` より前に書くなど) によらず名指しする
  - `gitlab`: `tracker.token`・`blocked` (§2.1・§2.2)
  - `jira`: `tracker.token`・`author`・`milestone`・`on: cl` の trigger (§2.1・§2.2)。`tracker.host` の欠落は必須の項目の欠落
  - `github` と `gitlab`: `status`・`type` (§2.2)
  - `github`: `tracker.host` は未知の key
- `jira` の次の誤りは、workflow 定義だけでは確かめられないので、loop の起動時・試運転・`doctor` の issue 置き場の確認 (§6) で名指しする。tick の中では確かめない
  - acli の認証の site が `tracker.host` と違う (acli は active な account の site を読むため)
  - `status` の status 名・`type` の issue type 名が実在しない
- 同じ key を 1 つの対応表に 2 回書いたこと
- action と本文の template を描画できないこと (§2.7。綴りの誤り・未知の変数・未知の関数)

### 2.9 事前検査

trigger ごとに、action の先頭の skill か command が呼べるかを確かめる (system.md §8)。作業対象に依らずに確かめられるよう、描画の前の template を見る。

- action の先頭 (前の空白を除く) が `/` なら、空白までを名前とし、次の置き場の skill と command から探す
  - **repo の `.claude/`**: workflow 定義の dir の `.claude/skills/<dir>/SKILL.md` と `.claude/commands/<名前>.md`
  - **`~/.claude/`**: HOME の `.claude/skills/<dir>/SKILL.md` と `.claude/commands/<名前>.md`
  - **plugin**: 次の 3 つから見つけた plugin の skill と command
    - `~/.claude/plugins/installed_plugins.json` が挙げる plugin のうち、`scope` が `user` のものと、`project` で `projectPath` が workflow 定義の dir のもの。`project` の plugin は commit された `.claude/settings.json` で有効になり、clone の worktree である workspace でも読まれる。`local` の plugin は commit しない `.claude/settings.local.json` で有効になり、workspace では読まれないので数えない
    - repo の `.claude/skills/` と `~/.claude/skills/` の直下の dir のうち、`.claude-plugin/plugin.json` を持つもの
    - `claude.args` の `--plugin-dir <path>` (相対 path は workflow 定義の dir から)
- 名前の当て方
  - skill の名前は、`SKILL.md` の YAML の front matter の `name`。無ければ dir の名前
  - command の名前は、`commands/` からの相対 path の `.md` を除き、`/` を `:` にしたもの (`commands/foo/bar.md` は `foo:bar`)。symlink の dir も辿る
  - plugin の skill と command は `<plugin の名前>:<名前>` で呼ぶ。plugin の名前は `.claude-plugin/plugin.json` の `name`。front matter に `name` を持つ plugin の skill は、接頭辞の無い `<名前>` でも呼ぶ
  - plugin の skill は、plugin の `skills/<dir>/SKILL.md` と、`plugin.json` の `skills` が挙げる path (その path の `SKILL.md`、無ければその下の `<dir>/SKILL.md`)。どちらも無い plugin は、root の `SKILL.md` を単一の skill として読む
  - plugin の command は、plugin の `commands/`。`plugin.json` に `commands` があれば `commands/` を置き換える: path (か path の列) ならその path の command、対応表なら key が command の名前
  - `plugin.json` の空の path は置き場にしない
  - skill と plugin の dir を探す置き場 (`.claude/skills/`・plugin の `skills/`・`plugin.json` の `skills` が挙げる path) の直下は、dir (指す先が dir の symlink を含む) だけを skill か plugin の dir とみなす。直下の通常の file (`.DS_Store`・README 等) は飛ばし、読めなかった置き場に数えない
- action の先頭を template 変数で始める (前の空白を除いて `{{` で始まる) と、先頭の `/名前` を確かめられないので、その trigger を失敗させる
- 先頭が `/` でも template 変数でもない action は、確かめない
- 誤りは trigger を名指しして 1 件 1 行で出す
- 置き場を読めなかったとき (無いのではなく、読み出しか解析に失敗したとき) は、見つからない理由の後ろに ` · 読めなかった置き場: <path> (<理由>), …` を足す

```
trigger implement: action の先頭の /playbook が見つからない (plugin・repo の .claude・~/.claude の skill と command)
trigger fix-ci: action が template 変数で始まるので、先頭の skill を確かめられない
```

| いつ | 落ちたら |
|---|---|
| `loop` の起動時 (§6) | 起動を失敗させる (exit 2) |
| 試運転 (§5) | 失敗させる (exit 2) |
| tick の中 (§6) | その trigger だけを評価から外し、再起動もしない。他の trigger は評価して起動する (外した trigger にも当たる作業対象は、他の trigger の候補になる) |
| `doctor` (§7.4) | `NG` の行に出す |

- Claude Code に組み込みの command (`/review` など) は 3 つの置き場に無いので、見つからないと数える
- plugin が有効か (settings の `enabledPlugins`) は見ない
- 置き場は Claude Code の読み方より広めに取る (見つからないと取り違えて起動を止めない側に倒す)。広めに取った分は、worker の起動の後に claude が名前を解けずに失敗する

## 3. exit code

| exit | 意味 |
|---|---|
| 0 | 成功 |
| 1 | 観測できなかった (tracker の CLI を起動できない・rate limit・読み切れない・その他の tracker の CLI の失敗) / 想定外の失敗 |
| 2 | 引数の誤り / workflow 定義の誤り (§2.8) / issue 置き場が見えない (綴りの誤りか、権限が無い) / `jira` の起動時の確認 (§6) で、acli の認証の site が `tracker.host` と違うか読めない・status 名か issue type 名が実在しない |
| 3 | 同じ scope key の loop が走っている (§6) |
| 4 | tracker の CLI (`tracker.kind` が `github` なら gh、`gitlab` なら glab、`jira` なら acli) の認証が通らない |

- 観測の失敗は、issue 置き場の部品が 認証 / 見えない / 読み切れない / rate limit に分けて返す (system.md §13)
  - 読み切れない: 1 往復で読む件数の上限を超えた (GitHub の issue の label・assignee・依存先、CL の label・review thread が 100 件を超えた)。切り詰めた像から候補を出さない
  - 外部 CLI (gh・glab・acli) の失敗のエラー文は、撃ったコマンド・exit code・stderr を載せる。stderr に HTML の本文 (`<!doctype html`・`<html`・`<head`・`<body` のうち最初のものから後) があれば、status に依らずそこから後を落とし、落としたことを書き添える。reverse proxy・SSO・load balancer は HTML のページを返しうるので、tick の error・`doctor` の理由・`setup` (§7.4) のエラー文にページを丸ごと載せない
  - glab は HTTP の失敗をどれも exit 1 で返すので、stderr の status から、401 を認証、404 を見えない、429 を rate limit と見分ける。分けるのは GitLab が JSON の本文で答えた status だけで、HTML の本文の応答 (`glab api` は status だけを出す) は status に依らずその他の失敗 (読めない) に置く。HTML は GitLab の手前の proxy・load balancer の応答でありうる。その 404 を issue が消えたと読むと、開いている issue を終端として扱ってしまう
    - 読むのは glab が status を出す位置だけで、本文の中の `(HTTP 401)`・`403` などには当てない。位置は経路で違う (`glab api` は `glab:` で始まる行、`setup` の `glab repo view` は `<METHOD> <URL>: <status>` の形。綴りの詳細は glab の版で変わりうるので、internal/gitlab の statusPattern のコメントが持つ)
  - glab の HTTP 403 はその他の失敗 (読めない) に置き、理由には glab の stderr の代わりに `HTTP 403 で拒否された (接続元のネットワークか、token の権限)` を出す (撃った glab のコマンドと exit code は残す)。stderr が撃った要求を含む経路 (`setup` の `glab repo view`) では、その `<METHOD> <URL>` (本文の前まで) を理由に添えて、拒否した host を読めるようにする。stderr が URL を含まない経路 (`glab api`。tick の error・`doctor` の理由) は理由だけを出す
    - 403 は、GitLab の手前の reverse proxy の拒否 (IP の許可リスト・VPN 必須など) でも、GitLab の拒否 (token の scope の不足など) でも起きる。どちらかの分類に寄せると、もう一方で誤った手当てを示すので分類を増やさない。glab は remote から API の host を選ぶので、ssh の alias などから別の host を撃って拒否されうる (internal/gitlab の CurrentProject のコメント)。そのときに拒否した host を読めるよう URL を残す
  - `gitlab` の `author` の判定は作者の access level を project の member の API で読む。この API は認証が要るので、glab が未認証なら public な project でも認証の失敗になる
  - acli はどの失敗も exit 1 で返し、文言は Jira の言語の設定で訳されるので、文言では分けない。読み出しが落ちたら `acli jira auth status` を撃ち、落ちれば認証の失敗とする。通れば、起動時の issue 置き場の確認 (§5・§6・§7.4) では見えない (exit 2)、tick の中ではその他の失敗 (tick の error) とする
    - rate limit は見分けられず、その他の失敗に入る。tick の error として残り、次の周期で読み直す
    - `acli jira auth status` は server に問い合わせる。network が落ちていると、これも落ちて認証の失敗になる

## 4. log.jsonl

1 行 1 JSON object。append-only。どの行も `ts` (RFC 3339 の UTC、秒まで)・`scope` (scope key)・`event` を持つ。

| `event` | いつ | ほかの key |
|---|---|---|
| `tick` | tick の終わり | `result` (`ok` / `error`)・`candidates` (候補の数)・`launched` (起動した作業対象の列)・`ambiguous` (曖昧な CL。`{"head": <head branch>, "targets": [<作業対象>…]}` の列)・`blocked` (事前検査 (§2.9) に落ちて起動しない trigger。`{"trigger": <名前>, "error": <理由>}` の列)・`running`・`waiting_retry`・`max_concurrent` (起動の判定に使った値。`result` が `ok` のとき)・`error` (`result` が `error` のとき) |
| `start` | worker を起動した | `target`・`trigger`・`attempt`・`session_id`・`workspace`・`pid` |
| `end` | worker 1 回分の終わり方を決めた | `target`・`trigger`・`attempt`・`session_id`・`outcome`・`reason`・`exit_code` (process が自分で終わったときだけ。signal で止まったら載せない)・`is_error`・`num_turns`・`permission_denial_count` (attempt の最後の `result` の要約。下の箇条) |
| `retry` | `failed` の後、次の attempt を予定した | `target`・`trigger`・`attempt`・`next_attempt`・`session_id`・`backoff` (秒。小数を含む) |
| `release` | 再起動を待つ claim を、起動せずに解いた | `target`・`trigger`・`attempt`・`session_id`・`reason` (`終端` / `trigger から外れた` / `trigger が workflow 定義から消えた` / `曖昧な CL`) |
| `abandon` | attempt の上限で打ち切った | `target`・`trigger`・`attempt`・`session_id` |
| `wait_slot` | backoff が明けた再起動が、並列上限に空きが無くて待ち始めた (1 回の待ちにつき 1 行) | `target`・`trigger`・`next_attempt`・`max_concurrent` |
| `unabandon` | 打ち切りを解いた (打ち切ったときの trigger から外れたのを観測した) | `target`・`trigger` |
| `error` | 処理は続けるが、運用者が知るべき失敗 | `target` (あれば)・`error` |

- `target` は、issue なら `issue#<番号>`、CL なら `cl#<番号>`
- `tick` の行の起動の判定に使った値は、§6 の tick の手順 7 (候補の起動) の判定のもの
  - `running`・`waiting_retry`: その tick で候補の起動を始める前の、走っている worker と再起動待ちの claim の数 (手順 7 が `limits.max_concurrent` から引く数)
  - `max_concurrent`: その tick で読み直した workflow 定義の `limits.max_concurrent`
  - 手順 7 は、`running`・`waiting_retry`・その tick で起動した数 (`launched` の数) の和が `max_concurrent` に達したところで候補の起動をやめる
- `outcome`

| 値 | 意味 |
|---|---|
| `completed` | worker が終わった後、作業対象が起動した trigger から外れていた。または終端になっていた。worker 自身が失敗していても completed とし、`reason` に失敗を添える |
| `failed` | worker が終わった後も作業対象が trigger に当たったままで、worker が失敗した (異常終了・hook の失敗・描画の失敗・起動できない・stall・上限時間の超過) か、正常に終わった。claim は解かず、`retry` か `abandon` の行が続く |
| `stopped` | loop が止めた (作業対象が終端になった・2 回目の停止要求) |

- `error` の行を書く場面: `after_run` と `before_remove` の失敗・workspace を消せない (次の tick の掃除で消し直す)・終わった worker の作業対象を読み直せない (claim を持ったまま次の tick で読み直す)・起動しようとした作業対象を読み直せない (その tick は起動せず、次の tick で候補になれば試み直す)・止める worker の process group に signal を送れない・worker log から attempt の最後の `result` を読めない (項目の型が違うときを含む)
- log.jsonl に行を書けなければ、そのことを stdout に出して続ける
- `attempt` は、その行が指す claim の最後に起動した (起動しようとした) attempt
- `end` の行の attempt の最後の `result` の要約: worker log (stream-json。§1) のうち、その attempt が追記した完結した行 (改行で終わる行) から、最後の `type: result` の行を読む。worker log は attempt を跨いで追記するので、前の attempt の `result` は読まない
  - `is_error`・`num_turns`: その `result` の同じ名前の項目の値
  - `permission_denial_count`: その `result` の `permission_denials` (配列) の要素の数
  - `result` の行が無ければ (claude を起動できなかった・stall や上限時間や signal で `result` を書く前に止めた 等) 3 つとも載せない。`result` の行にその項目が無ければ、その key だけ載せない。項目の型が違えば (`num_turns` が数でない 等)、その key を載せず `error` の行を残す。worker log を最後まで読めなければ (どれが最後の `result` か決まらない)、3 つとも載せず `error` の行を残す
  - 止めた attempt (stall・上限時間・停止要求) でも、止める前に `result` の行を書いていれば要約を載せる
- `session_id` は CLI が発行して最初の attempt に `--session-id` で渡した値。同じ claim の attempt を通して 1 つ。Claude Code の transcript へ辿る鍵
- claim を解くのは `completed` と `stopped` の `end`・`release`・`abandon` の行

## 5. 試運転 (`loop --dry-run`)

```
claude-dispatcher loop --dry-run [<workflow の path>]
```

workflow 定義を読んで検査し、事前検査 (§2.9) を通し、snapshot を作って trigger を評価し、起動するはずの作業対象を示して終わる。**何も書かない** (state dir を作らず、lock も取らない)。

成功したら、候補を 1 件 1 行で、評価の順 (§2.4) に stdout へ出す。候補が無ければ何も出さない。

```
implement	issue	#42	ログインの失敗を記録する
fix-ci	cl	#51	ログインの失敗を記録する
```

- 列はタブで区切る: trigger の名前・作業対象の種類 (`issue` / `cl`)・参照・題名
- 参照は tracker の綴りで書く: issue と GitHub の CL は `#<番号>`、GitLab の CL (merge request) は `!<番号>`、Jira の issue は key (`WIDGETS-123`)。GitLab では issue と merge request が別々に番号を振るので、同じ番号が両方にありうる
- `jira` では、snapshot を作る前に issue 置き場を確かめる (§6 の「起動時の検査」の `jira` の行と同じもの)
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
| workflow 定義を読めて、検査に通る (§2.8) | 2 |
| 事前検査 (§2.9) に通る | 2 |
| tracker の CLI (`tracker.kind` が `github` なら gh、`gitlab` なら glab、`jira` なら acli) と `claude.command` を解決できる (PATH と、よく使われる置き場)。`on: cl` の trigger があれば git も | 1 |
| `jira` だけ: issue 置き場の確認 (下) | 下 |
| state dir を作れる | 1 |
| 同じ scope key の loop が走っていない (`loop.lock` を取れる) | 3 |

- よく使われる置き場は、workflow 定義が撃つ依存 CLI (`tracker.kind` の CLI・claude・git) のどれかが PATH に無いときだけ PATH の前に足す。使わない方の tracker の CLI が無いことでは PATH を書き換えない (worker と hooks は loop の PATH を継ぐので、足すと worker が撃つ git などが入れ替わりうる)
- 同じ issue 置き場の 2 本目の loop は、clone や workflow 定義の path が違っても起動時に止まる
- `jira` の issue 置き場の確認は、次の順に撃ち、最初に落ちたもので終わる。試運転 (§5) と `doctor` (§7.4) も同じ確認を通す
  1. acli の認証の site: `acli jira auth status` が通り (落ちれば認証の失敗、exit 4)、出力の `Site:` の行の site が `tracker.host` と同じ (大文字と小文字を区別しない)。違うか `Site:` の行を読めなければ exit 2。acli は呼び出しごとに site を指定できず、active な account の site を読むため
  2. open な一覧 (`project = "<project key>" AND statusCategory != Done`) を読める。落ちたら §3 の acli の分類で、認証 (exit 4) か見えない (exit 2)
  3. trigger の `status` に書いた status 名が実在する (§2.2)。無い名前を trigger と一緒に名指しして exit 2。検索が落ちたら §3 の acli の分類で、認証なら exit 4。それ以外は名前の誤りと読み、acli の理由を添えて名指しする
  4. trigger の `type` に書いた issue type 名が project にある (§2.2)。無い名前を trigger と一緒に名指しして exit 2。project の issue type を読めなければ §3 の acli の分類で、認証 (exit 4) か見えない (exit 2)
- lock は loop の生存期間だけ持つ。loop が死ねば外れる
- `loop.lock` を取った loop は、続けて `alive.lock` も生存期間のあいだ持つ。`status` は `alive.lock` だけを確かめるので、`loop.lock` を取り合わない (`alive.lock` を `status` が一瞬持っていれば、外れるまで待つ)

**tick**: 起動直後に 1 回撃ち、以後は tick の終了から `polling.interval` だけ待って次を撃つ。tick ごとに workflow 定義を読み直す。

- 読み直した workflow 定義が検査に落ちたら、その tick は何も起動しない (#74 Q38)。周期は、最後に検査に通った版のものを使う
- 読み直した workflow 定義の scope key が起動時と違えば、その tick は何も起動しない (lock は起動時の scope key で取っている)

**tick の手順** (system.md §7):

作業対象の読み直しと open な一覧は、issue なら issue 置き場から、CL なら CL 置き場から読む。open な一覧は trigger に現れる種類のものだけを読む (§2.4)。

1. 突き合わせ: 走っている worker の作業対象を読み直し、終端 (issue の close、CL の merge か close) になっていれば worker を止め、`after_run` と `before_remove` を撃って workspace を消す。trigger から外れただけでは止めない
2. 終わった worker のうち、作業対象を読み直せなかったものを読み直す
3. workflow 定義を読み直し、事前検査 (§2.9) を通して snapshot を作る。事前検査に落ちた trigger は、手順 6 で再起動せず、手順 7 の評価から外す
4. 掃除: workspace root の下の `issue-<番号>` と `cl-<番号>` のうち、claim が無く open な一覧にも無いものを読み直し、終端になっていれば `before_remove` を撃って消す。起動の直後の tick が起動時の掃除を兼ね、以後の tick が、claim を解いた後に終端になったものと消し損ねたものを拾う
5. 打ち切りを解く: 打ち切った作業対象のうち、snapshot に無いか、打ち切ったときの trigger の述語に当たらなくなったもの (その trigger が workflow 定義から消えたもの・曖昧な CL になったものを含む) の打ち切りを解く。conflict を計算中の CL は、外れたとは数えない
6. 再起動: backoff の明けた再起動待ちの claim を、次の「再起動」の規則で起動する
7. trigger を評価し (§2.4)、候補のうち claim も打ち切りもされていないものを、`limits.max_concurrent` から走っている worker と再起動待ちの claim を引いた数だけ起動する (確かめ待ちの claim と、事前検査に落ちた trigger の再起動待ちの claim は数えない)
   - 起動の直前に、候補を 1 件ずつ読み直す (open な一覧の検索は、書き込みの直後に古い結果を返しうるため)。tracker の種類を問わず読み直す。次のときはその tick では起動せず、起動しなかった分の空きを次の候補に回す。次の tick で候補になれば試み直す
     - 当たるかをまだ決められない (conflict を計算中の CL。§2.3)・終端になっている・起動しようとした trigger から外れている・宣言順で先の trigger に当たるようになっている。人が読む行に `読み直しで起動せず` を出す
     - 読み直せない。error の行を残す
   - CL は、claim している作業対象の workspace で checkout されている branch を head に持つもの (同じ repo の CL) なら起動しない。一覧の CL で当たれば読み直さずに外し、読み直した CL でも確かめ直す。branch は workspace を cwd にして `git symbolic-ref --short -q HEAD` で読む。workspace が無い・git の作業ツリーでない・detached HEAD なら branch は無いとする
   - branch をそれ以外の理由で読めなければ、error の行を残し、その tick は CL の候補を起動しない (同じ branch に worker を重ねないため)
   - 起動した worker には、読み直した作業対象を渡す

**再起動** (system.md §7「失敗の扱い」):

- `failed` の後、attempt が `limits.max_attempts` に達していれば打ち切る。達していなければ、`min(10s × 2^(attempt−1), limits.max_retry_backoff)` の後に再起動を予定する
- backoff が明けたら、tick を待たずに再起動を試みる (tick の途中なら、その tick の手順 6 で試みる)
  - 走っている worker が `limits.max_concurrent` に達していれば、attempt を進めずに待ち直す。worker が終わって空きが出たときに試み直す
  - 起動した trigger が workflow 定義から消えていれば、claim を解く (`release`)
  - 作業対象を読み直す。終端なら `before_remove` を撃って workspace を消し、trigger から外れていれば、claim を解く (`release`)。読み直せなければ次の tick で試み直す
  - CL は、tick の中でだけ再起動を試みる (曖昧さと branch は tick が読んだ一覧で確かめるため)。曖昧な CL になっていれば claim を解き (`release`)、head branch を別の claim の workspace が checkout しているか、その branch を読めなければ、attempt を進めずに次の tick で試み直す
  - 当たるかをまだ決められなければ (conflict を計算中の CL。§2.3)、attempt を進めずに次の tick で試み直す
  - 当たったままなら、attempt を 1 つ進め、同じ trigger・同じ session id・同じ workspace で起動する
- 再起動は、そのときの workflow 定義 (trigger・本文・hooks・`claude`・`limits`) で行う。workspace の root だけは、最初に起動したときのものを使う (session は workspace の path ごとに保存されるので)
- 前の attempt で claude を起動できていなければ (hook・描画・起動の失敗)、session はまだ無いので `--session-id` で始める
- 停止要求を受けた後に失敗した attempt は、再起動を予定しない
- 打ち切りは作業対象ごとに memory に持つ。打ち切った作業対象は、どの trigger に当たっても起動しない。loop を起動し直すと消える
- 打ち切りを解くには、label を外して打ち切ったときの trigger から外し、1 周期待ってから付け直す (外してから付け直すまでが 1 周期に収まると、外れたのを観測できない)。`status` (§7) の打ち切りの行にこの手順を出す

- 作業対象の読み直しで issue が消えていたら (削除・移管)、終端と同じに扱う。消えたと読むのは次のとき
  - `github`: gh が `Could not resolve to an Issue` を返すか、応答に issue が無い
  - `gitlab`: glab が `(HTTP 404)` を返し、stderr に `Project Not Found` が無い (project が見えないときは `404 Project Not Found` なので、見えないの失敗として扱う)
  - `jira`: 次の手順で読み直す。issue を消した・別の project へ移した (旧 key が転送されてもされなくても)・権限を外して見えなくなった、のどれも終端として扱う
    1. `acli jira workitem view <key> --json` で読む。返った key の project key が `tracker.repo` と違えば (別の project へ移した) 終端
    2. 読み出しが落ちたら `acli jira auth status` を撃つ。落ちれば認証の失敗
    3. 通れば open な一覧を読む。読めればその中に key が無ければ終端、あれば読み直せない失敗。一覧を読めなければ、その失敗
    - 1 件の読み直しを JQL の `key = …` で撃たない。存在しない key を JQL に書くと検索ごと失敗し、消えた issue と見えない issue を区別できない

**worker の 1 回分**:

1. workspace が無ければ作って `after_create` を撃ち、`before_run` を撃つ
2. action と本文を描画し、本文を `prompts/<作業対象>.md` に書く
3. 次の argv で起動する。cwd は workspace、stdin は空。stdout (stream-json) は `workers/<作業対象>.log`、stderr は `workers/<作業対象>.stderr.log` に追記する (claude には file をそのまま渡す)

   ```
   <claude.command> <claude.args…> -p --output-format stream-json --verbose <session> --append-system-prompt-file <prompts の path> -- <描画した action>
   ```

   - `<session>` は、前の attempt で session を始めていれば同じ id で `--resume <uuid>` (止めた session の続きから始まる)、始めていなければ `--session-id <uuid>`
   - action は `--` の後ろに置く (`-` で始まる action を option と読ませない)
   - stream に活動 (下の「活動」) として数える行が `limits.stall_timeout` のあいだ書かれないか、起動からの経過が `limits.run_timeout` を超えたら、process group を止めて失敗とする (止め方は 2 回目の停止要求と同じ)。止める判断と停止要求が重なったら、停止要求で止めたことにする

4. 終わったら (止めたときを含む)、worker log からその attempt の最後の `result` を読んで `end` の行に要約を載せ (§4)、`after_run` を撃ち、作業対象を読み直す。終端か、起動した trigger から外れていれば `completed` として claim を解き、当たったままなら `failed` として再起動 (上) に回す
   - 当たるかをまだ決められなければ (conflict を計算中の CL)、読み直せなかったときと同じく、error の行を残して claim を持ったまま次の tick で確かめ直す
   - workspace を消すときは、worker を最初に起動したときの `workspace.root` と、消す時点の workflow 定義の hooks を使う

**画面**: loop の stdout が端末かどうかで形を変える。どちらも、状態 file (§7) と同じ中身を描く。

- 端末でなければ、log.jsonl (§4) に書く行と worker の活動を、人が読む形で 1 行ずつ追記する (下の例)。時刻は RFC 3339 の UTC
- 端末なら、周期ごと (1s) とログの行ごとに画面を描き直す。上から、`status` (§7.2) と同じ見出し、停止待ちの案内、`status` と同じセクション (`workers`・`要対処`)、`ログ` のセクション
  - 停止待ちの案内: 停止要求を受けた後、段階が `running` の worker (2 回目の停止要求が止める worker) がある間、見出しの下に `worker の終了を待っている。もう一度 Ctrl+C で走っている worker <n> 本を止めて終える` を出し続ける (`<n>` は段階が `running` の worker の数)
  - `ログ`: 下の行のうち活動の行を除いた、直近の 10 行。時刻は端末の local time の `HH:MM:SS`。最新の活動は `workers` の表の列で見る
  - 色と幅は §7.2 の規則に従う。`ログ` の行は、時刻を灰にし、行の頭の語に色を付ける: `起動` は青、`終了` は completed なら緑・failed なら赤、`error` は赤、`再起動を予定` と `停止待ち` は黄。`tick ok` の行は全体を薄く、`tick error` の行は全体を赤にする
- stdout に書けなくても loop は止めない (その行と画面は捨てる)

```
<時刻> loop を始めた: scope <scope key> · state dir <path> · workflow <path>
<時刻> tick ok · 候補 2: implement issue #42, fix-ci cl #51
<時刻> tick ok · 候補 0 · 曖昧な CL: worktree-issue-7 (cl#52, cl#53)
<時刻> tick ok · 候補 1: implement issue #42 · 起動しない trigger: implement (action の先頭の /playbook が見つからない …)
<時刻> tick error · <理由>
<時刻> 起動 issue#42 (implement, attempt 1, session <uuid>)
<時刻> 読み直しで起動せず issue#42 (implement): trigger から外れた
<時刻> 活動 issue#42: Bash go test ./...
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

端末の画面 (停止待ち):

```
● loop 停止待ち  github.com/acme/widgets
次の tick なし · 直近の tick 00:05:00 ok · 候補 1
走っている worker 1 本の終了を待っている。もう一度 Ctrl+C で worker を止めて終える

workers 1 ────────────────────────────────────────────
作業対象  trigger    attempt  段階     経過   活動
issue#42  implement  1        running  5m10s  Edit internal/status/status.go

ログ ─────────────────────────────────────────────────
00:00:00 loop を始めた: scope github.com/acme/widgets · state dir <path> · workflow <path>
00:00:00 tick ok · 候補 1: implement issue #42
00:00:00 起動 issue#42 (implement, attempt 1, session <uuid>)
00:05:00 tick ok · 候補 1: implement issue #42
00:05:10 停止待ち: 走っている worker 1 本の終了を待つ (もう一度で止める)
```

- tick の行の候補は、試運転 (§5) と同じ順で、`<trigger の名前> <種類> <参照>` を並べる。参照の綴りは試運転と同じ。候補が無ければ `tick ok · 候補 0`
- **活動**: worker の stream (stdout の stream-json) のうち、活動として数える最新の完結した行の要約。loop は 1s ごとに確かめ、変わったら `活動` の行を出す。log.jsonl には書かない (stream は worker log に残る)。`type: result` の行は、活動とは別に attempt の終わりに worker log から読み直し、`end` の行に要約を載せる (§4)

| stream の行 | 要約 |
|---|---|
| `type: assistant` の message の最後の content が text | その text の最初の行 |
| `type: assistant` の message の最後の content が thinking | `thinking` |
| `type: assistant` の message の最後の content が tool_use | `<tool の名前> <入力の要点>` (下の表。要点が無ければ tool の名前だけ) |
| `type: assistant` のそれ以外 (content が無い・最後の content が上のどれでもない) | `assistant` |
| `type: user` (tool の結果) | `tool の結果` |
| `type: system` で subtype が `init` | `system init` |
| `type: system` のそれ以外 | 活動として数えない |
| `type: result` | `result <subtype>` |
| それ以外の `type` (`tool_progress`・`rate_limit_event` を含む) | 活動として数えない |

| tool | 入力の要点 |
|---|---|
| `Bash` | `command` の最初の行 |
| `Read` / `Edit` / `Write` | `file_path`。workspace の中なら workspace からの相対 path、外なら絶対 path のまま |
| `Grep` / `Glob` | `pattern` |
| `Skill` | `skill` |
| `Agent` | `description` |
| それ以外 (MCP の tool を含む) | 無し |

- 入力を読めなければ (要点の項目が文字列でないなど)、要点を出さずに tool の名前だけにする

- 秘密が混ざりやすい入力 (`Write` の本文・MCP の tool の引数) は載せない
- 要約の制御文字 (タブ・改行・ESC など) は空白に置き換え、80 文字で切る (`…` を足す)
- 表で活動として数えない行は、worker が何をしているかを表さない。数えると、直前に数えた行 (tool_use の要点など) を上書きする
- JSON として読めない行 (項目の型が要約に使う形と合わない行を含む) と、`type` の無い行は、表によらず活動として数えない。改行を含めて 64 KiB を超える行も数えない (stream の file の末尾だけを読む)
- 前に確かめてから完結した行を新しい側から見て、活動として数える最初の行を要約する。数える行が無ければ活動は変えない
- 活動は今の attempt が書いた行だけから取る (worker log は attempt を跨いで追記する)。同じ行を活動として出し直さない
- stream の file を読めなければ、`stream を読めない: <理由>` を活動にする。活動の表示のためには worker を止めない
- stall (`limits.stall_timeout`) の時計は、活動として数える行を見つけるたびに戻す。要約が直前と同じでも戻す。数えない行・64 KiB を超える行・読めないあいだの stream では戻らないので、その状態が `limits.stall_timeout` 続けば stall で止める。読めないまま stall で止めたときは、失敗の理由に読めない理由を添える

**停止**: SIGINT / SIGTERM / SIGHUP を同じに扱う。

| 受けたとき | 振る舞い |
|---|---|
| 1 回目 | 新しい tick と起動 (再起動を含む) をやめ、走っている worker の終了を待って (終わり方の処理を済ませて) 止まる。待つ間も周期ごとに突き合わせ (tick の手順 1) だけを撃ち、終端になった作業対象の worker は止める。走っていなければ直ちに止まる。再起動待ちの claim は error の行を残して捨てる |
| 2 回目 | 走っている worker の process group に SIGTERM を送り、5s で終わらなければ SIGKILL を送る。worker が終わったら group に残った process も SIGKILL で止め、`after_run` を撃ち、`stopped` の `end` 行を書いて止まる |

exit: 0 = 停止要求で止まった / 1・2・3 = 起動時の検査 (上表)。

## 7. 状態 file / `status` / `paths`

### 7.1 状態 file (`status.json`)

loop の今の状態を、表示のためだけに書き出す (system.md §9)。loop は読み戻さない。

- loop が、tick の終わり・ログの行の後・1s ごとに書き換える。書くのは loop だけで、同じ dir の一時 file から rename する (読み手に書きかけを見せない)
- 経過は書かない。時刻を書き、読む側が今の時刻から出す
- loop は最初の tick の前と止まる直前にも書く。止まった後も残る。loop が生きているかは状態 file ではなく `alive.lock` (§6) で決める

```json
{
  "scope": "github.com/acme/widgets",
  "workflow": "/path/to/WORKFLOW.md",
  "started_at": "2026-10-01T00:00:00Z",
  "updated_at": "2026-10-01T00:05:00Z",
  "stopping": false,
  "next_tick_at": "2026-10-01T00:10:00Z",
  "last_tick": {"at": "2026-10-01T00:05:00Z", "result": "ok", "candidates": 1},
  "workers": [
    {"target": "issue#42", "trigger": "implement", "attempt": 1, "session_id": "<uuid>", "phase": "running",
     "started_at": "2026-10-01T00:05:00Z", "activity": {"at": "2026-10-01T00:05:10Z", "summary": "Bash go test ./..."}}
  ],
  "abandoned": [{"target": "issue#43", "trigger": "implement"}],
  "ambiguous": [{"head": "worktree-issue-7", "targets": ["cl#52", "cl#53"]}],
  "blocked": [{"trigger": "fix-ci", "error": "action の先頭の /fix が見つからない (plugin・repo の .claude・~/.claude の skill と command)"}]
}
```

| key | 中身 |
|---|---|
| `stopping` | 停止要求を受けて、worker の終了を待っている |
| `next_tick_at` | 次の tick の予定。停止要求の後は `null` |
| `last_tick` | 直近の tick。`result` が `error` なら `error` に理由 (事前検査の失敗 — workflow 定義の誤り・scope key の食い違い・観測の失敗 — を含む)。まだ tick が無ければ `null` |
| `workers[].phase` | `running` (走っている) / `stopping` (止めている) / `verifying` (終わり方を確かめ待ち) / `waiting_retry` (再起動待ち) |
| `workers[].started_at` | 今の attempt の worker を起動した時刻。起動の前 (再起動待ち) は `null` |
| `workers[].retry_at` | `waiting_retry` の backoff が明ける時刻。停止要求の後に失敗した claim は再起動を予定しないので、無い |
| `workers[].activity` | 活動 (§6 の画面)。まだ無ければ `null` |
| `abandoned` | 打ち切った作業対象と、打ち切ったときの trigger |
| `ambiguous` | 直近の tick の曖昧な CL (§2.4) |
| `blocked` | 直近の tick で事前検査 (§2.9) に落ちて、起動しない trigger |

### 7.2 `status`

```
claude-dispatcher status [<workflow の path>]
```

別の端末から loop の今の状態を見る。workflow 定義を読んで scope key を決め、その state dir の状態 file を描く。**何も書かず、tracker の CLI も撃たない**。

```
● loop 稼働中  github.com/acme/widgets
次の tick 00:10:00 · 直近の tick 00:05:00 ok · 候補 1

workers 2 ────────────────────────────────────────────
作業対象  trigger    attempt  段階           経過               活動
issue#42  implement  1        running        5m10s              Bash go test ./...
issue#44  implement  2        waiting_retry  00:06:40 に再起動

要対処 3 ─────────────────────────────────────────────
打ち切り issue#43 (implement): label を外して trigger から外し、1 周期待ってから付け直すと解ける
曖昧な CL worktree-issue-7: cl#52, cl#53
起動しない trigger fix-ci: action の先頭の /fix が見つからない (plugin・repo の .claude・~/.claude の skill と command)
```

- 見出しは 2 行。1 行目は loop の状態と scope key: loop が生きていれば `● loop 稼働中` (停止要求の後は `● loop 停止待ち`)、`alive.lock` を取れれば `○ loop なし`
- 2 行目は `次の tick <時刻>` (停止要求の後は `次の tick なし`)・`直近の tick <時刻> ok · 候補 <n>` を ` · ` で繋ぐ。直近の tick が error なら `直近の tick <時刻> error: <理由>`。`loop なし` のときは次の tick の欄を出さない。出す欄が無ければ 2 行目を出さない
- `loop なし` のときは見出しだけを出す (状態 file が残っていても、worker と打ち切りは loop の memory と一緒に消えている)
- 状態 file が無ければ、見出しの 2 行目は `記録なし`
- 見出しの後に空行を挟み、セクションを空行で区切って並べる。セクションの 1 行目はセクション名と件数の後に罫線を引く
- `workers <n>`: claim の表。列見出しの行 (作業対象・trigger・attempt・段階・経過・活動) の後に claim を 1 行ずつ並べる。claim が無ければ列見出しも出さない
  - 列は空白で揃える (列の間は 2 桁)
  - attempt は番号。再起動待ちの行は失敗した attempt の番号 (ログの行の `再起動を予定` は次の attempt の番号を出す)
  - 段階は `workers[].phase` (§7.1) の値のまま
  - 経過は、段階が `running` か `stopping` の claim の、今の attempt の起動からの時間 (`45s`・`5m10s`・`1h02m05s`)。`waiting_retry` の claim は `<時刻> に再起動`。停止要求の後に失敗した claim は再起動を予定しないので空
  - 活動は §6 の要約
- `要対処 <n>`: 打ち切り・曖昧な CL・起動しない trigger を 1 件 1 行で並べる。1 件も無ければセクションごと出さない
- 時刻は端末の local time の `HH:MM:SS`
- stdout が端末でなくても、描き方は同じ人が読む形 (列は空白揃え)。機械が読むなら状態 file (§7.1) を読む

**色と幅** (loop の画面 (§6) も同じ):

- 色は stdout が端末のときだけ付ける。端末でも、環境変数 `NO_COLOR` が空でない値で設定されていれば付けない
- 色を付けるところ:
  - 見出しの `● loop 稼働中` は緑、`● loop 停止待ち` は黄、`○ loop なし` は灰。scope key は太字。直近の tick の `ok` は緑、`error: <理由>` は赤。欄の名前 (`次の tick` など) は灰
  - セクション名は太字 (`要対処` は黄)、罫線は薄く、列見出しは灰
  - 段階は `running` が水色、`waiting_retry` が黄、`verifying` が灰
  - `要対処` の行の頭の語は、`打ち切り` と `起動しない trigger` が赤、`曖昧な CL` が黄
- stdout が端末なら、長い行を端末の幅で切り、`…` を付ける。幅は全角の文字と絵文字を 2 桁、結合文字・異体字セレクタなど幅を持たない文字を 0 桁、それ以外 (曖昧幅の `●` `─` `…` `·` を含む) を 1 桁と数える。制御文字 (改行・タブなど) は空白にして 1 行に収める。罫線は端末の幅まで引く
- stdout が端末でないか、端末の幅を取れなければ、行を切らず、罫線はセクションの 1 行目が 60 桁になるまで引く

exit: 0 = 描けた / 2 = 引数・workflow 定義の誤り / 1 = 状態 file か `alive.lock` を読めない、または workflow 定義の path を解決できない。

### 7.3 `paths`

```
claude-dispatcher paths --json [<workflow の path>]
```

workflow 定義から決まる置き場を JSON で stdout に出す。何も書かず、tracker の CLI も撃たない。

```json
{"scope_key": "github.com/acme/widgets", "state_dir": "<state dir>", "log": "<state dir>/log.jsonl",
 "status_file": "<state dir>/status.json", "workspace_root": "<workspace root>"}
```

exit: 0 / 2 = 引数・workflow 定義の誤り / 1 = workflow 定義の path を解決できない。

### 7.4 `setup` / `doctor`

```
claude-dispatcher setup [--kind jira --host <site> --repo <project key>] [<workflow の path>]
claude-dispatcher doctor [<workflow の path>]
```

**`setup`**: 実装 repo の clone を cwd にして撃ち、workflow 定義の雛形 (下) を path (既定 `WORKFLOW.md`) に書く。

- `--kind jira` を渡したら、`jira` の雛形を書き、`tracker.host` を `--host`、`tracker.repo` を `--repo` で埋める。git も tracker の CLI も撃たない
  - `--kind` の値は `jira` だけを受ける。`--kind jira` には `--host` と `--repo` の両方が要り、`--host` と `--repo` は `--kind jira` と一緒にだけ書ける。綴りは `tracker.host` と `tracker.repo` と同じ (§2.1)。外れたら引数の誤り (exit 2)
  - flag は `--kind jira` の形でも `--kind=jira` の形でも書ける
- flag が無ければ、`tracker.kind` を、cwd で `git remote get-url origin` が返す URL の host で決める。host が `github.com` なら `github`、それ以外は `gitlab`
  - URL は `https://<host>/…`・`ssh://<user>@<host>[:<port>]/…`・`<user>@<host>:…` (scp の綴り) を読む。host は port を除いて小文字にする
  - `github`: `tracker.repo` を、cwd で `gh repo view --json nameWithOwner` が返す repo で埋める
  - `gitlab`: cwd で `glab repo view --output json` が返す project で、`tracker.host` を `web_url` の host (port を含む) で、`tracker.repo` を `path_with_namespace` で埋める。origin の URL の host は ssh の host (alias・ssh 専用の host) でありうるので、`tracker.host` に使わず、glab にも渡さない (渡すと glab がその host の API を撃つ。URL が認証情報を含むときに argv へ載せないためでもある)
- path に file が既にあれば書かない (上書きしない)。そのことを stdout に出して exit 0
- 書いたら、path と、次に試運転 (§5) を撃つことを stdout に出す

exit: 0 = 書いた・既にある / 2 = 引数の誤り / 1 = repo を決められない (git・gh・glab の失敗、URL を読めない)・書けない。

**雛形**: `tracker.kind` ごとに持つ。`github` の雛形を下に置く。

- `gitlab` の雛形は、同じ trigger と action を "merge request"・`glab`・`Closes #N` の綴りで書く
  - `blocked` の述語を書かない (§2.2)
  - CL 側の trigger (conflict・review・CI) は `github` と同じ述語と絞り込みで置く
- `jira` の雛形は、issue 側の trigger `implement` だけを置く (CL 側の trigger は書けない。§2.1)
  - `status: { any: [Ready for Agent] }` に当てる。status 名は project ごとに違い、acli は project の status の一覧を返さないので並べない。雛形の comment に「project の status 名に合わせて書き換える」と書く (site に無い名前は起動時の確認が名指しする)
  - action は、issue `{{ .issue.key }}` を実装し、branch `claude-dispatcher/{{ .issue.key }}` で CL を出し、CL の題名に key を入れ、acli で status を Ready for Agent から移す旨。branch と CL の題名に key を入れるのは、Jira と GitHub / GitLab の連携を入れた project で、issue に branch と CL が出るため
  - `type` の trigger は置かない (issue type は project ごとに違う)

汎用の action (実装・conflict・review・CI) を置く。action は先頭に skill を書かない文で、plugin の無い環境でも事前検査 (§2.9) に通る。承認済みの CL (`approved: true`) に当てる trigger は置かない (merge を worker に任せるかは利用者が決める)。

```markdown
---
tracker:
  kind: github
  repo: <setup が埋める owner/name>
polling:
  interval: 5m
hooks:
  after_create: git -C "$CLAUDE_DISPATCHER_CLONE" worktree add --detach "$CLAUDE_DISPATCHER_WORKSPACE"
  before_remove: git -C "$CLAUDE_DISPATCHER_CLONE" worktree remove --force "$CLAUDE_DISPATCHER_WORKSPACE"
limits:
  max_concurrent: 1
claude:
  args: [--permission-mode, auto]
triggers:
  - name: implement
    on: issue
    when:
      labels:
        all: [ready-for-agent]
      blocked: false
    action: |
      issue #{{ .issue.number }} ({{ .issue.url }}) を実装する。branch claude-dispatcher/issue-{{ .issue.number }} で pull request を出し、ready-for-agent を外す。…
  - name: resolve-conflict
    on: cl
    when:
      conflict: true
      head: claude-dispatcher/*
      draft: false
    action: |
      pull request #{{ .cl.number }} ({{ .cl.url }}) の conflict を解く。head branch を detach で取り出し、`git push origin HEAD:{{ .cl.head }}` で push する。…
  - name: address-review
    on: cl
    when:
      review_unresolved: true
      head: claude-dispatcher/*
      draft: false
    action: |
      pull request #{{ .cl.number }} ({{ .cl.url }}) の未解決の review に応える。…
  - name: fix-ci
    on: cl
    when:
      ci_failed: true
      head: claude-dispatcher/*
      draft: false
    action: |
      pull request #{{ .cl.number }} ({{ .cl.url }}) の失敗している CI を直す。…
  # 承認済みの CL に当てる trigger (approved: true) は置かない。merge を worker に任せるなら自分で足す
---
<共通 prompt: 無人の worker としての作業規約>
```

- 上は抜粋 (action の文と本文を略した)。全文は `internal/scaffold/WORKFLOW.md` (`gitlab` は `internal/scaffold/WORKFLOW.gitlab.md`、`jira` は `internal/scaffold/WORKFLOW.jira.md`)
- 実装の worker は `claude-dispatcher/issue-<番号>` の branch で CL を出し、CL 側の trigger は `head: claude-dispatcher/*` で自分の出した CL に絞る
- CL の worker は head branch を detach で取り出して push する。issue の workspace は issue が閉じるまで残り、その branch を checkout したままで、同じ branch は 2 つの worktree で checkout できないため

**`doctor`**: 導入を確かめる。何も書かない。確かめたことを 1 件 1 行で、`ok` / `NG` / `警告` を頭に付けて stdout に出す。

1. workflow 定義を読めて、検査 (§2.8) に通る。落ちたら以降は確かめない
2. tracker の CLI (gh・glab・acli) の認証が通り、issue 置き場が見える (open な作業対象を読める)。issue 置き場は `github` なら `<owner>/<name>`、`gitlab` なら `<host>/<path>`、`jira` なら `<site>/<project key>` で出す
   - `jira` は loop の起動時と同じ issue 置き場の確認 (§6) を通す。落ちたら理由を `NG` の行に出す (存在しない status 名と issue type 名は、1 つ 1 行で trigger と一緒に名指しする)
3. tracker の CLI と `claude.command` を解決できる。`on: cl` の trigger があれば git も (loop の起動時の検査と同じ。§6)
4. 事前検査 (§2.9)。落ちた trigger ごとに `NG` の行
5. 利用者の約束に頼る宣言を `警告` の行に出す (system.md §11)
   - `approved: true` の CL 側の trigger (merge を worker に任せうる)
   - `head`・`labels.all`・`labels.any`・`same_repo: true` のどれも書いていない CL 側の trigger (人の CL や fork の CL に worker を送りうる)
6. Claude Code の settings (`permissions.allow`) に要りそうな entry を表示する。CLI は settings を書かない (ADR 0004)
   - `github`: `Bash(gh issue:*)`・`Bash(gh pr:*)`・`Bash(git push:*)`
   - `gitlab`: `Bash(glab issue:*)`・`Bash(glab mr:*)`・`Bash(git push:*)`
   - `jira`: `Bash(acli jira workitem:*)`・`Bash(git push:*)` と、entry の列の後に「CL を開く CLI (gh pr / glab mr) の entry も足す」の 1 行 (entry と取り違えて settings に写されないよう、字下げしない)。CL を開く CLI は workflow 定義から決められない (CL 置き場の宣言は #114)

```
ok   workflow 定義 /path/to/WORKFLOW.md
ok   issue 置き場 acme/widgets
ok   gh /opt/homebrew/bin/gh
ok   claude /opt/homebrew/bin/claude
ok   git /usr/bin/git
NG   trigger fix-ci: action の先頭の /fix が見つからない (plugin・repo の .claude・~/.claude の skill と command)
警告 trigger merge: approved: true の CL に action を当てている (merge を worker に任せうる)
警告 trigger fix-ci: head・labels.all・labels.any・same_repo: true のどれでも絞っていない (人の CL や fork の CL に worker を送りうる)
settings の permissions.allow に要りそうな entry (CLI は書かない):
  Bash(gh issue:*)
  Bash(gh pr:*)
  Bash(git push:*)
```

exit: 0 = `NG` が無い (`警告` は落とさない) / 1 = `NG` がある / 2 = 引数の誤り。

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
