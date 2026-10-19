# ADR 0022: trigger と作業対象を指名した単発起動を `run` として置く

- Status: Accepted
- Date: 2026-10-19

## Context

workflow 定義に trigger と action を書いた人は、それが実際の作業対象で期待どおりに動くかを確かめたい。今ある手段は 2 つで、どちらも足りない。

- 試運転 (`loop --dry-run`) は、どの作業対象にどの trigger が当たるかを示すだけで、worker を起動しない。action の誤りは見つからない
- trigger を 1 つだけ書いた別の workflow 定義を `loop` に渡すと起動はできる。ただし当たった作業対象すべてに起動し、終わっても loop は止まらず、完了しなければ retry する。人が見張って Ctrl+C で止める

ADR 0009 は、ADR 0006 が残していた単発の `tick` を撤去した。tick は loop の 1 周期分の仕事で、loop の記憶 (claim・打ち切り) を前提にするので、loop の外で 1 周だけ回す形は残さなかった。

## Decision

**`claude-dispatcher run <trigger> <作業対象> [WORKFLOW]` で、指名した 1 つの trigger と 1 つの作業対象について worker を 1 回だけ起動し、完了したかを確かめて終わる (単発起動)。** 単発の `tick` を戻すのではなく、trigger と作業対象を人が指名する別の実行として置く。

- 作業対象は、ログの行の `target` と同じ綴り (`issue#<番号>` / `cl#<番号>`) で書く
- 作業対象がその trigger に当たっているときだけ起動する。当たっていなければ起動せずに失敗する。完了は「worker が終わった後に trigger から外れていること」なので、当たっていない作業対象では完了を判定できない
- 同じ scope key の lock を取る。同じ scope key の loop が立っていれば起動時に止まる (project 1 ─ 0..1 loop を保つ)
- attempt は 1 回だけで、retry も打ち切りもしない。完了したかを exit code で返す
- workspace・hooks・時間の上限・停止要求・起動前の検査は loop と同じ規則にする。単発起動で作った workspace を後の loop がそのまま使える

## Consequences

### 良い影響

- 書いた action を、loop を見張らずに 1 件で確かめられる。結果は exit code で読める
- loop と同じ規則で動くので、単発起動で通った action は loop でも同じように動く

### 悪い影響 / 制約

- 利用者が script に書く CLI の契約が 1 つ増える。後から外すと、その script が止まる
- loop が立っている間は、同じ scope key の単発起動はできない。loop を止めてから使う
- 起動の規則を変えるときは、loop と単発起動の両方に効くかを確かめる

## Alternatives considered

### 単発の `tick` を戻す (`loop --once`)

- 却下理由: 当たった作業対象すべてに起動し、作業対象を指名できない。retry と打ち切りは loop の記憶にあるので、1 周で終わる process では持てず、loop と違う振る舞いになる

### trigger を 1 つだけ書いた workflow 定義を `loop` に渡す (今の回避策)

- 却下理由: 作業対象を絞れず、終わりを人が見張って止める。完了しなければ retry するので、action の誤りが 2 回目以降に隠れる

### 述語に当たっていない作業対象も強制で起動する

- 却下理由: 完了を判定できない。`when` の誤りも見落とす
