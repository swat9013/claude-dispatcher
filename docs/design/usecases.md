# ユースケース

- 本 file がユースケースの正本。構造 (システム境界・指示カタログ・状態の表現) は [`system.md`](system.md)、形式は [`formats.md`](formats.md)

## 記述規約

Cockburn『ユースケース実践ガイド』(Writing Effective Use Cases) の fully dressed を縮約した形。**テキストが中心成果物で、図は置かない** (LLM が読んで編集できる正本を優先する。全体の流れは system.md §2 の図が持つ)。

- 1 ユースケース = 1 見出し。フィールドは **Primary Actor / Scope / Level / Trigger / 事前条件 / 成功保証** を箇条書きで固定する
- **Main Success Scenario (MSS)**: 番号付きリスト。1 ステップ 1 文・アクターを明記・**意図**を書く (コマンドの綴りや schema の詳細は書かない)。**3〜9 ステップ**に収める。超えるならユースケースを分割する
- **Extensions**: 分岐点を `<step 番号><英字>` で示し (例 `2a.`)、内部ステップは `2a1.` 形式。復帰先を明記する
- **Level は user-goal (sea) 固定**
- Scope は原則 **dispatcher (機械システム = CLI とその入出力)**。LLM (orchestrator / worker)・人間は境界の外のアクター。**例外は UC-4** — worker の終了処理そのものを描く UC なので、Scope を dispatcher 運用全体 (機械システム + 外部 store) に取る

---

## UC-1 静止した project の tick を無音で終える

- **Primary Actor**: 人間 (loop を起動した運用者)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: loop の周期が来た
- **事前条件**: 宣言 config が検査済みで置かれている
- **成功保証**: LLM が 1 度も起動せず、log に実行記録が 1 行残っている

**Main Success Scenario**

1. loop が周期の到来で tick を始める
2. CLI が単一実行 lock を取り、宣言 config を読む
3. CLI が tracker と CL host を観測し、snapshot を作る
4. CLI が指示を導出し、0 件であることを確定する
5. CLI が実行記録を log へ 1 行 append し、claude を起動せずに tick を終えて次の周期を待つ

**Extensions**

- **2a.** 単発の tick がまだ走っている (lock が取れない)
  - 2a1. CLI は「見送った」ことだけを log に残して tick を終える (次の周期が拾う)
- **3a.** 外部 store の観測に失敗した
  - 3a1. CLI は「観測できなかった」をエラーとして log に残し、指示 0 件と混同せずに tick を終える。loop は止まらず次の周期へ進む

## UC-2 候補 issue に着手させ CL へ到達させる

- **Primary Actor**: orchestrator (LLM)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: CLI が `start` 指示を出した
- **事前条件**: 候補が 1 件以上あり、wip 枚数が上限 N 未満
- **成功保証**: 着手した issue に wip が付き worker が走っている。worker 終了後は closing reference 付きの CL が open で、残タスクと範囲外の欠陥が着手可 label の無い issue として tracker に残り、wip が剥がれ、worktree が残っていない

**Main Success Scenario**

1. CLI が空き slot・候補・選定母集合の playbook を添えた `start` 指示を書き、orchestrator を起動する
2. orchestrator が指示を読み、着手する issue を選び、issue 本文から作業種別を読んで playbook を選ぶ (**選定は LLM に残す**。選定規則は system.md §10)
3. orchestrator が issue に wip を付け、spawn prompt を決定ファイルに書いて終了する。CLI が決定ファイルを検査し、headless worker を detach 起動して tick を終える
4. worker が worktree を作り、playbook と原則索引を読んで実装する
5. worker が two-axis-review を通し、自律判断を説明文に載せた closing reference 付き CL を作成して、レビュー出力を CL へコメントする
6. worker が system.md §10 の対象を triage 待ちの issue にし、CL 本文の「user に残る作業」節から番号で指す (着手可 label は付けない)
7. worker が終了処理として wip を剥がし、worktree を消して終了する

**Extensions**

- **2a.** orchestrator が候補のいずれも着手に値しないと判断した
  - 2a1. 候補ごとの見送りと理由を決定ファイルに書いて終了する (issue には何も書かない)。CLI が log へ写す
- **3a.** wip の付与に失敗した、または決定ファイルを書けなかった
  - 3a1. orchestrator が付けてしまった wip を剥がして終了する
  - 3a2. 現実が変わっていなければ次 tick が同じ指示を再導出する (再試行の状態管理を持たない)
- **3b.** CLI が worker を起動できなかった (claude が無い / 起動失敗)、または orchestrator が正常終了しなかった (timeout / 異常終了 — 決定ファイルは読まず起動しない)
  - 3b1. CLI が失敗を log に残して終了する。wip が付いたままなら次 tick では候補に戻らず start も出ない — stale wip として triage が回収する
- **6a.** issue にするものが無い
  - 6a1. 何も起票せず step 7 へ進む
- **6b.** 同じ title の open issue が既にある
  - 6b1. system.md §10 の重複規則に従い、step 7 へ進む

## UC-3 CL イベントに worker を再入させる

- **Primary Actor**: orchestrator (LLM)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: CLI が `reenter` 指示 (`conditions` に `conflict` / `review` / `ci` のいずれかを併記) を出した
- **事前条件**: 対象 issue に紐づく worker 由来の open CL がちょうど 1 本あり、issue に `dispatcher:wip` も `ready-for-human` も付いていない
- **成功保証**: worker が既存 branch へ push しており、新規 CL が増えていない

**Main Success Scenario**

1. CLI が CL の状態 (conflict / 未解決 thread / checks 失敗。複数あれば併記) と条件別 playbook を添えた再入指示を書き、orchestrator を起動する
2. orchestrator が条件を読み直して立ったままの条件だけを残し、issue に `dispatcher:wip` を付け、条件別 playbook の path を条件順に載せた spawn prompt を決定ファイルに書く。CLI がそれを検査して worker を起動する
3. worker が CL の head branch から worktree を作り直し、条件別 playbook を順に Read してイベントに対応する (base branch を merge して conflict 解消 / thread へ返信・反映・resolve / CI 失敗の修正)
4. worker が既存 CL の branch へ push し、対応内容を CL 上に残す
5. worker が wip を剥がし、worktree を消して終了する

**Extensions**

- **2a.** orchestrator が wip 枚数を数え直すと空きが無い (CLI は tick 時点の空きの分だけ指示を出すが、tick の後に人や前 tick の worker が wip を付けていた)
  - 2a1. orchestrator が見送りとして理由を決定ファイルに残す。次 tick で再導出される
- **2b.** 読み直すと条件が全部外れていた (tick と今の間に人か worker が動いた)
  - 2b1. orchestrator が見送りとして理由を決定ファイルに残す
- **3a.** worker がレビュー指摘に同意できない、または指摘の書き手が collaborator でない
  - 3a1. 同意できる指摘を先に片付け、続行不能経路 (UC-4 の step 3 以降) へ倒す — issue は `ready-for-human` に落ち、その thread は未解決のまま残る
- **3b.** worker が対応不能と判断した (解消できない conflict / 手元で再現できない CI 失敗等)
  - 3b1. 続行不能経路 (UC-4 の step 3 以降) へ倒す

## UC-4 続行不能を人へ返す

- **Primary Actor**: worker (LLM)
- **Scope**: dispatcher 運用全体 (機械システム + 外部 store。記述規約の例外)
- **Level**: user-goal (sea)
- **Trigger**: worker が実装・対応を続行不能と判断した
- **事前条件**: 対象 issue に wip が付いている
- **成功保証**: issue に引き渡しコメント (停止理由 / ここまでの成果の所在 / 人が次にやること) と `ready-for-human` が付き、wip が剥がれている。人が label を外すまで候補に戻らない

**Main Success Scenario**

1. worker が続行不能の根拠と、解決に要ることを整理する
2. worker が途中までの成果 (あれば) を commit して push する (成果の所在をコメントに書けるようにする)
3. worker が system.md §10 の対象を triage 待ちの issue にする (着手可 label は付けない)
4. worker が issue へ引き渡しコメントを書く (step 3 で作った issue を番号で指す)
5. worker が issue に `ready-for-human` を付け、wip を剥がす (コメントの後 — label を先に動かすと成果の所在を書く前に人が動く)
6. worker が worktree を消して終了する (branch が永続)
7. 以後の tick で、CLI は `ready-for-human` の付いた issue を候補に含めない

**Extensions**

- **2a.** 残す価値のある成果が無い
  - 2a1. push せず、コメントの成果欄に「なし」と書く
- **3a.** issue にするものが無い
  - 3a1. 何も起票せず step 4 へ進む
- **3b.** 同じ title の open issue が既にある
  - 3b1. system.md §10 の重複規則に従い、step 4 へ進む

## UC-5 判断不能な観測を握り潰さずに上げる

- **Primary Actor**: orchestrator (LLM)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: CLI が `anomaly` 指示を出した (例: wip 枚数が上限 N を超えている / wip と `ready-for-human` が同居している / 1 issue に open CL が複数)
- **事前条件**: —
- **成功保証**: anomaly の issue ごとに、見送り (log に理由) か人へ返す (`ready-for-human` + 引き渡しコメント = 外部 store に残る) のどちらかの判断が下りている。silent に消えた観測が無い

**Main Success Scenario**

1. CLI が分類できない観測を `anomaly` 指示として書き、orchestrator を起動する
2. orchestrator が外部 store を読み直して状況を確かめる
3. orchestrator が「様子見 (見送り)」を既定に、放置すると誤った着手や二重作業が起きると読めた issue だけを「人へ返す」と判断する
4. orchestrator が anomaly の issue ごとの判断と理由を決定ファイルに書いて終了する。CLI が網羅を検査して log へ写す

**Extensions**

- **3a.** stale wip (worker が剥がさず死んだ) と確信できる
  - 3a1. それでも orchestrator は見送りでは wip を剥がさない — 回収は triage の人間判断 (誤回収で稼働中の作業を潰さない)。人へ返すと決めたときだけ、`ready-for-human` と引き換えに剥がす

## UC-6 project を導入する

- **Primary Actor**: 人間 (導入者)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: 新しい project で dispatcher を使い始める
- **事前条件**: claude-dispatcher・Claude Code・plugin `swat-skills`・gh が導入され、gh と claude が認証済み
- **成功保証**: 綴り誤りの config で観測が始まっていない。試運転で worker が着手していない。loop が回り始めている

**Main Success Scenario**

1. 導入者が実装 repo の clone で `setup` を実行し、CLI が宣言 config の雛形と state dir を作る
2. 導入者が宣言 config (issue 置き場 / CL 置き場 / 着手可 label / 上限 N) を埋める
3. CLI が導入者の承認を得て、機構が付ける label を issue 置き場に作る
4. CLI が config を検査して置き場 repo の実在を loud に確かめ、観測と指示の導出までを試運転し、何も起動せず何も書かずに指示の件数を示す
5. CLI が loop の起動コマンドを示して終わる
6. 導入者が `doctor` で Claude Code の settings に要る entry を確かめて自分で足す
7. 導入者が clone で loop を起動し、画面で最初の tick が回ったことを確かめる

**Extensions**

- **4a.** config の綴りが誤っている (未知 key / 実在しない repo)
  - 4a1. CLI が名指しで失敗し、観測を開始しない。導入者が直して step 4 へ戻る
- **7a.** 同じ project の loop が既に走っている
  - 7a1. CLI は 2 本目の loop を起動時に拒む。導入者は走っている loop の画面を見る
- **7b.** loop の画面で tick が `auth_error` になる (起動した環境から認証を取れない — ssh 越しの session 等)
  - 7b1. 導入者が宣言 config に token file を足す。loop は撃ち直さなくてよい (次の tick が config を読み直す)

## UC-7 loop を止める

- **Primary Actor**: 人間 (loop を起動した運用者)
- **Scope**: dispatcher (機械システム)
- **Level**: user-goal (sea)
- **Trigger**: 運用者が dispatcher を止めたい (端末を閉じる・binary を更新する・使うのをやめる)
- **事前条件**: loop が走っている
- **成功保証**: 新しい tick が始まらない。止めた時点で走っていた tick は log.jsonl に行を残している。起動済みの worker は止めずに走り続け、CL を出すか人へ返して wip を剥がす

**Main Success Scenario**

1. 運用者が loop に停止を求める (Ctrl+C)
2. loop が停止待ちを画面に示し、実行中の tick を最後まで進める (orchestrator の終了を待ち、決定どおり worker を起動し、tick 行を書く)
3. loop が画面を残し、停止の理由と、止めずに走っている worker の本数を 1 行足して終わる

**Extensions**

- **1a.** tick の合間 (sleep 中) に停止を求めた
  - 1a1. loop は tick を始めずに step 3 へ進む
- **1b.** 端末を閉じた (SIGHUP)
  - 1b1. 1 回目の停止要求と同じく step 2 へ進む。画面は見えないので、止まったことは別の端末の `status` で確かめる
- **2a.** 運用者が tick の終わりを待てず、もう一度停止を求めた
  - 2a1. loop は orchestrator を起動する前ならそれを起動せず、起動中ならその process group を止め、決定ファイルを読まずに error の tick 行を書く。orchestrator が正常終了した後なら、決定どおりの worker の起動を終えてから止まる (step 3 へ)
  - 2a2. loop は終了行に orchestrator log の path を示し、wip を付けたまま残った issue を確かめるよう促して終わる。残った wip は stale wip として人が回収する
