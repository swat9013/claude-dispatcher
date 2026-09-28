package loop

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/tick"
	"github.com/swat9013/claude-dispatcher/internal/ticklog"
)

// 周期は interval の下限 (1m) を black-box テストでは待てないので、壁時計を fake にしてここで確かめる。

var start = time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeTicks は tick の代役。呼ばれた時刻を calls へ流し、outcome を返す。duration だけ時計を進めてから返す (tick の所要時間)。
type fakeTicks struct {
	clock    *fakeClock
	duration time.Duration
	outcome  tick.Outcome
	calls    chan time.Time
}

func (f *fakeTicks) tick(Request) tick.Outcome {
	f.calls <- f.clock.Now()
	f.clock.advance(f.duration)
	out := f.outcome
	out.TS = f.clock.Now().Format(time.RFC3339Nano)
	return out
}

type harness struct {
	clock   *fakeClock
	ticks   *fakeTicks
	signals chan os.Signal
	stdout  *lockedBuffer
	exit    chan int
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func startLoop(t *testing.T, outcome tick.Outcome) *harness {
	t.Helper()
	clock := &fakeClock{now: start}
	h := &harness{
		clock:   clock,
		ticks:   &fakeTicks{clock: clock, duration: 10 * time.Second, outcome: outcome, calls: make(chan time.Time, 10)},
		signals: make(chan os.Signal, 4),
		stdout:  &lockedBuffer{},
		exit:    make(chan int, 1),
	}
	o := Options{
		Project: "widgets", Interval: 5 * time.Minute, IntervalText: "5m",
		Stdout: h.stdout, Stderr: &bytes.Buffer{}, Signals: h.signals,
		Tick: h.ticks.tick,
		Status: func() status.Report {
			return status.Report{Project: "widgets", Loop: status.Probed[bool]{Value: true, Known: true}, LastTick: status.Probed[*ticklog.Line]{Known: true}}
		},
		Now: clock.Now, Poll: time.Millisecond, Redraw: DefaultRedraw,
	}
	go func() { h.exit <- Run(o) }()
	t.Cleanup(func() {
		h.signals <- syscall.SIGINT
		<-h.exit
	})
	return h
}

// nextTick は tick が撃たれるのを待ち、loop がその終了を受けて待機に戻る (tick の直後の画面を追記する) まで待つ。
// 待たずに時計を進めると、loop が tick の終了の時刻を読む前に進んだ時計を読む。
func (h *harness) nextTick(t *testing.T) time.Time {
	t.Helper()
	drawn := strings.Count(h.stdout.String(), waitingMark)
	var at time.Time
	select {
	case at = <-h.ticks.calls:
	case <-time.After(5 * time.Second):
		t.Fatal("tick が撃たれない")
	}
	waitFor(t, func() bool { return strings.Count(h.stdout.String(), waitingMark) > drawn })
	return at
}

// waitingMark は待機の見出しの印
const waitingMark = "  待機 · "

func (h *harness) noTick(t *testing.T) {
	t.Helper()
	select {
	case at := <-h.ticks.calls:
		t.Fatalf("interval が経つ前に tick を撃った (%s)", at)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestLoopTicksRightAwayThenIntervalAfterTheTickEnded(t *testing.T) {
	h := startLoop(t, tick.Outcome{Result: tick.ResultOK})

	if first := h.nextTick(t); !first.Equal(start) {
		t.Fatalf("1 回目の tick = %s, want 起動直後 (%s)", first, start)
	}
	// 1 回目の tick は 10s 掛かって終わった。次は終了から 5m 後
	h.clock.advance(5*time.Minute - time.Second)
	h.noTick(t)
	h.clock.advance(time.Second)

	if second := h.nextTick(t); !second.Equal(start.Add(10*time.Second + 5*time.Minute)) {
		t.Fatalf("2 回目の tick = %s, want tick の終了から interval 後", second)
	}
}

func TestLoopGoesOnToTheNextPeriodWhenATickFails(t *testing.T) {
	for _, result := range []tick.Result{tick.ResultError, tick.ResultConfigError, tick.ResultLocked, tick.ResultAuthError} {
		t.Run(result.Name, func(t *testing.T) {
			h := startLoop(t, tick.Outcome{Result: result, Error: "落ちた"})
			h.nextTick(t)

			h.clock.advance(5 * time.Minute)

			h.nextTick(t)
		})
	}
}

func TestLoopDoesNotChaseMissedPeriods(t *testing.T) {
	h := startLoop(t, tick.Outcome{Result: tick.ResultOK})
	h.nextTick(t)

	// スリープ明け: 周期を 3 つ分取りこぼした
	h.clock.advance(16 * time.Minute)
	h.nextTick(t)

	h.noTick(t)
}

func TestLoopStopsRightAwayOnAStopRequestBetweenTicks(t *testing.T) {
	h := startLoop(t, tick.Outcome{Result: tick.ResultOK})
	h.nextTick(t)

	h.signals <- syscall.SIGTERM

	select {
	case exit := <-h.exit:
		h.exit <- exit // Cleanup が読む
		if exit != 0 {
			t.Fatalf("exit %d, want 0", exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tick の合間の停止要求で止まらない")
	}
	if !strings.Contains(h.stdout.String(), "loop を止めた (停止要求 SIGTERM)。止めずに走っている worker: 0 本") {
		t.Fatalf("終了行が無い:\n%s", h.stdout.String())
	}
}

func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("待った状態にならない")
		}
		time.Sleep(time.Millisecond)
	}
}
