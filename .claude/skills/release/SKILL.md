---
name: release
description: 最新の tag から版を上げた tag を、人が確かめた利用者向けの要約を本文にして origin/main の commit に打って push し、release workflow の完了まで見届ける
disable-model-invocation: true
argument-hint: "<patch|minor|major>"
allowed-tools: Bash(git fetch:*), Bash(git rev-parse:*), Bash(git tag -l:*), Bash(git log:*), Bash(git diff:*), Bash(gh run list:*), Bash(gh run watch:*), Bash(gh run view:*), Bash(gh release view:*)
---

# release

`v*` の tag を push すると、release workflow ([.github/workflows/release.yml](../../../.github/workflows/release.yml)) が gate を通してから GitHub Releases と tap `swat9013/homebrew-tap` の cask を更新する。方針の正本は [CONTRIBUTING.md](../../../CONTRIBUTING.md) の「release」。この skill は要約の下書きを作って人に確かめてもらい、tag を打って push し、run を見届けるところまでを受け持つ。

- 版は tag だけが持つ (GoReleaser が ldflags で binary に埋める)。版を書いた file は無いので、release の commit は作らない
- tag は **origin/main の commit** に打つ。手元の HEAD は worktree や古い main のことがあり、release workflow の `on-main` job は tag の commit が main に無いと止まる
- Release の本文の先頭は、tag の本文に書いた利用者向けの要約になる (`.goreleaser.yaml` の `release.header`)。その下の PR の一覧と比較の link は GoReleaser が足す。要約の決め方は CONTRIBUTING.md の「release」に従い、この skill は下書きを作って人に直してもらう
- push した tag は Releases と tap に外から見える形で出る。push は手順 5 の人の確認を通してからだけ撃つ。`git tag -a` と `git push` を allowed-tools に入れていないのは、permission の確認を 2 つ目の関門に残すため

## 手順

### 1. 対象の commit と前回の版を引く

```sh
git fetch origin main --tags
git rev-parse origin/main
git tag -l 'v*' --merged <sha> --sort=-v:refname
```

- `sha` は `git rev-parse origin/main` の出力 (40 桁のまま使う。`gh run list --commit` は短縮した SHA だと何も返さない)
- 前回の版は、`sha` の祖先にある tag の一覧の先頭から見て最初の安定版 (`-` を含まない `vX.Y.Z`)。prerelease の tag (`v1.2.0-rc.1` 等) は上げる元にしない。祖先に限るのは、release workflow が Release の PR の一覧の起点に渡す前回の安定版と同じ tag を選ぶため
- 前回からの commit 一覧は `git log --oneline <前回の版>..<sha>`。merge の形 (merge commit・squash・rebase) によらず、release に入る commit が全部出る。空なら `sha` は前回の版と同じ commit なので、「前回から変更が無い」と告げて終える

### 2. 新しい版を決める

引数で決める。

- `patch` / `minor` / `major`: 前回の版の該当する桁を 1 上げ、下の桁を 0 にする
- 引数が無い: 手順 1 の commit 一覧を見せ、`patch` / `minor` / `major` を AskUserQuestion で user に選ばせる。上げ幅は変更の中身を知る人が決める

### 3. main の CI の状態を引く

```sh
gh run list --commit <sha> --workflow ci.yml --event push --json status,conclusion,url
```

release workflow は gate を撃ち直すので、ここで落ちていても push 自体は止まらない。ただ、落ちる commit に打った tag は release されないまま残り、消す手間が出る。`conclusion` が `success` 以外 (失敗・実行中・run 無し) なら、手順 5 でその旨と url を目立たせて見せる。

### 4. 要約の下書きを作る

前回の版から `sha` までの、利用者向けの案内と形式の正本の差分 (主な材料) と、go.mod の差分を引く。

```sh
git diff <前回の版>..<sha> -- internal/guide/guide.md docs/design/formats.md
git diff <前回の版>..<sha> -- go.mod
```

主な材料に、CONTRIBUTING.md の「release」が挙げる補助の材料を加え、同じ節が挙げる節の順で要約を書く。補助の材料は、CONTRIBUTING.md がそれぞれの候補とする節に書く。各節は、CONTRIBUTING.md が要約の節として挙げる名前を括弧書きまでそのまま `## ` の見出しにし、中身の無い節は出さない。補助の材料は次のように引く。

- commit 一覧: 手順 1 の `git log` をそのまま使う。題名は開発者の語なので、利用者に見える修正だけを選び、利用者の語に書き直す。どれを残すかは手順 5 で人が commit 一覧と見比べて決める
- go.mod の `go` 行: 上の go.mod の差分に `-go <前の版>` と `+go <後の版>` の対があれば、`go install` に要る Go の版が前の版から後の版へ上がったことを書く。go.mod の他の行 (`require`・`toolchain` 等) は材料にしない

主な材料と補助の材料のどれにも利用者に見える変化が無ければ、要約は空にする (Release の本文は PR の一覧と比較の link だけになる)。主な材料の差分が空でも、補助の材料に利用者に見える変化があれば要約を書く。

### 5. 人の確認を取る

次を並べて見せ、push してよいかを AskUserQuestion で聞く。選択肢は「この要約で push する」「要約を直す」「push しない」にする。

- 新しい版と、前回の版
- 対象の commit (`sha` と、その commit の 1 行目)
- 前回の版からの commit 一覧 (手順 1 の `git log`)
- main の CI の状態 (手順 3)
- 要約の下書き (手順 4) の全文

「要約を直す」なら、直した文面を user から受け取り、その文面を要約にして同じ問いをもう一度聞く。「push する」以外で終わったら、何も打たずに終える。

### 6. tag を打って push する

tag は annotated で打ち、message は 1 行目を版、空行の後ろを手順 5 で確定した要約にする。要約は見出しの `#` で始まる行を持ち、`git tag` の既定の cleanup (`strip`) はそれを注釈として消すので、`--cleanup=verbatim` で打つ。message は作業ツリーの外 (OS の temp) の file に Write tool で書き、`-F` で渡す。要約が空なら、message は版の 1 行だけにする。push は refspec で 1 本だけ送る。

```sh
git tag -a <版> --cleanup=verbatim -F <message の file> <sha>
git tag -l --format='%(contents:body)' <版>
git push origin refs/tags/<版>
```

push の前に、`git tag -l` が出す tag の本文が確定した要約と同じかを見る (Release の本文の先頭にそのまま載る)。違えば push せず、`git tag -d <版>` で消してから、確定した要約と `git tag -l` の出力を並べて見せて終える (打ち直しても同じ手順は同じずれを生む)。

push を撃てなかったら (失敗した・permission の確認で断られた)、手元の tag を `git tag -d <版>` で消してから、失敗の出力をそのまま見せて終える (残すと次の実行で「既に在る」になる)。

### 7. release workflow を見届ける

```sh
gh run list --workflow release.yml --commit <sha> --event push --json databaseId,headBranch,url
```

`headBranch` が新しい版の run を選ぶ。push の直後は run がまだ無いことがあるので、無ければ 5 回まで撃ち直す。5 回撃っても無ければ、待つのをやめて次を返して終える (tag は push 済みなので、run が起動しなかったことを user が拾えるようにする)。

- push した tag 名と `sha`
- release workflow の run が見つからないこと
- Actions の url (`https://github.com/swat9013/claude-dispatcher/actions/workflows/release.yml`)。workflow が無効になっていないかをここで確かめる

```sh
gh run watch <databaseId> --exit-status
gh release view <版> --json url,body
```

- 成功: Release の url を返す。要約が空でなければ、`body` が確定した要約で始まるかも見る (改行コードの違いと、各行の行末と全体の前後の空白は無視して比べる)。始まらなければ (release workflow が tag の本文を読めていない)、そのことと `body` の先頭を url に添えて返す
- 失敗: `gh run view <databaseId>` を撃ち、run の url と、出力の `X` の付いた job・step と `ANNOTATIONS` の行をそのまま返す。release.yml は失敗の理由と次の手順を `::error::` に書いており、それが `ANNOTATIONS` に出る。tag は消さずに残し、消すかどうかは user に委ねる (Release が半端に出ていることがあるため)
