# ADR 0010: Jira を acli で読み、issue の key の番号を作業対象の番号にする

- Status: Accepted
- Date: 2026-10-06

## Context

Jira Cloud の 1 つの project を issue 置き場にする構成 (`tracker.kind: jira`) を足す。issue 置き場の部品は `tracker.kind` の adapter が組み立てる (system.md §13)。GitHub と GitLab の adapter は、どちらも tracker の CLI (gh / glab) を撃ち、その CLI 自身の認証を使う。dispatcher は秘密を持たない (`tracker.token` は gh に環境変数で渡すだけ)。

Jira に収めるには、次の 2 つを決める必要がある。

- **読み方**: Jira を何で読むか。候補は Atlassian の CLI (`acli`) と、Jira の REST を直接撃つこと
  - acli の一覧 (`acli jira workitem search`) は、`created`・`issuelinks`・`parent`・`resolution` を field に指定できない (指定すると失敗する)
  - acli はどの失敗も exit 1 で返し、文言は Jira の言語の設定で訳される
- **作業対象の識別子**: Jira の issue は `WIDGETS-123` の key で指す。dispatcher の作業対象は種類と番号の組 (`issue#123`) で、log.jsonl の `target`・state dir の file 名・hooks の `CLAUDE_DISPATCHER_NUMBER` に番号が出る。これらは公開契約 (formats.md の冒頭) で、型を変えるなら互換を壊す

## Decision

**Jira は acli だけで読み、REST を直接撃たない。Jira の key の数字の部分を作業対象の番号にする。** どちらも Jira を他の kind と同じ形 (tracker の CLI を撃つ adapter・番号で指す作業対象) に収めるための選択で、理由が同じなので 1 つの ADR に置く。

- 認証は acli 自身のもの (`acli jira auth login` の結果) を loop も worker も使う。`tracker.token` は書けない
- acli の一覧が返さない field は、次のように代える
  - 作成日時: 候補の並びを、作成日時の代わりに番号の小さい順にする
  - 依存 (`blocked`): どれかの trigger が `blocked` を書いていれば、依存を持つ issue だけを検索で絞り、1 件ずつ読み直す
  - 終端: resolution の代わりに status の区分 (`statusCategory`) が `done` であること
- 失敗は文言で分けない。読み出しが落ちたら `acli jira auth status` を撃ち、落ちれば認証、通れば (起動時の確認では) 見えない、(tick の中では) その他とする
- 番号は scope key (site と project key) の中で一意になる。log.jsonl・state dir・hooks の環境変数は形も意味も変えない。完全な key は template 変数 `.issue.key` (`kind: jira` でだけ使える) と、人が読む参照の列 (試運転・画面) で渡す
- project key を改名したら、workflow 定義を書き換えて loop を起動し直す (scope key が変わる)

## Consequences

### 良い影響

- dispatcher が秘密を持たない線と、tracker の CLI が自分で持つ認証を使う形が、gh / glab と揃う
- 公開契約 (log.jsonl・state dir・hooks の環境変数) を変えずに Jira を足せる
- adapter の外 (loop・trigger の評価) は、番号で指す作業対象のまま変わらない

### 悪い影響 / 制約

- 候補の並びが作成日時でなく番号の順になる。issue を別の project から移すと、新しい番号が振られて並びが崩れる
- `blocked` を書くと、依存を持つ open な issue 1 件につき 1 往復の読み直しを毎 tick 撃つ
- 失敗の分類が粗い。rate limit は見分けられずその他に入り、network が落ちると `auth status` も落ちるので認証の失敗に化ける
- acli は呼び出しごとに site を指定できず、active な account の site を読む。workflow 定義の site (`tracker.host`) と acli の site は、起動時の確認で突き合わせる
- 同じ番号の key は project ごとに別の issue なので、1 つの loop は 1 つの project だけを読む。1 つの project を複数の repo で分けて回す区分は持たない

## Alternatives considered

### Jira の REST を直接撃つ

- 却下理由: 一覧で `created`・`issuelinks`・`resolution` を読め、失敗も HTTP の status で分けられる。代わりに dispatcher が API token を持つことになり、秘密を持たない線と gh / glab との対称を崩す

### 作業対象の識別子を key (文字列) に広げる

- 却下理由: log.jsonl の `target`・state dir の file 名・`CLAUDE_DISPATCHER_NUMBER` の型が変わり、公開契約の互換を壊す。scope key が project key を含むので、番号だけで scope の中では一意になり、広げる必要が無い

### 1 件の読み直しを JQL の `key = …` で撃つ

- 却下理由: 存在しない key を JQL に書くと検索ごと失敗し、消えた issue と見えない issue を区別できない。1 件は `view` で読み、落ちたら open な一覧に key があるかで終端を決める
