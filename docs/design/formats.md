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
  loop.lock                           loop と単発起動の生存期間の flock の対象。同じ scope key の 2 本目を止める (§6、§11)
  alive.lock                          loop の生死を status に見せる flock の対象 (§6、§7.2)
  log.jsonl                           tick と worker の起動・終わり方の行 (§4)
  status.json                         loop の今の状態 (状態 file、§7)。loop が書き換え、status が読む
  max_concurrent                      max-concurrent が渡した並列上限 (§13)。max-concurrent が書き、loop が tick ごとに読む。loop が起動時に消す
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
- coordination board (`coordination.remote`) を指した project は、実装 repo の git remote の `refs/dispatcher/<scope dir>/` の下にも着手宣言と生存報告を置く (§12)。`<scope dir>` は state dir と同じ綴り
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
| 1 | 観測できなかった (置き場の CLI を起動できない・rate limit・読み切れない・その他の置き場の CLI の失敗) / coordination board (§12) を解けない・読めない・生存報告を置けない / `max-concurrent` (§13) を撃った scope key の loop が立っていないか、書いても効かない (§13) / 想定外の失敗 |
| 2 | 引数の誤り / workflow 定義の誤り (§2.8) / issue 置き場か `cl:` の CL 置き場が見えない (綴りの誤りか、権限が無い) / `jira` の起動時の確認 (§6) で、acli の認証の site が `tracker.host` と違うか読めない・status 名か issue type 名が実在しない |
| 3 | 同じ scope key の loop か単発起動が走っている (§6・§11) |
| 4 | 置き場の CLI (issue 置き場か CL 置き場の CLI。`github` なら gh、`gitlab` なら glab、`jira` なら acli) の認証が通らない |
| 5 | 単発起動 (§11) が worker を起動しなかった: 指名した作業対象が指名した trigger に当たっていない・終端・当たるかをまだ決められない・orphan が生きている・他の loop の着手宣言がある・着手宣言の push に負けた・coordination board に新しい版の ref がある・起動の前に停止要求を受けた・claude を起動する前に失敗した (hooks・描画・claude の起動) |
| 6 | 単発起動 (§11) が worker を起動したが完了しなかった: 終わった後も trigger に当たったまま・当たるかをまだ決められない・2 回目の停止要求で止めた・claude を止められない |

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

人が読む行 (§6 の画面) にだけ出す知らせと、log.jsonl にも残す知らせは、後から辿る必要があるかで分ける。運用の目標や上限を超えた・失敗に至らない異常のように、いつ・どの置き場で起きたかを後から読む必要があるものは、event の行として log.jsonl にも残す。その場の進み具合を知らせるだけの行 (待っていることの表示など) は画面にだけ出す。

| `event` | いつ | ほかの key |
|---|---|---|
| `loop_start` | loop を起動した (起動時の検査に通り、最初の tick の前。1 回の起動につき 1 行) | `version`・`commit` (loop を書いた binary の版と commit。値は `--version` (§8) と同じ)・`loop` (loop 名 (§12)。coordination board を指した project だけ) |
| `tick` | tick の終わり | `result` (`ok` / `error`)・`candidates` (候補の数)・`launched` (起動した作業対象の列)・`ambiguous` (曖昧な CL。`{"head": <head branch>, "targets": [<作業対象>…]}` の列)・`blocked` (事前検査 (§2.9) に落ちて起動しない trigger。`{"trigger": <名前>, "error": <理由>}` の列)・`running`・`waiting_retry`・`orphans`・`max_concurrent` (起動の判定に使った値。`result` が `ok` のとき)・`error` (`result` が `error` のとき) |
| `start` | worker を起動した | `target`・`trigger`・`attempt`・`session_id`・`workspace`・`pid` |
| `end` | worker 1 回分の終わり方を決めた | `target`・`trigger`・`attempt`・`session_id`・`outcome`・`reason`・`failure` (worker 自身が失敗したときだけ。下の `outcome` の表)・`exit_code` (process が自分で終わったときだけ。signal で止まったら載せない)・`is_error`・`num_turns`・`permission_denial_count` (attempt の最後の `result` の要約。下の箇条) |
| `retry` | `failed` の後、次の attempt を予定した | `target`・`trigger`・`attempt`・`next_attempt`・`session_id`・`backoff` (秒。小数を含む) |
| `release` | 再起動を待つ claim か、終わりを確かめ待ちの claim を、起動せずに解いた | `target`・`trigger`・`attempt`・`session_id`・`reason` (`terminal` / `left_trigger` / `trigger_removed` / `taken_over`) |
| `abandon` | attempt の上限で打ち切った | `target`・`trigger`・`attempt`・`session_id` |
| `wait_slot` | backoff が明けた再起動が、並列上限に空きが無くて待ち始めた (1 回の待ちにつき 1 行) | `target`・`trigger`・`next_attempt`・`max_concurrent` |
| `recheck_skip` | 起動しようとした候補を起動の直前に読み直し (§6 の tick の手順 7)、`reason` のどれかに当たって起動しなかった (tick ごとに、候補 1 件につき 1 行。同じ候補が次の tick でも当たれば、また 1 行書く。読み直せなかった候補は `error` の行で残し、この行は書かない) | `target`・`trigger`・`reason` (`undecided` / `terminal` / `left_trigger` / `earlier_trigger`) |
| `verify_wait` | worker が終わった後の確かめ (§6 の「worker の 1 回分」の手順 4) で、作業対象が trigger に当たるかをまだ決められず、claim を持ったまま次の tick で確かめ直す (確かめ直すたびに 1 行。失敗ではないので `error` の行は書かない。読み直せなかったときは `error` の行で残し、この行は書かない) | `target`・`trigger`・`attempt`・`session_id`・`reason` (`undecided`) |
| `unabandon` | 打ち切りを解いた (打ち切ったときの trigger から外れたのを観測した) | `target`・`trigger` |
| `orphan` | claim の無い workspace に、生きている orphan を見つけた (§6 の「orphan」。見つけた tick に 1 行。生きている間は書き直さない) | `target` |
| `orphan_end` | 前の tick まで生きていた orphan が終わったのを見た (1 行) | `target` |
| `claim_lost` | 着手宣言の push に負けて、claim を立てずに起動しなかった (§12。tick ごとに、候補 1 件につき 1 行) | `target`・`trigger` |
| `takeover` | 死んだ loop の着手宣言を乗っ取って claim を立てた (§12。続けて `start` の行) | `target`・`trigger`・`from` (死んだ loop の loop 名) |
| `inherit` | 生きている orphan (§6) の作業対象に残った死んだ loop の着手宣言を、自分の名前に引き継いだ (§12。claim は立てない。この経路では `takeover` の行を書かない) | `target`・`from` (死んだ loop の loop 名) |
| `taken_over` | 自分の着手宣言が乗っ取られていたのを見た (§12。走っている worker は止め、それ以外の claim は解く) | `target`・`trigger`・`by` (今の持ち主の loop 名。消えていたら空文字列、持ち主の commit message を読めなければ `?`) |
| `dead_loop` | 他の loop を死んだとみなし、その生存報告と、乗っ取らなかった着手宣言を消した (§12。1 つの loop につき 1 行) | `loop` (死んだ loop の loop 名)・`refs` (消した ref の列) |
| `slow_listing` | tick か loop の起動時の確認の open な一覧の読み出しが 15 秒を超えた (読み出し 1 回につき 1 行。いつ書くかは §6 の「遅い一覧」) | `kind` (`issue` / `cl`。読んだ置き場の種類)・`place` (置き場。綴りは画面の行の `<置き場>` と同じ) |
| `error` | 処理は続けるが、運用者が知るべき失敗 | `target` (あれば)・`error` |

- `target` は、issue なら `issue#<番号>`、CL なら `cl#<番号>`
- `tick` の行の起動の判定に使った値は、§6 の tick の手順 7 (候補の起動) の判定のもの
  - `running`・`waiting_retry`: その tick で候補の起動を始める前の、走っている worker と再起動待ちの claim の数 (手順 7 が並列上限から引く数)
  - `orphans`: その tick で生きていると見た orphan の数。これも手順 7 が引く。orphan を確かめられない tick は `result` が `error` で、この key を載せない
  - `max_concurrent`: その tick が採った並列上限。`max-concurrent` (§13) で渡した値があればその値、無ければ `loop` の `--max-concurrent` (§6) の値、それも無ければ 1
  - 手順 7 は、`running`・`waiting_retry`・`orphans`・その tick で起動した数 (`launched` の数) の和が `max_concurrent` に達したところで候補の起動をやめる
- `wait_slot` の行の `max_concurrent` は、空きの判定に使った値。直近に読めた tick が採った並列上限 (`tick` の行の `max_concurrent` と同じ決め方)。`wait_slot` は tick の外でも書かれ、§13 の値を読めなかった tick の後も前の値を使い続ける
- `outcome`

| 値 | 意味 |
|---|---|
| `completed` | worker が終わった後、作業対象が起動した trigger から外れていた。または終端になっていた。worker 自身が失敗していても completed とし、`failure` に失敗を載せる |
| `failed` | worker が終わった後も作業対象が trigger に当たったままで、worker が失敗した (異常終了・hook の失敗・描画の失敗・起動できない・stall・上限時間の超過) か、正常に終わった。claim は解かず、`retry` か `abandon` の行が続く |
| `stopped` | loop が止めた (作業対象が終端になった・2 回目の停止要求・着手宣言が乗っ取られた) |

- `reason` の token。画面の文は、loop が人の読む行 (§6) で token の代わりに出す文

| token | 意味 | 画面の文 | 出る行 |
|---|---|---|---|
| `terminal` | 作業対象が終端になっている | `終端` | `end`・`release`・`recheck_skip` |
| `left_trigger` | 作業対象が起動した (起動しようとした) trigger から外れている | `trigger から外れた` | `end`・`release`・`recheck_skip` |
| `still_matches` | worker が終わった後も、作業対象が trigger に当たったまま (`failed`) | `trigger に当たったまま` | `end` |
| `stop_requested` | 2 回目の停止要求で止めた (`stopped`) | `停止要求` | `end` |
| `taken_over` | 着手宣言が乗っ取られたので止めた (`stopped`)・claim を解いた (§12) | `着手宣言が乗っ取られた` | `end`・`release` |
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
- coordination board (§12) の失敗は `error` の行に残す: ref を読めない (その tick は `result` が `error`)・着手宣言の push の負け以外の失敗・commit message を読めない・自分の ref を消せない (着手宣言は次の tick で消し直す)・生存報告を置けない・死んだ loop の ref を消せない

## 5. 試運転 (`loop --dry-run`)

```
claude-dispatcher loop --dry-run [--max-concurrent <N>] [<workflow の path>]
```

`--max-concurrent` は `loop` (§6) と同じ綴りと検査で受ける。試運転は並列上限に依らずに候補を出すので、渡しても出力は変わらない。

workflow 定義を読んで検査し、事前検査 (§2.9) を通し、snapshot を作って trigger を評価し、起動するはずの作業対象を示して終わる。**何も書かない** (state dir を作らず、lock も取らない)。coordination board (§12) も読みも書きもしないので、他のマシンの loop が持つ作業対象も示しうる。

候補は、loop の起動の直前と同じ読み直し (§6 の tick の手順 7) を 1 件ずつ掛け、loop が起動しない候補は出さない (open な一覧の検索は、書き込みの直後に古い結果を返しうるため)。読み直しで起動しない理由は §6 と同じで、出さなかったことはどこにも示さない。候補が並列上限より多くても、どれも読み直す。候補を 1 件でも読み直せなければ、open な一覧を読めなかったときと同じく失敗する (下)。

成功したら、読み直した候補を 1 件 1 行で、評価の順 (§2.4) に stdout へ出す。題名は読み直した作業対象のもの。候補が無ければ何も出さない。

```
implement	issue	#42	ログインの失敗を記録する
fix-ci	cl	#51	ログインの失敗を記録する
```

- 列はタブで区切る: trigger の名前・作業対象の種類 (`issue` / `cl`)・参照・題名
- 参照は tracker の綴りで書く: issue と GitHub の CL は `#<番号>`、GitLab の CL (merge request) は `!<番号>`、Jira の issue は key (`WIDGETS-123`)。GitLab では issue と merge request が別々に番号を振るので、同じ番号が両方にありうる
- `jira` では、snapshot を作る前に issue 置き場を確かめる (§6 の「起動時の検査」の `jira` の行と同じもの)。続けて `cl:` の CL 置き場も確かめる (同じく §6)
  - snapshot は、この確認で読んだ open な一覧をそのまま使い、snapshot のために読み直さない。issue 置き場の一覧は、確認が status 名と issue type 名を確かめる前に読んだもの。その間に status が変わって候補になった issue は試運転に出ないが、この遅れは受け入れ、確認の順は変えない。出した候補は上の読み直しを通るので誤らず、遅れの間に候補になった issue は次の試運転か loop の tick で出る。一覧の検索を確認の最後にずらすと、最初に落ちた検査で終わる規則 (§6) の下で失敗の分類と exit code が変わる。CL 置き場の一覧は、`on: cl` の trigger が無くても確認で読み、`on: cl` の trigger があれば snapshot にも使う。snapshot までに、issue 置き場と CL 置き場の open な一覧はそれぞれ 1 回だけ読む
  - 候補の読み直し (上) は、作業対象を読めずに終端かを見分けるために一覧を読み足すことがある (§6 の「作業対象の読み直しで issue が消えていたら」の `jira` の手順)
- 題名の制御文字 (タブ・改行・ESC など) は空白に置き換える

観測の補助の要求が落ちたとき (§3) は失敗にせず、候補を出し、stderr に理由を出して exit 0 で終わる。

snapshot の一覧の読み出しが 15 秒を超えたら、読み終わる前に stderr に 1 行出す (§6 の「遅い一覧」)。

失敗したら、stdout に何も出さず、stderr に理由を出して §3 の exit code で終わる。workflow 定義の誤りは 1 件 1 行で出し、後ろに案内を指す行を足す (§2.8)。

## 6. `loop`

```
claude-dispatcher loop [--max-concurrent <N>] [<workflow の path>]
```

実装 repo の clone を cwd にして撃つ。

- `--max-concurrent <N>` は、loop の起動時の並列上限 (`system.md` §8)。省けば 1。`--max-concurrent 3` の形でも `--max-concurrent=3` の形でも書ける。値が無い (flag の直後が `-` で始まるときを含む)・値が空・2 回書いた・1 以上の整数でないときは引数の誤り (exit 2)
  - 走っている間は `max-concurrent` (§13) で変えられる。`max-concurrent` で渡した値は、この引数より優先する
  - 本節と §4 の並列上限は、`max-concurrent` で渡した値があればその値、無ければこの引数の値 (省けば 1)

**起動時の検査** (落ちたら stderr に理由を出して終わる):

| 検査 | exit |
|---|---|
| 引数の数と flag | 2 |
| workflow 定義を読めて、検査に通る (§2.8) | 2 |
| 事前検査 (§2.9) に通る | 2 |
| tracker の CLI (`tracker.kind` が `github` なら gh、`gitlab` なら glab、`jira` なら acli)・`jira` では `cl.kind` の CLI (gh か glab)・`claude.command` を解決できる (PATH と、よく使われる置き場)。`on: cl` の trigger があるか `coordination.remote` を書いたなら git も | 1 |
| `jira` だけ: issue 置き場の確認 (下) | 下 |
| `jira` だけ: `cl:` の CL 置き場の確認 (下)。`on: cl` の trigger が無くても撃つ | 下 |
| coordination board を指した project だけ: coordination board の確認 (§12) | 1 |
| state dir を作れる | 1 |
| 同じ scope key の loop が走っていない (`loop.lock` を取れる) | 3 |
| state dir の `max_concurrent` (§13) が無いか、消せる (前の loop が残した値を継がない。`alive.lock` を取る前に消す) | 1 |
| coordination board を指した project だけ: 最初の生存報告を置ける (§12) | 1 |

- よく使われる置き場は、workflow 定義が撃つ依存 CLI (`tracker.kind` の CLI・`cl.kind` の CLI・claude・git) のどれかが PATH に無いときだけ PATH の前に足す。使わない置き場の CLI が無いことでは PATH を書き換えない (worker と hooks は loop の PATH を継ぐので、足すと worker が撃つ git などが入れ替わりうる)
- 同じ issue 置き場の 2 本目の loop は、clone や workflow 定義の path が違っても起動時に止まる
- `jira` の issue 置き場の確認は、次の順に撃ち、最初に落ちたもので終わる。試運転 (§5) と `doctor` (§7.4) も同じ確認を通す
  1. acli の認証の site: `acli jira auth status` が通り (落ちれば認証の失敗、exit 4)、出力の `Site:` の行の site が `tracker.host` と同じ (大文字と小文字を区別しない)。違うか `Site:` の行を読めなければ exit 2。acli は呼び出しごとに site を指定できず、active な account の site を読むため
  2. open な一覧 (`project = "<project key>" AND statusCategory != Done`) を読める。落ちたら §3 の acli の分類で、認証 (exit 4) か見えない (exit 2)。試運転 (§5) は、ここで読んだ一覧を snapshot に使う
  3. trigger の `status` に書いた status 名が実在する (§2.2)。無い名前を trigger と一緒に名指しして exit 2。検索が落ちたら §3 の acli の分類で、認証なら exit 4。それ以外は名前の誤りと読み、acli の理由を添えて名指しする
  4. trigger の `type` に書いた issue type 名が project にある (§2.2)。無い名前を trigger と一緒に名指しして exit 2。project の issue type を読めなければ §3 の acli の分類で、認証 (exit 4) か見えない (exit 2)
- CL 置き場の確認は、`cl:` の CL 置き場の open な CL の一覧を 1 回読む。落ちたら §3 の gh / glab の分類で、認証 (exit 4) か見えない (exit 2)、それ以外は exit 1。観測の補助の要求だけが落ちたときは通す。試運転 (§5) と `doctor` (§7.4) も同じ確認を通す。試運転は、ここで読んだ一覧 (補助の要求の失敗を含む) を snapshot に使う (§5)。`cl:` には token が無く、gh / glab 自身の認証を使う
- loop は、起動時の確認で読んだ一覧を snapshot に使い回さず、起動直後の tick (下) で読み直す。tick は workflow 定義を読み直してから snapshot を作る (system.md §7) ので、使い回すとその順の例外になる
- lock は loop の生存期間だけ持つ。loop が死ねば外れる
- `loop.lock` を取った loop は、続けて `alive.lock` も生存期間のあいだ持つ。単発起動の lock は §11。`status` は `alive.lock` だけを確かめるので、`loop.lock` を取り合わない (`alive.lock` を `status` が一瞬持っていれば、外れるまで待つ)

**tick**: 起動直後に 1 回撃ち、以後は tick の終了から `polling.interval` だけ待って次を撃つ。tick ごとに workflow 定義を読み直す。

- 読み直した workflow 定義が検査に落ちたら、その tick は何も起動しない (#74 Q38)。周期は、最後に検査に通った版のものを使う
- 読み直した workflow 定義の、起動時に固定する項目のどれかが起動時と違えば、その tick を error で終え、何も起動しない (`tick` の行の `result` が `error`)。`error` は `workflow 定義の <項目> <今の値> が起動時の <起動時の値> と違う (loop を起動し直す)`。CL 置き場と `coordination.remote` の値は空になりうるので、Go の引用符付きの綴り (`"origin"`) で出す。項目と、起動時に固定する理由は次のとおり
  - scope key: lock は起動時の scope key で取っている
  - CL 置き場 (`jira` では `cl.kind`・`cl.host`・`cl.repo`。`github`・`gitlab` では issue 置き場と同じ場所なので、大文字と小文字以外の違いは scope key で先に止まる): claim している CL を、起動時と別の CL 置き場で読み直さない。scope key と同じく大文字と小文字を区別しない。`tracker.repo` (`github`・`gitlab`) か `cl.repo` (`jira`) の大文字と小文字だけを書き換えた版は同じ CL 置き場とみなし、tick を続ける
  - `coordination.remote` (足した・消した・名前を変えた): coordination board の remote は起動時に解いて最初の生存報告を置いている

**tick の手順** (system.md §7):

作業対象の読み直しと open な一覧は、issue なら issue 置き場から、CL なら CL 置き場から読む。open な一覧は trigger に現れる種類のものだけを読む (§2.4)。

**遅い一覧**: 1 つの置き場の open な一覧を 1 回読むのに 15 秒を超えたら、読み終わるのを待たずに次の 1 行を出す。15 秒は、1 つの置き場の一覧を 1 回読む時間の目標でもある。

```
<置き場の名前> <置き場> の一覧の読み出しが 15 秒を超えた
```

- `<置き場の名前>` は `issue 置き場` か `CL 置き場`。`<置き場>` は `doctor` の行 (§7.4) と同じ綴り
- 1 回の読み出しにつき 1 行だけ出す。読み出しは止めず、読み出しの結果も exit code も変えない (観測を失敗にしない)
- 閾値は 15 秒の固定値で、workflow 定義では変えられない
- loop は (起動時の確認でも) 人が読む行 (下の「画面」) に出す。tick の読み出しと loop の起動時の確認の読み出しでは、加えて log.jsonl (§4) に `slow_listing` の行を書く (§4 の冒頭の規則で、目標を超えた読み出しは後から辿る側に入る)。tick の読み出しの行は、その tick の画面の行と同じときに書く。起動時の確認は `loop.lock` を取って log.jsonl を開く前に読むので、その行は loop が起動できたとき (`loop_start` の行を書いたとき) だけ、`loop_start` の行の直後に書く (`ts` は書いた時刻)。起動に落ちた loop は書かない (lock を取れなかった loop の書く先は、その state dir を持つ他の loop の log.jsonl になる)。試運転 (§5)・`doctor` (§7.4)・単発起動 (§11) は stderr に出す
- 掛けるのは tick の手順 3 の snapshot と、試運転・`doctor` が置き場を読む一覧の読み出しと、起動時の確認 (上。`jira` の issue 置き場の確認の手順 2 と、`cl:` の CL 置き場の確認) が読む open な一覧
- 致命的な失敗の tick: tick の手順 3 の snapshot は、issue と CL の両方の種類の trigger があれば 2 つの一覧を並べて読む。どちらかの一覧が致命的に失敗しても、もう片方を読み終わるまで待ってから、種類の順で先の失敗を返す。そのため、もう片方の読み出しが 15 秒を超えれば、その置き場の行 (log.jsonl の `slow_listing` の行を含む) が、その tick の error の行 (画面の `tick error` の行と、log.jsonl の `result` が `error` の `tick` の行) より前に出る。error の行の中身は変わらない
- 試運転と `doctor` も、`cl:` を書かず (issue 置き場が CL 置き場を兼ねる) 両方の種類の trigger がある構成では 2 つの一覧を同じく並べて読むので、stderr のその行が失敗の行より前に出うる。起動時の確認 (上) は並べて読まない。loop の起動時と試運転は 1 つずつ読んで最初に落ちたもので終わり、`doctor` は issue 置き場の確認が落ちても `cl:` の CL 置き場の確認を続けて撃つ (§7.4)

1. 突き合わせ: 走っている worker の作業対象を読み直し、終端 (issue の close、CL の merge か close) になっていれば worker を止め、`after_run` と `before_remove` を撃って workspace を消す。trigger から外れただけでは止めない
2. 終わった worker のうち、作業対象を読み直せなかったものを読み直す
3. workflow 定義を読み直し、事前検査 (§2.9) を通して snapshot を作る。事前検査に落ちた trigger は、手順 6 で再起動せず、手順 7 の評価から外す
   - state dir の `max_concurrent` (§13) があれば読み、その tick の並列上限にする。読めない・1 以上の整数でなければ、その tick を error で終える (`tick` の行の `result` が `error`)。起動 (再起動を含む) をしない。読んだ値が前の tick の並列上限と違えば、人が読む行に `並列上限を変えた` (下の「画面」) を出す
   - coordination board を指した project では、続けて coordination board の突き合わせ (§12) を撃つ。ref を読めなければ、その tick を error で終える (`tick` の行の `result` が `error`)。掃除も起動 (再起動を含む) もしない
4. 掃除: workspace root の下の `issue-<番号>` と `cl-<番号>` のうち claim が無いものの orphan を確かめ (下の「orphan」)、orphan が生きておらず open な一覧にも無いものを読み直し、終端になっていれば `before_remove` を撃って消す。起動の直後の tick が起動時の掃除を兼ね、以後の tick が、claim を解いた後に終端になったものと消し損ねたものを拾う
5. 打ち切りを解く: 打ち切った作業対象のうち、終端になったか、打ち切ったときの trigger の述語に当たらなくなったもの (その trigger が workflow 定義から消えたものを含む) の打ち切りを解く。conflict を計算中の CL は、外れたとは数えない
   - 曖昧な CL になったことでは解かない。曖昧さは open な一覧でしか決まらず、古い一覧で曖昧に見えただけで解けてしまうため (曖昧な間はどの trigger でも起動しないので、保っても起動は遅れない)。曖昧な間も、ほかの作業対象と同じ規則で外れたかを確かめる
   - snapshot に無いか、snapshot で当たらなくなって見えるものは、置き場から 1 件読み直して確かめてから解く (open な一覧の検索は、書き込みの直後に古い結果を返しうるため)。読み直しても当たったままなら解かない。読み直せなければ error の行を残して解かず、次の tick で確かめ直す
   - snapshot で当たったままのものは読み直さない
6. 再起動: backoff の明けた再起動待ちの claim を、次の「再起動」の規則で起動する
7. trigger を評価し (§2.4)、候補のうち claim も打ち切りもされておらず orphan も生きていないものを、並列上限から走っている worker と再起動待ちの claim と生きている orphan を引いた数だけ起動する (確かめ待ちの claim と、事前検査に落ちた trigger の再起動待ちの claim は数えない)
   - 起動の直前に、候補を 1 件ずつ読み直す (open な一覧の検索は、書き込みの直後に古い結果を返しうるため)。tracker の種類を問わず読み直す。次のときはその tick では起動せず、起動しなかった分の空きを次の候補に回す。次の tick で候補になれば試み直す
     - 当たるかをまだ決められない (conflict を計算中の CL。§2.3)・終端になっている・起動しようとした trigger から外れている・宣言順で先の trigger に当たるようになっている。log.jsonl に `recheck_skip` の行 (§4) を書き、人が読む行に `読み直しで起動せず` を出す
     - 読み直せない。error の行を残す
   - CL は、claim している作業対象の workspace で checkout されている branch を head に持つもの (同じ repo の CL) なら起動しない。一覧の CL で当たれば読み直さずに外し、読み直した CL でも確かめ直す。branch は workspace を cwd にして `git symbolic-ref --short -q HEAD` で読む。workspace が無い・git の作業ツリーでない・detached HEAD なら branch は無いとする
   - branch をそれ以外の理由で読めなければ、error の行を残し、その tick は CL の候補を起動しない (同じ branch に worker を重ねないため)
   - 起動した worker には、読み直した作業対象を渡す
   - coordination board を指した project では、読み直しの後、claim を立てる直前に着手宣言を push する (§12)。負けたら `claim_lost` の行を書いて起動しない。負け以外の失敗は `error` の行を残して起動しない。どちらも、起動しなかった分の空きを次の候補に回す

**再起動** (system.md §7「失敗の扱い」):

- `failed` の後、attempt が `limits.max_attempts` に達していれば打ち切る。達していなければ、`min(10s × 2^(attempt−1), limits.max_retry_backoff)` の後に再起動を予定する
- backoff が明けたら、tick を待たずに再起動を試みる (tick の途中なら、その tick の手順 6 で試みる)。明けたときに起動できなかった claim は、下のとおり次の tick か worker が終わったときに試み直し、tick の外で試み直し続けない
  - 事前検査 (§2.9) に落ちた trigger の claim は、attempt を進めずに待ち直す (tick の手順 3)。tick の外で試みるときも事前検査は撃ち直さず、直近の tick の結果を使う。直近の tick の後に先頭の skill が消えても、次の tick が事前検査に落とすまでは再起動する
  - 走っている worker が並列上限に達していれば、attempt を進めずに待ち直す。worker が終わって空きが出たときに試み直す
  - 起動した trigger が workflow 定義から消えていれば、claim を解く (`release`)
  - 作業対象を置き場から 1 件読み直す。tick の中で試みるときも、tick の open な一覧は使わない (一覧の検索は、書き込みの直後に古い結果を返しうるため)。終端なら `before_remove` を撃って workspace を消し、終端か起動した trigger から外れていれば、claim を解く (`release`)。読み直せなければ次の tick で試み直す
  - 読み直しで確かめるのは、終端か、起動した trigger から外れたか (と、下の当たるかをまだ決められないか) だけ。宣言順で先の trigger に当たるようになったかは確かめない (再起動は同じ trigger で続ける。system.md §7)。起動の直前の読み直し (tick の手順 7) と違って `recheck_skip` の行は書かず、claim を解くときは `release` の行で残す
  - CL は、tick の中でだけ再起動を試みる (曖昧さと branch は tick が読んだ一覧で確かめるため)。tick の一覧で曖昧な CL に見えるか、head branch を別の claim の workspace が checkout しているか、その branch を読めなければ、attempt を進めずに次の tick で試み直す
    - 曖昧に見えても claim は解かない。曖昧さは一覧でしか決まらず、1 件の読み直しでは確かめられないので、古い一覧で曖昧に見えただけで解けてしまうため (解けると、次の tick で attempt 1 から数え直して起動しうる)。曖昧な間も claim は再起動待ちとして並列上限の枠を 1 つ使う。曖昧な CL は状態 file の `ambiguous` (§7.1) に出て、人が片方を close すれば解ける
  - 当たるかをまだ決められなければ (conflict を計算中の CL。§2.3)、attempt を進めずに次の tick で試み直す
  - 当たったままなら、attempt を 1 つ進め、同じ trigger・同じ session id・同じ workspace で起動する
- 再起動は、そのときの workflow 定義 (trigger・本文・hooks・`claude`・`limits`) で行う。workspace の root だけは、最初に起動したときのものを使う (session は workspace の path ごとに保存されるので)
- 前の attempt で claude を起動できていなければ (hook・描画・起動の失敗)、session はまだ無いので `--session-id` で始める
- 停止要求を受けた後に失敗した attempt は、再起動を予定しない
- coordination board (§12) を指した project では、tick の外で backoff が明けても `start` の行は次の tick まで書かない (再起動は tick の中だけ。system.md §7)
- coordination board (§12) に自分の知る版より新しい版の ref がある間は、attempt を進めずに待ち直す。着手宣言は claim を持つ間のものをそのまま使い、push し直さない
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
<時刻> 並列上限を変えた: 3 → 5
<時刻> 打ち切り issue#42 (implement, attempt 3 回): label を外して trigger から外すと解ける (外してから 1 周期待つ)
<時刻> 打ち切りを解いた issue#42 (implement)
<時刻> error issue#42: <理由>
<時刻> CL 置き場 acme/widgets の一覧の読み出しが 15 秒を超えた
<時刻> 停止待ち: 走っている worker 1 本の終了を待つ (もう一度で止める)
<時刻> 着手宣言に負けた issue#42 (implement): 他の loop が先に着手した
<時刻> 乗っ取り issue#42 (implement): 死んだ loop host-1a2b3c4d の着手宣言を乗っ取った
<時刻> 乗っ取られた issue#42 (implement): loop host-5e6f7a8b が着手宣言を持っている
<時刻> 引き継ぎ issue#46: orphan の claude が動いているので、死んだ loop host-1a2b3c4d の着手宣言を引き継いだ
<時刻> 死んだ loop host-1a2b3c4d: 生存報告と着手宣言 1 本を消した
<時刻> loop を止めた (停止要求 SIGINT)
```

- coordination board を指した project では、`loop を始めた` の行の scope の後に ` · loop <loop 名>` を足す

端末の画面 (停止待ち):

```
● loop 停止待ち  github.com/acme/widgets · v0.3.0
次の tick なし · 並列上限 1 · 直近の tick 00:05:00 ok · 候補 1
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
- claim は段階 `stopping` のまま残る (状態 file と `status` には、claude が終わるまで段階 `unstopped` で出し、pid と workspace を載せて要対処に並べる。§7)。同じ作業対象は起動せず、workspace は掃除せず、並列の枠を 1 つ使い続ける
- 停止要求の後も、claude が終わるまではその worker の終了を待たない。loop の終了までに claude が終わらなければ、`error` の行 `claude を止められないまま止まる (pid <pid>, workspace <path>)` を書く

claude が後で終われば、停止要求の後でも worker の終了を待ち、止めた worker が終わったときと同じく、`after_run` を撃ち、`result` を読み、止められて終わったなら `stopped` の `end` 行を書いて claim を解き、stall・上限時間で止めようとしたなら終わりを確かめる (上の worker の 1 回分の手順 4)。その後の loop の終了では、上の `claude を止められないまま止まる` の行を書かない。停止を決めた後、worker の終わりが届く前に claude が終わったときは、pid を書かずに `error` の行 `止められなかった claude は終わったが、after_run を待たずに止まる (workspace <path>)` を書く。

**orphan**: 前の loop が起動した claude が、その loop の止まった後も生きているもの (system.md §7)。止められないまま止まった loop と、落ちた loop が残す。

- loop は claude を起動する前に、`workers/<作業対象>.log` を開いた fd に `LOCK_EX` の flock を掛け、その fd を claude の stdout に渡す。claude (と fd を受け継いだ子孫) が生きている間、lock は外れない
- tick の手順 4 で、claim の無い workspace ごとに、その作業対象の `workers/<作業対象>.log` を開いて `LOCK_SH|LOCK_NB` を試す。取れない (`EWOULDBLOCK`) なら orphan が生きている。file が無いか、取れればすぐ外して、orphan はいない
- orphan が生きている作業対象は、その tick では workspace を掃除せず、候補にしない。その workspace の branch (手順 7 と同じ読み方) を head に持つ同じ repo の CL も起動しない。並列の枠を 1 つ数える (手順 7 の候補の起動と、再起動の空きの判定の両方)。`after_run` は撃たない
- 手順 7 で起動しようとする候補は、claim の無い workspace の一覧に無くても、起動の直前に同じ確かめをする。生きていれば起動せず、確かめられなければ `error` の行を残して起動しない (次の tick で試み直す)
- worker は `after_create`・`before_run` を撃つ前に `LOCK_EX|LOCK_NB` を取る。取れなければ hooks を撃たずに、その attempt を失敗にする (`failure` は、`EWOULDBLOCK` なら `worker log の lock を別の process が持っている (前の claude か、その fd を受け継いだ process が生きている)`、それ以外なら `worker log の lock を取れない: <理由>`)
- 見つけた tick に `orphan` の行 (§4) を書き、人が読む行に `orphan <作業対象>: 前の loop の claude がまだ動いている` を出す。前の tick まで生きていた orphan が終わっていれば、`orphan_end` の行を書き、人が読む行に `orphan <作業対象> が終わった` を出す。終わった作業対象は、同じ tick から普通の掃除と候補の扱いに戻る
- 確かめられない (`EWOULDBLOCK` 以外の失敗) か、claim の無い workspace の一覧を読めなければ、どの workspace の claude が生きているかを決められないので、その tick を error で終える (`tick` の行の `result` が `error`、`error` に理由)。掃除も起動 (再起動を含む。tick の外の再起動も、次に確かめられる tick まで試みない) もしない。打ち切りを解く手順 5 は、orphan を確かめる前に済ませる。`要対処` の orphan は前の tick で見たもののまま残す

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
  "max_concurrent": 3,
  "last_tick": {"at": "2026-10-01T00:05:00Z", "result": "ok", "candidates": 1},
  "workers": [
    {"target": "issue#42", "trigger": "implement", "attempt": 1, "session_id": "<uuid>", "phase": "running",
     "started_at": "2026-10-01T00:05:00Z", "activity": {"at": "2026-10-01T00:05:10Z", "summary": "Bash go test ./..."}}
  ],
  "abandoned": [{"target": "issue#43", "trigger": "implement"}],
  "ambiguous": [{"head": "worktree-issue-7", "targets": ["cl#52", "cl#53"]}],
  "blocked": [{"trigger": "fix-ci", "error": "action の先頭の /fix が見つからない (plugin・repo の .claude・~/.claude の skill と command)"}],
  "orphans": [{"target": "issue#45", "log": "workers/issue-45.log"}],
  "loop": "host-1a2b3c4d",
  "taken_over": [{"target": "issue#47", "by": "other-5e6f7a8b"}],
  "heartbeat_failures": {"count": 2, "error": "git push … が exit 128 で落ちた: …"},
  "newer_format": {"format": 2}
}
```

| key | 中身 |
|---|---|
| `version`・`commit` | 状態 file を書いた loop の binary の版と commit。値は `--version` (§8) と同じ。この 2 つを足す前の版が書いた状態 file には無い |
| `stopping` | 停止要求を受けて、worker の終了を待っている |
| `next_tick_at` | 次の tick の予定。停止要求の後は `null` |
| `max_concurrent` | 直近の tick が採った並列上限 (§6 の tick の手順 3)。最初の tick の前は起動時の値 (`--max-concurrent`。省けば 1)。この key を足す前の版が書いた状態 file には無い |
| `last_tick` | 直近の tick。`result` が `error` なら `error` に理由 (事前検査の失敗 — workflow 定義の誤り・scope key の食い違い・観測の失敗 — を含む)。まだ tick が無ければ `null` |
| `workers[].phase` | `running` (走っている) / `stopping` (止めている) / `unstopped` (SIGKILL を送れず claude が終わっていない。claude が終わるまで終わりを待たず、確かめも再起動も workspace の削除もしない) / `verifying` (終わり方を確かめ待ち) / `waiting_retry` (再起動待ち) |
| `workers[].pid`・`workers[].workspace` | `unstopped` の claim の claude の pid と workspace の path。ほかの段階では無い |
| `workers[].started_at` | 今の attempt の worker を起動した時刻。起動の前 (再起動待ち) は `null` |
| `workers[].retry_at` | `waiting_retry` の backoff が明ける時刻。停止要求の後に失敗した claim は再起動を予定しないので、無い |
| `workers[].activity` | 活動 (§6 の画面)。まだ無ければ `null` |
| `abandoned` | 打ち切った作業対象と、打ち切ったときの trigger |
| `ambiguous` | 直近の tick の曖昧な CL (§2.4) |
| `blocked` | 直近の tick で事前検査 (§2.9) に落ちて、起動しない trigger |
| `orphans` | 直近に確かめられた tick で生きていた orphan (§6) の作業対象 (`target`) と、lock を持たれている worker log の state dir からの相対 path (`log`)。この key を足す前の版が書いた状態 file には無い |
| `loop` | loop 名 (§12)。coordination board を指した project だけ |
| `taken_over` | この loop が着手宣言を乗っ取られた作業対象 (`target`) と、見たときの持ち主の loop 名 (`by`。消えていたら空文字列、持ち主の loop を読めなければ `?`)。loop を起動し直すまで残す。coordination board を指した project だけ |
| `heartbeat_failures` | 生存報告の置き直しが 2 回以上続けて落ちている間だけ、続けて落ちた回数 (`count`) と直近の理由 (`error`)。置き直せたら無くなる |
| `newer_format` | 直近の tick で、coordination board に自分の知る版より新しい版の ref を見た間だけ、見た版 (`format`。版を読めなければ `null`)。この間はその scope で worker を起動しない |

### 7.2 `status`

```
claude-dispatcher status [<workflow の path>]
```

別の端末から loop の今の状態を見る。workflow 定義を読んで scope key を決め、その state dir の状態 file を描く。**何も書かず、tracker の CLI も撃たない**。

```
● loop 稼働中  github.com/acme/widgets · v0.3.0
次の tick 00:10:00 · 並列上限 3 · 直近の tick 00:05:00 ok · 候補 1

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
orphan issue#45: 前の loop の claude がまだ動いている。終わるのを待つか、claude の pid (log.jsonl のこの作業対象の直近の start の行) を止める。止めても残れば、worker log (state_dir の workers/issue-45.log) を開いている process を lsof で探して止める
```

- 見出しは 2 行。1 行目は loop の状態と scope key と版: loop が生きていれば `● loop 稼働中` (停止要求の後は `● loop 停止待ち`)、`alive.lock` を取れれば `○ loop なし`。版は scope key に続けて ` · <version>` (状態 file の `version`) を出す。commit は出さない (状態 file と log.jsonl で読める)。`○ loop なし` のとき (状態 file の版は止まった loop のもの) と、状態 file に `version` が無いとき (版を足す前の版が書いた状態 file) は出さない
- 2 行目は `次の tick <時刻>` (停止要求の後は `次の tick なし`)・`並列上限 <n>` (状態 file の `max_concurrent`)・`直近の tick <時刻> ok · 候補 <n>` を ` · ` で繋ぐ。直近の tick が error なら `直近の tick <時刻> error: <理由>`。`loop なし` のときは次の tick と並列上限の欄を出さない。状態 file に `max_concurrent` が無ければ (足す前の版が書いた状態 file) 並列上限の欄を出さない。出す欄が無ければ 2 行目を出さない
- `loop なし` のときは見出しだけを出す (状態 file が残っていても、worker と打ち切りは loop の memory と一緒に消えている)。段階が `unstopped` の claim も描かない: 状態 file は止まった loop が最後に書いた記録で、その後に claude が終わったかを `status` は確かめられない (生死を確かめられるのは worker log の flock だけで、§6 の orphan)。終わった claude の pid を今のものとして描くと、古い状態を今のものと読ませる。残った claude の pid と workspace は、log.jsonl の §6 の `error` の行 (`claude を止められないまま止まる`) で読む
- 状態 file が無ければ、見出しの 2 行目は `記録なし`
- 見出しの後に空行を挟み、セクションを空行で区切って並べる。セクションの 1 行目はセクション名と件数の後に罫線を引く
- `workers <n>`: claim の表。列見出しの行 (作業対象・trigger・attempt・段階・経過・活動) の後に claim を 1 行ずつ並べる。claim が無ければ列見出しも出さない
  - 列は空白で揃える (列の間は 2 桁)
  - attempt は番号。再起動待ちの行は失敗した attempt の番号 (ログの行の `再起動を予定` は次の attempt の番号を出す)
  - 段階は `workers[].phase` (§7.1) の値のまま
  - 経過は、段階が `running`・`stopping`・`unstopped` の claim の、今の attempt の起動からの時間 (`45s`・`5m10s`・`1h02m05s`)。`waiting_retry` の claim は `<時刻> に再起動`。停止要求の後に失敗した claim は再起動を予定しないので空
  - 活動は §6 の要約
- `要対処 <n>`: 打ち切り・曖昧な CL・起動しない trigger・止められない worker (段階が `unstopped` の claim)・orphan・乗っ取られた・生存報告の失敗・新しい版の ref を 1 件 1 行で並べる。1 件も無ければセクションごと出さない
  - `乗っ取られた <作業対象>: loop <by> が着手宣言を持っている。worker を止めた。止まる前に push された成果を確かめる` (`by` が空なら `着手宣言が消えていた。…`、`?` なら `持ち主を読めない着手宣言に置き換わっていた。…`)
  - `生存報告 <count> 回続けて置けない: <error>。他のマシンの loop に死んだとみなされ、着手宣言を乗っ取られうる`
  - `新しい版の ref coordination board に版 <format> の ref がある: この scope では worker を起動しない。dispatcher を上げる` (版を読めなければ `版を読めない ref がある`)
- 時刻は端末の local time の `HH:MM:SS`
- stdout が端末でなくても、描き方は同じ人が読む形 (列は空白揃え)。機械が読むなら状態 file (§7.1) を読む

**色と幅** (loop の画面 (§6) も同じ):

- 色は stdout が端末のときだけ付ける。端末でも、環境変数 `NO_COLOR` が空でない値で設定されていれば付けない
- 色を付けるところ:
  - 見出しの `● loop 稼働中` は緑、`● loop 停止待ち` は黄、`○ loop なし` は灰。scope key は太字。直近の tick の `ok` は緑、`error: <理由>` は赤。欄の名前 (`次の tick` など) は灰
  - セクション名は太字 (`要対処` は黄)、罫線は薄く、列見出しは灰
  - 段階は `running` が水色、`waiting_retry` が黄、`unstopped` が赤、`verifying` が灰
  - `要対処` の行の頭の語は、`打ち切り`・`起動しない trigger`・`止められない`・`新しい版の ref` が赤、`曖昧な CL`・`orphan`・`乗っ取られた`・`生存報告` が黄
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
   - `jira` の issue 置き場と `cl:` の CL 置き場は、確認 (§6) で読んだ open な一覧を使い回し、読み直さない。issue 置き場と CL 置き場の open な一覧は、それぞれ 1 回だけ読む
   - 一覧の読み出しが 15 秒を超えたら、読み終わる前に stderr に 1 行出す (§6 の「遅い一覧」)。stdout の行と exit は変えない
3. tracker の CLI・`jira` では `cl.kind` の CLI・`claude.command` を解決できる。`on: cl` の trigger があるか `coordination.remote` を書いたなら git も (loop の起動時の検査と同じ。§6)
4. 事前検査 (§2.9)。落ちた trigger ごとに `NG` の行
   - coordination board を指した project では、続けて loop の起動時と同じ coordination board の確認 (§12) を通し、`ok   coordination board の remote <名前> (<URL>)` か、§12 の起動時の確認の文を載せた `NG   coordination board の remote <名前> を解けない: <理由>` / `NG   coordination board の remote <名前> を読めない: <理由>` の行を出す。push は撃たない
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
- `Usage:` は撃ち方の形を 1 つにつき 1 行で並べる: command を取る形 (`<command> [options] [WORKFLOW]`)・単発起動 (`run TRIGGER TARGET [WORKFLOW]`)・並列上限の変更 (`max-concurrent N [WORKFLOW]`)・`-h | --help | help`・`--version`。2 行目からは `claude-dispatcher` の桁を 1 行目に揃える
- `Commands:` は subcommand 1 つにつき 1 行で、名前と 1 行の説明を桁を揃えて並べる。行は 2 桁の字下げで始める
- `Options:` は受けるすべての flag (`-h, --help`・`--version` を含む) を 1 つにつき 1 行で並べる。subcommand に限る flag は、説明の頭に `(<subcommand>)` を書く。command の代わりにだけ書ける flag (`-h, --help`・`--version`) は、説明の頭に `(command の代わり)` を書く
- `Arguments:` は位置引数を並べる。workflow 定義の path は `WORKFLOW` と書き、省いたときの既定 (cwd の `WORKFLOW.md`) を説明に書く。`run` (§11) の位置引数 `TRIGGER`・`TARGET` も並べ、説明の頭に `(run)` を書く。`max-concurrent` (§13) の位置引数 `N` も並べ、説明の頭に `(max-concurrent)` を書く
- usage の placeholder は ASCII で空白を含めない (`<site>`・`<key>`・`WORKFLOW`)。引数がいくつかを一目で読めるようにする
- 最後の行は案内 (§9) を指す

exit: 0 (`--help`・`-h`・`help`) / 2 = 引数が無い・引数の誤り。

## 11. 単発起動 (`run`)

```
claude-dispatcher run <trigger> <作業対象> [<workflow の path>]
```

実装 repo の clone を cwd にして撃つ。指名した 1 つの trigger と 1 つの作業対象で worker を 1 回だけ起動し、終わりを待って完了したかを exit code で返す (system.md §7 の「単発起動」)。

- `<trigger>` は workflow 定義の trigger の名前。`<作業対象>` は log.jsonl (§4) の `target` と同じ綴り (`issue#42` / `cl#57`)。番号は 1 以上の 10 進数
- flag は受けない。位置引数が 2 つに足りない・4 つ以上は引数の誤り (exit 2)

**起動前の検査** (落ちたら stderr に理由を出して終わる): 作業対象の綴りを引数として先に読み、§6 の起動時の検査のうち lock の前までを同じ順・同じ exit code で通し、続けて次を確かめる。

| 検査 | exit |
|---|---|
| 作業対象の綴りが `issue#<番号>` / `cl#<番号>` の形 (引数として、workflow 定義を読む前に確かめる) | 2 |
| `<trigger>` の名前の trigger が workflow 定義にある | 2 |
| 作業対象の種類が、その trigger の `on` と同じ | 2 |
| 同じ scope key の loop か単発起動が走っていない (`loop.lock` を取れる) | 3 |
| 作業対象の worker log の orphan が生きていない (§6 の「orphan」。確かめられなければ exit 1) | 5 |
| 作業対象を置き場から読める | §3 |
| 読んだ作業対象が終端でなく、その trigger に当たっている (当たるかをまだ決められないときを含めず) | 5 |
| coordination board を指した project だけ: 最初の生存報告を置ける (§12) | 1 |
| coordination board を指した project だけ: ref を読めて、自分の知る版より新しい版の ref が無い | 1 / 5 (新しい版) |
| coordination board を指した project だけ: 作業対象に他の loop の着手宣言が無く、着手宣言の push に勝つ (負け以外の失敗は exit 1) | 5 |

- 事前検査は §6 と同じく、指名していない trigger が落ちても失敗させる
- 当たっているかは指名した trigger の述語だけで決める。宣言順で先の trigger に当たっていても起動する (loop の読み直しの `earlier_trigger` は当てない)
- coordination board を指した project では、loop と同じく、単発起動の終わりまで生存報告を置き直し (§12)、終わるときに着手宣言と生存報告を消す (起動しなかったときも、置いた分を消す)。止められない claude を残して終わるときは、どちらも消さない (§12 の後片付け)。乗っ取られたかは確かめない。ref を 1 回しか読まず、生存報告が新しくならない経過を数えられないので、死んだ loop を判定しない (他の loop の着手宣言があれば、持ち主が落ちていても起動しない。乗っ取りと死んだ loop の ref の掃除は loop に任せる)
- lock は §6 と同じ `loop.lock` だけを、単発起動の終わりまで持つ。`alive.lock` は取らないので、単発起動の間の `status` は `○ loop なし` と描く (止まった loop が残した状態 file を今の状態として描かない)。同じ scope key の loop・単発起動との排他は `loop.lock` が担う

**worker の 1 回分**: §6 の「worker の 1 回分」と同じ (workspace の用意と hooks・描画・`claude -p` の起動・`limits.stall_timeout`・`limits.run_timeout`・`after_run`)。attempt は 1 で、session id は新しく発行する (`--session-id`)。retry も打ち切りもしない。

- 終わった後に作業対象を 1 回読み直し、指名した trigger から外れていれば (終端を含む) 完了とする。worker 自身が失敗していても、外れていれば完了 (§4 の `completed` と同じ)
- 読み直せなければ exit 1。当たったままか、当たるかをまだ決められなければ exit 6
- claude を起動する前に失敗したら (hooks・描画・claude の起動)、読み直さずに exit 5
- workspace は loop と同じく、読み直した作業対象が終端なら `before_remove` を撃って消す。worker log の flock を claude の子孫がまだ持っていれば (§6 の「orphan」) 消さない。それ以外は残し、後の単発起動や loop が同じ workspace を使う。消せなければ stderr に出し、exit code は変えない

**停止**: SIGINT / SIGTERM / SIGHUP を同じに扱い、lock を取った後から受ける。worker を起動する前 (claude を起動する前の hooks の間を含む) に受けたら起動せずに exit 5。claude を起動した後は、1 回目は worker の終わりを待つ。2 回目は worker の process group を止め (止め方は §6 と同じ)、worker が終われば (止める前に自分で終わったときも) 読み直さずに exit 6 で終わる。SIGKILL を送れず claude が終わらなければ (stall・上限時間で止めようとしたときを含む)、停止要求を 1 回受けた時点で claude を残したまま exit 6 で終わる (loop と同じく、止められない claude の終わりは待たない)。残った claude は orphan になる。

**出力**: stdout に人の読む行を 1 件ずつ書く。log.jsonl と状態 file (§7.1) には書かない。worker log (§1) は loop と同じ path に追記する。

```
起動: issue#42 (implement, session 6f1c…, workspace /…/issue-42, pid 4242)
終了: issue#42 完了 (trigger から外れた)
```

- 終了の行は `完了 (<reason の画面の文>)` か `未完了 (<reason の画面の文>)` (§4 の `reason`。停止要求で止めたら `停止要求`、止められない claude を残したら `claude を止められない`)。claude を起動する前に失敗したら `起動できなかった`。worker 自身の失敗・`after_run` の失敗・`result` の要約の失敗は、stderr に 1 件 1 行で出す
- 着手宣言の push に負けたら stderr に `<作業対象> は他の loop が先に着手したので起動しない` を、他の生きている loop の着手宣言があれば `<作業対象> は loop <loop 名> が着手しているので起動しない` を、新しい版の ref があれば `coordination board に新しい版の ref があるので起動しない (dispatcher を上げる)` を出す
- 停止要求を受けたら、stderr に `停止要求 <signal>: worker の終了を待つ` (1 回目) / `worker を止める` (2 回目と、claude を起動する前の hooks の間) / `止められない claude を残して終える` を出す。`<signal>` は `SIGINT` の綴り

exit: 0 = 完了 / 1・2・3・4 = 起動前の検査・読み直し (§3) / 5 = 起動しなかった / 6 = 起動したが完了しなかった。

## 12. coordination board の ref

workflow 定義に `coordination.remote` を書いた project で、loop と単発起動が置く着手宣言と生存報告の形 (system.md §4 の「マシンを跨いだ分担」、ADR 0011・0017・0023)。

```
refs/dispatcher/<scope dir>/claims/<作業対象>    着手宣言。作業対象ごとに 1 本
refs/dispatcher/<scope dir>/loops/<loop 名>      生存報告。loop (と単発起動) ごとに 1 本
```

- `<scope dir>` は state dir の名前と同じ (§1)。`<作業対象>` は `issue-<番号>` か `cl-<番号>` (§1)
- **loop 名**: `<hostname を無害化したもの>-<乱数 8 hex>`。無害化は `<scope dir>` と同じ規則 (§1)。起動ごとに新しくする。単発起動も 1 回ごとに loop 名を持つ
- どちらの ref も、空の tree の commit を指す。作者と committer は `claude-dispatcher <claude-dispatcher@invalid>` で、利用者の git の設定に頼らない
- commit message は、1 行目が種類 (`claude-dispatcher claim` / `claude-dispatcher heartbeat`)、空行の後に `<key>: <値>` の行を並べる

```
claude-dispatcher claim

format: 1
loop: host-1a2b3c4d
target: issue#42
```

```
claude-dispatcher heartbeat

format: 1
loop: host-1a2b3c4d
interval: 60s
seq: 17
```

| key | 中身 |
|---|---|
| `format` | 形式の版。今の版は `1`。版を足すときは、新しい版の loop が古い版の ref も読めるようにする |
| `loop` | 持ち主の loop 名 |
| `target` | 着手宣言の作業対象 (log.jsonl (§4) の `target` の綴り) |
| `interval` | 生存報告の間隔 (Go の duration の綴り)。他の loop はこの値の 3 倍で死を判定する |
| `seq` | 生存報告を置き直すたびに 1 つ進める数。置き直すたびに commit の sha を変える |

- 知らない key は読み飛ばす。`format` が無いか、数として読めないか、自分の知る版より大きい ref は、新しい版の ref として扱う

**git の撃ち方**: どれも workflow 定義のある dir を cwd にして撃つ。push はすべて `--no-verify --porcelain` を付ける (ADR 0017)。

| 操作 | コマンド |
|---|---|
| remote を解く | `git remote get-url <coordination.remote>` |
| ref を読む | `git ls-remote <remote> refs/dispatcher/<scope dir>/*` |
| commit message を読む | 新しく見た sha だけ、`git fetch --no-tags --no-write-fetch-head <remote> <sha>…` の後に `git cat-file commit <sha>`。clone の ref には書かない |
| commit を作る | `git mktree` (stdin は空) の tree に `git commit-tree <tree> -m <message>` |
| 着手宣言を作る | `git push --no-verify --porcelain --force-with-lease=<ref>: <remote> <sha>:<ref>` (無いときだけ作る) |
| 着手宣言を乗っ取る | `git push --no-verify --porcelain --force-with-lease=<ref>:<旧 sha> <remote> <sha>:<ref>` |
| 生存報告を置く | `git push --no-verify --porcelain <remote> +<sha>:<ref>` (条件を付けない。書くのは持ち主の loop だけ) |
| ref を消す | `git push --no-verify --porcelain --force-with-lease=<ref>:<期待する sha> <remote> :<ref>` |

- `<remote>` は `coordination.remote` の名前のまま渡す (URL と認証はその clone の設定を git が使う)
- **負け**: push の exit code が非 0 で、porcelain の行 (タブ区切り) のうち `!` で始まる行が `[remote rejected]` か `[rejected]` を含むとき。理由の文言 (`cannot lock ref ...`・`stale info` など) は見ない。それ以外の非 0 は、負け以外の失敗 (git の stderr を理由に載せる)

**起動時の coordination board の確認** (loop の起動時 (§6)・単発起動 (§11)・`doctor` (§7.4)):

1. `git remote get-url --all <coordination.remote>` と `git remote get-url --push --all <coordination.remote>` が通り、どちらも 1 本だけの同じ URL を返す。落ちたら `coordination board の remote <名前> を解けない: <理由>`、違うか複数あれば `coordination board の remote <名前> の読む URL と書く URL が違う: <URL の列> と <URL の列>` (exit 1)
2. `git ls-remote <remote> refs/dispatcher/<scope dir>/*` が通る。落ちたら `coordination board の remote <名前> を読めない: <理由>` (exit 1)

loop と単発起動は、続けて lock を取った後に最初の生存報告を置く。落ちたら `coordination board の remote <名前> に生存報告を置けない: <理由>` (exit 1)。

**coordination board の突き合わせ** (§6 の tick の手順 3 の後。system.md §7):

1. ref を読み、新しく見た sha の commit message を読む。ref を読めなければ tick を error で終える。commit message を読めない sha は `error` の行を残し、その tick は持ち主を知らないものとして扱う (その作業対象は起動しない・持ち主を死んだとみなさない)
2. 自分が置いた着手宣言が、sha が違うか消えていれば `taken_over` の行を書き、その claim に「乗っ取られた」を渡す (走っている worker は止め、ほかの claim は `release` の行で解く。`reason` は `taken_over`)。要対処に出す
3. 自分の loop 名の着手宣言のうち、claim の無い作業対象のものを消す (8 で引き継いだ着手宣言は除く。その sha が変わったか消えていれば、引き継いだ記憶を忘れる。置き換わっていたら `error` の行 (`orphan のために引き継いだ着手宣言が置き換わった (orphan の claude と二重に動きうる)`) を書く)
4. 自分の知る版より新しい版の ref があれば、その tick は候補を起動せず、再起動を待ち直させ、5 を撃たない。要対処に出す
5. 生存報告ごとに、sha が変わったのを最後に見た時刻 (初めて見た時刻を含む) から、その報告の `interval` の 3 倍が過ぎていれば、その loop を死んだとみなす。持ち主の生存報告が無い着手宣言は、着手宣言の sha を初めて見た時刻から、自分の生存報告の間隔の 3 倍が過ぎていれば、持ち主を死んだとみなす。時刻は loop 自身の時計で数える
6. 候補の起動 (§6 の手順 7) で、作業対象に生きている他の loop の着手宣言があれば起動しない。死んだ loop の着手宣言があれば、旧 sha を期待値にして乗っ取る (勝てば `takeover` の行)
7. 候補の起動の後、死んだ loop の ref を消し (消す ref は system.md §7)、`dead_loop` の行を書く。消すときの負けは行を書かずに読み飛ばす (他の loop が先に消したか乗っ取った)
8. 7 で、生きている orphan (§6 の「orphan」) の作業対象の死んだ loop の着手宣言は消さず、旧 sha を期待値にした置き換えで自分の loop 名に引き継ぎ、`inherit` の行を書く (ADR 0027)。持ち主の loop 名の hostname の部分 (最後の `-` より前) が自分と同じなら、5 の判定を待たずに死んだとみなして引き継ぐ。負けは行を書かずに読み飛ばす。引き継いだ着手宣言は、その orphan が終わった tick に、§6 の tick の手順 4 の orphan の確かめの後、候補の起動の前に消す。消せなければ `error` の行を残し、消し直すまでその作業対象を起動しない。引き継ぎの push が結果の分からない失敗をしたら、その作業対象を覚えておく。push より前に落ちた (着手宣言の commit を作れない) 失敗は remote に届いていないので覚えない (`error` の行は同じ)。remote に届いていたら、次の tick の 3 で消さずに引き継いだものとして扱う。次の tick でも届いたかを決められない (着手宣言の commit message を読めない) 間は覚えたままにする。届いたかを決める前に loop が止まるときは、覚えておいた作業対象を引き継いだものと同じに扱い、orphan がまだ生きていれば生存報告を消さず、`error` の行 (`orphan のために引き継いだかもしれない着手宣言を残して止まる (引き継ぎの結果が分からず、orphan の claude が作業を続けうる)`) を書く。止まる時点で orphan が終わっていれば、覚えておいた作業対象は消さずに (sha を知らない) 生存報告を消す。このとき着手宣言が remote に届いていると、自分の名前で生存報告の無い着手宣言が残り、他の loop は死を判定するまでその作業対象を起動しない。今の tick の順では、着手宣言の commit message を読める限りこの組み合わせに届かない: 覚えるのは orphan が生きている tick だけで、orphan の生死は次の tick の 3 で覚えたものを決着させた後 (§6 の tick の手順 4) に確かめ直し、突き合わせが落ちた tick は orphan を確かめずに直前の生死を残すため。commit message を読めないまま orphan が終わってから止まるときは届く。この組み合わせで着手宣言が残ることは受け入れる: orphan は終わっているので、着手宣言を残して守る相手の claude がいない。残った着手宣言は、他の loop が 5 で持ち主を死んだとみなすまで、その作業対象の起動を遅らせるだけで、二重実行にはならない。§6 の tick の手順の順 (突き合わせと orphan の確かめの前後) を変えるときは、commit message を読める限りこの組み合わせに届かないかを見直す。loop が止まるときに直近の tick で orphan がまだ生きていれば、止められない claude を残して止まるときと同じく、着手宣言も生存報告も消さず、作業対象ごとに `error` の行 (`orphan のために引き継いだ着手宣言を残して止まる (orphan の claude が作業を続けうる)`。直近の tick で orphan が終わっていて消せなかったものは括弧の中が `orphan は終わったが消せなかった` で、生存報告はほかに残す理由が無ければ消す) を書く

**生存報告の置き直し** (いつ置き直すかは system.md §9): 落ちるたびに `error` の行 (`生存報告を置けない: <理由>`) を書く。2 回以上続けて落ちたら、状態 file の `heartbeat_failures` (§7.1) に出し、要対処に並べる。置き直しの結果を loop が受け取る前に溜まりすぎて捨てたら、次に結果を受けたとき (か loop が止まるとき) に `error` の行 (`生存報告の置き直しの結果 <N> 件を読まずに捨てた`) を書く。

**後片付け** (何をいつ消し、何を残すかは system.md §4・§7・§9): 着手宣言か生存報告を消せなければ `error` の行を残す。

## 13. 並列上限の変更 (`max-concurrent`)

```
claude-dispatcher max-concurrent <N> [<workflow の path>]
```

走っている loop の並列上限を、loop を止めずに変える (system.md §8)。workflow 定義を読んで scope key を決め、その state dir の `max_concurrent` (§1) に `<N>` を書く。tracker の CLI は撃たない。

- `<N>` は 1 以上の整数。`<N>` が無い・1 以上の整数でない・位置引数が 3 つ以上のときは引数の誤り (exit 2)。flag は受けない
- 書く前に、`status` (§7.2) と同じく `alive.lock` (§6) で loop が生きているかを確かめる。`alive.lock` を取れれば (loop が立っていない)、書かずに stderr に `loop が立っていない: <scope key>` を出して exit 1。書いても、次に起動する loop は起動時にこの file を消す (§6 の起動時の検査) ので効かない
- loop が生きていても、次のときは書かずに exit 1 にする。書いても次の tick で読まれず、効かない値に成功を返すことになる
  - 状態 file (§7.1) が無い (loop が起動した直後で、まだ書いていない): stderr に `loop の状態 file がまだ無い: <scope key> (起動した直後なら少し待って撃ち直す)`
  - 状態 file に `max_concurrent` が無い (この file を読まない版の loop。走っている loop は起動した時点の binary で回り続ける): stderr に `この loop は max-concurrent を読まない: <scope key> (この版の binary で loop を起動し直す)`。前の loop の状態 file が残っている間は、起動した直後の loop の版を見分けられない
  - 状態 file の `stopping` が true (停止待ちの loop は tick を回さない): stderr に `loop は停止待ち: <scope key>`
- 起動した loop は、`max-concurrent` から生きて見える (`alive.lock` を持つ) より前に、前の loop の `max_concurrent` を消す (§6 の起動時の検査)。生きて見えた後に消すと、その間に撃った `max-concurrent` に成功を返した値を消してしまう
- `max_concurrent` は 10 進の整数と改行の 1 行。呼び出しごとに名前の違う一時 file を同じ dir に作って書いてから rename する (loop に書きかけを見せず、同時に撃った呼び出しが一時 file を取り合わない)。書けなければ一時 file を消す
- 書けたら stdout に `並列上限を <N> にした: 次の tick から効く (<scope key>)` を出す
- loop は次の tick の手順 3 (§6) でこの file を読み、loop が止まるまで `--max-concurrent` より優先する。走っている worker の数より下げても、走っている worker は止めない (空きが出るまで新しい起動と再起動を待つ)
- 同じ値を続けて撃っても終状態は変わらない。元の値 (`--max-concurrent` か 1) へ戻す操作は持たないので、戻すなら戻したい値を撃つ

exit: 0 = 書けた / 2 = 引数・workflow 定義の誤り / 1 = loop が立っていない・状態 file がまだ無い・loop が `max_concurrent` を読まない版・loop が停止待ち・`alive.lock` か状態 file を読めない・file を書けない、または workflow 定義の path を解決できない。
