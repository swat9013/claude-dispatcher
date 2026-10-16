package loop_test

import (
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/precheck"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/worker"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// backoff の明けを tick を待たずに知らせる wake と、明けた後に起動できなかった再起動待ちの claim の試み直し
// (formats.md §6 の再起動)。試み直しは tick と worker の終わりで行い、wake を置き直さない。

const wakeInterval = time.Minute

// wakeLoop は 1 周期を wakeInterval とする loop。時計は tick を始めるたびに tick の時刻に合わせ、届けた wake の分だけ進む。
type wakeLoop struct {
	// def は tick (1 始まり) ごとの workflow 定義
	def func(tick int) workflow.Definition
	// open は open な一覧が返す作業対象。read は置き場からの読み直しが返すもので、loaded は workflow 定義を読んだ回数
	// (tick は突き合わせの後に workflow 定義を読むので、tick n の突き合わせでは n−1)
	open []target.Item
	read func(loaded int, ref target.Ref) target.Item
	// stoppable は Stop されるまで走り続ける worker の作業対象。ほかの worker は起動するとすぐに失敗で終わる
	stoppable target.Ref
	// precheck は事前検査。nil なら、どの trigger も通す
	precheck func(workflow.Definition) []precheck.Problem
	// deliverWakes は wake を届けるか (届けなければ、再起動は tick と worker の終わりでだけ試みる)
	deliverWakes bool
	// period は n 回目 (1 始まり) の周期の扱いを返す。周期も wake も、起動した worker の event を loop が処理し終えてから、
	// 予定の時刻の順に明ける (backoff の wake は周期より早いので先に明ける)
	period func(n int, w *wakeRun) timing
}

// wakeRun は回している loop の観測。loop の goroutine だけが書き、Run から戻った後に読む (rig は jobs を持つ)。
type wakeRun struct {
	*rig
	tick int
	// wakes は置いた wake の数
	wakes int
	// launchesAtTick は tick ごとの、その tick で workflow 定義を読んだ時点の起動の数 (tick の再起動はその後に試みる)
	launchesAtTick map[int]int
}

// wakeSpinLimit を超えて wake を置かれたら、それ以上は届けない (wake が回り続けるときに test を終わらせる)
const wakeSpinLimit = 1000

func (s wakeLoop) run(t *testing.T) *wakeRun {
	t.Helper()
	w := &wakeRun{rig: newRig(), launchesAtTick: map[int]int{}}
	periods := 0
	w.timer = func(kind timerKind, _ time.Duration) timing {
		if kind == period {
			periods++
			p := s.period(periods, w)
			p.keepClock = true
			return p
		}
		w.wakes++
		if !s.deliverWakes || w.wakes > wakeSpinLimit {
			return timing{fate: neverFires}
		}
		return timing{fate: fires}
	}
	w.script = func(job worker.Job) workerScript {
		if job.Item.Ref() == s.stoppable {
			return workerScript{untilStopped: true}
		}
		return exits(1)
	}
	w.run(t, loop.Options{
		Load: func() (workflow.Definition, error) {
			w.tick++
			w.launchesAtTick[w.tick] = len(w.jobs)
			w.setClock(time.Duration(w.tick-1) * wakeInterval)
			return s.def(w.tick), nil
		},
		Definition: s.def(1),
		Precheck:   s.precheck,
		Store: func(workflow.Definition) loop.Store {
			return memoryStore{
				observe: func() ([]target.Item, error) { return s.open, nil },
				reread:  func(ref target.Ref) (target.Item, error) { return s.read(w.tick, ref), nil },
			}.store()
		},
	})
	return w
}

func TestWaitingRetryOfACLThatCannotRestartOutsideATickWakesTheLoopOnlyOnceBeforeTheNextTick(t *testing.T) {
	// CL は tick の中でだけ再起動を試みるので、backoff が明けても次の tick までは起動できない
	fixCI := trigger.Trigger{Name: "fix-ci", On: target.KindCL}
	cl := target.CL{Number: 7, Head: "feature", HeadRepo: "acme/widgets", SameRepo: true}
	var wakesBeforeSecondTick int
	s := wakeLoop{
		def: func(int) workflow.Definition {
			return workflow.Definition{Tracker: githubTracker, Interval: wakeInterval, MaxConcurrent: 1, MaxAttempts: 5, MaxRetryBackoff: 5 * time.Minute, Triggers: []trigger.Trigger{fixCI}}
		},
		open:         []target.Item{cl},
		read:         func(int, target.Ref) target.Item { return cl },
		deliverWakes: true,
		period: func(n int, w *wakeRun) timing {
			if n == 1 {
				// 最初の worker の終わりで置かれた backoff の wake (10s) が、周期 (1m) より先に明ける
				return timing{fate: fires}
			}
			wakesBeforeSecondTick = w.wakes
			return timing{fate: stopsTheLoop}
		},
	}

	s.run(t)

	if wakesBeforeSecondTick != 1 {
		t.Fatalf("2 回目の tick までに置いた wake = %d, want 1 (backoff の明けに 1 回だけ。起動できなかった後は tick を待つ)", wakesBeforeSecondTick)
	}
}

func TestWakeRestartsWithThePrecheckOfTheLastTick(t *testing.T) {
	// tick 1 の事前検査は通り、その後に先頭の skill が消える (事前検査を撃ち直せば落ちる)。tick の外の wake は事前検査を
	// 撃ち直さず、tick 1 の結果で再起動する
	checks := 0
	s := wakeLoop{
		def: func(int) workflow.Definition {
			return workflow.Definition{Tracker: githubTracker, Interval: wakeInterval, MaxConcurrent: 1, MaxAttempts: 2, MaxRetryBackoff: 5 * time.Minute, Triggers: []trigger.Trigger{readyTrigger}}
		},
		open: []target.Item{readyIssue(1)},
		read: func(_ int, ref target.Ref) target.Item { return readyIssue(ref.Number) },
		precheck: func(workflow.Definition) []precheck.Problem {
			checks++
			if checks == 1 {
				return nil
			}
			return []precheck.Problem{{Trigger: readyTrigger.Name, Error: "action の先頭の /implement が見つからない"}}
		},
		deliverWakes: true,
		period: func(n int, _ *wakeRun) timing {
			if n == 1 {
				return timing{fate: fires}
			}
			return timing{fate: stopsTheLoop}
		},
	}

	w := s.run(t)

	if got := w.launchesAtTick[2]; got != 2 {
		t.Fatalf("tick 2 の前に起動した worker = %d, want 2 (backoff の明けた wake で attempt 2 を起動する)", got)
	}
}

func TestWaitingRetryForASlotIsRestartedWhenAStoppedWorkerFreesTheSlot(t *testing.T) {
	// issue#1 の worker は失敗して再起動を待つ。tick 2 で並列上限が 1 に下がり、issue#2 の worker が走っているので空きを
	// 待つ。tick 3 の突き合わせで issue#2 が終端になって止まると、空いた枠で次の tick を待たずに issue#1 を再起動する
	waiting, running := target.Ref{Kind: target.KindIssue, Number: 1}, target.Ref{Kind: target.KindIssue, Number: 2}
	s := wakeLoop{
		def: func(tick int) workflow.Definition {
			maxConcurrent := 2
			if tick >= 2 {
				maxConcurrent = 1
			}
			return workflow.Definition{Tracker: githubTracker, Interval: wakeInterval, MaxConcurrent: maxConcurrent, MaxAttempts: 5, MaxRetryBackoff: 5 * time.Minute, Triggers: []trigger.Trigger{readyTrigger}}
		},
		open: []target.Item{readyIssue(1), readyIssue(2)},
		read: func(loaded int, ref target.Ref) target.Item {
			if ref == running && loaded >= 2 {
				return target.Issue{Number: 2, Labels: []string{"ready"}, Closed: true}
			}
			return readyIssue(ref.Number)
		},
		stoppable: running,
		// 周期 1 は issue#1 の終わりを、周期 2 は issue#2 の起動を、周期 3 は止めた issue#2 の終わりを loop が処理し終えて
		// から明ける (wake は届けないので、周期のほかに明けるものは無い)
		period: func(n int, _ *wakeRun) timing {
			if n <= 3 {
				return timing{fate: fires}
			}
			return timing{fate: stopsTheLoop}
		},
	}

	w := s.run(t)

	launchesOfWaiting := 0
	for _, job := range w.jobs[:w.launchesAtTick[4]] {
		if job.Item.Ref() == waiting {
			launchesOfWaiting++
		}
	}
	if launchesOfWaiting != 2 {
		t.Fatalf("tick 4 の前に起動した issue#1 = %d 回, want 2 (止めた worker の終わりで空いた枠で再起動する)", launchesOfWaiting)
	}
}
