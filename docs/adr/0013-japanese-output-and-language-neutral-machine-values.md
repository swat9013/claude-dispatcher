# ADR 0013: 出力は日本語の 1 言語にし、機械が読む値は言語に依らない形にする

- Status: Accepted
- Date: 2026-10-11

## Context

CLI の出力 (エラー文・`status`・`loop` の画面・`setup` の雛形) はすべて日本語で、そう決めた記録は無かった (#163)。

log.jsonl の値のうち、`reason` だけが日本語の文 (`終端`・`trigger から外れた` など) だった。`event` (`abandon`・`release` など) や status.json の `phase` (`running`・`waiting_retry` など) は ASCII の token なので、`reason` が例外になっていた。加えて `end` の行の `reason` は、列挙の値と worker の失敗の自由文が 1 つの key に混ざっていた。`completed` では `終端 (worker は <失敗>)` と連結し、`failed` では失敗の文で丸ごと置き換えていた。log.jsonl を読む側は、`reason` の値を列挙として比べられなかった。

## Decision

1. **出力は日本語の 1 言語にする。`LANG` での切り替えは採らない**
   - 出力の言語は文書の読み手に従う。英語の出力は、README・案内 (`guide`)・雛形・`CONTEXT.md` を含めて英語にしないと成り立たない。文書が日本語のままでは、日本語を読めない人は導入できない。今の読み手は日本語を読む人だけ
2. **機械が読む値は言語に依らない。列挙の値は ASCII の token、自由文の値は人向けの日本語にする**
   - 規則は log.jsonl に限らず、status.json などの機械が読む出力にも掛かる。自由文の値 (log.jsonl の `error`・`tick` の `blocked[].error`、status.json の `last_tick.error`) は日本語のまま
3. **log.jsonl の `reason` を token にする**

   | 旧値 | token |
   |---|---|
   | `終端` | `terminal` |
   | `trigger から外れた` | `left_trigger` |
   | `trigger に当たったまま` | `still_matches` |
   | `停止要求` | `stop_requested` |
   | `trigger が workflow 定義から消えた` | `trigger_removed` |
   | `当たるかをまだ決められない` | `undecided` |
   | `宣言順で先の trigger に当たる` | `earlier_trigger` |

4. **`end` の行を `reason` (token) と `failure` (自由文) に分ける**
   - `failure` は worker 自身が失敗したときだけ載せ、`completed` と `failed` で同じ形にする
   - `failed` の `reason` は `still_matches` に固定する (失敗の文で置き換えない)
   - loop の画面の行は token ではなく人向けの文を出し、文言は変えない。token から画面の文への対応はコードの 1 か所に持つ
5. **互換の手当ては設けない**。旧値の受け入れや移行期間を置かず、1 回の release で変える。利用者は owner だけで、守る相手がいない

決定 3・4 は log.jsonl の公開契約 (formats.md の冒頭) の値の意味を変えるので、互換を壊す変更になる。

### 見直しの条件

読み手が日本語の外へ広がったら、文書と出力をまとめて英語の 1 言語へ移す。`LANG` での切り替えにはしない。機械が読む値は決定 2 により言語に依らないので、この移行では変わらない。

## Consequences

### 良い影響

- log.jsonl を読む側が `reason` を列挙として比べられる (`jq 'select(.reason == "terminal")'`)。worker の失敗は `failure` の有無で拾える
- 出力の言語を変えても、log.jsonl の列挙の値は変わらない

### 悪い影響 / 制約

- 旧値で `reason` を比べていた読み手は壊れる。この版の前後の log.jsonl が同じ file に混ざる
- log.jsonl の値の形を変えると、repo の中の読み手 (`.claude/skills/debrief` の log.jsonl の読み方) も合わせて直す必要がある
- 日本語を読めない人は、今の出力と文書では導入できない

## Alternatives considered

### `LANG` で日本語と英語を切り替える

- 却下理由: 文言の catalog と 2 言語分のテストの期待値が要り、利用者の規模に見合わない。出力だけ英語にしても、文書が日本語のままでは日本語を読めない人は導入できない

### `reason` は日本語の文のまま、列挙の token を別の key で足す

- 却下理由: 同じ意味を 2 つの key に書くと、片方だけ直したときに黙って食い違う。`event` や `phase` と同じく、列挙の値そのものを token にするほうが規則が 1 本で済む

### 旧値も受け入れる移行期間を置く

- 却下理由: log.jsonl を読む外部の読み手は owner だけで、互換を保って守る相手がいない
