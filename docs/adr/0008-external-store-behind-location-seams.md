# ADR 0008: CLI の外部 store の読み書きを issue 置き場と CL 置き場の部品に閉じる

- Status: Accepted (issue と CL の紐づけを CL 置き場の部品が返す部分は ADR 0009 が置き換えた)
- Date: 2026-09-27

## Context

CLI は外部 store を gh で読む (tick の観測・`status` の wip と CL・`setup` / `doctor` の置き場の検査) ほか、`setup` が導入者の承認を得て label を作る。今は gh の argv と JSON を包む関数群が 1 つあるだけで、次のものが呼び出し側に散っている。

- 認証を載せた gh の組み立て (2 箇所)、置き場 repo の実在の検査 (2 箇所)、label の実在の検査 (3 箇所。確かめる label の集合は 2 通り)、gh の失敗の分類
- 条件カタログの述語が、GitHub の応答の綴り (`CONFLICTING` / `FAILURE`) を正本の定義なしに比べる。同じ綴りは指示ファイルと `status` の表示という公開形式にも出ている
- CL と issue の紐づけを、issue 置き場と CL 置き場が同じ host に在る前提で絞る

tick のテストは、gh の argv を見て GraphQL の JSON を返す fake で書かれ、部品の interface の奥で検査している。GitLab を CL 置き場に、Jira を issue 置き場にする構想があり、そのとき 2 つの置き場は別の host になりうる。

## Decision

**CLI の外部 store の読み書きを、issue 置き場の部品と CL 置き場の部品に閉じる。どちらも config から組み立て、中立の事実と分類済みの失敗を返し、gh の adapter とテスト用の in-memory の adapter を持つ。** 部品の責務の正本は `docs/design/system.md` §13。

- CL の状態の語彙 (`mergeable` / `checks` / CL の state) は `docs/design/formats.md` が正本。今の GitHub の綴りを dispatcher の語彙として定義し直し、adapter が host の応答をその語彙へ写す (gh では写し方が恒等になる)
- CL 置き場は組み立てのときに issue 置き場を受け取り、紐づく issue をその置き場のものに絞って返す
- 置き場の検査は、呼び出し側が渡す label の集合について、見えない repo と無い label をデータで返す。集合を選ぶのは呼び出し側 (tick は機構の label、`setup` / `doctor` は着手可 label を含む全部)
- 範囲は CLI だけ。orchestrator の契約 file と playbook は gh を直接撃ち、この部品を通らない
- issue の同一性は番号のまま。Jira の key を受けるかは、Jira を issue 置き場にするときに公開形式と合わせて決める

## Consequences

### 良い影響

- 置き場の検査と失敗の分類が 1 箇所に集まる
- tick のテストが in-memory の adapter で書け、GraphQL の JSON を組まない
- 条件カタログの述語が、公開形式で定義された語彙を比べる
- GitLab / Jira の観測は adapter を足す形になる

### 悪い影響 / 制約

- 本番の adapter が gh 1 つのうちから部品を 2 つ持つ。2 つの置き場が同じ host の構成では、同じ gh を 2 つの部品が包む
- GitLab / Jira 対応の半分 (LLM 側の gh 操作) には効かない
- issue の同一性を key に広げるときは、部品の interface と公開形式 (log.jsonl・決定ファイル・worker の branch 名) を合わせて直す

## Alternatives considered

### 外部 store を 1 つの部品にし、別 host の構成が来たときに割る

- 却下理由: 用語と宣言 config が既に 2 つの置き場を分けており、別 host の構成 (Jira + GitHub) はこの線で割れる。後で割ると全呼び出し側を 2 度触る

### seam を作らず、gh を包む関数群の中で綴りの中立化と検査の統合だけをする

- 却下理由: tick のテストが gh の argv の fake のまま残り、別 host を足すときに呼び出し側に分岐が要る

### CL の状態を小文字の中立語に改める

- 却下理由: 指示ファイルと `status` の表示という公開形式を、使い手のいないまま変える。今の綴りを dispatcher の語彙として定義し直せば、adapter の責務は「その語彙へ写す」で言える

### 部品が中立の事実と host の生の綴りを両方返す

- 却下理由: 部品の interface に host の綴りが残り、adapter を足すたびに生の綴りの意味を呼び出し側が知ることになる
