package loop_test

import (
	"io"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/worker"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// backoff の明けを tick を待たずに知らせる wake と、明けた後に起動できなかった再起動待ちの claim の試み直し
// (formats.md §6 の再起動)。試み直しは tick と worker の終わりで行い、wake を置き直さない。

const wakeInterval = time.Minute

// wakeLoop は 1 周期を wakeInterval とする loop。時計は loop の goroutine だけが進める (tick を始めるたびと、wake を
// 置くたび)。
type wakeLoop struct {
	// def は tick (1 始まり) ごとの workflow 定義
	def func(tick int) workflow.Definition
	// open は open な一覧が返す作業対象。read は置き場からの読み直しが返すもので、loaded は workflow 定義を読んだ回数
	// (tick は突き合わせの後に workflow 定義を読むので、tick n の突き合わせでは n−1)
	open []target.Item
	read func(loaded int, ref target.Ref) target.Item
	// stoppable は Stop されるまで走り続ける worker の作業対象。ほかの worker は起動するとすぐに終わる
	stoppable target.Ref
	// deliverWakes は wake を届けるか (届けなければ、再起動は tick と worker の終わりでだけ試みる)
	deliverWakes bool
	// period は n 回目 (1 始まり) の周期の明けを返す。nil を返せば、その周期で停止要求を送る
	period func(n int, w *wakeRun) <-chan time.Time
}

// wakeRun は回している loop の観測。loop の goroutine だけが書き、Run から戻った後に読む。
type wakeRun struct {
	tick int
	jobs []worker.Job
	// wakes は置いた wake の数
	wakes int
	// launchesAtTick は tick ごとの、その tick で workflow 定義を読んだ時点の起動の数 (tick の再起動はその後に試みる)
	launchesAtTick map[int]int
	// ended は、すぐに終わる worker の event を loop が受け取り終えるたびに 1 つ届く
	ended chan struct{}
	// woke は、届けた wake を loop が受け取るたびに 1 つ届く
	woke chan struct{}
	// stopped は、Stop した worker の終わりを loop が受け取り終えると閉じる
	stopped chan struct{}
}

// wakeSpinLimit を超えて wake を置かれたら、それ以上は届けない (wake が回り続けるときに test を終わらせる)
const wakeSpinLimit = 1000

func (s wakeLoop) run(t *testing.T) *wakeRun {
	t.Helper()
	var (
		start      = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		now        = start
		w          = &wakeRun{launchesAtTick: map[int]int{}, ended: make(chan struct{}, 16), woke: make(chan struct{}, wakeSpinLimit+1), stopped: make(chan struct{})}
		periods    int
		signals    = make(chan os.Signal, 1)
		neverFires = make(chan time.Time)
	)
	exit := loop.Run(loop.Options{
		Load: func() (workflow.Definition, error) {
			w.tick++
			w.launchesAtTick[w.tick] = len(w.jobs)
			now = start.Add(time.Duration(w.tick-1) * wakeInterval)
			return s.def(w.tick), nil
		},
		Definition: s.def(1),
		Precheck:   noProblems,
		Store: func(workflow.Definition) loop.Store {
			return memoryStore{
				scopeKey: func() string { return startScope },
				observe:  func() ([]target.Item, error) { return s.open, nil },
				reread:   func(ref target.Ref) (target.Item, error) { return s.read(w.tick, ref), nil },
			}
		},
		Workspaces: func(workflow.Definition) loop.Workspaces { return noWorkspaces{} },
		Launch: func(_ workflow.Definition, job worker.Job, events chan<- worker.Event) loop.Worker {
			w.jobs = append(w.jobs, job)
			ref := job.Item.Ref()
			if ref == s.stoppable {
				go func() { events <- worker.Started{Target: ref, PID: 1} }()
				return &stoppableWorker{stop: func() {
					go func() {
						events <- worker.Ended{Target: ref, Result: worker.Result{Stopped: true}}
						close(w.stopped)
					}()
				}}
			}
			go func() {
				events <- worker.Started{Target: ref, PID: 1}
				code := 1
				events <- worker.Ended{Target: ref, Result: worker.Result{ExitCode: &code}}
				w.ended <- struct{}{}
			}()
			return endedWorker{}
		},
		NewSessionID: func() (string, error) { return "session-1", nil },
		ScopeKey:     startScope,
		Log:          io.Discard,
		Output:       loop.Appender{W: io.Discard},
		Publish:      func(status.Snapshot) error { return nil },
		Signals:      signals,
		Now:          func() time.Time { return now },
		After: func(d time.Duration) <-chan time.Time {
			if d == wakeInterval {
				periods++
				if ch := s.period(periods, w); ch != nil {
					return ch
				}
				signals <- syscall.SIGINT
				return neverFires
			}
			w.wakes++
			if !s.deliverWakes || w.wakes > wakeSpinLimit {
				return neverFires
			}
			now = now.Add(d)
			ch, at := make(chan time.Time), now
			go func() {
				ch <- at
				w.woke <- struct{}{}
			}()
			return ch
		},
	})
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
	return w
}

// stoppableWorker は Stop されるまで走り続ける worker。
type stoppableWorker struct {
	once sync.Once
	stop func()
}

func (s *stoppableWorker) Stop()                     { s.once.Do(s.stop) }
func (s *stoppableWorker) Activity() status.Activity { return status.Activity{} }

// firesAfter は、ready から値が届いてから明ける周期。
func firesAfter(ready <-chan struct{}) <-chan time.Time {
	ch := make(chan time.Time, 1)
	go func() {
		<-ready
		ch <- time.Time{}
	}()
	return ch
}

func TestWaitingRetryOfACLThatCannotRestartOutsideATickWakesTheLoopOnlyOnceBeforeTheNextTick(t *testing.T) {
	// CL は tick の中でだけ再起動を試みるので、backoff が明けても次の tick までは起動できない
	fixCI := trigger.Trigger{Name: "fix-ci", On: target.KindCL}
	cl := target.CL{Number: 7, Head: "feature", HeadRepo: "acme/widgets", SameRepo: true}
	var wakesBeforeSecondTick int
	s := wakeLoop{
		def: func(int) workflow.Definition {
			return workflow.Definition{Interval: wakeInterval, MaxConcurrent: 1, MaxAttempts: 5, MaxRetryBackoff: 5 * time.Minute, Triggers: []trigger.Trigger{fixCI}}
		},
		open:         []target.Item{cl},
		read:         func(int, target.Ref) target.Item { return cl },
		deliverWakes: true,
		period: func(n int, w *wakeRun) <-chan time.Time {
			if n == 1 {
				// 最初の worker の終わりで置かれた backoff の wake を loop が受け取ってから、周期を明ける
				return firesAfter(w.woke)
			}
			wakesBeforeSecondTick = w.wakes
			return nil
		},
	}

	s.run(t)

	if wakesBeforeSecondTick != 1 {
		t.Fatalf("2 回目の tick までに置いた wake = %d, want 1 (backoff の明けに 1 回だけ。起動できなかった後は tick を待つ)", wakesBeforeSecondTick)
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
			return workflow.Definition{Interval: wakeInterval, MaxConcurrent: maxConcurrent, MaxAttempts: 5, MaxRetryBackoff: 5 * time.Minute, Triggers: []trigger.Trigger{readyTrigger}}
		},
		open: []target.Item{readyIssue(1), readyIssue(2)},
		read: func(loaded int, ref target.Ref) target.Item {
			if ref == running && loaded >= 2 {
				return target.Issue{Number: 2, Labels: []string{"ready"}, Closed: true}
			}
			return readyIssue(ref.Number)
		},
		stoppable: running,
		period: func(n int, w *wakeRun) <-chan time.Time {
			switch n {
			case 1:
				return firesAfter(w.ended)
			case 2:
				ch := make(chan time.Time, 1)
				ch <- time.Time{}
				return ch
			case 3:
				return firesAfter(w.stopped)
			}
			return nil
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
