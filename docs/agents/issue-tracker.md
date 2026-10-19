# Issue tracker: GitHub

Issues and specs for this repo live as GitHub issues. Use the `gh` CLI for all operations.

## Conventions

- **Create an issue**: `gh issue create --title "..." --body "..."`. Use a heredoc for multi-line bodies.
- **Read an issue**: `gh issue view <number> --comments`, filtering comments by `jq` and also fetching labels.
- **List issues**: `gh issue list --state open --json number,title,body,labels,comments --jq '[.[] | {number, title, body, labels: [.labels[].name], comments: [.comments[].body]}]'` with appropriate `--label` and `--state` filters.
- **Comment on an issue**: `gh issue comment <number> --body "..."`
- **Apply / remove labels**: `gh issue edit <number> --add-label "..."` / `--remove-label "..."`
- **Close**: `gh issue close <number> --comment "..."`

Infer the repo from `git remote -v`; `gh` does this automatically when run inside a clone.

## issue を分けるか

issue を起票するとき・triage や深掘りで分けるかを問われたとき・実装中に分けたくなったときに、issue 本文の「判定条件」の項目ごとに決める。

- 同じ関数か、[`docs/design/`](../design/README.md) の正本の同じ箇所を直す条件同士は、同じ issue に残す。分けると、同じ箇所の書き換えが PR の間で conflict する
- 他の条件を示す手段 (例: 振る舞いを示す test の仕組み) にすぎない条件は、示す先の条件と同じ issue に残す。手段だけを先に着地させても、示す対象が無く単独では価値が出ない
- 直す code と正本の箇所が他の条件と重ならず、単独で着地して価値が出る条件だけを、別の issue に出す

## Pull requests as a triage surface

**PRs as a request surface: no.** _(Set to `yes` if this repo treats external PRs as feature requests; `/triage` reads this flag.)_

When set to `yes`, PRs run through the same labels and states as issues, using the `gh pr` equivalents:

- **Read a PR**: `gh pr view <number> --comments` and `gh pr diff <number>` for the diff.
- **List external PRs for triage**: `gh pr list --state open --json number,title,body,labels,author,authorAssociation,comments` then keep only `authorAssociation` of `CONTRIBUTOR`, `FIRST_TIME_CONTRIBUTOR`, or `NONE` (drop `OWNER`/`MEMBER`/`COLLABORATOR`).
- **Comment / label / close**: `gh pr comment`, `gh pr edit --add-label`/`--remove-label`, `gh pr close`.

GitHub shares one number space across issues and PRs, so a bare `#42` may be either: resolve with `gh pr view 42` and fall back to `gh issue view 42`.

## follow-up issue の起票

作業の中で見つけた範囲外の欠陥・指摘と、実装が要る残タスクを issue にするときは、次に従う。起票の規則の正本はこの節で、`WORKFLOW.md`「残りを issue にする」はこの節を指す。

- この CL の変更が生んだ指摘と、変更した行の近くにあり同じ CL で直せる指摘は、起票せずに直す (依頼や issue 本文が範囲を狭めているときはそれに従う)。ただしレビューが修正量の上限で今回の CL から外した指摘 (`[上限超過]`) は、近くにあっても直さずに起票する — 上限は、修正の差分を再レビュー 1 巡で見切れる量に保つためにある
- 起票するのは、担当範囲の外で見つけた欠陥・レビューの指摘のうち直さなかった Act on と Consider・`[上限超過]` を付けた Noted (区分の語はレビューの出力のもの)。それ以外の Noted (`[既存]` を含む) は起票しない — レビューの出力に記録が残る
- 欠陥のうち、起きる入力が呼び出し側の通常の入力に現れない形 (例: 検査の tool が出さない長さや文字の並び) に限られ、起きたときは処理が非 0 で終わって run や command の失敗として見えるもの (誤った結果を黙って残さないもの) は、レビューの区分にかかわらず起票しない — 直して読む費用が、見落とさない欠陥を防ぐ得を上回る
- 判定条件が同じで対象だけ違う指摘は 1 件に束ね、対象を本文の checklist に並べる
- 起票の前に、同じ指摘の issue を、open なものと、`docs/agents/triage-labels.md` の表で `wontfix` の役を持つ label で close したものから、題名と変更対象の file path の 2 通りで探す。当たった issue は本文を読み、判定条件と対象が同じかで同じ指摘かを決める。同じ指摘の issue があれば起票せず、open なら発端の issue と CL の番号があればコメントで足す
- 本文に `## 範囲` の節を置き、変更してよい file と「範囲外で見つけた欠陥・指摘は `docs/agents/issue-tracker.md`「follow-up issue の起票」に従って扱う」を書く
- `## 範囲` の節の直前に、発端の issue (作業が担当している issue) を指す独立した 1 行 `発端: #<番号>` を置く。follow-up issue がどの作業から起票されたかを後から辿るための行で、着手可の判定には使わない。issue を介さず人が始めた作業には指す issue が無いので、行を置かない
- 付ける label は triage 待ち (`docs/agents/triage-labels.md` の表で `needs-triage` の役を持つ label) だけにする。着手可は triage が付ける。`ready-for-agent` を付けると、dispatcher が worker 自身の起票を拾って作業を自己増殖させる

## When a skill says "publish to the issue tracker"

Create a GitHub issue.

## When a skill says "fetch the relevant ticket"

Run `gh issue view <number> --comments`.

## Wayfinding operations

Used by `/wayfinder`. The **map** is a single issue with **child** issues as tickets.

- **Map**: a single issue labelled `wayfinder:map`, holding the Notes / Decisions-so-far / Fog body. `gh issue create --label wayfinder:map`.
- **Child ticket**: an issue linked to the map as a GitHub sub-issue (`gh api` on the sub-issues endpoint). Where sub-issues aren't enabled, add the child to a task list in the map body and put `Part of #<map>` at the top of the child body. Labels: `wayfinder:<type>` (`research`/`prototype`/`grilling`/`task`). Once claimed, the ticket is assigned to the driving dev.
- **Blocking**: GitHub's **native issue dependencies**, the canonical, UI-visible representation. Add an edge with `gh api --method POST repos/<owner>/<repo>/issues/<child>/dependencies/blocked_by -F issue_id=<blocker-db-id>`, where `<blocker-db-id>` is the blocker's numeric **database id** (`gh api repos/<owner>/<repo>/issues/<n> --jq .id`, _not_ the `#number` or `node_id`). GitHub reports `issue_dependencies_summary.blocked_by` (open blockers only, the live gate). Where dependencies aren't available, fall back to a `Blocked by: #<n>, #<n>` line at the top of the child body. A ticket is unblocked when every blocker is closed.
- **Frontier query**: list the map's open children (`gh issue list --state open`, scoped to the map's sub-issues / task list), drop any with an open blocker (`issue_dependencies_summary.blocked_by > 0`, or an open issue in the `Blocked by` line) or an assignee; first in map order wins.
- **Claim**: `gh issue edit <n> --add-assignee @me`, the session's first write.
- **Resolve**: `gh issue comment <n> --body "<answer>"`, then `gh issue close <n>`, then append a context pointer (gist + link) to the map's Decisions-so-far.
