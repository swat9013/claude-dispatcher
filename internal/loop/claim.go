package loop

import (
	"fmt"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/worker"
)

// claim の遷移 (docs/design/statechart.puml の claim の 4 つの段階 Running・Stopping・AwaitingVerification・WaitingRetry)。
// 入口は出来事をまとめた 7 つ (workerUnstopped・workerEnded・observed・stopRequested・backoffElapsed・takenOverBy・loopEnded)。入口は claim の段階を
// 進め、書くログの行と loop がする I/O を effect で返す。I/O の実行と、claim の記録 (l.claims) からの削除は loop の apply
// がする。打ち切った作業対象が解けるか (stillAbandoned) は claim の外に残す (打ち切った作業対象は claim を持たない)。

// stopRequests は受けた停止要求 (なし・1 回目・2 回目)。2 回目より後の要求は 2 回目と同じに扱う。
type stopRequests int

const (
	stopNone stopRequests = iota
	// stopFirst は 1 回目の停止要求: 新しい起動をやめ、走っている worker の終わりを待つ
	stopFirst
	// stopSecond は 2 回目の停止要求: 走っている worker を止める
	stopSecond
)

// next は停止要求をもう 1 回受けた後の値。
func (s stopRequests) next() stopRequests {
	if s == stopNone {
		return stopFirst
	}
	return stopSecond
}

// requested は停止要求を 1 回以上受けたか。
func (s stopRequests) requested() bool { return s != stopNone }

// exit は claim の出口。
type exit int

const (
	// stay は claim を持ったまま
	stay exit = iota
	// release は claim を解く
	release
	// abandon は claim を解いて打ち切る
	abandon
)

// logLine はログの 1 行。errorMessage があれば error の行 (recorder.error)、無ければ event の行。
type logLine struct {
	errorMessage string
	event        string
	fields       map[string]any
	kind         LineKind
	label        string
	format       string
	args         []any
}

// effect は入口が返す、書くログの行と loop がする I/O。loop の apply は removeWorkspace・lines・stopWorker・startWorker・
// exit の順に実行する。readItem だけは apply でなく呼び出し側 (loop.handle) が受ける。
type effect struct {
	// removeWorkspace は workspace を消す (作業対象が終端)
	removeWorkspace bool
	lines           []logLine
	// stopWorker は走っている worker を止める
	stopWorker bool
	// startWorker は今の attempt の worker を起動する
	startWorker bool
	// readItem は作業対象を読み直し、結果を observed の入口へ渡す (終わりを確かめる)
	readItem bool
	exit     exit
}

// claimEnv は入口が段階を進めるのに読む、loop のそのときの値。
type claimEnv struct {
	now             time.Time
	stop            stopRequests
	maxAttempts     int
	maxRetryBackoff time.Duration
	maxConcurrent   int
}

// workerUnstopped は、止めるときに SIGKILL を送れず claude が終わっていないことの知らせを受ける。claude がまだ workspace と
// session を使いうるので、確かめも再起動も workspace の削除もせず、claude が終わって worker の終わりが届くまで止めている段階で
// 持つ (system.md §7 の tick の手順 1)。
func (c *claim) workerUnstopped(stopError string, exited <-chan struct{}) effect {
	// どちらも起きれば loop の誤り
	if c.phase != phaseRunning && c.phase != phaseStopping {
		return effect{lines: []logLine{errorLine("走っていない claim の worker の止められない知らせを受けた")}}
	}
	if c.exited != nil {
		return effect{lines: []logLine{errorLine("同じ worker の止められない知らせを 2 回受けた")}}
	}
	c.phase, c.exited = phaseStopping, exited
	return effect{lines: []logLine{errorLine(fmt.Sprintf("claude を止められない (pid %d, workspace %s): 終わるまで workspace を残し、終わりを確かめず再起動もしない: %s", c.pid, c.workspace, strings.ReplaceAll(stopError, "\n", " / ")))}}
}

// workerEnded は worker の終わりを受ける。stall 上限・上限時間で止まった worker も、ここで worker の終わりとして受ける
// (理由は result の Failure が持つ)。止められなかった claude が後で終わったときも、ここで受ける。止めている worker が
// 止められて終われば claim を解き、それ以外は終わりを確かめ待ちへ進めて作業対象を読み直させる。
func (c *claim) workerEnded(ref target.Ref, result worker.Result) effect {
	if c.phase != phaseRunning && c.phase != phaseStopping {
		// 走っていない claim に終わる worker は無い (起きれば loop の誤り)
		return effect{lines: []logLine{errorLine("走っていない claim の worker の終わりを受けた")}}
	}
	c.run, c.exited = nil, nil
	if c.phase == phaseStopping && (result.Stopped || c.takenOver) {
		// 乗っ取られた worker は、止める前に終わっていても終わりを確かめない。乗っ取った loop が同じ作業対象を作業している
		reason := Reason(c.stopReason)
		if c.takenOver {
			reason = reasonTakenOver
		}
		return effect{
			removeWorkspace: c.stopReason == stoppedAtTerminal && result.Stopped,
			lines:           []logLine{c.endLine(ref, stopped, reason, result)},
			exit:            release,
		}
	}
	c.phase, c.ended = phaseAwaitingVerification, &result
	return effect{readItem: true}
}

// observation は作業対象の読み直しの結果: 02 (Trigger.Observe) の 4 値か、読み直しの失敗。
type observation struct {
	observed trigger.Observation
	// readErr は読み直しの失敗。nil でなければ observed を見ない
	readErr error
	// current は再起動待ちの claim が続ける trigger をそのときの workflow 定義から引き直したもの。読み直せたら claim の trigger をこれに替える
	current *trigger.Trigger
}

// observed は作業対象の読み直しの観測を受ける。走っている claim は終端でだけ止め (ADR 0009)、確かめ待ちの claim は終わり方
// を決め、再起動待ちの claim は終端か trigger から外れていれば起動せずに解く。止めている claim の作業対象は読み直さない。
func (c *claim) observed(ref target.Ref, o observation, env claimEnv) effect {
	switch c.phase {
	case phaseRunning:
		if o.readErr != nil {
			return effect{lines: []logLine{errorLine("走っている worker の作業対象を読み直せない: " + oneLine(o.readErr))}}
		}
		if o.observed == trigger.ObservedTerminal {
			c.phase, c.stopReason = phaseStopping, stoppedAtTerminal
			return effect{stopWorker: true}
		}
		return effect{}
	case phaseAwaitingVerification:
		return c.verified(ref, o, env)
	case phaseWaitingRetry:
		if o.readErr != nil {
			return effect{lines: []logLine{errorLine("再起動を待つ作業対象を読み直せない (次の tick で読み直す): " + oneLine(o.readErr))}}
		}
		if o.current != nil {
			c.trigger = *o.current
		}
		switch o.observed {
		case trigger.ObservedTerminal, trigger.ObservedLeftTrigger:
			return effect{removeWorkspace: o.observed == trigger.ObservedTerminal, lines: []logLine{c.releaseLine(ref, reasonOf(o.observed))}, exit: release}
		}
		// 当たるか未決なら待ち直す。当たったままは backoffElapsed で再起動する条件として読む
		return effect{}
	}
	return effect{}
}

// verified は終わりを確かめ待ちの claim の終わり方を決める。終端か trigger から外れていれば、worker 自身の失敗に関わらず
// 完了として claim を解く。当たったままなら失敗とし、attempt の上限で打ち切るか、再起動待ちへ進める。読み直せないか当たるかを
// まだ決められなければ、claim を持ったまま次の tick で確かめ直す (完了とも失敗とも数えない)。
func (c *claim) verified(ref target.Ref, o observation, env claimEnv) effect {
	if o.readErr != nil {
		return effect{lines: []logLine{errorLine("終わった worker の作業対象を読み直せない (次の tick で読み直す): " + oneLine(o.readErr))}}
	}
	result := *c.ended
	switch o.observed {
	case trigger.ObservedUndecided:
		// 失敗ではない正常な待ちなので、error ではなく verify_wait の行で残す
		return effect{lines: []logLine{{
			event: "verify_wait", fields: map[string]any{
				"target": ref.String(), "trigger": c.trigger.Name, "attempt": c.attempt,
				"session_id": c.sessionID, "reason": reasonUndecided,
			}, kind: LineNote, label: "確かめ待ち", format: " %s (%s, attempt %d): %s",
			args: []any{ref, c.trigger.Name, c.attempt, reasonUndecided.screen()},
		}}}
	case trigger.ObservedTerminal, trigger.ObservedLeftTrigger:
		return effect{
			removeWorkspace: o.observed == trigger.ObservedTerminal,
			lines:           []logLine{c.endLine(ref, completed, reasonOf(o.observed), result)},
			exit:            release,
		}
	}
	lines := []logLine{c.endLine(ref, failed, reasonStillMatches, result)}
	name := ref.String()
	if c.attempt >= env.maxAttempts {
		return effect{lines: append(lines, logLine{
			event:  "abandon",
			fields: map[string]any{"target": name, "trigger": c.trigger.Name, "attempt": c.attempt, "session_id": c.sessionID},
			kind:   LineNote, label: "打ち切り", format: " %s (%s, attempt %d 回): label を外して trigger から外すと解ける (外してから 1 周期待つ)",
			args: []any{name, c.trigger.Name, c.attempt},
		}), exit: abandon}
	}
	// 前の attempt の起動の時刻と活動は、次の attempt を起動するまで持たない
	c.phase, c.ended, c.retryAt, c.startedAt, c.activity = phaseWaitingRetry, nil, time.Time{}, nil, nil
	if env.stop.requested() {
		// 停止要求を受けた後は再起動を予定しない (loop は再起動待ちを error の行に残して止まる)
		return effect{lines: lines}
	}
	wait := backoff(c.attempt, env.maxRetryBackoff)
	c.retryAt = env.now.Add(wait)
	return effect{lines: append(lines, logLine{
		event: "retry", fields: map[string]any{
			"target": name, "trigger": c.trigger.Name, "attempt": c.attempt, "next_attempt": c.attempt + 1,
			"session_id": c.sessionID, "backoff": wait.Seconds(),
		}, kind: LineRetry, label: "再起動を予定", format: " %s (%s, attempt %d, %s 後)",
		args: []any{name, c.trigger.Name, c.attempt + 1, wait},
	})}
}

// stopRequested は停止要求を受ける。2 回目で走っている worker を止める。1 回目ではどの段階も変わらない (再起動待ちは、
// 停止要求の後に再起動を予定しないことで止まる)。
func (c *claim) stopRequested(requests stopRequests) effect {
	if requests == stopSecond && c.phase == phaseRunning {
		c.phase, c.stopReason = phaseStopping, stoppedByRequest
		return effect{stopWorker: true}
	}
	return effect{}
}

// hindrance は backoff の明けた再起動待ちの claim の起動を妨げる事情。
type hindrance int

const (
	// hindranceNone は妨げが無い (作業対象は当たったまま)
	hindranceNone hindrance = iota
	// hindranceNoSlot は並列上限に空きが無い
	hindranceNoSlot
	// hindranceAmbiguousCL は CL が tick の一覧で曖昧に見える
	hindranceAmbiguousCL
	// hindranceBranchHeld は CL の head branch を別の claim の workspace が checkout している (か branch を読めない)
	hindranceBranchHeld
	// hindranceEarlierTrigger は宣言順で先の trigger に当たる。再起動は同じ trigger で続ける (system.md §7) ので、loop は
	// 渡さない。statechart の組を表で引けるよう値を持つ
	hindranceEarlierTrigger
	// hindrancePrecheck は trigger が事前検査に落ちている
	hindrancePrecheck
	// hindranceDefinitionUnavailable は workflow 定義を採れない。その tick は retry を撃たないので、loop は渡さない
	hindranceDefinitionUnavailable
	// hindranceTriggerRemoved は起動した trigger が workflow 定義から消えた
	hindranceTriggerRemoved
	// hindranceNewerFormat は調停 remote に自分の知る版より新しい版の ref がある
	hindranceNewerFormat
)

// backoffElapsed は再起動待ちの claim の backoff の明けを受ける。妨げが無ければ次の attempt を起動し、trigger が消えて
// いれば起動せずに claim を解く。ほかの妨げでは attempt を進めずに待ち直す (曖昧に見えても claim は解かない。formats.md §6
// の再起動)。
func (c *claim) backoffElapsed(ref target.Ref, h hindrance, item target.Item, env claimEnv) effect {
	if c.phase != phaseWaitingRetry {
		return effect{}
	}
	switch h {
	case hindranceNone:
		c.item, c.waitingSlot = item, false
		c.attempt++
		c.phase, c.startedAt, c.activity, c.pid, c.workspace = phaseRunning, nil, nil, 0, ""
		return effect{startWorker: true}
	case hindranceNoSlot:
		if c.waitingSlot {
			return effect{}
		}
		c.waitingSlot = true
		return effect{lines: []logLine{{
			event:  "wait_slot",
			fields: map[string]any{"target": ref.String(), "trigger": c.trigger.Name, "next_attempt": c.attempt + 1, "max_concurrent": env.maxConcurrent},
			kind:   LineNote, label: "再起動を待つ", format: " %s (%s, attempt %d): 並列上限 %d に空きが出るまで待つ",
			args: []any{ref, c.trigger.Name, c.attempt + 1, env.maxConcurrent},
		}}}
	case hindranceTriggerRemoved:
		return effect{lines: []logLine{c.releaseLine(ref, reasonTriggerRemoved)}, exit: release}
	}
	return effect{}
}

// takenOverBy は、自分の着手宣言が乗っ取られた (sha が変わったか消えた) ことを受ける。走っている worker は止め、止めている
// worker は終われば claim を解く。終わりを確かめ待ちと再起動待ちの claim はその場で解く (system.md §7 の調停 remote の突き合わせ)。
func (c *claim) takenOverBy(ref target.Ref, by string) effect {
	c.claimSHA, c.takenOver = "", true
	line := logLine{
		event: "taken_over", fields: map[string]any{"target": ref.String(), "trigger": c.trigger.Name, "by": by},
		kind: LineError, label: "乗っ取られた", format: " %s (%s): %s",
		args: []any{ref, c.trigger.Name, holderScreen(by)},
	}
	switch c.phase {
	case phaseRunning:
		c.phase, c.stopReason = phaseStopping, stoppedByTakeover
		return effect{lines: []logLine{line}, stopWorker: true}
	case phaseAwaitingVerification, phaseWaitingRetry:
		return effect{lines: []logLine{line, c.releaseLine(ref, reasonTakenOver)}, exit: release}
	}
	return effect{lines: []logLine{line}}
}

// holderScreen は乗っ取られた着手宣言の今の持ち主を人が読む文。
func holderScreen(by string) string {
	switch by {
	case "":
		return "着手宣言が消えていた"
	case "?":
		return "持ち主を読めない着手宣言に置き換わっていた"
	}
	return "loop " + by + " が着手宣言を持っている"
}

// newClaim は候補になった作業対象に立てる claim。attempt 1 の worker が走っている段階で始まる。
func newClaim(item target.Item, t trigger.Trigger, sessionID, workspaceRoot, claimSHA string) *claim {
	return &claim{item: item, trigger: t, attempt: 1, sessionID: sessionID, workspaceRoot: workspaceRoot, phase: phaseRunning, claimSHA: claimSHA}
}

// loopEnded は loop の終了を受ける。claim を持ったまま止まることを error の行に残す。
func (c *claim) loopEnded() effect {
	if c.claudeAlive() {
		return effect{lines: []logLine{errorLine(fmt.Sprintf("claude を止められないまま止まる (pid %d, workspace %s)", c.pid, c.workspace))}}
	}
	if c.exited != nil {
		// 停止を決めた後に claude が終わった。worker の後始末は loop の終了で途切れうる
		return effect{lines: []logLine{errorLine(fmt.Sprintf("止められなかった claude は終わったが、after_run を待たずに止まる (workspace %s)", c.workspace))}}
	}
	if c.phase == phaseWaitingRetry {
		return effect{lines: []logLine{errorLine(fmt.Sprintf("再起動を待ったまま止まる (%s, attempt %d まで)", c.trigger.Name, c.attempt))}}
	}
	return effect{lines: []logLine{errorLine(fmt.Sprintf("終わり方を確かめられないまま止まる (%s)", c.trigger.Name))}}
}

// claudeAlive は、止められなかった claude がまだ終わっていないか。終わっていれば、worker は after_run を撃ってから終わりを
// 送るので、loop の停止はその終わりを待てる。
func (c *claim) claudeAlive() bool {
	if c.exited == nil {
		return false
	}
	select {
	case <-c.exited:
		return false
	default:
		return true
	}
}

func errorLine(message string) logLine { return logLine{errorMessage: message} }

// releaseLine は再起動を待つ claim を、起動せずに解いた行。
func (c *claim) releaseLine(ref target.Ref, reason Reason) logLine {
	name := ref.String()
	return logLine{
		event:  "release",
		fields: map[string]any{"target": name, "trigger": c.trigger.Name, "attempt": c.attempt, "session_id": c.sessionID, "reason": reason},
		kind:   LineNote, label: "再起動せずに解いた", format: " %s (%s): %s",
		args: []any{name, c.trigger.Name, reason.screen()},
	}
}

// endLine は worker 1 回分の終わり方の行。worker 自身が失敗していれば、その理由を reason とは別の failure に載せる。
func (c *claim) endLine(ref target.Ref, o outcome, reason Reason, result worker.Result) logLine {
	name := ref.String()
	fields := map[string]any{
		"target": name, "trigger": c.trigger.Name, "attempt": c.attempt, "session_id": c.sessionID,
		"outcome": string(o), "reason": reason,
	}
	if result.Failure != "" {
		fields["failure"] = result.Failure
	}
	if result.ExitCode != nil {
		fields["exit_code"] = *result.ExitCode
	}
	if summary := result.ResultSummary; summary != nil {
		if summary.IsError != nil {
			fields["is_error"] = *summary.IsError
		}
		if summary.NumTurns != nil {
			fields["num_turns"] = *summary.NumTurns
		}
		if summary.PermissionDenialCount != nil {
			fields["permission_denial_count"] = *summary.PermissionDenialCount
		}
	}
	return logLine{event: "end", fields: fields, kind: endKinds[o], label: "終了", format: " %s (%s): %s — %s",
		args: []any{name, c.trigger.Name, o, endScreen(o, reason, result.Failure)}}
}
