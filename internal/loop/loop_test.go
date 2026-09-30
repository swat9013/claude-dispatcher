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

// loop を Run の単位で回す。周期は interval の下限 (1m) を black-box テストでは待てないので、壁時計を fake にしてここで確かめる。
// 画面は Run が stdout へ書いたものを読む (formats.md §13)。

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

// tickCall は fake の tick が受けた呼び出し。
type tickCall struct {
	at      time.Time
	control tick.Control
}

// harness は fake の tick・時計・status で Run を回す。tick は calls へ呼び出しを流し、outcomes から結果を受けて返す
// (受けるまで返らない)。所要時間は tickDuration だけ時計を進めて表す。
type harness struct {
	t            *testing.T
	clock        *fakeClock
	calls        chan tickCall
	outcomes     chan tick.Outcome
	tickDuration time.Duration
	signals      chan os.Signal
	stdout       *lockedBuffer
	exit         chan int
	finished     bool
}

type setting func(*Options)

func onTerminal(o *Options) { o.Terminal = true }

func notes(lines ...string) setting {
	return func(o *Options) {
		o.Status = func() status.Report { return runningReport(lines...) }
	}
}

func runningReport(notes ...string) status.Report {
	return status.Report{
		Project: "widgets", Loop: status.Probed[bool]{Value: true, Known: true}, LastTick: status.Probed[*ticklog.Line]{Known: true},
		RunningWorkers: status.Probed[int]{Value: 0, Known: true}, Notes: notes,
	}
}

func startLoop(t *testing.T, settings ...setting) *harness {
	t.Helper()
	h := &harness{
		t: t, clock: &fakeClock{now: start}, calls: make(chan tickCall, 10), outcomes: make(chan tick.Outcome, 10),
		tickDuration: 10 * time.Second, signals: make(chan os.Signal, 4), stdout: &lockedBuffer{}, exit: make(chan int, 1),
	}
	o := Options{
		Project: "widgets", Interval: Interval{Duration: 5 * time.Minute, Text: "5m"},
		Stdout: h.stdout, Stderr: &bytes.Buffer{}, Signals: h.signals,
		Tick: func(control tick.Control) tick.Outcome {
			h.calls <- tickCall{at: h.clock.Now(), control: control}
			out := <-h.outcomes
			h.clock.advance(h.tickDuration)
			out.TS = h.clock.Now().Format(time.RFC3339Nano)
			return out
		},
		Status: func() status.Report { return runningReport() },
		Fit:    func() status.Fit { return status.FitPlain },
		Now:    h.clock.Now, Poll: time.Millisecond,
	}
	for _, s := range settings {
		s(&o)
	}
	go func() { h.exit <- Run(o) }()
	t.Cleanup(func() {
		if !h.finished {
			h.signals <- syscall.SIGINT
			h.signals <- syscall.SIGINT
			h.outcomes <- tick.Outcome{Result: tick.ResultOK, Logged: true}
			<-h.exit
		}
	})
	return h
}

// loggedOK は log.jsonl に書けた ok の tick
var loggedOK = tick.Outcome{Result: tick.ResultOK, Logged: true}

// tickCalled は tick が呼ばれるのを待つ。
func (h *harness) tickCalled() tickCall {
	h.t.Helper()
	select {
	case c := <-h.calls:
		return c
	case <-time.After(5 * time.Second):
		h.t.Fatal("tick が撃たれない")
		return tickCall{}
	}
}

// tickEnds は呼ばれている tick を out で終わらせ、loop が待機に戻って画面を描くまで待つ。待たずに時計を進めると、loop が
// tick の終了の時刻を読む前に進んだ時計を読む。
func (h *harness) tickEnds(out tick.Outcome) {
	h.t.Helper()
	drawn := strings.Count(h.stdout.String(), "  待機 · ")
	h.outcomes <- out
	h.waitFor(func() bool { return strings.Count(h.stdout.String(), "  待機 · ") > drawn })
}

// ticks は次の tick を out で回し、呼ばれた時刻を返す。
func (h *harness) ticks(out tick.Outcome) time.Time {
	h.t.Helper()
	at := h.tickCalled().at
	h.tickEnds(out)
	return at
}

func (h *harness) waitFor(done func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			h.t.Fatalf("待った出力にならない:\n%s", h.stdout.String())
		}
		time.Sleep(time.Millisecond)
	}
}

// tickReturnsAndStops は呼ばれている tick を out で終わらせ、loop が止まるのを待つ。
func (h *harness) tickReturnsAndStops(out tick.Outcome) {
	h.t.Helper()
	h.outcomes <- out
	h.stops()
}

// secondStopRequest は tick の実行中に停止要求を 2 回送り、tick へ「orchestrator を止めよ」が届くのを待つ。
func (h *harness) secondStopRequest(call tickCall) {
	h.t.Helper()
	h.signals <- syscall.SIGINT
	h.signals <- syscall.SIGINT
	<-call.control.StopOrchestrator
}

func (h *harness) stops() int {
	h.t.Helper()
	select {
	case exit := <-h.exit:
		h.finished = true
		return exit
	case <-time.After(5 * time.Second):
		h.t.Fatalf("止まらない:\n%s", h.stdout.String())
		return -1
	}
}

// lastScreen は端末に最後に描いた画面 (最後の消去の後)。
func (h *harness) lastScreen() string {
	out := h.stdout.String()
	i := strings.LastIndex(out, clearScreen)
	if i < 0 {
		return ""
	}
	return out[i+len(clearScreen):]
}

// --- 周期 ---

func TestLoopTicksRightAfterStarting(t *testing.T) {
	h := startLoop(t)

	first := h.tickCalled().at

	if !first.Equal(start) {
		t.Fatalf("1 回目の tick = %s, want 起動直後 (%s)", first, start)
	}
}

func TestLoopTicksAgainIntervalAfterTheTickEnded(t *testing.T) {
	h := startLoop(t)
	h.ticks(loggedOK) // 10s 掛かって終わる

	h.clock.advance(5 * time.Minute)

	// 2 回目がこれより早く撃たれていれば、ここで受けるのは早い方の時刻
	if second, want := h.tickCalled().at, start.Add(10*time.Second+5*time.Minute); !second.Equal(want) {
		t.Fatalf("2 回目の tick = %s, want tick の終了から interval 後 (%s)", second, want)
	}
}

func TestLoopGoesOnToTheNextPeriodWhenATickFails(t *testing.T) {
	for _, result := range []tick.Result{tick.ResultError, tick.ResultConfigError, tick.ResultLocked, tick.ResultAuthError} {
		t.Run(result.Name, func(t *testing.T) {
			h := startLoop(t)
			h.ticks(tick.Outcome{Result: result, Logged: true, Error: "落ちた"})

			h.clock.advance(5 * time.Minute)

			h.tickCalled()
		})
	}
}

func TestLoopDoesNotChaseMissedPeriods(t *testing.T) {
	h := startLoop(t)
	h.ticks(loggedOK)
	// スリープ明け: 周期を 3 つ分取りこぼした
	h.clock.advance(16 * time.Minute)
	resumed := h.ticks(loggedOK)

	h.clock.advance(5 * time.Minute)
	next := h.tickCalled().at

	// 取りこぼした周期を追い掛けていれば、ここで受けるのは追い掛けの tick の時刻
	if want := resumed.Add(h.tickDuration + 5*time.Minute); !next.Equal(want) {
		t.Fatalf("スリープ明けの次の tick = %s, want スリープ明けの tick の終了から interval 後 (%s)", next, want)
	}
}

// --- 停止 ---

func TestLoopStopsRightAwayOnAStopRequestBetweenTicksNamingTheSignal(t *testing.T) {
	for _, tc := range []struct {
		sig  syscall.Signal
		name string
	}{{syscall.SIGINT, "SIGINT"}, {syscall.SIGTERM, "SIGTERM"}, {syscall.SIGHUP, "SIGHUP"}} {
		t.Run(tc.name, func(t *testing.T) {
			h := startLoop(t)
			h.ticks(loggedOK)

			h.signals <- tc.sig

			if exit := h.stops(); exit != 0 {
				t.Fatalf("exit %d, want 0", exit)
			}
			if !strings.HasSuffix(h.stdout.String(), " [widgets] loop を止めた (停止要求 "+tc.name+")。止めずに走っている worker: 0 本\n") {
				t.Fatalf("終了行が無い:\n%s", h.stdout.String())
			}
		})
	}
}

func TestLoopStoppedBeforeLaunchingTheOrchestratorSaysSoOnTheEndLine(t *testing.T) {
	h := startLoop(t)
	h.secondStopRequest(h.tickCalled())

	h.tickReturnsAndStops(tick.Outcome{Result: tick.ResultError, Logged: true, Halt: tick.HaltedBeforeLaunch})

	if !strings.Contains(h.stdout.String(), "loop を止めた (2 回目の停止要求で orchestrator を起動せずに止めた)。") {
		t.Fatalf("終了行の理由が違う:\n%s", h.stdout.String())
	}
}

func TestLoopStoppedDuringTheOrchestratorPointsToItsLogOnTheEndLine(t *testing.T) {
	h := startLoop(t)
	h.secondStopRequest(h.tickCalled())

	h.tickReturnsAndStops(tick.Outcome{Result: tick.ResultError, Logged: true, Halt: tick.HaltedDuringRun, OrchestratorLog: "/s/o.log"})

	if !strings.Contains(h.stdout.String(), "loop を止めた (2 回目の停止要求で orchestrator を止めた — 経過は /s/o.log。wip を付けたまま残った issue が無いか確かめる)。") {
		t.Fatalf("終了行の理由が違う:\n%s", h.stdout.String())
	}
}

func TestLoopFirstStopRequestDoesNotStopTheOrchestrator(t *testing.T) {
	h := startLoop(t)
	call := h.tickCalled()
	h.signals <- syscall.SIGINT

	h.tickReturnsAndStops(loggedOK)

	select {
	case <-call.control.StopOrchestrator:
		t.Fatal("1 回目の停止要求で orchestrator を止めた")
	default:
	}
}

// --- 画面 ---

func TestLoopOnATerminalRedrawsTheWaitingScreenWithTheGuideAfterATick(t *testing.T) {
	h := startLoop(t, onTerminal, notes("wip を読めない"))

	h.ticks(tick.Outcome{Result: tick.ResultOK, Logged: true, Instructions: map[string]int{"start": 1}, Spawned: []int{42}})

	want := "widgets  loop 5m  待機 · 次の tick 2026-09-26T03:05:10Z (あと 5m)\n" +
		"最終 tick 2026-09-26T03:00:10Z ok · 指示 start 1 · 起動 #42\n" +
		"  ! wip を読めない\n" +
		"\n" +
		"Ctrl+C で停止\n"
	if got := h.lastScreen(); got != want {
		t.Fatalf("画面 =\n%q\nwant\n%q", got, want)
	}
}

func TestLoopOnATerminalShowsTheOrchestratorElapsedAndTheStopPendingGuide(t *testing.T) {
	h := startLoop(t, onTerminal)
	call := h.tickCalled()
	call.control.OrchestratorStarted(h.clock.Now())
	h.clock.advance(2 * time.Minute)

	h.signals <- syscall.SIGINT

	h.waitFor(func() bool {
		return strings.HasPrefix(h.lastScreen(), "widgets  loop 5m  停止待ち · orchestrator 2m (上限 15m)\n")
	})
	if screen := h.lastScreen(); !strings.HasSuffix(screen, "\n停止待ち: この tick を終えたら止まる。もう一度 Ctrl+C で orchestrator を止めて止まる (付いた wip は残りうる)\n") {
		t.Fatalf("停止待ちの操作案内が無い:\n%s", screen)
	}
}

func TestLoopOnATerminalShowsTheRunningTickBeforeTheFirstTickEnds(t *testing.T) {
	h := startLoop(t, onTerminal)
	h.tickCalled()

	h.waitFor(func() bool { return strings.Contains(h.stdout.String(), clearScreen) })

	if screen := h.lastScreen(); !strings.HasPrefix(screen, "widgets  loop 5m  tick 実行中 · 0s\n最終 tick なし\n") ||
		!strings.HasSuffix(screen, "\nCtrl+C: この tick を終えてから停止\n") {
		t.Fatalf("tick 実行中の画面 =\n%s", screen)
	}
}

func TestLoopOnATerminalRedrawsTheScreenEvery15Seconds(t *testing.T) {
	h := startLoop(t, onTerminal)
	h.ticks(loggedOK)
	drawn := strings.Count(h.stdout.String(), clearScreen)

	h.clock.advance(15 * time.Second)

	h.waitFor(func() bool { return strings.Count(h.stdout.String(), clearScreen) > drawn })
	if !strings.Contains(h.lastScreen(), "(あと 4m45s)") && !strings.Contains(h.lastScreen(), "(あと 4m)") {
		t.Fatalf("15 秒後の画面の残りが進んでいない:\n%s", h.lastScreen())
	}
}

func TestLoopLeavesAStoppedScreenWithoutTheGuide(t *testing.T) {
	h := startLoop(t, onTerminal)
	h.ticks(loggedOK)

	h.signals <- syscall.SIGINT
	h.stops()

	screen := h.lastScreen()
	if !strings.HasPrefix(screen, "widgets  loop 5m  停止\n") || strings.Contains(screen, "Ctrl+C") {
		t.Fatalf("止まった後の画面 =\n%s", screen)
	}
}

func TestLoopShowsTheErrorOfTheLastTickFirstAmongTheNotes(t *testing.T) {
	h := startLoop(t, notes("wip を読めない"))

	h.ticks(tick.Outcome{Result: tick.ResultConfigError, Logged: true, Error: "未知の key"})

	if !strings.Contains(h.stdout.String(), " config_error · 指示 0\n  ! 未知の key\n  ! wip を読めない\n") {
		t.Fatalf("画面 =\n%s", h.stdout.String())
	}
}

func TestLoopShowsATickThatCouldNotWriteItsLogLineEvenWhenItWasOK(t *testing.T) {
	h := startLoop(t)

	h.ticks(tick.Outcome{Result: tick.ResultOK, Logged: false, Error: "log.jsonl に書けない: disk full"})

	if !strings.Contains(h.stdout.String(), " ok · 指示 0\n  ! log.jsonl に書けない: disk full\n") {
		t.Fatalf("画面 =\n%s", h.stdout.String())
	}
}
