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
- [gh](https://cli.github.com/) と、その認証 (`gh auth login`。対応する tracker は GitHub だけ)
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

## 導入

実装 repo の clone を cwd にして撃つ。流れの正本は [`docs/design/usecases.md`](docs/design/usecases.md) の UC-5、各コマンドの形式は [`docs/design/formats.md`](docs/design/formats.md) §5・§7.4。

1. **雛形を置く**: `setup` が cwd に `WORKFLOW.md` の雛形を書く。`tracker.repo` は `gh repo view` が返す repo で埋まる。既にある file は上書きしない

   ```sh
   cd ~/src/widgets
   claude-dispatcher setup
   ```

2. **自分の project に合わせて書く**: trigger の述語 (どの label・どの head branch に当てるか)・action (worker に何をさせるか)・hooks (workspace の作り方)・並列上限を直し、repo に commit する。書ける項目は [`docs/design/formats.md`](docs/design/formats.md) §2、この repo 自身の例は [`WORKFLOW.md`](WORKFLOW.md)
   - worker が作業を終えたら作業対象を trigger から外すよう、action か本文 (共通 prompt) に書く (例: CL を開いたら `ready-for-agent` を外す)。外さないと、失敗として数えられて再起動される
   - 承認済みの CL (`approved: true`) に action を当てるかは自分で決める。当てると merge を worker に任せうる
   - workspace の置き場 (`workspace.root`。既定は clone の中の `.claude-dispatcher/workspaces`) を clone の中に置くなら、`.gitignore` に足す
3. **試運転**: 何も起動せず、何も書かずに、起動するはずの作業対象と trigger を 1 件 1 行で示す

   ```sh
   claude-dispatcher loop --dry-run
   ```

4. **導入を確かめる**: `doctor` が workflow 定義・issue 置き場・依存 CLI (gh・claude・git)・事前検査を確かめ、利用者の約束に頼る宣言 (承認済みの CL への action・絞り込みの無い CL 側の trigger) を警告する。Claude Code の settings に要りそうな entry も示すので、自分で足す (CLI は settings を書かない)

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
- tick ごとに workflow 定義を読み直すので、`WORKFLOW.md` の変更は loop を撃ち直さずに効く。tick の中で事前検査に落ちた trigger だけは起動しない
- 同じ issue 置き場の loop は、同じマシンで 1 本しか起動できない。マシンを跨いだ排他は持たないので、1 つの issue 置き場は 1 台から回す
- loop は自分では起き直さない。端末を閉じたりマシンを再起動したりしたら、同じコマンドを撃ち直す。`brew upgrade` の後も撃ち直す (走っている loop は起動した時点の binary で回り続ける)

## 動いているかを見る

loop を撃った端末の画面が一番早い。`status` と同じ見出しと worker の表 (経過と stream の最新の活動を含む) の下に、直近 10 行の事象を描き直し続ける。stdout を pipe や file へ流しているときは、事象を 1 行ずつ追記する (形式は [`docs/design/formats.md`](docs/design/formats.md) §6)。

別の端末からは `status` で見る。loop が書き出す状態 file (`status.json`) を描き、何も書かず gh も撃たない。見出しに loop が生きているか (`loop 稼働中` / `loop 停止待ち` / `loop なし`) と直近の tick を出し、走っている worker・再起動待ち・打ち切り・曖昧な CL・事前検査に落ちた trigger を並べる (形式は [`docs/design/formats.md`](docs/design/formats.md) §7)。

```sh
claude-dispatcher status
```

過去の tick と worker の起動・終わり方は log.jsonl に 1 行ずつ残る。worker の stream は worker log に残る。置き場は `claude-dispatcher paths --json` で引ける。

打ち切られた作業対象は、どの trigger にも起動されない。直して再び回すなら、打ち切ったときの trigger の label を外し、1 周期待ってから付け直す。

## 止める

1. loop の端末で Ctrl+C を押す (SIGTERM・SIGHUP も同じ)。新しい起動と再起動をやめ、走っている worker が終わるのを待って止まる。worker が走っていなければすぐ止まる
2. 待てないときはもう一度 Ctrl+C を押す。走っている worker を止めて止まる。worker が途中まで書いた成果は workspace と remote branch に残る。作業対象が trigger に当たったままなら、次に起動した loop が同じ workspace で worker を起動し直す (session は新しく、attempt も 1 から数え直す。前の成果を拾わせるなら、共通 prompt にそう書く)
3. 使うのをやめるなら、`paths --json` が返す `state_dir` と `workspace_root` を消す (workspace が worktree なら、消した後に clone で `git worktree prune` を撃つ)。tracker の label は残る

## License

[MIT](LICENSE)
