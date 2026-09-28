// Package loop は `claude-dispatcher loop <project> <interval>` (formats.md §13) を回す。
//
// 1 つの project の tick を、起動直後と、tick の終了から interval ごとに process の中で回す。tick を跨いで状態を持ち越さない
// (各 tick が外部 store を読み直す)。停止は段階的で、1 回目の停止要求は実行中の tick を終えてから、2 回目は orchestrator を
// 止めてから止まる (system.md §9)。起動済みの worker はどちらでも止めない。
package loop

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/tick"
)

// 本番の周期の見直し間隔と画面の描き直し間隔
const (
	DefaultPoll   = time.Second
	DefaultRedraw = 15 * time.Second
)

// Options は loop の入力。Tick / Status / Now / Signals は差し替えの口 (テストは fake を渡す)。
type Options struct {
	Project  string
	Interval time.Duration
	// IntervalText は撃たれた interval の綴り (見出しに出す)
	IntervalText string
	Stdout       io.Writer
	Stderr       io.Writer
	// Terminal は stdout が端末か。端末なら画面を消して描き直し、端末でなければ追記する
	Terminal bool
	// Signals は停止要求 (SIGINT / SIGTERM / SIGHUP)
	Signals <-chan os.Signal
	// Tick は tick の 1 回分を回す
	Tick func(Request) tick.Outcome
	// Status は project の今 (status の現況) を組む
	Status func() status.Report
	// Now は壁時計。次の tick の時刻はこれで判定する (スリープ中に monotonic clock が進まない OS がある — system.md §9)
	Now func() time.Time
	// Poll は壁時計を見直す間隔、Redraw は端末の画面を描き直す間隔
	Poll   time.Duration
	Redraw time.Duration
}

// Request は loop が tick の 1 回分へ渡すもの。
type Request struct {
	// StopOrchestrator は 2 回目の停止要求で閉じる
	StopOrchestrator <-chan struct{}
	// OrchestratorStarted は tick が orchestrator を起動する直前に呼ぶ (画面の経過の起点)
	OrchestratorStarted func(at time.Time)
}

// Run は停止要求で止まるまで tick を回し、exit code を返す (停止要求で止まったら 0、想定外の失敗なら 1)。
func Run(o Options) (exit int) {
	defer func() {
		if v := recover(); v != nil {
			fmt.Fprintf(o.Stderr, "panic: %v\n%s", v, debug.Stack())
			exit = 1
		}
	}()
	l := &loop{
		o:         o,
		tickDone:  make(chan tick.Outcome, 1),
		collected: make(chan status.Report, 1),
		report:    status.Report{Project: o.Project},
	}
	return l.run()
}

type loop struct {
	o     Options
	phase phase
	next  time.Time
	// tickStarted と orchestratorStarted は実行中の tick の経過の起点。orchestratorStarted は tick の goroutine が書く
	tickStarted         time.Time
	orchestratorStarted atomic.Pointer[time.Time]
	tickDone            chan tick.Outcome
	last                *tick.Outcome

	// stops は受けた停止要求の数、firstStop は 1 回目の signal
	stops     int
	firstStop os.Signal
	// stopOrchestrator は実行中の tick へ渡した「orchestrator を止めよ」の口
	stopOrchestrator chan struct{}

	report status.Report
	// collected は裏で組んだ status の現況の受け口。組んでいる間は collecting、その間に描き直しを求められたら collectAgain
	collected    chan status.Report
	collecting   bool
	collectAgain bool
	lastRefresh  time.Time
	drawn        bool
}

func (l *loop) run() int {
	poll := time.NewTicker(l.o.Poll)
	defer poll.Stop()
	l.next = wall(l.o.Now())
	for {
		if l.phase == waiting && !wall(l.o.Now()).Before(l.next) {
			l.startTick()
		}
		select {
		case sig := <-l.o.Signals:
			if l.onStopRequest(sig) {
				return l.finish(l.stopReason(nil))
			}
		case out := <-l.tickDone:
			l.last = &out
			l.tickStarted = time.Time{}
			l.orchestratorStarted.Store(nil)
			if l.stops > 0 {
				return l.finish(l.stopReason(&out))
			}
			// 周期は tick の終了から数える。取りこぼした周期は追い掛けない
			l.phase = waiting
			l.next = wall(l.o.Now()).Add(l.o.Interval)
			l.refresh()
		case <-poll.C:
			// 周期の描き直しは組んでいる間は積まない (gh が遅いと組み直しが途切れなく続く)
			if l.o.Terminal && !l.collecting && l.o.Now().Sub(l.lastRefresh) >= l.o.Redraw {
				l.refresh()
			}
		case report := <-l.collected:
			l.collecting = false
			l.report = report
			l.draw(true)
			if l.collectAgain {
				l.collectAgain = false
				l.refresh()
			}
		}
	}
}

// wall は monotonic clock の読みを落とし、比較と差を壁時計で行わせる。
func wall(t time.Time) time.Time { return t.Round(0) }

func (l *loop) startTick() {
	l.phase = ticking
	l.tickStarted = l.o.Now()
	l.orchestratorStarted.Store(nil)
	l.stopOrchestrator = make(chan struct{})
	req := Request{
		StopOrchestrator:    l.stopOrchestrator,
		OrchestratorStarted: func(at time.Time) { l.orchestratorStarted.Store(&at) },
	}
	go func() {
		defer func() {
			// tick の 1 回分は自分で panic を行に写すので、ここへ来るのは組み立ての失敗だけ。loop は次の周期へ進む
			if v := recover(); v != nil {
				l.tickDone <- tick.Outcome{Result: tick.ResultError, Error: fmt.Sprintf("想定外の失敗で止まった: %v", v)}
			}
		}()
		l.tickDone <- l.o.Tick(req)
	}()
	if l.o.Terminal {
		l.refresh()
	}
}

// onStopRequest は停止要求を受ける。すぐ止まってよければ true。
func (l *loop) onStopRequest(sig os.Signal) bool {
	l.stops++
	switch {
	case l.phase == waiting:
		// tick の合間: 次の tick を始めずに止まる
		l.firstStop = sig
		return true
	case l.stops == 1:
		// 実行中の tick を最後まで進めてから止まる
		l.firstStop = sig
		l.phase = stopping
		if l.o.Terminal {
			l.refresh()
		}
	case l.stops == 2:
		// 段階の解釈 (起動前・起動中・正常終了の後) は tick の 1 回分が持つ
		close(l.stopOrchestrator)
		if l.o.Terminal {
			l.refresh()
		}
	}
	return false
}

func (l *loop) stopReason(out *tick.Outcome) string {
	if out != nil {
		switch out.Halt {
		case tick.HaltedDuringRun:
			return "2 回目の停止要求で orchestrator を止めた — 経過は " + out.OrchestratorLog + "。wip を付けたまま残った issue が無いか確かめる"
		case tick.HaltedBeforeLaunch:
			return "2 回目の停止要求で orchestrator を起動せずに止めた"
		}
	}
	return "停止要求 " + signalName(l.firstStop)
}

func signalName(sig os.Signal) string {
	switch sig {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGHUP:
		return "SIGHUP"
	}
	return sig.String()
}

// refresh は status の現況を裏で組ませ、組めたら描く。組んでいる間に求められたら、組み終えてからもう一度組む。
func (l *loop) refresh() {
	l.lastRefresh = l.o.Now()
	if l.collecting {
		l.collectAgain = true
		return
	}
	l.collecting = true
	go func() { l.collected <- l.collectStatus() }()
}

// collectStatus は status の現況を組む。組めなければ理由を注記にした空の現況にする (画面を止めない)。
func (l *loop) collectStatus() (report status.Report) {
	defer func() {
		if v := recover(); v != nil {
			report = status.Report{Project: l.o.Project, Notes: []string{fmt.Sprintf("status の現況を組めない: %v", v)}}
		}
	}()
	return l.o.Status()
}

func (l *loop) view() view {
	v := view{
		project: l.o.Project, interval: l.o.IntervalText, phase: l.phase, now: l.o.Now(),
		next: l.next, tickStarted: l.tickStarted, last: l.last, report: l.report,
	}
	if at := l.orchestratorStarted.Load(); at != nil {
		v.orchestratorStarted = *at
	}
	return v
}

// draw は画面を描く。端末なら消して描き直し、端末でなければ操作案内を除いて空行で区切って追記する。
// 書き込みの失敗 (読み手の消えた pipe 等) は捨てる — 描けなくても停止要求で止まれるように。
func (l *loop) draw(withGuide bool) {
	lines := l.view().lines(withGuide && l.o.Terminal)
	text := strings.Join(lines, "\n") + "\n"
	switch {
	case l.o.Terminal:
		text = clearScreen + text
	case l.drawn:
		text = "\n" + text
	}
	l.drawn = true
	_, _ = io.WriteString(l.o.Stdout, text)
}

// finish は画面を残したまま止まる: 今の現況で操作案内を除いた画面を描き、終了行を 1 行足す。
// 現況を組む間 (gh が遅いと分単位) に次の停止要求が来たら、組むのを待たずに直前の現況で描いて止まる。
func (l *loop) finish(reason string) int {
	collected := make(chan status.Report, 1)
	go func() { collected <- l.collectStatus() }()
	running := "?"
	select {
	case l.report = <-collected:
		if n := l.report.RunningWorkers(); n.Known {
			running = fmt.Sprint(n.Value)
		}
	case <-l.o.Signals:
	}
	l.phase = stopping
	l.draw(false)
	_, _ = fmt.Fprintf(l.o.Stdout, "%s [%s] loop を止めた (%s)。止めずに走っている worker: %s 本\n",
		l.o.Now().UTC().Format(timeLayout), l.o.Project, reason, running)
	return 0
}
