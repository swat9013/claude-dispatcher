# claude-dispatcher の案内

`claude-dispatcher guide` が出す文書。導入の流れ・loop の回し方と止め方・action が守る約束・workflow 定義 (`WORKFLOW.md`) の書き方を書く。中身は、この binary と同じ版の形式。

claude-dispatcher は、tracker (GitHub・GitLab・Jira Cloud) の issue と CL (GitHub の pull request・GitLab の merge request) を周期ごとに読み、workflow 定義が宣言した trigger に当たったものへ Claude Code の worker (`claude -p` で無人で起動するセッション) を起動する。CLI は tracker にも CL にも書かない。label を付け替える・CL を開く・人へ返すのは worker と人の仕事。

この文書での語:

- **作業対象**: worker を起動する単位。issue 1 件か CL 1 本
- **trigger**: 作業対象に当てる述語 (`when`) と、当たったときに worker へ渡す指示 (`action`) の組
- **tick**: 周期ごとに 1 回、作業対象を読んで trigger を評価し、worker を起動する処理
- **終端**: 作業対象が閉じた状態 (issue の close、CL の merge か close、Jira の status の区分が `done`)

節:

- 導入の流れ
- 回す・見る・止める
- action が守る約束
- workflow 定義の書き方: 項目 / issue 側の述語 / CL 側の述語 / 評価の規則 / `$VAR` による間接参照 / hooks / template / 検査 / 事前検査

全文は長いので、file に保存してから要る節を引いてもよい (`claude-dispatcher guide > guide.md`)。

## 導入の流れ

実装 repo の clone を cwd にして、次の順に撃つ。`setup`・`loop`・`run`・`status`・`paths`・`doctor` は、workflow 定義の path を最後の引数で渡せる (省けば cwd の `WORKFLOW.md`)。

1. **雛形を置く**: `claude-dispatcher setup` が cwd に `WORKFLOW.md` の雛形を書く。既にある file は上書きしない
   - origin の host が `github.com` なら GitHub の雛形で、`tracker.repo` は `gh repo view` が返す repo で埋まる
   - それ以外の host なら GitLab の雛形で、`tracker.host` と `tracker.repo` は `glab repo view` が返す project の web の host と path で埋まる
   - Jira を issue 置き場にするなら `claude-dispatcher setup --kind jira --host <site> --repo <key>` (`<key>` は Jira の project key)。CL 置き場 (`cl:`) は、origin の host で GitHub か GitLab を決め、上と同じ手順で埋まる
2. **自分の project に合わせて書く**: trigger の述語・action・hooks を直し、repo に commit する。書ける項目は下の「workflow 定義の書き方」
   - worker が作業を終えたら作業対象を trigger から外すよう、action か本文 (共通 prompt) に書く (下の「action が守る約束」)
   - workspace の置き場 (`workspace.root`) が clone の中なら、`.gitignore` に足す。既定の `.claude-dispatcher/workspaces` は clone の中に入る。`setup` は `.gitignore` を書かない
   - **チームで分担するなら、調停 remote を宣言する**: 同じ issue 置き場を複数のマシンの loop で分け合うなら、`WORKFLOW.md` に `coordination.remote: origin` (clone の git remote の名前) を書いて commit する。書かなければ 1 台で回す
     - loop は、その remote の `refs/dispatcher/` の下に、作業対象ごとの着手宣言と loop ごとの生存報告を置き合い、先に置けた loop だけが worker を起動する。branch・CL・tracker には書かない
     - loop を回す各マシンの clone から、その remote へ push できる必要がある (認証は clone の git の設定をそのまま使う)。読む URL と書く URL が 1 本ずつの同じ URL の remote を指す (`pushurl` を別の repo に向けた remote や、URL が複数の remote は起動時に落ちる。その clone では別の remote 名を足して指す)。ssh の remote は、passphrase や host key の確認を出さない ssh (`ssh -o BatchMode=yes`) で撃つので、agent か鍵の設定で prompt なしに通るようにしておく。`core.sshCommand` か `GIT_SSH_COMMAND` を設定していれば、dispatcher は何も足さずにその設定を使う。push は `--no-verify` で撃つので、実装 repo の pre-push hook は通らない
     - 落ちたマシンの作業対象は、生存報告の間隔 (`polling.interval` と 1 分の長いほう) の 3 倍の後に、生きている loop が拾い直す。network が切れたマシンの worker は、戻ったときに乗っ取られていれば止まり、`要対処` に出る
     - attempt と打ち切りはマシンごとに数える。あるマシンで打ち切った作業対象を、別のマシンが拾い直しうる
     - 版はチームで揃えて上げる。新しい版の loop が置いた ref を見た古い版の loop は、worker を起動せずに `要対処` に出す
     - 試運転 (`loop --dry-run`) は調停 remote を読まないので、他のマシンが作業している作業対象も示しうる。`doctor` は調停 remote を読めるかを確かめる (push は撃たない)
3. **試運転**: `claude-dispatcher loop --dry-run` が workflow 定義を検査し、起動するはずの作業対象と trigger を 1 件 1 行で示す。何も起動せず、何も書かない
   - action まで確かめるなら、`claude-dispatcher run <trigger> <作業対象>` (作業対象は `issue#<番号>` か `cl#<番号>` と書く) で、その作業対象に worker を 1 回だけ起動する (単発起動)。作業対象が trigger に当たっているときだけ起動し、終わった後に trigger から外れていれば exit 0、起動しなかったら exit 5、起動したが当たったままなら exit 6 で終わる。retry はしない。同じ issue 置き場の loop が走っている間は撃てない (exit 3)。workspace・hooks・時間の上限・Ctrl+C の扱い (1 回目は待つ、2 回目は止める) は loop と同じ。workspace は作業対象が閉じていなければ残り、後の loop がそのまま使う。`coordination.remote` (上の 2) を書いた project では、走っている間は loop と同じく着手宣言と生存報告を置き、他のマシンの loop が作業している作業対象では起動しない (exit 5)
4. **導入を確かめる**: `claude-dispatcher doctor` が workflow 定義・issue 置き場・依存 CLI・事前検査を確かめ、利用者の約束に頼る宣言を警告する。Claude Code の settings (`permissions.allow`) に要りそうな entry も示すので、自分で足す (CLI は settings を書かない)
   - 依存 CLI は、gh・glab・acli のどれか・claude・git (git は CL 側の trigger があるときと、`coordination.remote` を書いたとき)
   - `coordination.remote` を書いたなら、その remote を解いて読めることも確かめる
   - 警告する宣言は、承認済みの CL への action と、絞り込みの無い CL 側の trigger
5. **回す**: `claude-dispatcher loop` を撃つ (下の「回す・見る・止める」)

workflow 定義に誤りがあれば、試運転・`loop` の起動・`doctor` が項目の位置を名指しして失敗する。直して 3 へ戻る。

## 回す・見る・止める

### 回す

- `claude-dispatcher loop` は前面で回り続け、止めるまで戻らない。起動直後に 1 回、以後は tick の終わりから `polling.interval` だけ待って次の tick を回す
  - 戻るのを待つ撃ち方 (tool の呼び出しの中で終わりを待つなど) はしない。端末で撃つか background の process として撃ち、様子は下の `status` で見る
- 起動時に workflow 定義の検査と事前検査を通す。落ちれば誤りを名指しして起動しない
- tick ごとに workflow 定義を読み直す。読み直した定義が検査に落ちた tick と、事前検査に落ちた trigger は、何も起動しない
- `claude-dispatcher loop --max-concurrent <N>` で、その loop の並列上限 (同時に走らせる worker の上限) を決める (1 以上の整数。省けば 1)。並列上限は loop を回すマシンで決まる値なので、`WORKFLOW.md` には書かない (書くと未知の key として検査に落ちる)
  - loop を回したまま変えるなら、別の端末で `claude-dispatcher max-concurrent <N>` を撃つ。次の tick から効き、loop を止めるまで続く。走っている worker の数より下げても、走っている worker は止めず、空きが出るまで新しい起動を待つ。loop が立っていなければ何も書かずに exit 1 で終わる
  - loop を起動し直すと、`max-concurrent` で変えた値は消え、`--max-concurrent` の値 (省けば 1) から始まる
- 同じ issue 置き場の loop は、同じマシンで 1 本しか起動できない (2 本目と、`run` が走っている間の loop は exit 3 で止まる)。マシンを跨いで分け合うなら `coordination.remote` を書く (上の「導入の流れ」の 2)。書かなければマシンを跨いだ排他は持たないので、1 つの issue 置き場は 1 台から回す
- `coordination.remote` を書いた project では、loop の起動時に調停 remote を解いて読めて、生存報告を置けることを確かめる。落ちれば exit 1 で起動しない
- loop は自分では起き直さない。端末を閉じた・マシンを再起動した・binary を入れ替えた (走っている loop は起動した時点の binary で回り続ける) ときは、同じコマンドを撃ち直す

### 見る

- loop を撃った端末には、`status` と同じ内容と直近のログを描き直し続ける。stdout が端末でなければ、ログの行と worker の活動を 1 行ずつ追記する
  - 直近のログは、tick・起動・終了・再起動・停止・error の行の直近 10 行。worker の最新の活動は `workers` の表の列で見る
  - 置き場の一覧の読み出しが 15 秒を超えると、読み終わる前に `CL 置き場 acme/widgets の一覧の読み出しが 15 秒を超えた` のような行が出る。止まったのではなく、まだ読んでいる。試運転 (`loop --dry-run`) と `doctor` では同じ行が stderr に出る
  - 色は stdout が端末のときだけ付く。環境変数 `NO_COLOR` を設定すれば付かない (`status` も同じ)
- 別の端末からは `claude-dispatcher status` で見る。loop が書き出す状態 file を描くだけで、何も書かず、tracker の CLI も撃たない
  - 見出しの 1 行目は loop が生きているか (`loop 稼働中` / `loop 停止待ち` / `loop なし`)、2 行目は次の tick・今の並列上限・直近の tick の結果
  - loop が止まった後 (`loop なし`) は見出しだけを描き、止められない worker も描かない。loop が止まった後に残った claude の pid と workspace は、log.jsonl の `error` の行 `claude を止められないまま止まる (pid <pid>, workspace <path>)` で読む
  - `workers` に走っている worker と再起動待ちを (段階・経過・最新の活動を含む)、`要対処` に打ち切り・曖昧な CL・事前検査に落ちた trigger・止められない worker (SIGKILL を送れず claude が終わっていない。段階は `unstopped` で、pid と workspace の path を出す)・orphan (前の loop が止まった後も動いている claude。終わるまでその作業対象では起動しない) を解き方とともに並べる。`coordination.remote` を書いた project では、乗っ取られた作業対象 (worker を止めたので、止まる前に push された成果を確かめる)・続けて落ちている生存報告・新しい版の ref (dispatcher を上げる) も並べる
- 置き場は `claude-dispatcher paths --json` で引く。何も書かず、tracker の CLI も撃たない

| key | 中身 |
|---|---|
| `scope_key` | issue 置き場から決まる loop の識別子 |
| `state_dir` | loop の状態を置く dir |
| `log` | 過去の tick と worker の起動・終わり方を 1 行ずつ残す log (JSON Lines) |
| `status_file` | `status` が描く状態 file (JSON) |
| `workspace_root` | workspace を置く dir |

- worker の stream は `<state_dir>/workers/<作業対象>.log` (stderr は `.stderr.log`) に残る。worker が何をして終わったかはここで読む
- 打ち切りは loop の memory にだけ持つので、loop を撃ち直すと解ける。loop を止めずに解くなら、作業対象を打ち切ったときの trigger から外し (label で当てる trigger なら label を外す)、1 周期待ってから戻す

### 止める

1. loop に停止要求を送る (端末なら Ctrl+C。SIGINT・SIGTERM・SIGHUP のどれでもよい)。新しい起動と再起動をやめ、走っている worker が終わるのを待って止まる。worker が走っていなければすぐ止まる
2. 待てないときは、もう一度停止要求を送る。走っている worker を止めて止まる
   - claude に SIGKILL を送れず止められなかった worker は、claude が終わるまでは終わりを待たない。log.jsonl に `claude を止められない` の `error` の行が出て、claude の pid と workspace の path が載る。loop が止まるまでに claude が終われば、止めた worker と同じく後始末をして止まる。終わらなければ `claude を止められないまま止まる` の `error` の行も出る (止まる間際に claude が終わったときは、pid の無い `after_run を待たずに止まる` の行が出る。その workspace は片付ける)。`止められないまま止まる` の行が出ても、loop は起動し直してよい。残った claude (orphan) が終わるまで、次の loop はその workspace を消さず、その作業対象で worker を起動しない。急ぐならその pid の claude を止める。loop が落ちて claude が残ったときも同じ
   - worker が途中まで書いた成果は workspace と remote branch に残る
   - 作業対象が trigger に当たったままなら、次に起動した loop が同じ workspace で worker を起動し直す。session は新しく、attempt も 1 から数え直す。前の成果を拾わせるなら、共通 prompt にそう書く
3. 使うのをやめるなら、`paths --json` が返す `state_dir` と `workspace_root` を消す (workspace が worktree なら、消した後に clone で `git worktree prune` を撃つ)。tracker の label は残る

## action が守る約束

worker は action (と本文) の文面どおりに作業する。dispatcher が worker の終わりを判断する規則は次のとおりで、action はこれに合わせて書く。

- **完了は、worker が終わった後に、作業対象が起動した trigger から外れていること**。worker が label を外す・CL を ready にする・Jira の status を移す、などで外す
  - 例: `labels.all: [ready-for-agent]` に当てる trigger なら、CL を開いた後に `ready-for-agent` を外すよう action に書く
  - 見送る・人へ返すときも外す (例: `ready-for-agent` を外して `ready-for-human` を付ける)
- **外さないまま終わった worker は、正常に終わっていても失敗として数える**
  - 失敗すると attempt (同じ作業対象への何回目の起動か。1 から数える) を 1 つ進め、backoff (`min(10s × 2^(attempt−1), limits.max_retry_backoff)`) だけ待って、同じ trigger で worker を起動し直す
  - 起動し直した worker は、前の attempt の session を続ける (`claude --resume`)。workspace も同じ
  - 再起動の前に作業対象を読み直し、終端になっているか trigger から外れていれば、起動し直さない
- **attempt が `limits.max_attempts` に達して失敗したら打ち切る**。打ち切った作業対象は、どの trigger に当たっても起動しない
  - 打ち切りは loop の memory にだけ持ち、tracker には書かない。loop の画面と `claude-dispatcher status` に解き方とともに出る
- **次のときも失敗として数える**
  - worker の stream に progress (worker が進んでいる兆候の行) が `limits.stall_timeout` のあいだ書かれない (stall)
  - worker 1 回分が `limits.run_timeout` を超える
  - action か本文の描画、`hooks.after_create`・`hooks.before_run` が失敗する
- **作業対象が終端になったら、走っている worker を止めて workspace を消す**。trigger から外れただけでは、走っている worker は止めない
  - 止めるときに SIGKILL を送れず claude が終わらなければ、claude が終わるまで worker は終わっていないものとして扱う。`hooks.after_run` を撃たず、workspace を消さず、終わりを確かめず、起動し直さない。claude が後で終われば、止めた worker と同じく後始末へ進む (上の「止める」)
- **loop が止まった後も claude が動いていれば (orphan)、起動し直した loop はその claude が終わるまで、その workspace を消さず、その作業対象で worker を起動しない**。その分の `hooks.after_run` は撃たない。claude が終われば、普通の掃除と起動に戻る
- 1 つの作業対象で同時に走る worker は 1 本。残りの trigger は、その worker が終わった後の tick で評価し直す
- worker は repo を編集できるので、`WORKFLOW.md` を書き換えて、自分が次に起動される trigger を変えうる。dispatcher はこれを防がない

worker に渡るもの:

- **user prompt**: 当たった trigger の action を描画したもの。先頭に `/skill-name` を書けば、Claude Code が起動の前に skill を展開する。途中に書いた `/skill-name` は展開されず、呼ばれるかどうかは model 次第
- **共通 prompt**: workflow 定義の本文を描画したもの。`--append-system-prompt-file` で、Claude Code の既定の system prompt の後ろに足す。worker の作業規約 (レビューの通し方・人への返し方・残りを issue にするときの label など) はここに書く。dispatcher 自身は作業規約を持たない
- 起動のコマンドは `<claude.command> <claude.args…> -p --output-format stream-json --verbose <session の flag> --append-system-prompt-file <描画した本文の path> -- <描画した action>`。cwd は作業対象の workspace

## workflow 定義の書き方

実装 repo の中に置く。tick ごとに読み直すので、直せば loop を起動し直さずに効く。ただし issue 置き場 (`tracker.kind`・`tracker.host`・`tracker.repo`) か CL 置き場 (`cl`) を変えたら、loop を起動し直すまで、どの tick も何も起動しない。

file は YAML の front matter と本文からなる。

- 1 行目が `---` の行で、次の `---` の行までが front matter
- その後ろが本文。本文は共通 prompt。空でもよい
- front matter の YAML のコメント (`#`) は worker に渡らない。本文に書いたものは、どの worker にも渡る

```markdown
---
tracker:
  kind: github               # 必須。github・gitlab・jira のどれか
  repo: acme/widgets         # 必須。issue 置き場 (github は owner/name、gitlab は group/…/name、jira は project key)。$VAR で書ける
  token: $WIDGETS_GH_TOKEN   # 任意。gh・worker・hooks に GH_TOKEN として渡す。$VAR でだけ書ける
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
  max_attempts: 3            # 任意。作業対象 1 件の attempt の上限。既定 3
  max_retry_backoff: 5m      # 任意。backoff の上限。既定 5m
  stall_timeout: 15m         # 任意。worker の progress が途絶えてから止めるまで。既定 15m。0 で無効
  run_timeout: 1h            # 任意。worker 1 回分の上限時間。既定 1h。0 で無効
claude:
  command: claude            # 任意。既定 claude
  # 任意。既定は空。再入の仕組みを塞ぐ (理由は項目の表の下の注記「再入の仕組みを塞ぐ」)。
  # --disallowedTools は値を複数取るが、dispatcher が後ろに渡す -p で止まる
  args: [--permission-mode, auto, --disallowedTools, "ScheduleWakeup,CronCreate,CronList,CronDelete,Monitor"]
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
      /implement issue #{{ .issue.number }} ({{ .issue.url }})。CL を開いたら ready-for-agent を外す
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

### 項目

| key | 型 | 中身 |
|---|---|---|
| `tracker.kind` | 文字列 | tracker の種類。`github` (gh で読む)・`gitlab` (glab で読む)・`jira` (acli で読む。Jira Cloud) のどれか |
| `tracker.host` | 文字列 | `gitlab` と `jira` でだけ書ける。`$VAR` で書ける。`github` で書くと未知の key<br>- `gitlab`: GitLab の host 名 (`gitlab.example.com`)。既定 `gitlab.com`<br>- `jira`: Jira Cloud の site (`acme.atlassian.net`)。既定を持たず必須。綴りは `[A-Za-z0-9.-]+` |
| `tracker.repo` | 文字列 | issue 置き場。`github` は `<owner>/<name>`、`gitlab` は `<group>/<name>` の段数を問わない path (`acme/sub/widgets`。どの段も `[A-Za-z0-9._-]+`)、`jira` は Jira の project key (`WIDGETS`。`[A-Za-z][A-Za-z0-9_]*`。大文字に揃えて読む)。`$VAR` で書ける |
| `tracker.token` | 文字列 | `github` と `gitlab` でだけ書ける。tracker の CLI が読む環境変数 (`github` は `GH_TOKEN`、`gitlab` は `GITLAB_TOKEN`) として渡す token。loop が撃つ tracker の CLI・worker・hooks のすべてに渡し、loop を起動した環境に同じ変数があっても上書きする。`$VAR` でだけ書ける (値そのものを書かない)。省くと、loop を起動した環境の認証を CLI がそのまま使う。ssh 越しの shell などで CLI の認証を取れないときに使う。`gitlab` では、glab が `GITLAB_TOKEN` を host を問わず使うので、worker や hooks が `tracker.host` と別の GitLab の host へ glab を撃つと、token がその host にも送られる。その構成では、`tracker.token` を書かず、loop を起動した環境の `GITLAB_TOKEN` も外して、host ごとに `glab auth login --hostname <host>` で認証するか、別の host を撃つ呼び出しだけ `GITLAB_TOKEN` をその host の token に差し替える |
| `cl` | 対応表 | issue 置き場と別の CL 置き場 (worker が CL を出す repo)。`tracker.kind: jira` でだけ書け、そのときは必須 (`github`・`gitlab` は issue 置き場と同じ場所が CL 置き場)。`on: cl` の trigger が無くても、`loop` の起動時・試運転・`doctor` で CL 置き場の open な一覧を 1 回読んで確かめる。token は持たず、gh / glab 自身の認証 (`gh auth login` / `glab auth login`) を使う |
| `cl.kind` | 文字列 | 必須。CL 置き場の種類。`github` (gh で読む) か `gitlab` (glab で読む) |
| `cl.host` | 文字列 | `cl.kind: gitlab` でだけ書ける。GitLab の host 名。既定 `gitlab.com`。`$VAR` で書ける。`github` で書くと未知の key |
| `cl.repo` | 文字列 | 必須。CL 置き場。綴りは `tracker.repo` の `github` (`<owner>/<name>`)・`gitlab` (`<group>/…/<name>`) と同じ。`$VAR` で書ける |
| `polling.interval` | 文字列 | 周期。Go の duration の綴り (`90s` / `5m` / `1h30m`) で、1s 以上 24h 以下。短い周期は tracker の rate limit を食う |
| `workspace.root` | 文字列 | workspace (作業対象ごとに worker が作業する dir) を置く dir。相対 path は workflow 定義の dir から、`~/` は HOME から (HOME が絶対 path でなければ失敗)。`$VAR` で書ける。workspace は `<workspace.root>/<作業対象>` (`issue-<番号>` か `cl-<番号>`) に作り、作業対象が終端になるまで残す |
| `hooks.after_create` | 文字列 | workspace を作った直後に撃つ shell script。失敗したら workspace を消し、その attempt は失敗。`before_remove` と合わせて、作業対象を跨いで直列に撃つ (同時に 1 本まで)。遅い `after_create` は他の workspace の用意と削除を待たせ、削除を待つ間は loop も停止要求・tick に応えない |
| `hooks.before_run` | 文字列 | worker を起動する前に毎回撃つ。失敗したら、その attempt は失敗。作業対象を跨いで並列に走りうる (clone を触るなら、hook の中で排他する) |
| `hooks.after_run` | 文字列 | worker が終わった後 (止めたときも) に毎回撃つ。失敗は log に残して続ける。作業対象を跨いで並列に走りうる (clone を触るなら、hook の中で排他する) |
| `hooks.before_remove` | 文字列 | workspace を消す前に撃つ。失敗は log に残して続ける。`after_create` と合わせて、作業対象を跨いで直列に撃つ |
| `hooks.timeout` | 文字列 | hook 1 回の上限時間。既定 60s。超えたら process group ごと止め、失敗として扱う |
| `limits.max_attempts` | 整数 | 作業対象 1 件の attempt の上限。1 以上。既定 3。上限の attempt が失敗したら打ち切る |
| `limits.max_retry_backoff` | 文字列 | backoff の上限。Go の duration の綴りで、0 より長い。既定 5m |
| `limits.stall_timeout` | 文字列 | worker の stream (stdout の stream-json) に progress (heartbeat (`tool_progress`)・rate limit の通知・読めない行を除いた行) が書かれなくなってから、止めて失敗とするまでの時間。既定 15m。`0s` で無効。claude が行を書かない区間 (長い build や test などの tool の実行・API の retry の backoff・compaction) より長くする |
| `limits.run_timeout` | 文字列 | worker 1 回分の上限時間。起動からの経過が超えたら止めて失敗とする。既定 1h。`0s` で無効 |
| `coordination.remote` | 文字列 | 同じ issue 置き場を複数のマシンの loop で分け合うときの調停 remote。workflow 定義のある clone の git remote の名前 (`origin` など) で書く。綴りは `[A-Za-z0-9._/-]+` で、`-` で始めない。その remote の `refs/dispatcher/` の下に着手宣言と生存報告を置き合う (上の「導入の流れ」の 2)。URL と認証は clone の git の設定を使う。省くと調停 remote を読みも書きもせず、1 台で回す |
| `claude.command` | 文字列 | worker として起動する command。PATH から探す。既定 `claude` |
| `claude.args` | 文字列の列 | command に、dispatcher が渡す引数 (上の「action が守る約束」の起動のコマンド) より前に渡す引数。既定は空。permission の扱い (`--permission-mode` など) はここで決める。再入の仕組み (`ScheduleWakeup`・`CronCreate`・`CronList`・`CronDelete`・`Monitor`) を `--disallowedTools` で塞ぐことを勧める (理由と見分け方は表の下の「再入の仕組みを塞ぐ」) |
| `triggers` | 列 | trigger の宣言。1 つ以上。宣言順が起動の優先順 (先頭の trigger ほど先に起動する) |
| `triggers[].name` | 文字列 | trigger の名前。`[A-Za-z0-9._-]+`。trigger の間で重複しない |
| `triggers[].on` | 文字列 | 作業対象の種類。`issue` か `cl` (`gitlab` では merge request)。`cl` は CL 置き場の CL に当たる (`jira` では `cl:` の CL 置き場) |
| `triggers[].when` | 対応表 | 述語。書いた条件はすべて AND で評価する。書ける key は `on` で変わる (下の「issue 側の述語」「CL 側の述語」)。省くと、その種類の open な作業対象のすべてに当たる |
| `triggers[].action` | 文字列 | worker に渡す prompt の template (下の「template」)。空にできない |

**再入の仕組みを塞ぐ**: worker が再入の仕組み (`ScheduleWakeup`・`CronCreate`・`CronList`・`CronDelete`・`Monitor`) を使うと、`-p` は仕事を終えた後も pending な wakeup の発火や `Monitor` の見張りの終わりを待って終わらず、並列の枠を塞ぐ (終わらない command を見張る `Monitor` では、`-p` は止めるまで終わらなかった)。`claude.args` の `--disallowedTools` で塞ぐことを勧める (background の subagent の完了は、`-p` が自分で新しい turn を始めて拾うので、待つための wakeup は要らない)。background の Bash・subagent・`Workflow` も、終わるまで `-p` を終わらせない。ただし `--disallowedTools` は tool の名前で塞ぐので、`Bash` や subagent の tool を塞ぐと前景の呼び出しまで塞がり、worker が仕事をできなくなる。これらは列に足さず、終わらない command (dev server や watch など) を background に残したまま終えないよう、action で worker に求める。塞ぐのは名前の固定の列なので、Claude Code が再入の tool を足す・改名すると、黙って素通りする (`--disallowedTools` は知らない名前を検査しない)。そのときは、仕事を終えたはずの worker が `status` に走ったまま残り、`limits.stall_timeout` か `limits.run_timeout` で止められるまで枠を塞いで、失敗として数えられる。長い build や test で止められたのと見分けるには、worker の stream (`<state_dir>/workers/<作業対象>.log`) の最後の tool の呼び出しを見る。再入の tool なら、その名前を列に足す。最後の tool がそれ以外でも、stream に完了の通知が来ていない background の Bash か subagent があれば、それが待たせている。列には足さず、action でそれを止めてから終えるよう求める。

**YAML の読み方**: 値を書いていない key (`when:` だけの行) は、空の対応表として読む。anchor と alias は辿る。merge key (`<<: *base`) は持たない (`<<` は未知の key として失敗する)。

**置き場のサービスごとの違い**: 宣言を書けるかは、その宣言が属する置き場のサービスで決まる。issue 側の述語・`tracker.*`・`cl:` は issue 置き場 (`tracker.kind`) の、`on: cl` と CL 側の述語は CL 置き場 (`github`・`gitlab` では issue 置き場と同じ場所、`jira` では `cl.kind`) のサービスで決まる。

| 宣言 | 属する置き場 | GitHub | GitLab | Jira |
|---|---|---|---|---|
| 読む CLI と認証 | - | gh (`gh auth login`。issue 置き場なら `tracker.token` も) | glab (`glab auth login`。issue 置き場なら `tracker.token` も) | acli (`acli jira auth login`) |
| `tracker.host` | issue | 書けない | 任意 (既定 `gitlab.com`) | 必須 (Jira Cloud の site) |
| `tracker.repo` | issue | `<owner>/<name>` | `<group>/…/<name>` | project key |
| `tracker.token` | issue | 書ける (`GH_TOKEN`) | 書ける (`GITLAB_TOKEN`) | 書けない |
| `cl:` | issue | 書けない | 書けない | 必須 |
| issue 側の `author` | issue | 書ける | 書ける | 書けない |
| issue 側の `milestone` | issue | 書ける | 書ける | 書けない |
| issue 側の `blocked` | issue | 書ける | 書けない | 書ける |
| issue 側の `status`・`type` | issue | 書けない | 書けない | 書ける |
| `.issue.key` (template) | issue | 使えない | 使えない | 使える |
| `on: cl` の trigger | CL | 書ける (pull request) | 書ける (merge request) | CL 置き場にならない |
| CL 側の `author` | CL | 書ける | 書ける | CL 置き場にならない |

書けない宣言は、項目を名指しして失敗する。

- `github`・`gitlab` の `cl:`: issue 置き場と同じ場所が CL 置き場なので、別の CL 置き場を宣言しない
- `gitlab` の `blocked`: GitLab の issue の依存は有償の tier の機能で、CE の API は依存を返さない。読むと常に 0 件になり、`blocked: false` が黙って全件に当たるため
- Jira を CL 置き場にすること: Jira に CL は無い。`tracker.kind: jira` では、`cl:` で GitHub か GitLab の CL 置き場をかならず宣言する
- `jira` の `author`: worker を誰の書き込みで起動してよいかの線を、Jira の権限 (repo の push 権限と別の線) に引けないため
- `jira` の `milestone`: sprint は無いことがあり、fixVersions は acli の一覧で読めないため

### issue 側の述語

`on: issue` の `when` に書く。

| key | 型 | 当たる issue |
|---|---|---|
| `labels.all` | 文字列の列 | 列の label をすべて持つ |
| `labels.any` | 文字列の列 | 列の label のどれかを持つ。空の列は書けない |
| `labels.none` | 文字列の列 | 列の label をどれも持たない |
| `assignee` | 文字列 | その login (GitLab は username、Jira は accountId) の人が assignee に居る |
| `unassigned` | 真偽 | `true` なら assignee が居ない、`false` なら 1 人以上居る |
| `author` | 文字列 | `collaborator` なら作者が collaborator、`non_collaborator` ならそれ以外。collaborator の線は issue 置き場のサービスごとに下の表 |
| `milestone` | 文字列 | その題名の milestone に入っている |
| `blocked` | 真偽 | `true` なら未解決 (open) の依存先 (blocked by) が 1 つ以上ある、`false` なら無い |
| `status.any` / `status.none` | 文字列の列 | `jira` でだけ書ける。status 名が列のどれかである / どれでもない。`any` の空の列は書けない |
| `type.any` / `type.none` | 文字列の列 | `jira` でだけ書ける。issue type 名 (subtask・epic を含む) が列のどれかである / どれでもない。`any` の空の列は書けない |

| 置き場のサービス | collaborator と数える作者 |
|---|---|
| GitHub | repo の owner・organization の member・collaborator (GitHub の `authorAssociation` が `OWNER` / `MEMBER` / `COLLABORATOR`) |
| GitLab | project での access level (group から継承したものを含む) が Developer (30) 以上の member。worker は push 権限を持つので、誰の書き込みで起動してよいかの線を push できる人に引く |
| Jira | `author` は書けない |

- `assignee` と `unassigned` は一緒に書けない
- label と login の綴りは、tracker によらず大文字と小文字を区別せずに比べる (GitLab の label は大文字と小文字を区別するが、dispatcher は揃えて比べる。誤って当たるのは、大文字と小文字だけが違う label を 2 つ持つ project に限られる)
- `gitlab` の作業対象は、種類が issue のもの (`issue_type` が `issue`) だけ。同じ一覧に出る task・incident・test case は読まない
- `gitlab` の `author` は、作者の access level を project の member の API で読む。この API は認証が要るので、glab が未認証 (`glab auth login` も `tracker.token` も無い) なら public な project でも認証の失敗になる

`tracker.kind` が `jira` のときの述語の意味:

| key | `jira` での意味 |
|---|---|
| `labels` | Jira の label。他の kind と同じく大文字と小文字を区別せずに比べる |
| `assignee` | 担当者の accountId (Jira Cloud の user は accountId でしか一意に識別できない) |
| `unassigned` | 担当者が居ない / 居る |
| `blocked` | link type `Blocks` の inward ("is blocked by") の link のうち、相手の status の区分 (`statusCategory.key`) が `done` でないものが 1 つ以上あれば `true` |
| `status` | status 名 (`fields.status.name`)。大文字と小文字を区別せずに比べる |
| `type` | issue type 名 (`fields.issuetype.name`)。大文字と小文字を区別せずに比べる |

- `jira` の作業対象は、project の全種類の issue (subtask・epic を含む)。絞るなら `type` を書く
- `jira` の終端は、status の区分 (`status.statusCategory.key`) が `done` であること。Wontfix のような done 区分の status も終端
- `jira` の作業対象の番号は、issue の key (`WIDGETS-123`) の数字の部分 (`123`)。workspace の名前・hooks の `CLAUDE_DISPATCHER_NUMBER` は番号で書く。key は template の `.issue.key` で使える
- `status` と `type` の名前が実在するかは、`loop` の起動時・試運転・`doctor` で確かめる。tick の中では確かめない
  - status 名は `project = "<project key>" AND status = "<名前>"` の検索の件数を撃ち、JQL の失敗で見分ける。acli は project の status の一覧を返さないので、site の単位の検査にとどまる。他の project にだけある status 名は検査を通る
  - issue type 名は project の issue type (`acli jira project view --key <project key> --json` の `issueTypes`) と比べる
- `blocked` を書いた trigger があると、tick ごとに open な一覧に加えて、依存を持つ open な issue を検索し (`issueLinkType = "is blocked by"`)、当たった issue を 1 件ずつ読み直して依存先の status の区分を見る
- 1 つの Jira project は 1 本の loop と 1 つの実装 repo で回す (同じ project の 2 本目の loop は起動時に止まる)。project key を改名したら、workflow 定義を書き換えて loop を起動し直す
- acli は呼び出しごとに site を選べず、active な account の site を読む。`loop` の起動時・試運転・`doctor` で、acli の認証の site が `tracker.host` と違えば止まる (`acli jira auth switch --site <site>` で切り替える)
- `jira` の issue と `cl:` の CL を、dispatcher は結び付けない。CL を merge しても、closing reference は置き場を跨げないので Jira の issue は終端にならない。status を動かすのは人か worker (action に書く)

### CL 側の述語

`on: cl` の `when` に書く。CL の状態は真偽の key で書く。`true` なら その状態の CL に、`false` なら その状態でない CL に当たる。

| key | 当たる CL (`true` のとき) |
|---|---|
| `conflict` | merge conflict を持つ。GitHub が conflict を計算し終えていない CL (`mergeable` が `UNKNOWN`) は、`true` にも `false` にも当たらない (次の tick で見直す) |
| `review_unresolved` | collaborator が書いた未解決の review thread が 1 本以上ある。thread の書き手は、thread の最初の comment の作者。public repo では誰でも review comment を残せるので、collaborator 以外の thread は数えない |
| `ci_failed` | head commit の checks の集計が失敗 (`FAILURE` か `ERROR`) している。checks が無いか、走っている途中なら失敗ではない |
| `approved` | review で承認済み (`reviewDecision` が `APPROVED`)。当てると merge を worker に任せうるので、当てるかは project が決める |

絞り込み:

| key | 型 | 当たる CL |
|---|---|---|
| `labels.all` / `labels.any` / `labels.none` | 文字列の列 | issue 側と同じ |
| `head` | 文字列 | head branch の名前が pattern に当たり、かつ head が同じ repo の branch である。pattern は Go の `path.Match` の綴り (`*` は `/` 以外の文字の列、`?` は `/` 以外の 1 文字、`[...]` は文字の組) |
| `same_repo` | 真偽 | `true` なら head が同じ repo の branch (fork でない)、`false` なら fork の branch |
| `author` | 文字列 | issue 側と同じ。collaborator の線は CL 置き場のサービスで決まる (`jira` では `cl.kind` のサービス) |
| `draft` | 真偽 | `true` なら draft、`false` なら draft でない |

- `head` を書いたら、fork の CL には当たらない。fork からは誰でも同じ綴りの head branch を作れるため。`head` と `same_repo: false` は一緒に書けない
- head branch の名前は、大文字と小文字を区別して比べる (git と同じ)
- worker は利用者本人の認証で CL を開くので、`author` では人の CL と worker の CL を区別できない。区別したいなら `head` の pattern か label で絞る
- `head`・`labels.all`・`labels.any`・`same_repo: true` のどれでも絞っていない CL 側の trigger は、人の CL や fork の CL に worker を送りうる (`doctor` が警告する)

上の表は `github` の field で書いた。`gitlab` では、merge request の field (GraphQL の `MergeRequest`) に次のように写す。

| key | GitLab の field |
|---|---|
| `conflict` | `mergeStatusEnum` が `CANNOT_BE_MERGED` なら conflict を持ち、`CAN_BE_MERGED` なら持たない。`UNCHECKED` / `CHECKING` / `CANNOT_BE_MERGED_RECHECK` なら、計算し終えていないので `true` にも `false` にも当たらない (次の tick で見直す)。それ以外の値は読めない失敗にする。draft の merge request でも同じに決める |
| `review_unresolved` | 解決できる discussion (`resolvable`) で、解決されていない (`resolved` でない) もののうち、最初の note の作者が collaborator のものが 1 本以上ある。最初の note が system note の discussion は数えない。最初の note の作者が collaborator かは、その note の `maxAccessLevelOfAuthor` (project での access level) で決める。access level の名前を読めなければ読めない失敗にする |
| `ci_failed` | head の pipeline (`headPipeline`) の `status` が `FAILED`。`CANCELED` は失敗に数えない (人が意図して止めた pipeline に worker を送らない)。pipeline が無ければ失敗ではない |
| `approved` | `approved`。CE では 1 人以上の承認、有償の tier では承認ルールの充足で、どちらも host の判断に従う |
| `draft` | `draft` |
| `same_repo` | `sourceProjectId` と `targetProjectId` が同じ。source の fork が消えて `sourceProjectId` が null なら `false` |
| `head` | `sourceBranch` |
| `author` | issue 側と同じ線 (access level が Developer 以上) |

- `gitlab` の merge request は、open な一覧を GraphQL の問い合わせ (10 本ごとに 1 往復) で、承認・pipeline・discussion とまとめて読む。CL 側の trigger を置くと、毎 tick、次の往復を撃つ
  - 一覧の page の数だけ GraphQL を 1 往復。discussion が 100 本を超えた merge request は、その残りを 100 本ごとに 1 往復で読み足す
  - merge request の作者の access level を、その観測でまだ読んでいない作者について、100 人ごとに 1 往復で読む (REST の project の member の API)
  - open な一覧に conflict を計算し終えていない merge request があれば、100 本ごとに 1 往復で、GitLab に計算し直させる (REST の merge request の一覧の `with_merge_status_recheck`)。GraphQL の読み出しは計算し直しを起こさないので、これが無いと計算し終えないまま残る。結果は次の tick で読む。起動の直前の読み直しでは求めない (次の tick の一覧で求める)。計算し直しの求めが失敗しても、観測は失敗にせず、落ちたことを log.jsonl の `error` の行に残し、その merge request は conflict を決められないまま次の tick で求め直す。GitLab が計算し直すのは、glab の user が project で Developer 以上の access level を持つときだけ
- `gitlab` の merge request の終端は `state` が `merged` か `closed`。`locked` (merge の処理中) は終端にしない (merge に失敗すると `opened` に戻る)

### 評価の規則

- 作業対象ごとに trigger を宣言順に評価し、最初に当たった 1 つだけを採る。issue には `on: issue` の trigger だけを、CL には `on: cl` の trigger だけを当てる
- 候補は、trigger の宣言順を先に、同じ trigger の中では作業対象の作成日時の古い順に並べる。作成日時が同じなら番号の小さい順。`jira` では、作成日時の代わりに番号の小さい順 (acli の一覧は作成日時を返さない)
- 空いている並列の枠 (並列上限 (`max-concurrent` で変えたならその値、変えていなければ `loop --max-concurrent` の値。省けば 1) から、走っている worker と再起動待ちの数を引いた分) だけ、候補を先頭から起動する。起動の直前に作業対象を読み直し、終端になっているか trigger から外れていれば起動しない
- **曖昧な CL** (同じ repo の同じ head branch から、open な CL が 2 本以上ある) には、CL 側の trigger を当てない。fork の head branch は、fork ごとに別の branch として数える (fork は GitHub では head の repo の名前、GitLab では `source_project_id` で見分ける)。head の repo が消えた fork の CL (GitHub の `headRepository` が null、GitLab の `source_project_id` が null) は、どの fork の branch か分からないので、曖昧さを数えるときに数えない
- loop は、worker を割り当てている作業対象 (worker が走っている・止めている・終わりを確かめ待ち・再起動待ちのどれか) の workspace で checkout されている branch を head に持つ、同じ repo の CL にも CL 側の trigger を当てない。issue の worker が CL を開いた後も作業を続けている間に、同じ branch へ CL の worker を重ねないため。試運転はこの除外を掛けない
- open な一覧は、trigger に現れる種類のものだけを読む (`on: cl` の trigger が 1 つも無ければ CL の一覧を読まず、`on: issue` の trigger が無ければ issue の一覧を読まない)

### `$VAR` による間接参照

- 「`$VAR` で書ける」とした項目は、値の全体を `$NAME` (`NAME` は `[A-Za-z_][A-Za-z0-9_]*`) にすると、loop を起動した環境の変数 `NAME` の値に置き換わる
- 変数が未設定か空なら、項目と変数を名指しして失敗する
- 値の一部だけを置き換える書き方 (`acme/$NAME`) は持たない。`$` を含む値は、そのままの綴りとして読む
- 秘密 (token) は値そのものを書かず、`$VAR` で渡す

### hooks

- `sh -c` で撃つ。cwd は作業対象の workspace
- 環境変数は loop の環境に、次のものを足す

| 変数 | 中身 |
|---|---|
| `CLAUDE_DISPATCHER_WORKSPACE` | workspace の絶対 path |
| `CLAUDE_DISPATCHER_CLONE` | workflow 定義の dir の絶対 path |
| `CLAUDE_DISPATCHER_KIND` | 作業対象の種類 (`issue` か `cl`) |
| `CLAUDE_DISPATCHER_NUMBER` | 作業対象の番号 |

- workspace の作り方は hooks に任せる。issue なら branch を切った worktree、CL なら head を detach で checkout した worktree、など。同じ branch は 2 つの worktree で checkout できないので、issue の workspace が checkout している branch の CL を CL の worker が扱うなら、detach で取り出して `git push origin HEAD:<branch>` で push する

### template

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
- `jira` の `.issue.url` は `https://<site>/browse/<key>`、`.issue.labels` は Jira の label の綴りのまま。`.issue.key` を他の kind で書くと、未知の変数として検査で落ちる
- 未知の変数 (`.issue.body` など、CL の worker の `.issue`) と未知の関数は、描画の失敗にする。描画に失敗した attempt は失敗
- 検査で、action と本文を見本の変数 (どれも空でない値) で描画してみる。action はその trigger の種類の見本で、本文はどの worker にも渡るので、trigger に現れる種類すべての見本で描画する。描画できなければ検査で落ちるので、作業対象を読んでから描画に失敗するのは、見本では通った分岐だけになる
- 例: `/implement issue #{{ .issue.number }} ({{ .issue.url }})`。`/implement` は repo の `.claude/commands/implement.md` に置いた command (下の「事前検査」)

### 検査

workflow 定義の次のものが誤りで、項目の位置 (`triggers[0].when.labels.all` の形) を名指しして失敗する。

- file が無い・読めない・front matter が無い・YAML として読めない (YAML の誤りの行番号も file の行番号で出す)
- 未知の key (綴りの誤りを黙って無視しない)
- 同じ key を 1 つの対応表に 2 回書いたこと
- 型の誤り・必須の項目の欠落・未知の値 (`tracker.kind`・`triggers[].on`・`author`)
- 空の文字列 (空の `assignee` や label を「条件なし」と取り違えないため)
- 上の表の各項目の制約 (`polling.interval` の範囲・trigger の名前の綴りと重複・`assignee` と `unassigned` の併記・空の `labels.any` / `status.any` / `type.any`・`head` の pattern の綴り・`head` と `same_repo: false` の併記・空の `action`)
- `$VAR` の未設定と、`tracker.token` に値そのものを書いたこと
- 置き場のサービスが支えない宣言 (上の「置き場のサービスごとの違い」)。key を書く順 (`triggers` を `tracker` や `cl` より前に書くなど) によらず名指しする
- `tracker.kind: jira` で `cl` を書かないこと・`cl.kind` に CL 置き場にならない種類 (`jira`) を書いたこと
- action と本文の template を描画できないこと (綴りの誤り・未知の変数・未知の関数)
- `jira` の次の誤りは、workflow 定義だけでは確かめられないので、`loop` の起動時・試運転・`doctor` で issue 置き場を確かめるときに名指しする。tick の中では確かめない
  - acli の認証の site が `tracker.host` と違う
  - `status` の status 名・`type` の issue type 名が実在しない

### 事前検査

trigger ごとに、action の先頭の skill か command が worker から呼べるかを確かめる。作業対象に依らずに確かめられるよう、描画の前の template を見る。

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
  - skill と plugin の dir を探す置き場 (`.claude/skills/`・plugin の `skills/`・`plugin.json` の `skills` が挙げる path) の直下は、dir (指す先が dir の symlink を含む) だけを skill か plugin の dir とみなす。直下の通常の file (`.DS_Store`・README 等) は飛ばす
  - repo の `.claude/skills/` と `~/.claude/skills/` の直下の dir は、`SKILL.md` を持てば skill の dir、`.claude-plugin/plugin.json` を持てば plugin の dir とする (両方を持つ dir は両方に数える)
- 次のものが誤りで、trigger を名指しして失敗する
  - 先頭の `/名前` がどの置き場にも見つからない (置き場を読めない・`--plugin-dir` が dir でなく file を指す、などの設定の誤りを含む)
  - action の先頭を template 変数で始める (前の空白を除いて `{{` で始まる)。先頭の `/名前` を確かめられないため
- 先頭が `/` でも template 変数でもない action は、確かめない
- Claude Code に組み込みの command (`/review` など) は上の置き場に無いので、見つからないと数える
- plugin が有効か (settings の `enabledPlugins`) は見ない
- 置き場は Claude Code の読み方より広めに取る (見つからないと取り違えて起動を止めない側に倒す)。広めに取った分は、worker の起動の後に claude が名前を解けずに失敗する

