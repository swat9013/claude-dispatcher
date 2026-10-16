package loop

import (
	"errors"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/worker"
)

// 段階 × 入口の組の表。docs/design/statechart.puml の claim の 4 つの段階 (Running・Stopping・AwaitingVerification・
// WaitingRetry) から出る遷移と、その 4 段階で uncovered とした組を、statechart を見ながら手で 1 行ずつ写す。
// 「候補になる」は claim の無い作業対象の出来事なので、claim の入口を持たない (4 段階ではどれも uncovered)。

// claimIO は effect のうち loop がする I/O と出口。
type claimIO struct {
	stopWorker, startWorker, removeWorkspace, readItem bool
	exit                                               exit
}

type claimEvent struct {
	name string
	fire func(c *claim, ref target.Ref, env claimEnv) effect
}

func ended(result worker.Result) func(*claim, target.Ref, claimEnv) effect {
	return func(c *claim, ref target.Ref, _ claimEnv) effect { return c.workerEnded(ref, result) }
}

func seen(o observation) func(*claim, target.Ref, claimEnv) effect {
	return func(c *claim, ref target.Ref, env claimEnv) effect { return c.observed(ref, o, env) }
}

func stopAsked(s stopRequests) func(*claim, target.Ref, claimEnv) effect {
	return func(c *claim, _ target.Ref, _ claimEnv) effect { return c.stopRequested(s) }
}

func elapsed(h hindrance) func(*claim, target.Ref, claimEnv) effect {
	return func(c *claim, ref target.Ref, env claimEnv) effect { return c.backoffElapsed(ref, h, nil, env) }
}

func loopEnd(c *claim, _ target.Ref, _ claimEnv) effect { return c.loopEnded() }

var (
	workerEnd      = claimEvent{"worker終了", ended(worker.Result{})}
	stoppedEnd     = claimEvent{"worker終了 [止められて終わった]", ended(worker.Result{Stopped: true})}
	stall          = claimEvent{"stall上限", ended(worker.Result{Failure: "stall"})}
	timeLimit      = claimEvent{"上限時間", ended(worker.Result{Failure: "上限時間"})}
	terminal       = claimEvent{"終端の観測", seen(observation{observed: trigger.ObservedTerminal})}
	leftTrigger    = claimEvent{"trigger外れの観測", seen(observation{observed: trigger.ObservedLeftTrigger})}
	stillMatches   = claimEvent{"当たったままの観測", seen(observation{observed: trigger.ObservedStillMatches})}
	undecided      = claimEvent{"当たるか未決", seen(observation{observed: trigger.ObservedUndecided})}
	readFailure    = claimEvent{"読み直しの失敗", seen(observation{readErr: errors.New("読めない")})}
	firstStop      = claimEvent{"停止要求1回目", stopAsked(stopFirst)}
	secondStop     = claimEvent{"停止要求2回目", stopAsked(stopSecond)}
	backoffDone    = claimEvent{"backoffの明け", elapsed(hindranceNone)}
	noSlot         = claimEvent{"空きが無い", elapsed(hindranceNoSlot)}
	ambiguousCL    = claimEvent{"曖昧なCL", elapsed(hindranceAmbiguousCL)}
	branchHeld     = claimEvent{"branchの重なり", elapsed(hindranceBranchHeld)}
	earlierTrigger = claimEvent{"先のtriggerに当たる", elapsed(hindranceEarlierTrigger)}
	precheckFailed = claimEvent{"事前検査に落ちた", elapsed(hindrancePrecheck)}
	noDefinition   = claimEvent{"workflow定義を採れない", elapsed(hindranceDefinitionUnavailable)}
	triggerRemoved = claimEvent{"triggerの削除", elapsed(hindranceTriggerRemoved)}
	loopEnded      = claimEvent{"loopの終了", loopEnd}
)

func TestClaimTransitions(t *testing.T) {
	const maxAttempts = 3
	noIO := claimIO{}
	tests := []struct {
		from    phase
		attempt int
		event   claimEvent
		want    phase
		io      claimIO
	}{
		// --- 走っている ---
		{phaseRunning, 1, workerEnd, phaseAwaitingVerification, claimIO{readItem: true}},
		{phaseRunning, 1, stall, phaseAwaitingVerification, claimIO{readItem: true}},
		{phaseRunning, 1, timeLimit, phaseAwaitingVerification, claimIO{readItem: true}},
		{phaseRunning, 1, terminal, phaseStopping, claimIO{stopWorker: true}},
		{phaseRunning, 1, firstStop, phaseRunning, noIO},
		{phaseRunning, 1, secondStop, phaseStopping, claimIO{stopWorker: true}},
		{phaseRunning, 1, loopEnded, phaseRunning, noIO},
		// uncovered
		{phaseRunning, 1, leftTrigger, phaseRunning, noIO},
		{phaseRunning, 1, stillMatches, phaseRunning, noIO},
		{phaseRunning, 1, undecided, phaseRunning, noIO},
		{phaseRunning, 1, readFailure, phaseRunning, noIO},
		{phaseRunning, 1, backoffDone, phaseRunning, noIO},
		{phaseRunning, 1, noSlot, phaseRunning, noIO},
		{phaseRunning, 1, ambiguousCL, phaseRunning, noIO},
		{phaseRunning, 1, branchHeld, phaseRunning, noIO},
		{phaseRunning, 1, precheckFailed, phaseRunning, noIO},
		{phaseRunning, 1, noDefinition, phaseRunning, noIO},
		{phaseRunning, 1, earlierTrigger, phaseRunning, noIO},
		{phaseRunning, 1, triggerRemoved, phaseRunning, noIO},

		// --- 止めている ---
		{phaseStopping, 1, stoppedEnd, phaseStopping, claimIO{removeWorkspace: true, exit: release}},
		{phaseStopping, 1, workerEnd, phaseAwaitingVerification, claimIO{readItem: true}},
		{phaseStopping, 1, stall, phaseAwaitingVerification, claimIO{readItem: true}},
		{phaseStopping, 1, timeLimit, phaseAwaitingVerification, claimIO{readItem: true}},
		{phaseStopping, 1, loopEnded, phaseStopping, noIO},
		// uncovered
		{phaseStopping, 1, terminal, phaseStopping, noIO},
		{phaseStopping, 1, leftTrigger, phaseStopping, noIO},
		{phaseStopping, 1, stillMatches, phaseStopping, noIO},
		{phaseStopping, 1, undecided, phaseStopping, noIO},
		{phaseStopping, 1, readFailure, phaseStopping, noIO},
		{phaseStopping, 1, backoffDone, phaseStopping, noIO},
		{phaseStopping, 1, noSlot, phaseStopping, noIO},
		{phaseStopping, 1, ambiguousCL, phaseStopping, noIO},
		{phaseStopping, 1, branchHeld, phaseStopping, noIO},
		{phaseStopping, 1, precheckFailed, phaseStopping, noIO},
		{phaseStopping, 1, noDefinition, phaseStopping, noIO},
		{phaseStopping, 1, firstStop, phaseStopping, noIO},
		{phaseStopping, 1, secondStop, phaseStopping, noIO},
		{phaseStopping, 1, earlierTrigger, phaseStopping, noIO},
		{phaseStopping, 1, triggerRemoved, phaseStopping, noIO},

		// --- 終わりを確かめ待ち ---
		{phaseAwaitingVerification, 1, terminal, phaseAwaitingVerification, claimIO{removeWorkspace: true, exit: release}},
		{phaseAwaitingVerification, 1, leftTrigger, phaseAwaitingVerification, claimIO{exit: release}},
		{phaseAwaitingVerification, 1, stillMatches, phaseWaitingRetry, noIO},
		{phaseAwaitingVerification, maxAttempts, stillMatches, phaseAwaitingVerification, claimIO{exit: abandon}},
		{phaseAwaitingVerification, 1, readFailure, phaseAwaitingVerification, noIO},
		{phaseAwaitingVerification, 1, undecided, phaseAwaitingVerification, noIO},
		{phaseAwaitingVerification, 1, loopEnded, phaseAwaitingVerification, noIO},
		// uncovered
		{phaseAwaitingVerification, 1, workerEnd, phaseAwaitingVerification, noIO},
		{phaseAwaitingVerification, 1, stall, phaseAwaitingVerification, noIO},
		{phaseAwaitingVerification, 1, timeLimit, phaseAwaitingVerification, noIO},
		{phaseAwaitingVerification, 1, backoffDone, phaseAwaitingVerification, noIO},
		{phaseAwaitingVerification, 1, noSlot, phaseAwaitingVerification, noIO},
		{phaseAwaitingVerification, 1, ambiguousCL, phaseAwaitingVerification, noIO},
		{phaseAwaitingVerification, 1, branchHeld, phaseAwaitingVerification, noIO},
		{phaseAwaitingVerification, 1, precheckFailed, phaseAwaitingVerification, noIO},
		{phaseAwaitingVerification, 1, noDefinition, phaseAwaitingVerification, noIO},
		{phaseAwaitingVerification, 1, firstStop, phaseAwaitingVerification, noIO},
		{phaseAwaitingVerification, 1, secondStop, phaseAwaitingVerification, noIO},
		{phaseAwaitingVerification, 1, earlierTrigger, phaseAwaitingVerification, noIO},
		{phaseAwaitingVerification, 1, triggerRemoved, phaseAwaitingVerification, noIO},

		// --- 再起動待ち ---
		{phaseWaitingRetry, 1, backoffDone, phaseRunning, claimIO{startWorker: true}},
		{phaseWaitingRetry, 1, noSlot, phaseWaitingRetry, noIO},
		{phaseWaitingRetry, 1, readFailure, phaseWaitingRetry, noIO},
		{phaseWaitingRetry, 1, undecided, phaseWaitingRetry, noIO},
		{phaseWaitingRetry, 1, ambiguousCL, phaseWaitingRetry, noIO},
		{phaseWaitingRetry, 1, branchHeld, phaseWaitingRetry, noIO},
		{phaseWaitingRetry, 1, earlierTrigger, phaseWaitingRetry, noIO},
		{phaseWaitingRetry, 1, precheckFailed, phaseWaitingRetry, noIO},
		{phaseWaitingRetry, 1, noDefinition, phaseWaitingRetry, noIO},
		{phaseWaitingRetry, 1, firstStop, phaseWaitingRetry, noIO},
		{phaseWaitingRetry, 1, terminal, phaseWaitingRetry, claimIO{removeWorkspace: true, exit: release}},
		{phaseWaitingRetry, 1, leftTrigger, phaseWaitingRetry, claimIO{exit: release}},
		{phaseWaitingRetry, 1, triggerRemoved, phaseWaitingRetry, claimIO{exit: release}},
		{phaseWaitingRetry, 1, loopEnded, phaseWaitingRetry, noIO},
		// uncovered
		{phaseWaitingRetry, 1, workerEnd, phaseWaitingRetry, noIO},
		{phaseWaitingRetry, 1, stall, phaseWaitingRetry, noIO},
		{phaseWaitingRetry, 1, timeLimit, phaseWaitingRetry, noIO},
		{phaseWaitingRetry, 1, stillMatches, phaseWaitingRetry, noIO},
		{phaseWaitingRetry, 1, secondStop, phaseWaitingRetry, noIO},
	}
	for _, tt := range tests {
		t.Run(phaseNames[tt.from]+" x "+tt.event.name, func(t *testing.T) {
			ref := target.Ref{Kind: target.KindIssue, Number: 1}
			w := &FakeWorker{}
			c := &claim{phase: tt.from, attempt: tt.attempt, trigger: trigger.Trigger{Name: "t"}, stopReason: stoppedAtTerminal}
			if tt.from == phaseRunning || tt.from == phaseStopping {
				c.run = w
			}
			if tt.from == phaseAwaitingVerification {
				c.ended = &worker.Result{}
			}
			env := claimEnv{now: time.Unix(0, 0), maxAttempts: maxAttempts, maxRetryBackoff: time.Minute, maxConcurrent: 1}

			e := tt.event.fire(c, ref, env)

			if c.phase != tt.want {
				t.Errorf("段階 = %s, want %s", phaseNames[c.phase], phaseNames[tt.want])
			}
			got := claimIO{stopWorker: e.stopWorker, startWorker: e.startWorker, removeWorkspace: e.removeWorkspace, readItem: e.readItem, exit: e.exit}
			if got != tt.io {
				t.Errorf("I/O = %+v, want %+v", got, tt.io)
			}
		})
	}
}

func TestLoopEndedLeavesAnErrorLine(t *testing.T) {
	for _, p := range []phase{phaseAwaitingVerification, phaseWaitingRetry} {
		c := &claim{phase: p, attempt: 2, trigger: trigger.Trigger{Name: "t"}}
		if lines := c.loopEnded().lines; len(lines) != 1 || lines[0].errorMessage == "" {
			t.Errorf("%s の loop の終了の行 = %+v, want error の行 1 つ", phaseNames[p], lines)
		}
	}
}

func TestStopRequestsSaturateAtSecond(t *testing.T) {
	s := stopNone.next().next().next()
	if s != stopSecond {
		t.Errorf("3 回目の停止要求の後 = %d, want stopSecond", s)
	}
}

func TestBackoffAfterStopRequestDoesNotSchedule(t *testing.T) {
	c := &claim{phase: phaseAwaitingVerification, attempt: 1, ended: &worker.Result{}, trigger: trigger.Trigger{Name: "t"}}
	env := claimEnv{now: time.Unix(0, 0), stop: stopFirst, maxAttempts: 3, maxRetryBackoff: time.Minute}

	e := c.observed(target.Ref{Kind: target.KindIssue, Number: 1}, observation{observed: trigger.ObservedStillMatches}, env)

	if c.phase != phaseWaitingRetry || !c.retryAt.IsZero() || len(e.lines) != 1 {
		t.Errorf("段階 = %s, retryAt = %v, 行 = %d, want 再起動待ち・予定なし・終了の行だけ", phaseNames[c.phase], c.retryAt, len(e.lines))
	}
}

var phaseNames = map[phase]string{
	phaseRunning: "Running", phaseStopping: "Stopping", phaseAwaitingVerification: "AwaitingVerification", phaseWaitingRetry: "WaitingRetry",
}
