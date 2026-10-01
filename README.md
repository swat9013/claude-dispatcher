# claude-dispatcher

> **作り直し中 (#74)**: [openai/symphony の SPEC](https://github.com/openai/symphony/blob/main/SPEC.md) を土台に、project が宣言した trigger で worker を起動する汎用の形へ作り直している ([ADR 0009](docs/adr/0009-rebuild-on-symphony-spec.md)、新しい設計は [`docs/design/system.md`](docs/design/system.md))。以下は作り直す前の使い方で、作り直しの間は動かないことがある。この注記と下の使い方は #83 で書き直す。

issue tracker の「着手可」の issue を Claude Code に無人で実装させ、CL (PR) まで運ぶ CLI。CL に conflict・未解決の review・CI 失敗が立てば、同じ branch へ手直しに戻す。

## 仕組み

```
claude-dispatcher loop ─周期ごと─▶ tick ─指示─▶ orchestrator (claude -p) ─決定ファイル─▶ tick ─起動─▶ worker (claude -p)
(端末で起動し Ctrl+C で止める)      観測 + 指示の導出  選定 + wip label の付与              決定どおり起動      実装 → CL
```

- 専用の台帳を持たない。状態は tracker の label (`dispatcher:wip` / `ready-for-human`) と open CL の存在に置き、tick ごとに読み直す
- 定期起動は cron ではなく、端末で撃つ `loop` が持つ。止めるのも撃ち直すのも人が行う
- CLI は tracker にも CL host にも書かない。選ぶ・見送る・人へ返すは LLM が決める
- 指示が無い tick は LLM を起動しない (静止時のコストはゼロ)
- 続行できない worker は、issue に引き渡しコメントを書き `ready-for-human` を付けて人へ返す

詳しくは [`docs/design/`](docs/design/)、用語は [`CONTEXT.md`](CONTEXT.md)、決定の理由は [`docs/adr/`](docs/adr/)。

## 前提

- macOS か Linux
- [Claude Code](https://docs.claude.com/en/docs/claude-code) と、その認証
- Claude Code plugin `swat-skills@swat9013` ([swat9013/claude-skills](https://github.com/swat9013/claude-skills))。worker が使う playbook・原則索引・レビュー skill を提供する
  ```
  /plugin marketplace add swat9013/claude-skills
  /plugin install swat-skills@swat9013
  ```
- [gh](https://cli.github.com/) と、その認証 (`gh auth login`。対応する tracker は GitHub だけ)

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

実装 repo の clone を cwd にして、project 名 (`[A-Za-z0-9._-]+`。以下 `myproj`) を決めて撃つ。流れの正本は作り直す前の `docs/design/usecases.md` にあった (作り直し後の導入は [`docs/design/usecases.md`](docs/design/usecases.md) の「project を導入する」)。各段の形式は [`docs/design/formats.md`](docs/design/formats.md) §11 / §12。

```sh
cd ~/src/widgets
claude-dispatcher setup myproj
```

`setup` は次の段を順に進め、済んだ段は何もせずに通る。途中で止まったら、示された内容を直して同じコマンドを撃ち直す。

1. **宣言 config の雛形**: `~/.config/claude-dispatcher/myproj/config.toml` が無ければ雛形を書いて止まる。issue 置き場 (`[issue].repo`)・着手可 label (`ready_label`)・並列上限 (`[limits].max_wip`) を確かめて直す。書ける項目は [`docs/design/formats.md`](docs/design/formats.md) §2
2. **config の検査**: 綴りの誤りや実在しない置き場を名指しで止める
3. **label**: issue 置き場に無い label (`dispatcher:wip` / `ready-for-human` / 着手可 label / `triage_label`) を示し、`y` と答えたら作る
4. **試運転**: `tick --dry-run` を撃ち、指示の件数を示す。何も起動せず、何も書かない
5. **loop の起動コマンド**: 試運転が通ったら、次に撃つ `loop` のコマンドを示して終わる

承認を尋ねる段は `y` / `yes` のときだけ書く。それ以外の答えと端末の無い実行では書かずに、自分で撃つコマンドを示して止まる。

最後に `doctor` で導入の充足を確かめ、Claude Code の settings に要る entry を自分で足す (CLI は settings を書かない)。

```sh
claude-dispatcher doctor myproj
```

## 回す

実装 repo の clone で `loop` を撃つ。起動直後に 1 回、以後は tick が終わるたびに指定の間隔 (`90s` / `5m` / `1h` 等。1m〜24h) を空けて tick を回す。

```sh
cd ~/src/widgets
claude-dispatcher loop myproj 5m
```

端末には今の worker の表と、loop の状態 (次の tick の時刻・直近の tick の結果) が描き直され続ける。loop は自分では起き直さないので、端末を閉じたりマシンを再起動したりしたら、同じコマンドを撃ち直す。同じ project の loop は 1 本しか起動できない。

ssh 越しの session 等で keyring の認証が読めず tick が `auth_error` になるときは、config の `[auth]` に token file を書く (formats.md §2)。loop は撃ち直さなくてよい (次の tick が config を読み直す)。

## 動いているかを見る

loop を撃った端末の画面が一番早い。`status` と同じ見出しと worker の表 (経過と stream の最新の活動を含む) の下に、直近 10 行の事象を描き直し続ける。stdout を pipe や file へ流しているときは、事象を 1 行ずつ追記する (形式は [`docs/design/formats.md`](docs/design/formats.md) §6)。

別の端末からは `status` で見る。loop が書き出す状態 file (`status.json`) を描き、何も書かず gh も撃たない。見出しに loop が生きているか (`loop 稼働中` / `loop 停止待ち` / `loop なし`) と直近の tick を出し、走っている worker・再起動待ち・打ち切り・曖昧な CL を並べる (形式は [`docs/design/formats.md`](docs/design/formats.md) §7)。

```sh
claude-dispatcher status [<workflow の path>]
```

過去の tick の結果は log.jsonl に 1 行ずつ残る。置き場は `claude-dispatcher paths --json [<workflow の path>]` で引ける。

## 止める

1. loop の端末で Ctrl+C を押す。tick の合間ならすぐ止まる。tick の実行中なら、その tick を最後まで進めて (orchestrator の判断を待ち、決まった worker を起動して) から止まる
2. 待てないときはもう一度 Ctrl+C を押す。orchestrator を止めて止まる。orchestrator が wip を付けた後だと、worker の起動されない wip が残りうる。終了行が示す orchestrator log を読み、残った wip を手で外す (起動記録が無いので `status` には出ない)
3. 走っている worker はどちらでも止まらず、最後まで進んで CL を出すか人へ返して wip を剥がす。すぐ止めたいときは `status ps myproj` で `running` の issue を確かめ、log.jsonl の `spawned` にある `pid` の process を止めてから、その issue の `dispatcher:wip` を手で外す
4. 使うのをやめるなら、`paths --json myproj` が返す `config_file` の dir と `state_dir` を消す。issue 置き場の label は残る

## License

[MIT](LICENSE)
