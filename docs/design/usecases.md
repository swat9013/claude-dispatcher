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

1. loop が候補を trigger の宣言順と作成日時の順 (`jira` では番号の順) に並べ、空いている分だけ選んで claim する
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

1. loop が走っている worker ごとに、活動として数える stream の行が最後に書かれてからの経過を測る
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
- **事前条件**: claude-dispatcher・Claude Code と、`tracker.kind` に応じた tracker の CLI (`github` なら gh、`gitlab` なら glab、`jira` なら acli) が導入され、その CLI と claude が認証済み
- **成功保証**: 誤った workflow 定義で起動が始まっていない。試運転で worker が起動していない。loop が回り始めている

**Main Success Scenario**

1. 導入者が実装 repo の clone で `setup` を実行し、CLI が汎用の action を入れた workflow 定義の雛形を置く
2. 導入者が workflow 定義の issue 置き場・trigger・action・hooks を自分の project に合わせて書き、repo に commit する
3. CLI が workflow 定義を検査し、issue 置き場が見えること・述語が文法に合うこと (`jira` では status 名と issue type 名が実在すること)・各 action の先頭の skill が呼べることを確かめる
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
  - 6c1. 導入者は認証を環境変数で渡して loop を起動し直す (`jira` は token を渡せないので、acli が認証を読めるようにしてから起動し直す。README の「前提」)

## UC-6 loop を止める

- **Primary Actor**: 人間 (loop を起動した運用者)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: 運用者が dispatcher を止めたい (端末を閉じる・binary を更新する・使うのをやめる)
- **事前条件**: loop が走っている
- **成功保証**: 新しい worker が起動しない。止めた時点の worker は、終わったか止められたか、止められずに claude が残ったかが log に残っている。止められた作業対象は、trigger に当たったままなら、次に起動した loop が再び拾える

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
  - 3a2. SIGKILL を送れず claude が終わらない worker は、claude が終わるまで hooks を撃たず、終わりも待たない。loop が止まるまでに claude が終われば、3a1 と同じく hooks を撃って log に残す。終わらなければ、loop は claude の pid と workspace を error として log に残して step 4 へ進む。残った claude は orphan になる。運用者は loop を起動し直してよい。次の loop は orphan が終わるまでその workspace を残し、その作業対象で worker を起動しない。急ぐなら運用者がその pid を止める (system.md §7)
  - 3a3. 止められた worker が途中まで書いた成果は、workspace と remote branch に残る。次に起動した loop は、trigger に当たったままなら同じ workspace で続きから始める

## UC-7 trigger と action を 1 件で確かめる (単発起動)

- **Primary Actor**: 人間 (workflow 定義を書いた運用者)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: 運用者が、書いた trigger と action が実際の作業対象で期待どおりに動くかを確かめたい
- **事前条件**: workflow 定義が commit されている。同じ issue 置き場の loop が走っていない。確かめる作業対象が trigger に当たっている
- **成功保証**: 指名した作業対象に worker が 1 回だけ起動し、終わった後に trigger から外れたか (完了したか) を exit code で読める。retry はしない

**Main Success Scenario**

1. 運用者が trigger の名前と作業対象を指名して単発起動を求める
2. dispatcher が loop の起動時と同じ検査を通し、trigger と作業対象の種類が合うことを確かめ、同じ issue 置き場の lock を取る
3. dispatcher が作業対象を読み、指名した trigger に当たっていることを確かめる
4. dispatcher が loop と同じ規則で workspace を用意し、worker を 1 回起動する
5. dispatcher が worker の終わりを待ち、作業対象を読み直す
6. dispatcher が trigger から外れたことを示し、完了として終わる

**Extensions**

- **2a.** trigger の名前が workflow 定義に無いか、作業対象の種類が trigger の `on` と合わない
  - 2a1. dispatcher は起動せずに引数の誤りとして終わる
- **2b.** 同じ issue 置き場の loop が走っている
  - 2b1. dispatcher は起動せずに終わる。運用者は loop を止めてから撃ち直す
- **3a.** 作業対象が trigger に当たっていない・終端・当たるかをまだ決められない、か前の loop の orphan が生きている
  - 3a1. dispatcher は起動しなかったことを示して終わる。運用者は述語 (`when`) か作業対象を直して step 1 へ戻る
- **5a.** 運用者が停止を求めた
  - 5a1. 1 回目は worker の終わりを待って step 5 を続ける。2 回目は worker を止め、完了しなかったとして終わる
  - 5a2. claude を止められない (stall・上限時間で止めようとしたときを含む) なら、1 回目で claude を残して完了しなかったとして終わる。残った claude は orphan になる
- **4a.** worker を起動する前に停止を求められたか、claude を起動する前に失敗した (hooks・描画・claude の起動)
  - 4a1. dispatcher は起動しなかったことを示して終わる
- **6a.** 作業対象が trigger に当たったまま (worker の失敗を含む)
  - 6a1. dispatcher は完了しなかったことを示して終わる。運用者は worker log を読んで action を直し、step 1 へ戻る。作業対象が終端でなければ workspace は残るので、続きの単発起動や loop が同じ workspace を使う

---

## UC-8 1 つの issue 置き場をマシンを跨いで分担する

- **Primary Actor**: 人間 (チームの各メンバー。それぞれ自分のマシンで loop を回す)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: チームが、同じ issue 置き場を複数のマシンの loop で二重に起動せずに分け合いたい
- **事前条件**: workflow 定義に調停 remote (`coordination.remote`) が commit されている。各マシンの clone が、その名前の git remote へ push できる
- **成功保証**: 1 つの作業対象の worker は、どの時点でもいずれか 1 台の loop だけが起動する (乗っ取りの直後の、まれな二重実行を除く)。落ちたマシンの作業対象は、生存報告の間隔の 3 倍の後に、生きている loop が拾い直す

**Main Success Scenario**

1. 各メンバーが自分のマシンで loop を起動する
2. dispatcher (各マシンの loop) が調停 remote を解いて読めることを確かめ、生存報告を置き、以後は間隔ごとに置き直す
3. dispatcher が tick ごとに調停 remote の ref を読み、他の生きている loop の着手宣言が立っている作業対象を候補から外す
4. dispatcher が候補に着手宣言を条件付きで push し、通ったマシンの loop だけが claim を立てて worker を起動する
5. dispatcher が claim を解いたときに、自分の着手宣言を消す
6. 運用者が loop を止めると、dispatcher が自分の着手宣言と生存報告を消して止まる

**Extensions**

- **2a.** 調停 remote を解けないか読めない、または生存報告を置けない
  - 2a1. dispatcher は loop の起動を失敗させる。運用者は remote の名前と push の権限を直して step 1 へ戻る
- **4a.** 他のマシンの loop が先に着手宣言を push した
  - 4a1. dispatcher は claim を立てず、log に行を残すだけで step 3 へ戻る
- **3a.** 他のマシンの loop の生存報告が、間隔の 3 倍新しくならない (そのマシンが落ちた)
  - 3a1. dispatcher はその loop を死んだとみなす。その着手宣言の作業対象が候補になれば、旧 sha を期待値にした置き換えで乗っ取って起動し、残りの着手宣言と生存報告を消す
- **3b.** 自分の着手宣言が乗っ取られていた (network が切れて、生きたまま死んだとみなされた)
  - 3b1. dispatcher はその作業対象の worker を止めて claim を解き、要対処に出す。運用者は止まる前に push された成果を確かめる
- **3c.** 調停 remote に、自分の知る版より新しい版の ref がある (他のメンバーが dispatcher を先に上げた)
  - 3c1. dispatcher はその scope で worker を起動せず、要対処に出す。運用者は dispatcher を上げて step 1 へ戻る
- **2b.** 生存報告の置き直しが続けて落ちる
  - 2b1. dispatcher は要対処に出す。他のマシンの loop に乗っ取られうることを運用者が知る

## UC-9 loop を止めずに並列上限を変える

- **Primary Actor**: 人間 (loop を起動した運用者)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: 運用者が、走っている worker を中断せずに、同時に走らせる worker の数を絞りたい・広げたい (マシンの負荷・rate limit・急ぎの作業対象)
- **事前条件**: loop が走っている
- **成功保証**: 次の tick から、loop は運用者が渡した並列上限で空きを判定し、loop が止まるまでその値を使う。走っている worker は止まらない。loop が新しい値を採ったことと今の値が、画面と `status` に残る

**Main Success Scenario**

1. 運用者が別の端末で、新しい並列上限を CLI に渡す
2. CLI が loop が立っていることを確かめ、値を loop の state dir に書き、次の tick から効くことを返す
3. loop が次の tick で値を読み、前の値と新しい値をログの行に 1 行残す
4. loop がその tick から新しい並列上限で空きを判定し、画面の見出しに今の値を出す

**Extensions**

- **2a.** loop が立っていない
  - 2a1. CLI は値を書かずに失敗する。運用者は loop を起動するときに引数で並列上限を渡す
- **3a.** state dir の値が読めない (人が手で書き換えた)
  - 3a1. loop はその tick を error で終え、起動しない。運用者は値を CLI で撃ち直して step 1 へ戻る
- **4a.** 新しい並列上限が、走っている worker の数より小さい
  - 4a1. loop は走っている worker を止めず、走っている数が並列上限を下回るまで新しい起動と再起動を待つ
- **4b.** 運用者が loop を起動し直した
  - 4b1. loop は起動時に前の値を消し、起動時の引数の値 (省けば 1) から始める
