# claude-dispatcher

issue tracker の「着手可」の issue を Claude Code に無人で実装させ、CL (PR) まで運ぶ CLI。CL に conflict・未解決の review・CI 失敗が立てば、同じ branch へ手直しに戻す。

> 開発中です。現時点では設計 doc だけがあり、CLI は未実装です。

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

- [Claude Code](https://docs.claude.com/en/docs/claude-code) と、その認証
- Claude Code plugin `swat-skills@swat9013` ([swat9013/claude-skills](https://github.com/swat9013/claude-skills))。worker が使う playbook・原則索引・レビュー skill を提供する
  ```
  /plugin marketplace add swat9013/claude-skills
  /plugin install swat-skills@swat9013
  ```
- [gh](https://cli.github.com/) と、その認証 (初版の対応 tracker は GitHub だけ)
- cron (macOS / Linux)

## 導入の流れ (予定)

CLI が未実装のため、以下は設計上の予定の流れです。実装が入った時点で実際の手順に置き換えます。設計の正本は [`docs/design/usecases.md`](docs/design/usecases.md) の UC-6。

1. CLI を入れる (`go install` か GitHub Releases の binary)
2. 実装 repo の clone で `setup` を撃つ。宣言 config の雛形作成・label の作成・試運転・crontab の登録までを、承認を取りながら進める
3. `doctor` が示す entry を Claude Code の settings に足す (CLI は settings を書かない)
4. 周期の 2 倍待ち、log.jsonl の最終行の `ts` が進んでいることで tick が回っていると確かめる (置き場は `paths --json` で引ける)

## License

[MIT](LICENSE)
