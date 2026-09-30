# ユースケース

- 本 file がユースケースの正本
  - 構造 (システム境界・trigger・状態の表現) は [`system.md`](system.md)
  - 形式は [`formats.md`](formats.md)

## 記述規約

Cockburn『ユースケース実践ガイド』(Writing Effective Use Cases) の fully dressed を縮約した形。**テキストが中心成果物で、図は置かない**。LLM が読んで編集できる正本を優先する。全体の流れは system.md §2 の図が持つ。

- 1 ユースケース = 1 見出し。フィールドは **Primary Actor / Scope / Level / Trigger / 事前条件 / 成功保証** を箇条書きで固定する
  - フィールドの「Trigger」は Cockburn の用語で、ユースケースの起点を指す。CONTEXT.md の trigger (workflow 定義の宣言) とは別物
- **Main Success Scenario (MSS)**: 番号付きリストで書く
  - 1 ステップ 1 文で、アクターを明記し、**意図**を書く。コマンドの綴りや schema の詳細は書かない
  - **3〜9 ステップ**に収める。超えるならユースケースを分割する
- **Extensions**: 分岐点を `<step 番号><英字>` で示し (例 `2a.`)、内部ステップは `2a1.` の形で書く。復帰先を明記する
- **Level は user-goal (sea) 固定**
- **Scope は dispatcher (機械システム = CLI とその入出力)**。worker (LLM) と人間は境界の外のアクター。worker が action の中で何をするかは workflow 定義が決めるので、ユースケースは描かない

---

## UC-1 静止した project の tick を無音で終える

- **Primary Actor**: 人間 (loop を起動した運用者)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: loop の周期が来た
- **事前条件**: loop が走っている
- **成功保証**: LLM が 1 度も起動せず、log に tick の記録が 1 行残っている

**Main Success Scenario**

1. loop が周期の到来で tick を始める
2. loop が走っている worker を外部 store と突き合わせる (走っていなければ何もしない)
3. loop が workflow 定義を読み直し、事前検査を通す
4. loop が tracker と CL host を観測して snapshot を作る
5. loop が trigger を評価し、候補が 0 件であることを確定する
6. loop が tick の記録を log へ 1 行書き、claude を起動せずに次の周期を待つ

**Extensions**

- **3a.** workflow 定義が読めない、または文法に合わない (綴りの誤り・未知の key)
  - 3a1. loop は誤りを名指しで画面と log に出し、この tick では起動しない。突き合わせ (step 2) は済んでいる。次の周期へ進む
- **3b.** ある trigger の action の template の先頭の skill が見つからない
  - 3b1. loop はその trigger を名指しで画面と log に出し、その trigger だけを評価から外して step 4 へ進む
- **4a.** 外部 store の観測に失敗した
  - 4a1. loop は「観測できなかった」を error として log に残し、候補 0 件と混同せずに tick を終える。次の周期へ進む
- **5a.** 曖昧な CL がある
  - 5a1. loop はその CL を CL 側の trigger の対象から外し、log と状態 file に出して step 5 を続ける

## UC-2 trigger に当たった作業対象へ worker を起動し、完了させる

- **Primary Actor**: 人間 (workflow 定義を書き、作業対象に label を付けた運用者)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: tick の評価で候補が 1 件以上ある
- **事前条件**: 並列上限に空きがある
- **成功保証**: worker が終わった後に、作業対象が起動した trigger から外れていて、作業対象の claim が解けている

**Main Success Scenario**

1. loop が候補を trigger の宣言順と作成日時の順に並べ、空いている分だけ選んで claim する
2. loop が作業対象の workspace を用意する (無ければ作って hooks を撃つ)
3. loop が action と共通 prompt を描画し、worker を子 process として起動する
4. loop が worker の stream を読み、画面と状態 file に進行を出す
5. worker が action を実行し、作業対象を trigger から外して終わる
6. loop が作業対象を読み直し、trigger から外れたことを確かめて claim を解く
7. loop が起動と終わり方を log に残す

**Extensions**

- **2a.** hook が失敗した、または上限時間を超えた
  - 2a1. loop はこの attempt を失敗として扱い、UC-3 へ進む
- **3a.** action の描画に失敗した (未知の変数)、または claude を起動できなかった
  - 3a1. loop はこの attempt を失敗として扱い、UC-3 へ進む
- **5a.** worker が、作業対象を見送るか人へ返すと判断した
  - 5a1. worker は、そのことを外部 store に残し (コメント・label)、作業対象を trigger から外して終わる。step 6 へ進む
- **6a.** trigger に当たったまま残った、または worker が異常終了した
  - 6a1. UC-3 へ進む
- **6b.** 作業対象の読み直しに失敗した
  - 6b1. loop は error として log に残し、完了とも失敗とも数えずに claim を持ち続ける。次の tick で step 6 をやり直す

## UC-3 完了しなかった作業対象を retry し、上限で打ち切る

- **Primary Actor**: 人間 (loop を起動した運用者)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: worker の attempt が失敗した (異常終了・stall・上限時間の超過・trigger に当たったまま)
- **事前条件**: 作業対象に claim がある
- **成功保証**: 作業対象は、完了するか、終端になるか、打ち切られて log と `status` に出ている。loop の 1 回の寿命の中では、1 つの action の不具合が上限を超えて起動を繰り返していない (打ち切りは loop を起動し直すと消える)

**Main Success Scenario**

1. loop が attempt を 1 つ進め、backoff の間は作業対象を再起動待ちとして claim したまま置く
2. 待ちが明けたら、loop が作業対象を読み直し、起動した trigger にまだ当たっていることを確かめる
3. loop が同じ workspace で、前の session を続ける worker を起動する (UC-2 の step 2 へ)

**Extensions**

- **1a.** attempt が上限に達した
  - 1a1. loop は作業対象を打ち切り、claim を解き、打ち切りを log と状態 file に出す
  - 1a2. 打ち切った作業対象は、どの trigger に当たっても起動しない
  - 1a3. 以後の tick で、作業対象が打ち切ったときの trigger から一度外れたのを観測したら、loop は打ち切りを解く。人が label を外して 1 周期待ち、付け直せば再び候補になる
- **2a.** 作業対象が終端になっていた
  - 2a1. loop は claim を解き、workspace を消して終わる
- **2b.** trigger から外れていた (人か別の手が動かした)
  - 2b1. loop は完了として claim を解いて終わる
- **2c.** 並列上限に空きが無い
  - 2c1. loop は attempt を進めずに、空きを待ち直す
- **2d.** 作業対象の読み直しに失敗した
  - 2d1. loop は error として log に残し、attempt を進めずに、次の tick で読み直す

## UC-4 走っている worker を外部 store と突き合わせる

- **Primary Actor**: 人間 (loop を起動した運用者)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: tick が始まった
- **事前条件**: worker が 1 本以上走っている
- **成功保証**: stall した worker と、終端になった作業対象の worker が残っていない。終端になった作業対象の workspace が消えている

**Main Success Scenario**

1. loop が走っている worker ごとに、stream の最後の event からの経過を測る
2. loop が走っている作業対象を外部 store で読み直す
3. loop が、終端になった作業対象の worker を止め、hooks を撃ってから workspace を消し、claim を解く
4. loop が、突き合わせの結果を log に残す

**Extensions**

- **1a.** 経過が stall の上限を超えた
  - 1a1. loop はその worker を止め、失敗として UC-3 へ渡す
- **2a.** 読み直しに失敗した
  - 2a1. loop は error として log に残し、worker を止めずに走らせ続け、次の tick で読み直す (SPEC §8.5)
- **3a.** `before_remove` が失敗した、または workspace を消せなかった
  - 3a1. loop は error として log に残す。workspace が残っていれば、次の tick で消し直す
- **2b.** trigger から外れていた (worker が作業の途中で label を外したなど)
  - 2b1. loop は worker を止めない。worker の終了後に UC-2 の step 6 で完了を確かめる

## UC-5 project を導入する

- **Primary Actor**: 人間 (導入者)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: 新しい project で dispatcher を使い始める
- **事前条件**: claude-dispatcher・Claude Code・gh が導入され、gh と claude が認証済み
- **成功保証**: 誤った workflow 定義で起動が始まっていない。試運転で worker が起動していない。loop が回り始めている

**Main Success Scenario**

1. 導入者が実装 repo の clone で `setup` を実行し、CLI が汎用の action を入れた workflow 定義の雛形を置く
2. 導入者が workflow 定義の issue 置き場・trigger・action・hooks・並列上限を自分の project に合わせて書き、repo に commit する
3. CLI が workflow 定義を検査し、issue 置き場が見えること・述語が文法に合うこと・各 action の先頭の skill が呼べることを確かめる
4. CLI が試運転で、起動するはずの作業対象と trigger を示す。何も起動せず、何も書かない
5. 導入者が `doctor` で、Claude Code の settings に要る entry を確かめて自分で足す
6. 導入者が clone で loop を起動し、画面で最初の tick が回ったことを確かめる

**Extensions**

- **3a.** workflow 定義に誤りがある
  - 3a1. CLI が名指しで失敗する。導入者が直して step 3 へ戻る
- **6a.** loop の起動時に workflow 定義の検査が落ちた (step 3 の後に定義を書き換えた、など)
  - 6a1. loop は誤りを名指しして起動を失敗させる。導入者が直して step 3 へ戻る
- **6b.** 同じ issue 置き場の loop が既に走っている
  - 6b1. CLI は 2 本目の loop を起動時に拒む。導入者は走っている loop の画面を見る
- **6c.** tick が認証の失敗で止まる (起動した環境から認証を取れない。ssh 越しの session など)
  - 6c1. 導入者は認証を環境変数で渡して loop を起動し直す

## UC-6 loop を止める

- **Primary Actor**: 人間 (loop を起動した運用者)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: 運用者が dispatcher を止めたい (端末を閉じる・binary を更新する・使うのをやめる)
- **事前条件**: loop が走っている
- **成功保証**: 新しい worker が起動しない。止めた時点の worker は、終わったか止められたかが log に残っている。止められた作業対象は、trigger に当たったままなら、次に起動した loop が再び拾える

**Main Success Scenario**

1. 運用者が loop に停止を求める (Ctrl+C)
2. loop が停止待ちを画面に示し、新しい起動と再起動待ちをやめる
3. loop が走っている worker の終了を待ち、それぞれの終わり方を log に残す
4. loop が画面を残し、停止の理由を 1 行足して終わる

**Extensions**

- **1a.** worker が走っていない
  - 1a1. loop は step 4 へ進む
- **1b.** 端末を閉じた (SIGHUP)
  - 1b1. 1 回目の停止要求と同じく step 2 へ進む。画面は見えないので、止まったことは別の端末の `status` で確かめる
- **3a.** 運用者が worker の終わりを待てず、もう一度停止を求めた
  - 3a1. loop は走っている worker の process group を止め、hooks を撃ち、止めた作業対象を log に残して step 4 へ進む
  - 3a2. 止められた worker が途中まで書いた成果は、workspace と remote branch に残る。次に起動した loop は、trigger に当たったままなら同じ workspace で続きから始める
