---
name: release
description: 最新の tag から版を上げた tag を origin/main の commit に打って push し、release workflow の完了まで見届ける
disable-model-invocation: true
argument-hint: "<patch|minor|major>"
allowed-tools: Bash(git fetch:*), Bash(git rev-parse:*), Bash(git tag -l:*), Bash(git log:*), Bash(gh run list:*), Bash(gh run watch:*), Bash(gh run view:*), Bash(gh release view:*)
---

# release

`v*` の tag を push すると、release workflow ([.github/workflows/release.yml](../../../.github/workflows/release.yml)) が gate を通してから GitHub Releases と tap `swat9013/homebrew-tap` の cask を更新する。方針の正本は [CONTRIBUTING.md](../../../CONTRIBUTING.md) の「release」。この skill は tag を打って push し、run を見届けるところまでを受け持つ。

- 版は tag だけが持つ (GoReleaser が ldflags で binary に埋める)。版を書いた file は無いので、release の commit は作らない
- tag は **origin/main の commit** に打つ。手元の HEAD は worktree や古い main のことがあり、release workflow の `on-main` job は tag の commit が main に無いと止まる
- push した tag は Releases と tap に外から見える形で出る。push は手順 4 の人の確認を通してからだけ撃つ。`git tag -a` と `git push` を allowed-tools に入れていないのは、permission の確認を 2 つ目の関門に残すため

## 手順

### 1. 対象の commit と前回の版を引く

```sh
git fetch origin main --tags
git rev-parse origin/main
git tag -l 'v*' --sort=-v:refname
```

- `sha` は `git rev-parse origin/main` の出力 (40 桁のまま使う。`gh run list --commit` は短縮した SHA だと何も返さない)
- 前回の版は、tag 一覧の先頭から見て最初の安定版 (`-` を含まない `vX.Y.Z`)。prerelease の tag (`v1.2.0-rc.1` 等) は上げる元にしない
- 前回からの commit 一覧は `git log --oneline <前回の版>..<sha>`。merge の形 (merge commit・squash・rebase) によらず、release に入る commit が全部出る。空なら `sha` は前回の版と同じ commit なので、「前回から変更が無い」と告げて終える

### 2. 新しい版を決める

引数で決める。

- `patch` / `minor` / `major`: 前回の版の該当する桁を 1 上げ、下の桁を 0 にする
- 引数が無い: 手順 1 の commit 一覧を見せ、`patch` / `minor` / `major` を AskUserQuestion で user に選ばせる。上げ幅は変更の中身を知る人が決める

### 3. main の CI の状態を引く

```sh
gh run list --commit <sha> --workflow ci.yml --event push --json status,conclusion,url
```

release workflow は gate を撃ち直すので、ここで落ちていても push 自体は止まらない。ただ、落ちる commit に打った tag は release されないまま残り、消す手間が出る。`conclusion` が `success` 以外 (失敗・実行中・run 無し) なら、手順 4 でその旨と url を目立たせて見せる。

### 4. 人の確認を取る

次を並べて見せ、push してよいかを AskUserQuestion で聞く。「push する」以外が選ばれたら、何も打たずに終える。

- 新しい版と、前回の版
- 対象の commit (`sha` と、その commit の 1 行目)
- 前回の版からの commit 一覧 (手順 1 の `git log`)
- main の CI の状態 (手順 3)

### 5. tag を打って push する

既存の tag と同じく、annotated で message を tag 名にする。push は refspec で 1 本だけ送る。

```sh
git tag -a <版> -m <版> <sha>
git push origin refs/tags/<版>
```

push が失敗したら、手元の tag を `git tag -d <版>` で消してから、失敗の出力をそのまま見せて終える (残すと次の実行で「既に在る」になる)。

### 6. release workflow を見届ける

```sh
gh run list --workflow release.yml --commit <sha> --event push --json databaseId,headBranch,url
```

`headBranch` が新しい版の run を選ぶ。push の直後は run がまだ無いことがあるので、無ければ 5 回まで撃ち直す。5 回撃っても無ければ、待つのをやめて次を返して終える (tag は push 済みなので、run が起動しなかったことを user が拾えるようにする)。

- push した tag 名と `sha`
- release workflow の run が見つからないこと
- Actions の url (`https://github.com/swat9013/claude-dispatcher/actions/workflows/release.yml`)。workflow が無効になっていないかをここで確かめる

```sh
gh run watch <databaseId> --exit-status
gh release view <版> --json url
```

- 成功: Release の url を返す
- 失敗: `gh run view <databaseId>` を撃ち、run の url と、出力の `X` の付いた job・step と `ANNOTATIONS` の行をそのまま返す。release.yml は失敗の理由と次の手順を `::error::` に書いており、それが `ANNOTATIONS` に出る。tag は消さずに残し、消すかどうかは user に委ねる (Release が半端に出ていることがあるため)
