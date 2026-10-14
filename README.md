# claude-dispatcher

issue tracker の issue と CL (pull request) を周期ごとに読み、project が宣言した trigger に当たったものへ Claude Code の worker を無人で起動する CLI。「`ready-for-agent` の issue を実装して CL を出す」「CL に conflict・未解決の review・CI の失敗が立ったら手直しする」といった流れを、repo に置く workflow 定義 (`WORKFLOW.md`) で決める。

## 仕組み

```
 人 ── WORKFLOW.md を書く / label を付ける (triage) / merge
                    │
 claude-dispatcher loop ─周期ごと─▶ issue と CL を読む ─▶ trigger を評価 ─▶ 当たったものへ worker を起動
 (端末で起動し Ctrl+C で止める)      (読むだけ)                                 workspace (worktree) で claude -p
                                                                                 action の文面どおりに作業し、
                                                                                 作業対象を trigger から外して終える
```

- **状態は tracker と CL host に置く**。CLI は tracker にも CL host にも書かない。label を付け替える・CL を開く・人へ返すのは worker (action の文面) と人
- **trigger** は、作業対象 (issue か CL) の述語と action (worker に渡す prompt) の組。宣言順に評価し、最初に当たった 1 つで起動する
- **完了は「worker が終わった後に、作業対象が trigger から外れていること」**。外れないまま終わった worker は失敗として数え、backoff して同じ session を続けさせる。上限の回数で打ち切る
- **定期起動は cron ではなく、端末で撃つ `loop` が持つ**。止めるのも撃ち直すのも人が行う

## 前提

- macOS か Linux
- [Claude Code](https://docs.claude.com/en/docs/claude-code) と、その認証
- tracker の CLI と、その認証。tracker が GitHub なら [gh](https://cli.github.com/) (`gh auth login` か `tracker.token`)、GitLab なら [glab](https://gitlab.com/gitlab-org/cli) (`glab auth login` か `tracker.token`)、Jira Cloud なら [acli](https://developer.atlassian.com/cloud/acli/) (`acli jira auth login`)。GitLab は issue 置き場と merge request の置き場が同じ project の構成を扱う。対象の GitLab は、GitLab が security 修正を出している版 ([maintenance policy](https://docs.gitlab.com/policy/maintenance/) の、最新の minor から 3 つ)
- Jira は Jira Cloud の 1 つの project を issue 置き場にする (Data Center / Server は扱わない)。CL 置き場は workflow 定義の `cl:` で GitHub か GitLab を宣言し、その CLI (gh か glab) とその認証も用意する。loop は `cl:` の CL 置き場の CL に CL 側の trigger を当て、worker もその CLI で CL を開く
  - 1 つの Jira project は 1 本の loop と 1 つの実装 repo で回す。component や JQL で 1 つの project を複数の repo に分けて回すことはできない (同じ project の 2 本目の loop は起動時に止まる)
  - acli の active な account の site を、workflow 定義の `tracker.host` にしておく。acli は呼び出しごとに site を選べないので、loop は起動時に site が違えば止まる (`acli jira auth switch --site <site>` で切り替える)
  - acli は macOS では資格情報を keychain に置く。ssh 越しの shell では keychain が unlock されておらず、acli が認証を読めないことがある。そのときは `security unlock-keychain` を撃ってから loop を起動するか、API token で `acli jira auth login` をし直す
- git (workspace を worktree で作る hooks と、CL 側の trigger で使う)
- action の先頭に書く skill と command (`/swat-skills:playbook-implementation` など) を、worker が呼べる場所 (plugin・repo の `.claude/`・`~/.claude/`) に入れておく。特定の plugin には依存しない。呼べない名前は起動の前の事前検査で名指しされる

## install

Homebrew・`go install`・GitHub Releases のどれかで `claude-dispatcher` を PATH の通った場所に置く。

**Homebrew** (macOS / Linux): tap `swat9013/tap` の cask で入れる。tap 名付きの完全修飾名 (`swat9013/tap/claude-dispatcher`) で撃つと、Homebrew の tap trust (第三者 tap の中身を読む前に求める明示の信頼) がこの cask にだけ与えられる。

```sh
brew install --cask swat9013/tap/claude-dispatcher
brew upgrade --cask swat9013/tap/claude-dispatcher   # 新しい版が出たとき
```

binary は署名していないので、macOS では cask が install 時に quarantine (Gatekeeper の検査対象にする拡張属性) を外す。走っている `loop` は起動した時点の binary で回り続けるので、`brew upgrade` の後は撃ち直す。

**`go install`** (Go は [go.mod](go.mod) の `go` 行の版以上):

```sh
go install github.com/swat9013/claude-dispatcher/cmd/claude-dispatcher@latest
```

**GitHub Releases の binary**: [Releases](https://github.com/swat9013/claude-dispatcher/releases) の OS と CPU に合う `claude-dispatcher_<version>_<darwin|linux>_<amd64|arm64>.tar.gz` を `checksums.txt` と一緒に取り、確かめてから展開する。下は macOS (Apple silicon) の例で、置き場の `~/.local/bin` は PATH に入れておく。

```sh
gh release download -R swat9013/claude-dispatcher -p '*_darwin_arm64.tar.gz' -p checksums.txt
shasum -a 256 -c checksums.txt --ignore-missing   # Linux は sha256sum -c checksums.txt --ignore-missing
tar xzf claude-dispatcher_*_darwin_arm64.tar.gz claude-dispatcher
mkdir -p ~/.local/bin && install -m 755 claude-dispatcher ~/.local/bin/
```

binary は署名していない。macOS でブラウザから取った archive は Gatekeeper に起動を止められるので、`gh release download` か `curl -LO` で取る (ブラウザで取ったなら `xattr -d com.apple.quarantine ~/.local/bin/claude-dispatcher`)。

入れた版は `claude-dispatcher --version` で確かめる。不具合を報告するときは、この出力を添える。

## 導入

実装 repo の clone を cwd にして、次の順に撃つ。各手順で何が起き、何を確かめるかは `claude-dispatcher guide` の「導入の流れ」。

1. **雛形を置く**: `setup` が cwd に `WORKFLOW.md` の雛形を書く

   ```sh
   cd ~/src/widgets
   claude-dispatcher setup
   claude-dispatcher setup --kind jira --host acme.atlassian.net --repo WIDGETS   # Jira の issue 置き場
   ```

2. **自分の project に合わせて書く**: trigger・action・hooks・並列上限を直し、repo に commit する。書き方は `claude-dispatcher guide` の「workflow 定義の書き方」と「action が守る約束」(利用者のセッションに読ませてもよい)、この repo 自身の例は [`WORKFLOW.md`](WORKFLOW.md)
3. **試運転**: 何も起動せず、起動するはずの作業対象と trigger を示す

   ```sh
   claude-dispatcher loop --dry-run
   ```

4. **導入を確かめる**: workflow 定義・issue 置き場・依存 CLI・事前検査を確かめ、settings に要りそうな entry を示す

   ```sh
   claude-dispatcher doctor
   ```

## 回す

実装 repo の clone で `loop` を撃つ。起動時の検査、`WORKFLOW.md` の変更の効き方、撃ち直しが要るときは `claude-dispatcher guide` の「回す・見る・止める」の「回す」。issue 置き場か CL 置き場を変えたときは同じ案内の「workflow 定義の書き方」、loop を起動した環境から gh や glab の認証を取れない (ssh 越しの session など) ときは「項目」の `tracker.token`。

```sh
cd ~/src/widgets
claude-dispatcher loop
```

## 動いているかを見る

loop を撃った端末の画面か、別の端末の `status` で見る。log と worker の stream の置き場は `paths --json` で引く。画面と `status` の読み方、打ち切られた作業対象の解き方は `claude-dispatcher guide` の「回す・見る・止める」の「見る」。

```sh
claude-dispatcher status
claude-dispatcher paths --json
```

## 止める

loop の端末で Ctrl+C を押す。待てないときはもう一度押す。それぞれで worker と成果がどうなるか、使うのをやめるときに消すものは `claude-dispatcher guide` の「回す・見る・止める」の「止める」。

## 開発に加わる

開発中の版の動かし方・gate・commit と PR の規約は [`CONTRIBUTING.md`](CONTRIBUTING.md)。

## License

[MIT](LICENSE)
