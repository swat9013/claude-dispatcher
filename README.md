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

詳しくは [`docs/design/`](docs/design/)、用語は [`CONTEXT.md`](CONTEXT.md)、決定の理由は [`docs/adr/`](docs/adr/)。

## 前提

- macOS か Linux
- [Claude Code](https://docs.claude.com/en/docs/claude-code) と、その認証
- tracker の CLI と、その認証。tracker が GitHub なら [gh](https://cli.github.com/) (`gh auth login`)、GitLab なら [glab](https://gitlab.com/gitlab-org/cli) (`glab auth login`)。GitLab は issue 側だけに対応し、merge request に当てる trigger はまだ書けない
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

実装 repo の clone を cwd にして撃つ。流れの正本は [`docs/design/usecases.md`](docs/design/usecases.md) の UC-5、各コマンドの形式は [`docs/design/formats.md`](docs/design/formats.md) §5・§7.4。

1. **雛形を置く**: `setup` が cwd に `WORKFLOW.md` の雛形を書く。origin の host が `github.com` なら GitHub の雛形で、`tracker.repo` は `gh repo view` が返す repo で埋まる。それ以外の host なら GitLab の雛形で、`tracker.host` と `tracker.repo` は origin を渡した `glab repo view` が返す project の web の host と path で埋まる。既にある file は上書きしない

   ```sh
   cd ~/src/widgets
   claude-dispatcher setup
   ```

2. **自分の project に合わせて書く**: trigger の述語 (どの label・どの head branch に当てるか)・action (worker に何をさせるか)・hooks (workspace の作り方)・並列上限を直し、repo に commit する。書ける項目は [`docs/design/formats.md`](docs/design/formats.md) §2、この repo 自身の例は [`WORKFLOW.md`](WORKFLOW.md)
   - worker が作業を終えたら作業対象を trigger から外すよう、action か本文 (共通 prompt) に書く (例: CL を開いたら `ready-for-agent` を外す)。外さないと、失敗として数えられて再起動される
   - 承認済みの CL (`approved: true`) に action を当てるかは自分で決める。当てると merge を worker に任せうる
   - workspace の置き場 (`workspace.root`) が clone の中なら、`.gitignore` に足す。既定の `.claude-dispatcher/workspaces` (`WORKFLOW.md` の dir からの相対) は clone の中に入る。`setup` は `.gitignore` を書かない
3. **試運転**: 何も起動せず、何も書かずに、起動するはずの作業対象と trigger を 1 件 1 行で示す

   ```sh
   claude-dispatcher loop --dry-run
   ```

4. **導入を確かめる**: `doctor` が workflow 定義・issue 置き場・依存 CLI (gh か glab・claude・git)・事前検査を確かめ、利用者の約束に頼る宣言 (承認済みの CL への action・絞り込みの無い CL 側の trigger) を警告する。Claude Code の settings に要りそうな entry も示すので、自分で足す (CLI は settings を書かない)

   ```sh
   claude-dispatcher doctor
   ```

どのコマンドも、workflow 定義の path を最後の引数で渡せる (省けば cwd の `WORKFLOW.md`)。

## 回す

実装 repo の clone で `loop` を撃つ。起動直後に 1 回、以後は `polling.interval` ごとに tick を回す。

```sh
cd ~/src/widgets
claude-dispatcher loop
```

- 起動時に workflow 定義の検査と事前検査 (action の先頭の skill が呼べるか) を通す。落ちれば誤りを名指しして起動しない
- tick ごとに workflow 定義を読み直すので、`WORKFLOW.md` の変更は loop を撃ち直さずに効く。ただし次の場合は効き方が違う
  - 読み直した定義が検査に落ちたら、その tick は何も起動しない
  - tick の中で事前検査に落ちた trigger は起動しない
  - `tracker.repo` を変えたら、loop を撃ち直すまでどの tick も何も起動しない
- 同じ issue 置き場の loop は、同じマシンで 1 本しか起動できない。マシンを跨いだ排他は持たないので、1 つの issue 置き場は 1 台から回す
- loop を起動した環境から gh の認証を取れない (ssh 越しの session など) なら、`tracker.token` に `$VAR` で token を渡して撃ち直す ([`docs/design/formats.md`](docs/design/formats.md) §2.1)
- loop は自分では起き直さない。端末を閉じたりマシンを再起動したりしたら、同じコマンドを撃ち直す。`brew upgrade` の後も撃ち直す (走っている loop は起動した時点の binary で回り続ける)

## 動いているかを見る

loop を撃った端末の画面が一番早い。`status` と同じ見出しと `workers` の表 (段階・経過・stream の最新の活動を含む)・`要対処` の下に、直近 10 行のログ (tick・起動・終了・再起動・停止・error) を描き直し続ける。色は端末のときだけ付き、`NO_COLOR` を設定すれば付かない。stdout を pipe や file へ流しているときは、ログの行と worker の活動を 1 行ずつ追記する (形式は [`docs/design/formats.md`](docs/design/formats.md) §6)。

別の端末からは `status` で見る。loop が書き出す状態 file (`status.json`) を描き、何も書かず tracker の CLI も撃たない。見出しに loop が生きているか (`loop 稼働中` / `loop 停止待ち` / `loop なし`) と直近の tick を出し、走っている worker・再起動待ち・打ち切り・曖昧な CL・事前検査に落ちた trigger を並べる (形式は [`docs/design/formats.md`](docs/design/formats.md) §7)。

```sh
claude-dispatcher status
```

過去の tick と worker の起動・終わり方は log.jsonl に 1 行ずつ残る。置き場は `claude-dispatcher paths --json` の `log` で引ける。worker の stream は `<state_dir>/workers/<作業対象>.log` (stderr は `.stderr.log`) に残る。

打ち切られた作業対象は、どの trigger にも起動されない。打ち切りは loop の memory にだけ持つので、loop を撃ち直すと解ける。loop を止めずに解くなら、作業対象を打ち切ったときの trigger から外し (label で当てる trigger なら label を外す)、1 周期待ってから戻す。

## 止める

1. loop の端末で Ctrl+C を押す (SIGTERM・SIGHUP も同じ)。新しい起動と再起動をやめ、走っている worker が終わるのを待って止まる。worker が走っていなければすぐ止まる
2. 待てないときはもう一度 Ctrl+C を押す。走っている worker を止めて止まる。worker が途中まで書いた成果は workspace と remote branch に残る。作業対象が trigger に当たったままなら、次に起動した loop が同じ workspace で worker を起動し直す (session は新しく、attempt も 1 から数え直す。前の成果を拾わせるなら、共通 prompt にそう書く)
3. 使うのをやめるなら、`paths --json` が返す `state_dir` と `workspace_root` を消す (workspace が worktree なら、消した後に clone で `git worktree prune` を撃つ)。tracker の label は残る

## 開発中の版を動かす

この repo の checkout を build して動かすなら、`scripts/claude-dispatcher-dev.sh` を使う。その checkout を build し直してから、渡した引数で実行する。Go は手元の版が [go.mod](go.mod) の `toolchain` 行より古くても、`go` コマンドがその版を取ってきて使う。

```sh
alias claude-dispatcher-dev=<claude-dispatcher の clone>/scripts/claude-dispatcher-dev.sh
cd ~/src/widgets
claude-dispatcher-dev --version        # 版に checkout の commit が出る (未 commit の変更があれば +dirty)
claude-dispatcher-dev loop --dry-run
```

- build した binary は checkout の `dist/dev/claude-dispatcher` に置く。素の `claude-dispatcher` は PATH 上の版のまま
- 状態 (state dir) は配布版と共有する。同じ issue 置き場の loop は 1 本しか動かせないので、配布版の loop を止めてから dev の loop を撃つ
- `--dry-run` を付けない loop は、本物の tracker を読んで worker を起動する

詳しい注意と、テスト・lint の撃ち方は [`CONTRIBUTING.md`](CONTRIBUTING.md) の「開発中の claude-dispatcher を試す」と「gate」。

## License

[MIT](LICENSE)
