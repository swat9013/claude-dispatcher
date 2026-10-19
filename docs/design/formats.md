# 外から観測できる形式

- 本 file が CLI の外から観測できる形式の正本。black-box テストはここを参照して書き、実装とテストが食い違ったら本 file に合わせる
- workflow 定義の書き方の正本は、binary に埋め込んだ案内 ([`internal/guide/guide.md`](../../internal/guide/guide.md)) に置く (§2、ADR 0012)。本 file は、その検査で CLI が何を出すかを持つ
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
  workers/<作業対象>.log              worker の stdout (stream-json)。attempt を跨いで追記する。claude の生死を見せる flock の対象 (§6 の「orphan」)
  workers/<作業対象>.stderr.log       worker の stderr。attempt を跨いで追記する
```

`<作業対象>` は、issue なら `issue-<番号>`、CL なら `cl-<番号>`。

workspace は state dir の外、workflow 定義の `workspace.root` の下に作業対象ごとに置く (§2.1)。

```
<workspace root>/<作業対象>/          作業対象の workspace。作業対象が終端になるまで残す
```

- **scope key**: issue 置き場の役割を担う provider が、読んだ置き場から決める、issue 置き場の識別子 (system.md §13)
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

- **書き方の正本は、binary に埋め込んだ案内** [`internal/guide/guide.md`](../../internal/guide/guide.md) (`claude-dispatcher guide` が出す。§9、ADR 0012)。file の形・項目・述語・評価の規則・`$VAR`・hooks・template・何が誤りになるか・事前検査がどこを探すかは案内が持つ
- 本節は、コード・テスト・ADR の `formats.md §2.x` の参照が辿れるよう §2.1〜§2.9 の見出しを残し、各節は案内の対応する節を指す
- §2.8・§2.9 に、CLI が何を出すか (誤りの行の書式・exit・いつ落ちるか) を残す。外から観測できる形式で、black-box テストの正本

### 2.1 項目

案内の「workflow 定義の書き方」(file の形と例) と「項目」(YAML の読み方・置き場のサービスごとの違いを含む)。

### 2.2 issue 側の述語 (`on: issue` の `when`)

案内の「issue 側の述語」。

### 2.3 CL 側の述語 (`on: cl` の `when`)

案内の「CL 側の述語」。

### 2.4 評価の規則

案内の「評価の規則」。

### 2.5 `$VAR` による間接参照

案内の「`$VAR` による間接参照」。

### 2.6 hooks

案内の「hooks」。

### 2.7 template

案内の「template」。

### 2.8 検査

何が誤りになるかは案内の「検査」。CLI の出力は次のとおり。

- 誤りは 1 件 1 行で、すべてを出す。行は `<workflow 定義の絶対 path>: ` で始まり、項目の誤りなら続けて項目の位置 (`triggers[0].when.labels.all` の形)・file の行番号 (` (<n> 行目)`。分からなければ付けない)・理由を出す
  - 項目の位置を持たない誤り (file を読めない・front matter が無い・YAML として読めない・本文の template を描画できない) は、path の後ろに理由だけを出す。YAML の誤りの行番号は file の行番号で出す
  - 誤りは file の中の順 (行番号の順) に並べる
- 次の出力では、誤りの行の後ろに 1 行 ``書き方は `claude-dispatcher guide` を参照`` を足す。workflow 定義の誤りは案内を読めば直せるため
  - `loop` の起動時と試運転 (§5): stderr
  - `doctor` (§7.4): stdout。`NG` の行の後ろに、頭に何も付けずに出す
- 次の出力では足さない
  - tick の中の読み直しの失敗 (tick の error。§6)
  - `status` と `paths` (§7.2・§7.3) の失敗
  - 事前検査 (§2.9) の失敗
  - `jira` の issue 置き場の確認と CL 置き場の確認 (§6) の失敗 (下)

| いつ | 落ちたら |
|---|---|
| `loop` の起動時 (§6) | 起動を失敗させる (exit 2) |
| 試運転 (§5) | stdout に何も出さずに失敗させる (exit 2) |
| tick の中 (§6) | その tick は何も起動しない |
| `doctor` (§7.4) | `NG` の行に出し、以降は確かめない (exit 1) |
| `status`・`paths` (§7.2・§7.3) | 失敗させる (exit 2) |

- `jira` の、acli の認証の site と status 名・issue type 名の実在は、workflow 定義だけでは確かめられないので、検査ではなく issue 置き場の確認 (§6) で名指しする

### 2.9 事前検査

探す置き場・名前の当て方・何が誤りになるかは案内の「事前検査」。CLI の出力は次のとおり。

- 誤りは trigger を名指しして 1 件 1 行で出す
- 置き場を読めなかったとき (無いのではなく、読み出しか解析に失敗したとき) は、見つからない理由の後ろに ` · 読めなかった置き場: <path> (<理由>), …` を足す
  - skill と plugin の dir を探す置き場の直下の通常の file (`.DS_Store`・README 等) は飛ばし、読めなかった置き場に数えない
  - repo の `.claude/skills/` と `~/.claude/skills/` の直下の dir で、`SKILL.md` を確かめられない (dir に権限が無い等) ものは、その失敗 1 件だけを読めなかった置き場に数え、`plugin.json` は確かめられたときだけ plugin の dir とする
- 置き場を探す path の途中か置き場そのものが、dir であるはずなのに file のとき (`plugin.json` の `skills` が file を指す・`--plugin-dir` が file を指す・plugin の `.claude-plugin` が file 等。OS の返す理由は not a directory) は、読み出しの失敗ではなく設定の誤りとして、読めなかった置き場に載せず、見つからない理由の後ろに ` · 設定の誤り (dir でなく file を指す): <path>, …` を足す。`<path>` は file になっている path (`--plugin-dir` なら workflow 定義の dir から解いた path) で、その下の path を何通り探しても 1 度だけ載せる。両方あるときは設定の誤りを先に足す
- 事前検査の失敗には、案内を指す行 (§2.8) を足さない。多くは skill の未導入で、書き方では解けない

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

## 3. exit code

| exit | 意味 |
|---|---|
| 0 | 成功 |
| 1 | 観測できなかった (置き場の CLI を起動できない・rate limit・読み切れない・その他の置き場の CLI の失敗) / 想定外の失敗 |
| 2 | 引数の誤り / workflow 定義の誤り (§2.8) / issue 置き場か `cl:` の CL 置き場が見えない (綴りの誤りか、権限が無い) / `jira` の起動時の確認 (§6) で、acli の認証の site が `tracker.host` と違うか読めない・status 名か issue type 名が実在しない |
| 3 | 同じ scope key の loop が走っている (§6) |
| 4 | 置き場の CLI (issue 置き場か CL 置き場の CLI。`github` なら gh、`gitlab` なら glab、`jira` なら acli) の認証が通らない |

- 観測の失敗は、issue 置き場の部品が 認証 / 見えない / 読み切れない / rate limit に分けて返す (system.md §13)
  - 読み切れない: 1 往復で読む件数の上限を超えた (GitHub の issue の label・assignee・依存先、CL の label・review thread、GitLab の merge request の label が 100 件を超えた)。切り詰めた像から候補を出さない
  - 観測の読み出しの結果を変えない補助の要求 (GitLab に conflict の計算し直しを求める、など) が落ちても、観測は失敗にしない。読み終えた作業対象で tick を続け、落ちたことを log.jsonl の `error` の行 (§4) に残す。補助の要求のために、関係の無い trigger までその tick に止めない
  - 外部 CLI (gh・glab・acli) と workspace の hooks (§2.6) の失敗のエラー文は、撃ったコマンド (hook はその名前)・exit code・stderr を載せる。stderr に HTML の本文 (`<!doctype html` か `<html` から後) があれば、status に依らずそこから後を落とし、落としたことを書き添える。reverse proxy・SSO・load balancer は HTML のページを返しうるので、tick の error・`doctor` の理由・`setup` (§7.4) のエラー文・log.jsonl (§4) の `end` の `failure`・`error` の `error` (hook の失敗の行き先) にページを丸ごと載せない
  - gh は、exit 4・status 401・message の `Bad credentials` を認証、status 404・message の `Could not resolve to a Repository` を見えない、status 429・message の `rate limit` を rate limit と見分ける
    - status は gh が status を出す位置でだけ読み、message の中の `HTTP 404` などには当てない。message は gh が印を付けて出す行 (と `gh api` の message の塊の続きの行) だけを読み、message の書き出しで当てる語 (`Bad credentials`・`Could not resolve to …`) は message の途中で触れただけのものには当てない。綴りの詳細は gh の版で変わりうるので、internal/github の apiStatusPattern のコメントが持つ
    - message の無い応答 (`gh api` は本文が JSON でないと status だけを出し、本文を stdout へ出す) は、status に依らずその他の失敗に置く。この応答は GitHub の手前の proxy・SSO の HTML でありうるので、その 401・404 を認証・見えないと読むと、token や repo の綴りという誤った手当てを示す
  - glab は HTTP の失敗をどれも exit 1 で返すので、stderr の status から、401 を認証、404 を見えない、429 を rate limit と見分ける。分けるのは `glab api` が本文の message (GitLab の JSON) とともに出した status だけで、message の無い応答 (`glab api` は status だけを出す) は status に依らずその他の失敗 (読めない) に置く。message の無い応答は GitLab の手前の proxy・load balancer の HTML でありうる。その 404 を issue が消えたと読むと、開いている issue を終端として扱ってしまう
    - GraphQL の経路 (`glab api graphql`。merge request の読み出し) は、GitLab が GraphQL の `errors` で答えると、glab が status を出さずに message だけを出す (認証の失敗も `glab: Invalid token` の形になる)。message とともに status を出さなかった失敗 (message だけ・status だけ) では、REST で project を 1 回読み、その失敗を上の規則で分ける (認証・見えない・rate limit)。REST が通れば、GraphQL の失敗をその他の失敗 (読めない) に置く。GraphQL が `project` を null で返したら (project が見えない)、見えないとする
    - 読むのは glab が status を出す位置だけで、本文の中の `(HTTP 401)`・`403` などには当てない。位置は経路で違う (`glab api` は `glab:` で始まる行、`setup` の `glab repo view` は `<METHOD> <URL>: <status>` の形。綴りの詳細は glab の版で変わりうるので、internal/gitlab の statusPattern のコメントが持つ)
  - glab の HTTP 403 はその他の失敗 (読めない) に置き、理由には glab の stderr の代わりに `HTTP 403 で拒否された (接続元のネットワークか、token の権限)` を出す (撃った glab のコマンドと exit code は残す)。stderr が撃った要求を含む経路 (`setup` の `glab repo view`) では、その `<METHOD> <URL>` (本文の前まで) を理由に添えて、拒否した host を読めるようにする。stderr が URL を含まない経路 (`glab api`。tick の error・`doctor` の理由) は理由だけを出す
    - 403 は、GitLab の手前の reverse proxy の拒否 (IP の許可リスト・VPN 必須など) でも、GitLab の拒否 (token の scope の不足など) でも起きる。どちらかの分類に寄せると、もう一方で誤った手当てを示すので分類を増やさない。glab は remote から API の host を選ぶので、ssh の alias などから別の host を撃って拒否されうる (internal/gitlab の CurrentProject のコメント)。そのときに拒否した host を読めるよう URL を残す
  - `gitlab` の `author` の判定は作者の access level を project の member の API で読む。この API は認証が要るので、glab が未認証 (`glab auth login` も `tracker.token` も無い) なら public な project でも認証の失敗になる
  - acli はどの失敗も exit 1 で返し、文言は Jira の言語の設定で訳されるので、文言では分けない。読み出しが落ちたら `acli jira auth status` を撃ち、落ちれば認証の失敗とする。通れば、起動時の issue 置き場の確認 (§5・§6・§7.4) では見えない (exit 2)、tick の中ではその他の失敗 (tick の error) とする
    - rate limit は見分けられず、その他の失敗に入る。tick の error として残り、次の周期で読み直す
    - `acli jira auth status` は server に問い合わせる。network が落ちていると、これも落ちて認証の失敗になる

## 4. log.jsonl

1 行 1 JSON object。append-only。どの行も `ts` (RFC 3339 の UTC、秒まで)・`scope` (scope key)・`event` を持つ。

値のうち、列挙の値 (`event`・`result`・`outcome`・`reason`) は言語に依らない ASCII の token、自由文の値 (`error`・`failure` など) は人向けの日本語にする (ADR 0013)。

| `event` | いつ | ほかの key |
|---|---|---|
| `loop_start` | loop を起動した (起動時の検査に通り、最初の tick の前。1 回の起動につき 1 行) | `version`・`commit` (loop を書いた binary の版と commit。値は `--version` (§8) と同じ) |
| `tick` | tick の終わり | `result` (`ok` / `error`)・`candidates` (候補の数)・`launched` (起動した作業対象の列)・`ambiguous` (曖昧な CL。`{"head": <head branch>, "targets": [<作業対象>…]}` の列)・`blocked` (事前検査 (§2.9) に落ちて起動しない trigger。`{"trigger": <名前>, "error": <理由>}` の列)・`running`・`waiting_retry`・`orphans`・`max_concurrent` (起動の判定に使った値。`result` が `ok` のとき)・`error` (`result` が `error` のとき) |
| `start` | worker を起動した | `target`・`trigger`・`attempt`・`session_id`・`workspace`・`pid` |
| `end` | worker 1 回分の終わり方を決めた | `target`・`trigger`・`attempt`・`session_id`・`outcome`・`reason`・`failure` (worker 自身が失敗したときだけ。下の `outcome` の表)・`exit_code` (process が自分で終わったときだけ。signal で止まったら載せない)・`is_error`・`num_turns`・`permission_denial_count` (attempt の最後の `result` の要約。下の箇条) |
| `retry` | `failed` の後、次の attempt を予定した | `target`・`trigger`・`attempt`・`next_attempt`・`session_id`・`backoff` (秒。小数を含む) |
| `release` | 再起動を待つ claim を、起動せずに解いた | `target`・`trigger`・`attempt`・`session_id`・`reason` (`terminal` / `left_trigger` / `trigger_removed`) |
| `abandon` | attempt の上限で打ち切った | `target`・`trigger`・`attempt`・`session_id` |
| `wait_slot` | backoff が明けた再起動が、並列上限に空きが無くて待ち始めた (1 回の待ちにつき 1 行) | `target`・`trigger`・`next_attempt`・`max_concurrent` |
| `recheck_skip` | 起動しようとした候補を起動の直前に読み直し (§6 の tick の手順 7)、`reason` のどれかに当たって起動しなかった (tick ごとに、候補 1 件につき 1 行。同じ候補が次の tick でも当たれば、また 1 行書く。読み直せなかった候補は `error` の行で残し、この行は書かない) | `target`・`trigger`・`reason` (`undecided` / `terminal` / `left_trigger` / `earlier_trigger`) |
| `verify_wait` | worker が終わった後の確かめ (§6 の「worker の 1 回分」の手順 4) で、作業対象が trigger に当たるかをまだ決められず、claim を持ったまま次の tick で確かめ直す (確かめ直すたびに 1 行。失敗ではないので `error` の行は書かない。読み直せなかったときは `error` の行で残し、この行は書かない) | `target`・`trigger`・`attempt`・`session_id`・`reason` (`undecided`) |
| `unabandon` | 打ち切りを解いた (打ち切ったときの trigger から外れたのを観測した) | `target`・`trigger` |
| `orphan` | claim の無い workspace に、生きている orphan を見つけた (§6 の「orphan」。見つけた tick に 1 行。生きている間は書き直さない) | `target` |
| `orphan_end` | 前の tick まで生きていた orphan が終わったのを見た (1 行) | `target` |
| `error` | 処理は続けるが、運用者が知るべき失敗 | `target` (あれば)・`error` |

- `target` は、issue なら `issue#<番号>`、CL なら `cl#<番号>`
- `tick` の行の起動の判定に使った値は、§6 の tick の手順 7 (候補の起動) の判定のもの
  - `running`・`waiting_retry`: その tick で候補の起動を始める前の、走っている worker と再起動待ちの claim の数 (手順 7 が `limits.max_concurrent` から引く数)
  - `orphans`: その tick で生きていると見た (確かめられなかったものを含む) orphan の数。これも手順 7 が引く
  - `max_concurrent`: `loop` に `--max-concurrent` (§6) を渡したならその値、渡さなければその tick で読み直した workflow 定義の `limits.max_concurrent`
  - 手順 7 は、`running`・`waiting_retry`・その tick で起動した数 (`launched` の数) の和が `max_concurrent` に達したところで候補の起動をやめる
- `wait_slot` の行の `max_concurrent` は、空きの判定に使った値。`--max-concurrent` を渡したならその値、渡さなければ loop が直近に読めた版の workflow 定義の `limits.max_concurrent` (`wait_slot` は tick の外でも書かれ、読み直しに落ちた tick は前の版を使い続ける)
- `outcome`

| 値 | 意味 |
|---|---|
| `completed` | worker が終わった後、作業対象が起動した trigger から外れていた。または終端になっていた。worker 自身が失敗していても completed とし、`failure` に失敗を載せる |
| `failed` | worker が終わった後も作業対象が trigger に当たったままで、worker が失敗した (異常終了・hook の失敗・描画の失敗・起動できない・stall・上限時間の超過) か、正常に終わった。claim は解かず、`retry` か `abandon` の行が続く |
| `stopped` | loop が止めた (作業対象が終端になった・2 回目の停止要求) |

- `reason` の token。画面の文は、loop が人の読む行 (§6) で token の代わりに出す文

| token | 意味 | 画面の文 | 出る行 |
|---|---|---|---|
| `terminal` | 作業対象が終端になっている | `終端` | `end`・`release`・`recheck_skip` |
| `left_trigger` | 作業対象が起動した (起動しようとした) trigger から外れている | `trigger から外れた` | `end`・`release`・`recheck_skip` |
| `still_matches` | worker が終わった後も、作業対象が trigger に当たったまま (`failed`) | `trigger に当たったまま` | `end` |
| `stop_requested` | 2 回目の停止要求で止めた (`stopped`) | `停止要求` | `end` |
| `trigger_removed` | 再起動を待つ claim を起動した trigger が、workflow 定義から消えた | `trigger が workflow 定義から消えた` | `release` |
| `undecided` | 作業対象が trigger に当たるかをまだ決められない (conflict を計算中の CL。§2.3) | `当たるかをまだ決められない` | `verify_wait`・`recheck_skip` |
| `earlier_trigger` | 作業対象が、宣言順で先の trigger に当たる | `宣言順で先の trigger に当たる` | `recheck_skip` |

- `end` の `failure`: worker 自身の失敗の理由 (異常終了・hook の失敗・描画の失敗・起動できない・stall・上限時間の超過) の自由文。失敗したときだけ載せ、`completed` と `failed` で同じ形にする。`stopped` には載せない。`reason` には失敗を含めない

- `error` の行を書く場面: `after_run` と `before_remove` の失敗・workspace を消せない (次の tick の掃除で消し直す)・終わった worker の作業対象を読み直せない (claim を持ったまま次の tick で読み直す)・起動しようとした作業対象を読み直せない (その tick は起動せず、次の tick で候補になれば試み直す)・打ち切った作業対象を読み直せない (打ち切りを保ち、次の tick で確かめ直す)・止める worker の process group に signal を送れない・SIGKILL を送れず claude を止められない (§6 の「止められない worker」)・worker log から attempt の最後の `result` を読めない (項目の型が違うときを含む)・観測の補助の要求が落ちた (§3。`target` を持たない。読み終えた作業対象で tick を続け、次の tick で求め直す)
- log.jsonl に行を書けなければ、そのことを stdout に出して続ける
- `attempt` は、その行が指す claim の最後に起動した (起動しようとした) attempt
- `end` の行の attempt の最後の `result` の要約: worker log (stream-json。§1) のうち、その attempt が追記した完結した行 (改行で終わる行) から、最後の `type: result` の行を読む。worker log は attempt を跨いで追記するので、前の attempt の `result` は読まない
  - `is_error`・`num_turns`: その `result` の同じ名前の項目の値
  - `permission_denial_count`: その `result` の `permission_denials` (配列) の要素の数
  - `result` の行が無ければ (claude を起動できなかった・stall や上限時間や signal で `result` を書く前に止めた 等) 3 つとも載せない。`result` の行にその項目が無ければ、その key だけ載せない。項目の型が違えば (`num_turns` が数でない 等)、その key を載せず `error` の行を残す。worker log を最後まで読めなければ (どれが最後の `result` か決まらない)、3 つとも載せず `error` の行を残す
  - 止めた attempt (stall・上限時間・停止要求) でも、止める前に `result` の行を書いていれば要約を載せる
  - 要約は attempt 開始時の offset から読む。終了時の大きさがその offset より小さければ 0 から読む
- `session_id` は CLI が発行して最初の attempt に `--session-id` で渡した値。同じ claim の attempt を通して 1 つ。Claude Code の transcript へ辿る鍵
- claim を解くのは `completed` と `stopped` の `end`・`release`・`abandon` の行

## 5. 試運転 (`loop --dry-run`)

```
claude-dispatcher loop --dry-run [--max-concurrent <N>] [<workflow の path>]
```

`--max-concurrent` は `loop` (§6) と同じ綴りと検査で受ける。試運転は並列上限に依らずに候補を出すので、渡しても出力は変わらない。

workflow 定義を読んで検査し、事前検査 (§2.9) を通し、snapshot を作って trigger を評価し、起動するはずの作業対象を示して終わる。**何も書かない** (state dir を作らず、lock も取らない)。

候補は、loop の起動の直前と同じ読み直し (§6 の tick の手順 7) を 1 件ずつ掛け、loop が起動しない候補は出さない (open な一覧の検索は、書き込みの直後に古い結果を返しうるため)。読み直しで起動しない理由は §6 と同じで、出さなかったことはどこにも示さない。候補が `limits.max_concurrent` より多くても、どれも読み直す。候補を 1 件でも読み直せなければ、open な一覧を読めなかったときと同じく失敗する (下)。

成功したら、読み直した候補を 1 件 1 行で、評価の順 (§2.4) に stdout へ出す。題名は読み直した作業対象のもの。候補が無ければ何も出さない。

```
implement	issue	#42	ログインの失敗を記録する
fix-ci	cl	#51	ログインの失敗を記録する
```

- 列はタブで区切る: trigger の名前・作業対象の種類 (`issue` / `cl`)・参照・題名
- 参照は tracker の綴りで書く: issue と GitHub の CL は `#<番号>`、GitLab の CL (merge request) は `!<番号>`、Jira の issue は key (`WIDGETS-123`)。GitLab では issue と merge request が別々に番号を振るので、同じ番号が両方にありうる
- `jira` では、snapshot を作る前に issue 置き場を確かめる (§6 の「起動時の検査」の `jira` の行と同じもの)。続けて `cl:` の CL 置き場も確かめる (同じく §6)
- 題名の制御文字 (タブ・改行・ESC など) は空白に置き換える

観測の補助の要求が落ちたとき (§3) は失敗にせず、候補を出し、stderr に理由を出して exit 0 で終わる。

失敗したら、stdout に何も出さず、stderr に理由を出して §3 の exit code で終わる。workflow 定義の誤りは 1 件 1 行で出し、後ろに案内を指す行を足す (§2.8)。

## 6. `loop`

```
claude-dispatcher loop [--max-concurrent <N>] [<workflow の path>]
```

実装 repo の clone を cwd にして撃つ。

- `--max-concurrent <N>` は、workflow 定義の `limits.max_concurrent` を上書きする並列上限 (`system.md` §8)。`--max-concurrent 3` の形でも `--max-concurrent=3` の形でも書ける。値が無い (flag の直後が `-` で始まるときを含む)・値が空・2 回書いた・1 以上の整数でないときは引数の誤り (exit 2)
  - 渡した値は loop が止まるまで変わらない。tick ごとに workflow 定義を読み直しても、`limits.max_concurrent` は効かない
  - 本節と §4 の `limits.max_concurrent` は、`--max-concurrent` を渡した loop ではその値と読み替える

**起動時の検査** (落ちたら stderr に理由を出して終わる):

| 検査 | exit |
|---|---|
| 引数の数と flag | 2 |
| workflow 定義を読めて、検査に通る (§2.8) | 2 |
| 事前検査 (§2.9) に通る | 2 |
| tracker の CLI (`tracker.kind` が `github` なら gh、`gitlab` なら glab、`jira` なら acli)・`jira` では `cl.kind` の CLI (gh か glab)・`claude.command` を解決できる (PATH と、よく使われる置き場)。`on: cl` の trigger があれば git も | 1 |
| `jira` だけ: issue 置き場の確認 (下) | 下 |
| `jira` だけ: `cl:` の CL 置き場の確認 (下)。`on: cl` の trigger が無くても撃つ | 下 |
| state dir を作れる | 1 |
| 同じ scope key の loop が走っていない (`loop.lock` を取れる) | 3 |

- よく使われる置き場は、workflow 定義が撃つ依存 CLI (`tracker.kind` の CLI・`cl.kind` の CLI・claude・git) のどれかが PATH に無いときだけ PATH の前に足す。使わない置き場の CLI が無いことでは PATH を書き換えない (worker と hooks は loop の PATH を継ぐので、足すと worker が撃つ git などが入れ替わりうる)
- 同じ issue 置き場の 2 本目の loop は、clone や workflow 定義の path が違っても起動時に止まる
- `jira` の issue 置き場の確認は、次の順に撃ち、最初に落ちたもので終わる。試運転 (§5) と `doctor` (§7.4) も同じ確認を通す
  1. acli の認証の site: `acli jira auth status` が通り (落ちれば認証の失敗、exit 4)、出力の `Site:` の行の site が `tracker.host` と同じ (大文字と小文字を区別しない)。違うか `Site:` の行を読めなければ exit 2。acli は呼び出しごとに site を指定できず、active な account の site を読むため
  2. open な一覧 (`project = "<project key>" AND statusCategory != Done`) を読める。落ちたら §3 の acli の分類で、認証 (exit 4) か見えない (exit 2)
  3. trigger の `status` に書いた status 名が実在する (§2.2)。無い名前を trigger と一緒に名指しして exit 2。検索が落ちたら §3 の acli の分類で、認証なら exit 4。それ以外は名前の誤りと読み、acli の理由を添えて名指しする
  4. trigger の `type` に書いた issue type 名が project にある (§2.2)。無い名前を trigger と一緒に名指しして exit 2。project の issue type を読めなければ §3 の acli の分類で、認証 (exit 4) か見えない (exit 2)
- CL 置き場の確認は、`cl:` の CL 置き場の open な CL の一覧を 1 回読む。落ちたら §3 の gh / glab の分類で、認証 (exit 4) か見えない (exit 2)、それ以外は exit 1。観測の補助の要求だけが落ちたときは通す。試運転 (§5) と `doctor` (§7.4) も同じ確認を通す。`cl:` には token が無く、gh / glab 自身の認証を使う
- lock は loop の生存期間だけ持つ。loop が死ねば外れる
- `loop.lock` を取った loop は、続けて `alive.lock` も生存期間のあいだ持つ。`status` は `alive.lock` だけを確かめるので、`loop.lock` を取り合わない (`alive.lock` を `status` が一瞬持っていれば、外れるまで待つ)

**tick**: 起動直後に 1 回撃ち、以後は tick の終了から `polling.interval` だけ待って次を撃つ。tick ごとに workflow 定義を読み直す。

- 読み直した workflow 定義が検査に落ちたら、その tick は何も起動しない (#74 Q38)。周期は、最後に検査に通った版のものを使う
- 読み直した workflow 定義の scope key が起動時と違えば、その tick は何も起動しない (lock は起動時の scope key で取っている)
- 読み直した workflow 定義の CL 置き場 (`jira` では `cl.kind`・`cl.host`・`cl.repo`。`github`・`gitlab` では issue 置き場と同じ場所なので、大文字と小文字以外の違いは上の scope key の規則で先に止まる) が起動時と違っても、その tick は何も起動しない。claim している CL を、起動時と別の CL 置き場で読み直さないため。比べるときは scope key と同じく大文字と小文字を区別しない。`tracker.repo` (`github`・`gitlab`) か `cl.repo` (`jira`) の大文字と小文字だけを書き換えた版は同じ CL 置き場とみなし、tick を続ける

**tick の手順** (system.md §7):

作業対象の読み直しと open な一覧は、issue なら issue 置き場から、CL なら CL 置き場から読む。open な一覧は trigger に現れる種類のものだけを読む (§2.4)。

1. 突き合わせ: 走っている worker の作業対象を読み直し、終端 (issue の close、CL の merge か close) になっていれば worker を止め、`after_run` と `before_remove` を撃って workspace を消す。trigger から外れただけでは止めない
2. 終わった worker のうち、作業対象を読み直せなかったものを読み直す
3. workflow 定義を読み直し、事前検査 (§2.9) を通して snapshot を作る。事前検査に落ちた trigger は、手順 6 で再起動せず、手順 7 の評価から外す
4. 掃除: workspace root の下の `issue-<番号>` と `cl-<番号>` のうち claim が無いものの orphan を確かめ (下の「orphan」)、orphan が生きておらず open な一覧にも無いものを読み直し、終端になっていれば `before_remove` を撃って消す。起動の直後の tick が起動時の掃除を兼ね、以後の tick が、claim を解いた後に終端になったものと消し損ねたものを拾う
5. 打ち切りを解く: 打ち切った作業対象のうち、終端になったか、打ち切ったときの trigger の述語に当たらなくなったもの (その trigger が workflow 定義から消えたものを含む) の打ち切りを解く。conflict を計算中の CL は、外れたとは数えない
   - 曖昧な CL になったことでは解かない。曖昧さは open な一覧でしか決まらず、古い一覧で曖昧に見えただけで解けてしまうため (曖昧な間はどの trigger でも起動しないので、保っても起動は遅れない)。曖昧な間も、ほかの作業対象と同じ規則で外れたかを確かめる
   - snapshot に無いか、snapshot で当たらなくなって見えるものは、置き場から 1 件読み直して確かめてから解く (open な一覧の検索は、書き込みの直後に古い結果を返しうるため)。読み直しても当たったままなら解かない。読み直せなければ error の行を残して解かず、次の tick で確かめ直す
   - snapshot で当たったままのものは読み直さない
6. 再起動: backoff の明けた再起動待ちの claim を、次の「再起動」の規則で起動する
7. trigger を評価し (§2.4)、候補のうち claim も打ち切りもされておらず orphan も生きていないものを、`limits.max_concurrent` から走っている worker と再起動待ちの claim と生きている orphan を引いた数だけ起動する (確かめ待ちの claim と、事前検査に落ちた trigger の再起動待ちの claim は数えない)
   - 起動の直前に、候補を 1 件ずつ読み直す (open な一覧の検索は、書き込みの直後に古い結果を返しうるため)。tracker の種類を問わず読み直す。次のときはその tick では起動せず、起動しなかった分の空きを次の候補に回す。次の tick で候補になれば試み直す
     - 当たるかをまだ決められない (conflict を計算中の CL。§2.3)・終端になっている・起動しようとした trigger から外れている・宣言順で先の trigger に当たるようになっている。log.jsonl に `recheck_skip` の行 (§4) を書き、人が読む行に `読み直しで起動せず` を出す
     - 読み直せない。error の行を残す
   - CL は、claim している作業対象の workspace で checkout されている branch を head に持つもの (同じ repo の CL) なら起動しない。一覧の CL で当たれば読み直さずに外し、読み直した CL でも確かめ直す。branch は workspace を cwd にして `git symbolic-ref --short -q HEAD` で読む。workspace が無い・git の作業ツリーでない・detached HEAD なら branch は無いとする
   - branch をそれ以外の理由で読めなければ、error の行を残し、その tick は CL の候補を起動しない (同じ branch に worker を重ねないため)
   - 起動した worker には、読み直した作業対象を渡す

**再起動** (system.md §7「失敗の扱い」):

- `failed` の後、attempt が `limits.max_attempts` に達していれば打ち切る。達していなければ、`min(10s × 2^(attempt−1), limits.max_retry_backoff)` の後に再起動を予定する
- backoff が明けたら、tick を待たずに再起動を試みる (tick の途中なら、その tick の手順 6 で試みる)。明けたときに起動できなかった claim は、下のとおり次の tick か worker が終わったときに試み直し、tick の外で試み直し続けない
  - 事前検査 (§2.9) に落ちた trigger の claim は、attempt を進めずに待ち直す (tick の手順 3)。tick の外で試みるときも事前検査は撃ち直さず、直近の tick の結果を使う。直近の tick の後に先頭の skill が消えても、次の tick が事前検査に落とすまでは再起動する
  - 走っている worker が `limits.max_concurrent` に達していれば、attempt を進めずに待ち直す。worker が終わって空きが出たときに試み直す
  - 起動した trigger が workflow 定義から消えていれば、claim を解く (`release`)
  - 作業対象を置き場から 1 件読み直す。tick の中で試みるときも、tick の open な一覧は使わない (一覧の検索は、書き込みの直後に古い結果を返しうるため)。終端なら `before_remove` を撃って workspace を消し、終端か起動した trigger から外れていれば、claim を解く (`release`)。読み直せなければ次の tick で試み直す
  - 読み直しで確かめるのは、終端か、起動した trigger から外れたか (と、下の当たるかをまだ決められないか) だけ。宣言順で先の trigger に当たるようになったかは確かめない (再起動は同じ trigger で続ける。system.md §7)。起動の直前の読み直し (tick の手順 7) と違って `recheck_skip` の行は書かず、claim を解くときは `release` の行で残す
  - CL は、tick の中でだけ再起動を試みる (曖昧さと branch は tick が読んだ一覧で確かめるため)。tick の一覧で曖昧な CL に見えるか、head branch を別の claim の workspace が checkout しているか、その branch を読めなければ、attempt を進めずに次の tick で試み直す
    - 曖昧に見えても claim は解かない。曖昧さは一覧でしか決まらず、1 件の読み直しでは確かめられないので、古い一覧で曖昧に見えただけで解けてしまうため (解けると、次の tick で attempt 1 から数え直して起動しうる)。曖昧な間も claim は再起動待ちとして `limits.max_concurrent` の枠を 1 つ使う。曖昧な CL は状態 file の `ambiguous` (§7.1) に出て、人が片方を close すれば解ける
  - 当たるかをまだ決められなければ (conflict を計算中の CL。§2.3)、attempt を進めずに次の tick で試み直す
  - 当たったままなら、attempt を 1 つ進め、同じ trigger・同じ session id・同じ workspace で起動する
- 再起動は、そのときの workflow 定義 (trigger・本文・hooks・`claude`・`limits`) で行う。workspace の root だけは、最初に起動したときのものを使う (session は workspace の path ごとに保存されるので)
- 前の attempt で claude を起動できていなければ (hook・描画・起動の失敗)、session はまだ無いので `--session-id` で始める
- 停止要求を受けた後に失敗した attempt は、再起動を予定しない
- 打ち切りは作業対象ごとに memory に持つ。打ち切った作業対象は、どの trigger に当たっても起動しない。loop を起動し直すと消える
- 打ち切りを解くには、label を外して打ち切ったときの trigger から外し、1 周期待ってから付け直す (外してから付け直すまでが 1 周期に収まると、外れたのを観測できない)。`status` (§7) の打ち切りの行にこの手順を出す

- 作業対象の読み直しで issue が消えていたら (削除・移管)、終端と同じに扱う。消えたと読むのは次のとき
  - `github`: gh が GraphQL の message の書き出しに `Could not resolve to an Issue` を返すか (§3。repo が見えないという message も並べば見えないの失敗)、応答に issue が無い
  - `gitlab`: glab が `(HTTP 404)` を返し、stderr に `Project Not Found` が無い (project が見えないときは `404 Project Not Found` なので、見えないの失敗として扱う)
  - `jira`: 次の手順で読み直す。issue を消した・別の project へ移した (旧 key が転送されてもされなくても)・権限を外して見えなくなった、のどれも終端として扱う
    1. `acli jira workitem view <key> --json` で読む。返った key の project key が `tracker.repo` と違えば (別の project へ移した) 終端
    2. 読み出しが落ちたら `acli jira auth status` を撃つ。落ちれば認証の失敗
    3. 通れば open な一覧を読む。読めればその中に key があれば読み直せない失敗。一覧を読めなければ、その失敗
    4. 一覧に key が無ければ、もう 1 回 `acli jira workitem view` で読む。読めればその issue (1 と同じく、別の project の key なら終端)、また落ちれば 2 と同じく `acli jira auth status` を撃ち、落ちれば認証の失敗、通れば終端。一覧の検索は書き込みの直後に古い結果を返しうるので、一覧に無いことだけでは終端としない (view の一時的な失敗と古い一覧が重なると、存在する issue を終端と読むため)
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
   - stream に progress (下の「progress」) が `limits.stall_timeout` のあいだ書かれないか、起動からの経過が `limits.run_timeout` を超えたら、process group を止めて失敗とする (止め方は 2 回目の停止要求と同じ)。止める判断と停止要求が重なったら、停止要求で止めたことにする

4. 終わったら (止めたときを含む)、worker log からその attempt の最後の `result` を読んで `end` の行に要約を載せ (§4)、`after_run` を撃ち、作業対象を読み直す (SIGKILL を送れず claude が終わっていなければ、claude が終わるまでどれもしない。下の「止められない worker」)。終端か、起動した trigger から外れていれば `completed` として claim を解き、当たったままなら `failed` として再起動 (上) に回す
   - 当たるかをまだ決められなければ (conflict を計算中の CL)、完了とも失敗とも数えない。log.jsonl に `verify_wait` の行 (§4) を書き、人が読む行に `確かめ待ち` を出して、claim を持ったまま次の tick で確かめ直す
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
<時刻> 確かめ待ち cl#52 (fix-conflict, attempt 1): 当たるかをまだ決められない
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
● loop 停止待ち  github.com/acme/widgets · v0.3.0
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
- 活動は stall の判定に使わない。stall の時計は下の progress で戻す
- **progress**: worker の stream のうち、worker が進んでいる兆候として stall (`limits.stall_timeout`) の時計を戻す行。活動の表とは別に決める (活動の表示を変えても stall の判定は変わらない)

| stream の行 | progress |
|---|---|
| 改行を含めて 64 KiB を超える行 | `type` を見ずに数える |
| `type: tool_progress` (tool の実行中の heartbeat) | 数えない |
| `type: rate_limit_event` | 数えない |
| JSON として読めない行 | 数えない |
| `type` の無い行 | 数えない |
| それ以外 (`type: system` のすべての subtype と、知らない `type` を含む) | 数える |

- 表は上から当て、最初に当たった行で決める
- 数えないのは、止まっていても書かれる行と、中身を判定できない行だけにする。数えすぎた誤りは `limits.run_timeout` で止まるが、数えなさすぎた誤りは進んでいる worker の attempt を失わせるため。claude が新しく書き始めた `type` や subtype も数える
- 64 KiB を超える行は tool の結果や発話の行で、heartbeat や通知の行は小さいので、`type` を見ずに数える。1 行は頭の 64 KiB までだけを持って判定する
- 確認のたびに、前の確認で読み終えた offset から今の file の大きさまでの完結した行を、末尾の窓に限らずすべて見る。1 回に読む量に上限は置かない。今の attempt が書いた行だけを見る
- 見た行に progress が 1 行でもあれば、stall の時計をその確認の時刻に戻す
- 監視は stream を、loop が claude に渡した worker log の fd (またはその dup) で読む。path を開き直さない。path の rename・削除・置き換えは監視に影響しない
- 縮みは、fd で Stat した大きさが前の確認で読み終えた offset より小さいことで判定し、progress と活動の読み終えた offset を 0 に戻し、縮んだ後の行を頭から見る。縮んだこと自体では時計を戻さない。1 回の確認のあいだの truncate と再成長は検出しない (loop の外の書き手が無い前提)
- stream の file を読めなければ (大きさを確かめられない・中身を読み出せない のどちらも)、時計を戻さず、`stream を読めない: <理由>` を活動にする。その状態が `limits.stall_timeout` 続けば stall で止め、失敗の理由に読めない理由を添える。読めなかった後は、伸びていなくても読み直す
- 行の書かれない区間 (長い tool の実行・API の retry の backoff・compaction・`-p` の終わりに background の作業を待つ区間・最後の `result` の後) が `limits.stall_timeout` より長いと、進んでいても stall で止まる。区間を見分ける行は確かめられていないので、`limits.stall_timeout` を区間より長くして吸収する
- `result` は worker の終わりを表さない。worker の終わりは process の終了だけで決める (ADR 0018)

**停止**: SIGINT / SIGTERM / SIGHUP を同じに扱う。

| 受けたとき | 振る舞い |
|---|---|
| 1 回目 | 新しい tick と起動 (再起動を含む) をやめ、走っている worker の終了を待って (終わり方の処理を済ませて) 止まる。待つ間も周期ごとに突き合わせ (tick の手順 1) だけを撃ち、終端になった作業対象の worker は止める。走っていなければ直ちに止まる。再起動待ちの claim は error の行を残して捨てる |
| 2 回目 | 走っている worker の process group に SIGTERM を送り、5s で終わらなければ SIGKILL を送る。worker が終わったら group に残った process も SIGKILL で止め、`after_run` を撃ち、`stopped` の `end` 行を書いて止まる。SIGKILL を送れず claude が終わっていない worker は待たない (下の「止められない worker」) |

**止められない worker**: 止めるとき (stall・上限時間・終端・2 回目の停止要求) に SIGKILL を送れず、claude が終わっていなければ (system.md §7)、claude が終わるまで次のとおりにする。

- `after_run` を撃たず、worker log の `result` も読まない。`end` の行は書かない (worker は終わっていない)
- signal を送れなかった理由を別の行に分けず、`error` の行 `claude を止められない (pid <pid>, workspace <path>): 終わるまで workspace を残し、終わりを確かめず再起動もしない: <signal を送れなかった理由>` を 1 行書く
- claim は段階 `stopping` のまま残る (状態 file と `status` にも `stopping` で出る)。同じ作業対象は起動せず、workspace は掃除せず、並列の枠を 1 つ使い続ける
- 停止要求の後も、claude が終わるまではその worker の終了を待たない。loop の終了までに claude が終わらなければ、`error` の行 `claude を止められないまま止まる (pid <pid>, workspace <path>)` を書く

claude が後で終われば、停止要求の後でも worker の終了を待ち、止めた worker が終わったときと同じく、`after_run` を撃ち、`result` を読み、止められて終わったなら `stopped` の `end` 行を書いて claim を解き、stall・上限時間で止めようとしたなら終わりを確かめる (上の worker の 1 回分の手順 4)。その後の loop の終了では、上の `claude を止められないまま止まる` の行を書かない。停止を決めた後、worker の終わりが届く前に claude が終わったときは、pid を書かずに `error` の行 `止められなかった claude は終わったが、after_run を待たずに止まる (workspace <path>)` を書く。

**orphan**: 前の loop が起動した claude が、その loop の止まった後も生きているもの (system.md §7)。止められないまま止まった loop と、落ちた loop が残す。

- loop は claude を起動する前に、`workers/<作業対象>.log` を開いた fd に `LOCK_EX` の flock を掛け、その fd を claude の stdout に渡す。claude (と fd を受け継いだ子孫) が生きている間、lock は外れない
- tick の手順 4 で、claim の無い workspace ごとに、その作業対象の `workers/<作業対象>.log` を開いて `LOCK_SH|LOCK_NB` を試す。取れない (`EWOULDBLOCK`) なら orphan が生きている。file が無いか、取れればすぐ外して、orphan はいない
- orphan が生きている作業対象は、その tick では workspace を掃除せず、候補にしない。並列の枠を 1 つ数える。`after_run` は撃たない
- 見つけた tick に `orphan` の行 (§4) を書き、人が読む行に `orphan <作業対象>: 前の loop の claude がまだ動いている` を出す。前の tick まで生きていた orphan が終わっていれば、`orphan_end` の行を書き、人が読む行に `orphan <作業対象> が終わった` を出す。終わった作業対象は、同じ tick から普通の掃除と候補の扱いに戻る
- 確かめられない (`EWOULDBLOCK` 以外の失敗) か、claim の無い workspace の一覧を読めなければ、どの workspace の claude が生きているかを決められないので、その tick を error で終える (`tick` の行の `result` が `error`、`error` に理由)。掃除も起動 (再起動を含む) もしない。`要対処` の orphan は前の tick で見たもののまま残す

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
  "version": "v0.3.0",
  "commit": "<commit の hash>",
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
  "blocked": [{"trigger": "fix-ci", "error": "action の先頭の /fix が見つからない (plugin・repo の .claude・~/.claude の skill と command)"}],
  "orphans": [{"target": "issue#45"}]
}
```

| key | 中身 |
|---|---|
| `version`・`commit` | 状態 file を書いた loop の binary の版と commit。値は `--version` (§8) と同じ。この 2 つを足す前の版が書いた状態 file には無い |
| `stopping` | 停止要求を受けて、worker の終了を待っている |
| `next_tick_at` | 次の tick の予定。停止要求の後は `null` |
| `last_tick` | 直近の tick。`result` が `error` なら `error` に理由 (事前検査の失敗 — workflow 定義の誤り・scope key の食い違い・観測の失敗 — を含む)。まだ tick が無ければ `null` |
| `workers[].phase` | `running` (走っている) / `stopping` (止めている) / `unstopped` (SIGKILL を送れず claude が終わっていない。claude が終わるまで終わりを待たず、確かめも再起動も workspace の削除もしない) / `verifying` (終わり方を確かめ待ち) / `waiting_retry` (再起動待ち) |
| `workers[].pid`・`workers[].workspace` | `unstopped` の claim の claude の pid と workspace の path。ほかの段階では無い |
| `workers[].started_at` | 今の attempt の worker を起動した時刻。起動の前 (再起動待ち) は `null` |
| `workers[].retry_at` | `waiting_retry` の backoff が明ける時刻。停止要求の後に失敗した claim は再起動を予定しないので、無い |
| `workers[].activity` | 活動 (§6 の画面)。まだ無ければ `null` |
| `abandoned` | 打ち切った作業対象と、打ち切ったときの trigger |
| `ambiguous` | 直近の tick の曖昧な CL (§2.4) |
| `blocked` | 直近の tick で事前検査 (§2.9) に落ちて、起動しない trigger |
| `orphans` | 直近に確かめられた tick で生きていた orphan (§6) の作業対象。この key を足す前の版が書いた状態 file には無い |

### 7.2 `status`

```
claude-dispatcher status [<workflow の path>]
```

別の端末から loop の今の状態を見る。workflow 定義を読んで scope key を決め、その state dir の状態 file を描く。**何も書かず、tracker の CLI も撃たない**。

```
● loop 稼働中  github.com/acme/widgets · v0.3.0
次の tick 00:10:00 · 直近の tick 00:05:00 ok · 候補 1

workers 3 ────────────────────────────────────────────
作業対象  trigger    attempt  段階           経過               活動
issue#42  implement  1        running        5m10s              Bash go test ./...
issue#44  implement  2        waiting_retry  00:06:40 に再起動
issue#46  implement  1        unstopped      12m03s

要対処 5 ─────────────────────────────────────────────
打ち切り issue#43 (implement): label を外して trigger から外し、1 周期待ってから付け直すと解ける
曖昧な CL worktree-issue-7: cl#52, cl#53
起動しない trigger fix-ci: action の先頭の /fix が見つからない (plugin・repo の .claude・~/.claude の skill と command)
止められない issue#46: claude (pid 4242) が終わっていない。workspace /path/to/workspaces/issue-46。その pid の claude を止めると、loop が後始末して解ける (loop が先に止まれば、次の loop が orphan として扱う)
orphan issue#45: 前の loop の claude がまだ動いている。終わるのを待つか、log.jsonl のこの作業対象の直近の start の行 (止められなかったなら claude を止められない の行) の pid を止める
```

- 見出しは 2 行。1 行目は loop の状態と scope key と版: loop が生きていれば `● loop 稼働中` (停止要求の後は `● loop 停止待ち`)、`alive.lock` を取れれば `○ loop なし`。版は scope key に続けて ` · <version>` (状態 file の `version`) を出す。commit は出さない (状態 file と log.jsonl で読める)。`○ loop なし` のとき (状態 file の版は止まった loop のもの) と、状態 file に `version` が無いとき (版を足す前の版が書いた状態 file) は出さない
- 2 行目は `次の tick <時刻>` (停止要求の後は `次の tick なし`)・`直近の tick <時刻> ok · 候補 <n>` を ` · ` で繋ぐ。直近の tick が error なら `直近の tick <時刻> error: <理由>`。`loop なし` のときは次の tick の欄を出さない。出す欄が無ければ 2 行目を出さない
- `loop なし` のときは見出しだけを出す (状態 file が残っていても、worker と打ち切りは loop の memory と一緒に消えている)
- 状態 file が無ければ、見出しの 2 行目は `記録なし`
- 見出しの後に空行を挟み、セクションを空行で区切って並べる。セクションの 1 行目はセクション名と件数の後に罫線を引く
- `workers <n>`: claim の表。列見出しの行 (作業対象・trigger・attempt・段階・経過・活動) の後に claim を 1 行ずつ並べる。claim が無ければ列見出しも出さない
  - 列は空白で揃える (列の間は 2 桁)
  - attempt は番号。再起動待ちの行は失敗した attempt の番号 (ログの行の `再起動を予定` は次の attempt の番号を出す)
  - 段階は `workers[].phase` (§7.1) の値のまま
  - 経過は、段階が `running`・`stopping`・`unstopped` の claim の、今の attempt の起動からの時間 (`45s`・`5m10s`・`1h02m05s`)。`waiting_retry` の claim は `<時刻> に再起動`。停止要求の後に失敗した claim は再起動を予定しないので空
  - 活動は §6 の要約
- `要対処 <n>`: 打ち切り・曖昧な CL・起動しない trigger・止められない worker (段階が `unstopped` の claim)・orphan を 1 件 1 行で並べる。1 件も無ければセクションごと出さない
- 時刻は端末の local time の `HH:MM:SS`
- stdout が端末でなくても、描き方は同じ人が読む形 (列は空白揃え)。機械が読むなら状態 file (§7.1) を読む

**色と幅** (loop の画面 (§6) も同じ):

- 色は stdout が端末のときだけ付ける。端末でも、環境変数 `NO_COLOR` が空でない値で設定されていれば付けない
- 色を付けるところ:
  - 見出しの `● loop 稼働中` は緑、`● loop 停止待ち` は黄、`○ loop なし` は灰。scope key は太字。直近の tick の `ok` は緑、`error: <理由>` は赤。欄の名前 (`次の tick` など) は灰
  - セクション名は太字 (`要対処` は黄)、罫線は薄く、列見出しは灰
  - 段階は `running` が水色、`waiting_retry` が黄、`unstopped` が赤、`verifying` が灰
  - `要対処` の行の頭の語は、`打ち切り`・`起動しない trigger`・`止められない` が赤、`曖昧な CL` と `orphan` が黄
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

- `--kind jira` を渡したら、`jira` の雛形を書き、`tracker.host` を `--host`、`tracker.repo` を `--repo` で埋める。`cl:` は、下の flag が無いときと同じ手順 (origin の host で種類を決め、gh か glab で置き場を引く) で `cl.kind`・`cl.host`・`cl.repo` を埋める。`cl:` を受ける flag は無い。acli は撃たない
  - `--kind` の値は `jira` だけを受ける。`--kind jira` には `--host` と `--repo` の両方が要り、`--host` と `--repo` は `--kind jira` と一緒にだけ書ける。綴りは `tracker.host` と `tracker.repo` と同じ (§2.1)。外れたら引数の誤り (exit 2)
  - flag は `--kind jira` の形でも `--kind=jira` の形でも書ける。値が無い (flag の直後が `-` で始まるときを含む)・値が空・同じ flag を 2 回書いたときは引数の誤り (exit 2)
- flag が無ければ、`tracker.kind` を、cwd で `git remote get-url origin` が返す URL の host で決める。host が `github.com` なら `github`、それ以外は `gitlab`
  - URL は `https://<host>/…`・`ssh://<user>@<host>[:<port>]/…`・`<user>@<host>:…` (scp の綴り) を読む。host は port を除いて小文字にする
  - `github`: `tracker.repo` を、cwd で `gh repo view --json nameWithOwner` が返す repo で埋める
  - `gitlab`: cwd で `glab repo view --output json` が返す project で、`tracker.host` を `web_url` の host (port を含む) で、`tracker.repo` を `path_with_namespace` で埋める。origin の URL の host は ssh の host (alias・ssh 専用の host) でありうるので、`tracker.host` に使わず、glab にも渡さない (渡すと glab がその host の API を撃つ。URL が認証情報を含むときに argv へ載せないためでもある)
- path に file が既にあれば書かない (上書きしない)。そのことを stdout に出して exit 0
- 書いたら、path と、次に試運転 (§5) を撃つことを stdout に出す

exit: 0 = 書いた・既にある / 2 = 引数の誤り / 1 = repo を決められない (git・gh・glab の失敗、URL を読めない。`--kind jira` の `cl:` も同じ)・書けない。

**雛形**: `tracker.kind` ごとに持つ。`github` の雛形を下に置く。

- `gitlab` の雛形は、同じ trigger と action を "merge request"・`glab`・`Closes #N` の綴りで書く
  - `blocked` の述語を書かない (§2.2)
  - CL 側の trigger (conflict・review・CI) は `github` と同じ述語と絞り込みで置く
- `jira` の雛形は、`cl:` の block と、issue 側の trigger `implement` と、`cl.kind` の雛形 (`github` か `gitlab`) と同じ文面の CL 側の trigger 3 本を置く
  - `cl:` の行に「cwd の clone の origin から埋めた。CL を出す先が違えば直す」の comment を書く
  - `implement` の action と本文 (共通 prompt) が CL を開く CLI は、`cl.kind` の CLI (gh か glab) 1 つに決め打ちする
  - `status: { any: [Ready for Agent] }` に当てる。status 名は project ごとに違い、acli は project の status の一覧を返さないので並べない。雛形の comment に「project の status 名に合わせて書き換える」と書く (site に無い名前は起動時の確認が名指しする)
  - action は、issue `{{ .issue.key }}` を実装し、branch `claude-dispatcher/{{ .issue.key }}` で CL を出し、acli で status を Ready for Agent から移す旨。issue と CL を結ぶ手掛かりは branch 名だけにする (下の「雛形の action は、利用者の運用方針…を決めない」)。CL 側の trigger の `head: claude-dispatcher/*` はこの branch に当たる
  - merge の trigger は置かない。CL を merge しても Jira の issue は終端にならないので、status を動かすのは人
  - `type` の trigger は置かない (issue type は project ごとに違う)

汎用の action (実装・conflict・review・CI) を置く。action は先頭に skill を書かない文で、plugin の無い環境でも事前検査 (§2.9) に通る。承認済みの CL (`approved: true`) に当てる trigger は置かない (merge を worker に任せるかは利用者が決める)。

- 雛形の action は、利用者の運用方針 (issue の識別子を CL の題名・本文・commit に書くか、など) を決めない。dispatcher の機構が要るもの (CL 側の trigger の `head` が当たる branch 名) だけを求める

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
  args: [--permission-mode, auto, --disallowedTools, "ScheduleWakeup,CronCreate,CronList,CronDelete,Monitor"]
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

1. workflow 定義を読めて、検査 (§2.8) に通る。落ちたら誤りを 1 件 1 行の `NG` の行に出し、後ろに案内を指す行を足して (§2.8)、以降は確かめない
2. tracker の CLI (gh・glab・acli) の認証が通り、issue 置き場が見える (open な作業対象を読める)。issue 置き場は `github` なら `<owner>/<name>`、`gitlab` なら `<host>/<path>`、`jira` なら `<site>/<project key>` で出す
   - `jira` は、続けて loop の起動時と同じ `cl:` の CL 置き場の確認 (§6) を通し、`ok   CL 置き場 <置き場>` か `NG   CL 置き場 <置き場>: <理由>` の行を出す。置き場は `cl.kind` が `github` なら `<owner>/<name>`、`gitlab` なら `<host>/<path>` で出す
   - `jira` は loop の起動時と同じ issue 置き場の確認 (§6) を通す。落ちたら理由を `NG` の行に出す (存在しない status 名と issue type 名は、1 つ 1 行で trigger と一緒に名指しする)
   - 観測の補助の要求が落ちたとき (§3) は `NG` にせず、`ok` の行の後ろに ` (補助の要求が落ちた: <理由>)` を足す
3. tracker の CLI・`jira` では `cl.kind` の CLI・`claude.command` を解決できる。`on: cl` の trigger があれば git も (loop の起動時の検査と同じ。§6)
4. 事前検査 (§2.9)。落ちた trigger ごとに `NG` の行
5. 利用者の約束に頼る宣言を `警告` の行に出す (system.md §11)
   - `approved: true` の CL 側の trigger (merge を worker に任せうる)
   - `head`・`labels.all`・`labels.any`・`same_repo: true` のどれも書いていない CL 側の trigger (人の CL や fork の CL に worker を送りうる)
6. Claude Code の settings (`permissions.allow`) に要りそうな entry を、issue 置き場の役割・CL 置き場の役割・共通の順に表示する。各役割の entry は、その置き場の provider (CL 置き場は `github`・`gitlab` では `tracker.kind`、`jira` では `cl.kind`) から引く。CLI は settings を書かない (ADR 0004)
   - `github`: `Bash(gh issue:*)`・`Bash(gh pr:*)`・`Bash(git push:*)`
   - `gitlab`: `Bash(glab issue:*)`・`Bash(glab mr:*)`・`Bash(git push:*)`
   - `jira`: `Bash(acli jira workitem:*)` と、`cl.kind` の CL 置き場の entry (`Bash(gh pr:*)` か `Bash(glab mr:*)`) と、`Bash(git push:*)`

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

## 9. `guide`

```
claude-dispatcher guide
```

案内 ([`internal/guide/guide.md`](../../internal/guide/guide.md)。workflow 定義の書き方・action が守る約束・導入の流れ・loop の回し方と見方と止め方) を stdout にそのまま出す。

- workflow 定義も state dir も読まず、tracker の CLI も撃たない。`--version` (§8) と同じく、導入が壊れていても撃てる
- `tracker.kind` で出し分けない。置き場のサービスごとの違いは案内の中の表に並べる
- usage (§10) の末尾に、案内を指す 1 行を出す

exit: 0 / 2 = 引数の誤り (`guide` の後ろに引数を渡した)。

## 10. usage

```
claude-dispatcher --help
claude-dispatcher -h
claude-dispatcher help
```

使い方の一覧を stdout に出す。引数が無いときは同じ一覧だけを、引数の誤りのときは誤りの行の後ろに同じ一覧を、stderr に出す。

- 節を `Usage:` → `Commands:` → `Options:` → `Arguments:` の順に並べ、節の間を空行で区切る。主流の CLI help と同じ節の並び (見出しの綴りは tool ごとに違う) にし、一覧として流し読みできるようにする
- `Usage:` は撃ち方の形を 1 つにつき 1 行で並べる: command を取る形 (`<command> [options] [WORKFLOW]`)・`-h | --help | help`・`--version`。2 行目からは `claude-dispatcher` の桁を 1 行目に揃える
- `Commands:` は subcommand 1 つにつき 1 行で、名前と 1 行の説明を桁を揃えて並べる。行は 2 桁の字下げで始める
- `Options:` は受けるすべての flag (`-h, --help`・`--version` を含む) を 1 つにつき 1 行で並べる。subcommand に限る flag は、説明の頭に `(<subcommand>)` を書く。command の代わりにだけ書ける flag (`-h, --help`・`--version`) は、説明の頭に `(command の代わり)` を書く
- `Arguments:` は位置引数を並べる。workflow 定義の path は `WORKFLOW` と書き、省いたときの既定 (cwd の `WORKFLOW.md`) を説明に書く
- usage の placeholder は ASCII で空白を含めない (`<site>`・`<key>`・`WORKFLOW`)。引数がいくつかを一目で読めるようにする
- 最後の行は案内 (§9) を指す

exit: 0 (`--help`・`-h`・`help`) / 2 = 引数が無い・引数の誤り。
