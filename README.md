# claude-dispatcher

issue tracker の「着手可」の issue を Claude Code に無人で実装させ、CL (PR) まで運ぶ CLI。CL に conflict・未解決の review・CI 失敗が立てば、同じ branch へ手直しに戻す。

## 仕組み

```
cron ─▶ claude-dispatcher tick ─指示─▶ orchestrator (claude -p) ─決定ファイル─▶ tick ─起動─▶ worker (claude -p)
        観測 + 指示の導出              選定 + wip label の付与              決定どおり起動      実装 → CL
```

- 専用の台帳を持たない。状態は tracker の label (`dispatcher:wip` / `ready-for-human`) と open CL の存在に置き、tick ごとに読み直す
- CLI は tracker にも CL host にも書かない。選ぶ・見送る・人へ返すは LLM が決める
- 指示が無い tick は LLM を起動しない (静止時のコストはゼロ)
- 続行できない worker は、issue に引き渡しコメントを書き `ready-for-human` を付けて人へ返す

詳しくは [`docs/design/`](docs/design/)、用語は [`CONTEXT.md`](CONTEXT.md)、決定の理由は [`docs/adr/`](docs/adr/)。

## 前提

- macOS か Linux と、cron
- [Claude Code](https://docs.claude.com/en/docs/claude-code) と、その認証
- Claude Code plugin `swat-skills@swat9013` ([swat9013/claude-skills](https://github.com/swat9013/claude-skills))。worker が使う playbook・原則索引・レビュー skill を提供する
  ```
  /plugin marketplace add swat9013/claude-skills
  /plugin install swat-skills@swat9013
  ```
- [gh](https://cli.github.com/) と、その認証 (`gh auth login`。対応する tracker は GitHub だけ)

## install

どちらかで `claude-dispatcher` を PATH の通った場所に置く。crontab には置いた場所の絶対 path が書かれるので、`go run` の一時 build からは導入できない。

**`go install`** (Go 1.24 以上):

```sh
go install github.com/swat9013/claude-dispatcher/cmd/claude-dispatcher@latest
```

**GitHub Releases の binary**: [Releases](https://github.com/swat9013/claude-dispatcher/releases) から OS と CPU に合う `claude-dispatcher_<version>_<darwin|linux>_<amd64|arm64>.tar.gz` を取り、`checksums.txt` で確かめてから展開する。

```sh
shasum -a 256 -c checksums.txt --ignore-missing   # Linux は sha256sum -c checksums.txt --ignore-missing
tar xzf claude-dispatcher_<version>_darwin_arm64.tar.gz claude-dispatcher
install -m 755 claude-dispatcher ~/.local/bin/
```

## 導入

実装 repo の clone を cwd にして、project 名 (`[A-Za-z0-9._-]+`。以下 `myproj`) を決めて撃つ。

```sh
cd ~/src/widgets
claude-dispatcher setup myproj
```

`setup` は次の段を順に進め、済んだ段は何もせずに通る。途中で止まったら、示された内容を直して同じコマンドを撃ち直す。

1. **宣言 config の雛形**: `~/.config/claude-dispatcher/myproj/config.toml` が無ければ雛形を書いて止まる。issue 置き場 (`[issue].repo`)・着手可 label (`ready_label`)・並列上限 (`[limits].max_wip`) を確かめて直す。書ける項目は [`docs/design/formats.md`](docs/design/formats.md) §2
2. **config の検査**: 綴りの誤りや実在しない置き場を名指しで止める
3. **label**: issue 置き場に無い label (`dispatcher:wip` / `ready-for-human` / 着手可 label / `triage_label`) を示し、`y` と答えたら作る
4. **試運転**: `tick --dry-run` と、cron と同じ最小の環境で撃ち直す `tick --dry-run --cron-env` を撃ち、指示の件数を示す。何も起動せず、何も書かない
5. **crontab**: 5 分ごとに tick を撃つ 1 行を示し、`y` と答えたら登録する。この project の tick 行が別の形で既にあれば、並べて示すだけで置き換えない

承認を尋ねる段は `y` / `yes` のときだけ書く。それ以外の答えと端末の無い実行では書かずに、自分で撃つコマンドを示して止まる。

最後に `doctor` で導入の充足を確かめ、Claude Code の settings に要る entry を自分で足す (CLI は settings を書かない)。

```sh
claude-dispatcher doctor myproj
```

cron からは親 shell の環境変数も keyring も読めないことがある。試運転の `--cron-env` だけが落ちるとき、または登録後に tick が `auth_error` で止まるときは、config の `[auth]` に token file を書く (formats.md §2)。

## 動いているかを見る

**死活は 2 段で読む**。置き場は `claude-dispatcher paths --json myproj` で引ける。

1. `log.jsonl` の最終行の `ts` が周期 (5 分) の 2 倍より新しければ、tick は回っている
2. 古ければ `cron.log` の更新時刻を見る
   - `cron.log` が `log.jsonl` の最終行より新しい: cron は撃っているが CLI の手前か起動で落ちている。`cron.log` の末尾の行に理由が出る (binary や clone の path、認証)
   - `cron.log` も古い: cron 自体が撃っていない (crontab の行が無い / マシンがスリープしている)。`crontab -l` と `doctor` で確かめる

`cron.log` には失敗した tick の出力だけが溜まる。中身があることは異常を意味しないので、更新時刻で見る。

**今の worker** は `status` で見る。log.jsonl の起動記録を起点に、process の生死・wip label・作業ツリー・CL を読み直して並べる。

```sh
claude-dispatcher status ps myproj      # 1 回だけ
claude-dispatcher status watch          # 全 project を 5 秒ごとに描き直す (Ctrl-C で終わる)
```

`STATE` が `exited` なのに `WIP` が `yes` の行は、worker が wip を剥がさずに死んだ issue (stale wip)。issue を確かめて、wip label を手で外す。

## 止める

1. `crontab -e` で `tick myproj` の行を消す (コメントにしてもよい)。次の tick から何も起動しなくなる
2. 走っている worker はそのまま最後まで進み、CL を出すか人へ返して wip を剥がす。すぐ止めたいときは `status ps myproj` で `running` の issue を確かめ、log.jsonl の `spawned` にある `pid` の process を止めてから、その issue の `dispatcher:wip` を手で外す
3. 使うのをやめるなら、`paths --json myproj` が返す `config_file` の dir と `state_dir` を消す。issue 置き場の label は残る

## License

[MIT](LICENSE)
