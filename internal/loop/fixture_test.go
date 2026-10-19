package loop_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/precheck"
	"github.com/swat9013/claude-dispatcher/internal/provider"
	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/worker"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// loop の test が共有する fixture。loop.Options の定型、fake の時計と After、scripted worker、worker の終わりの待ち方を
// 1 種類ずつ持つ。各 test の harness は、tick ごとの workflow 定義・置き場・timer の明け方だけを組み、残りは rig に任せる。
//
// rig は loop を 1 本の筋で進める。loop は select の直前に必ず状態 file を書き出す (Publish) ので、rig はそこで
// loop が待ちに入ったと知り、次に届けるものを 1 つだけ用意する: 起動した worker の event が残っていれば、送った順に
// 1 つ。残っていなければ、明ける timer のうち時刻の最も早いもの (同じ時刻なら wake) を 1 つ。select で
// 受け取れるものが常に 1 つなので、受け取る順と loop が読む時刻は goroutine の割り振りに依らない。

const startScope = "scope-a"

// githubTracker は、テストの workflow 定義の tracker (作業対象を人が読む行に出す呼び方を決める)。
var githubTracker = workflow.Tracker{Kind: provider.GitHub}

// sameScope は、どの版の workflow 定義にも起動時の scope key を返す。
func sameScope(workflow.Definition) string { return startScope }

// noProblems は、どの trigger も事前検査に通す。
func noProblems(workflow.Definition) []precheck.Problem { return nil }

// memoryStore は in-memory の置き場。observe は作業対象の種類に依らず同じものを返す。
type memoryStore struct {
	observe func() ([]target.Item, error)
	reread  func(ref target.Ref) (target.Item, error)
}

func (m memoryStore) OpenIssues() ([]target.Item, error) { return m.observe() }
func (m memoryStore) OpenCLs() ([]target.Item, error)    { return m.observe() }
func (m memoryStore) ReadIssue(n int) (target.Item, error) {
	return m.reread(target.Ref{Kind: target.KindIssue, Number: n})
}
func (m memoryStore) ReadCL(n int) (target.Item, error) {
	return m.reread(target.Ref{Kind: target.KindCL, Number: n})
}

// store は m を issue 置き場と CL 置き場の両方の部品にした置き場。
func (m memoryStore) store() loop.Store { return loop.Store{Issues: m, CLs: m} }

// noWorkspaces は workspace を持たない掃除の口。
type noWorkspaces struct{}

func (noWorkspaces) Existing() ([]target.Ref, error)   { return nil, nil }
func (noWorkspaces) Remove(target.Ref) error           { return nil }
func (noWorkspaces) Branch(target.Ref) (string, error) { return "", nil }

// timerKind は loop が After で置いた timer の種類。
type timerKind int

const (
	// period は次の tick までの周期
	periodTimer timerKind = iota
	// wake は再起動の backoff の明けを知らせる wake
	wakeTimer
)

// fate は After が返した timer の行き先。
type fate int

const (
	// neverFires は明けない
	neverFires fate = iota
	// fires は、届ける番が来たら時計を timer の時刻へ進めて明ける
	fires
	// stopsTheLoop は、届ける番が来たら明ける代わりに停止要求を送る
	stopsTheLoop
)

// timing は 1 つの timer の扱い。
type timing struct {
	fate fate
	// keepClock なら明けても時計を進めない (時計は harness が setClock で tick ごとに合わせる)
	keepClock bool
}

// workerScript は起動した worker の振る舞い。
type workerScript struct {
	// result は起動の直後に送る終わり。untilStopped なら送らず、Stop されてから Stopped の終わりを送る
	result       worker.Result
	untilStopped bool
	// unstopped は起動の直後に送る「止められない」知らせの理由。"" なら送らない
	unstopped string
	// neverEnds なら終わりを送らない (止められない claude が loop の終了まで終わらない)
	neverEnds bool
}

// exits は起動するとすぐに exit code で終わる worker。
func exits(code int) workerScript { return workerScript{result: worker.Result{ExitCode: &code}} }

// armed は置かれた timer。
type armed struct {
	ch     chan time.Time
	at     time.Time
	timing timing
}

// queued は loop へ送る番を待つ worker の event。
type queued struct {
	events chan<- worker.Event
	ev     worker.Event
}

// rig は loop を 1 回回す fixture。field はどれも loop の goroutine だけが読み書きし、test は Run から戻った後に読む。
type rig struct {
	// timer は After(d) の扱いを kind ごとに決める。nil なら、どの timer も明けない
	timer func(kind timerKind, d time.Duration) timing
	// script は起動した worker の振る舞いを決める。nil なら、起動するとすぐに正常に終わる
	script func(job worker.Job) workerScript
	// stopSignal は stopsTheLoop の timer が送る停止要求。nil なら SIGINT
	stopSignal os.Signal
	// signals は loop の停止要求。Run の前に積めば、最初の tick の後に受ける
	signals chan os.Signal

	now time.Time
	// events は送る番を待つ worker の event (送った順)
	events []queued
	// period と wake は loop が待っている周期と wake。loop は周期を明けるまで置き直さず、wake は早い予定が出るたびに
	// 置き直す (前の wake は捨てる)。周期は、loop の起動の後と周期の明けの後の最初の After で置かれる (loop.go の Run で
	// 周期を置いてから arm が wake を置く) ので、周期を待っていない間の After を周期とみなす
	period, wake *armed
	stopped      bool
	// done は loop が止まると閉じる
	done chan struct{}

	// 観測の結果
	jobs      []worker.Job
	waits     []time.Duration
	stdout    bytes.Buffer
	log       bytes.Buffer
	published []status.Snapshot
}

var rigStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newRig() *rig {
	return &rig{now: rigStart, signals: make(chan os.Signal, 4), done: make(chan struct{})}
}

// run は o の空の field を rig の既定で埋めて loop を回し、止まるまで待つ。Load が nil なら毎 tick Definition を返す。
// Publish・Now・After は rig が届ける順を決める口なので、常に rig のものにする。
func (r *rig) run(t *testing.T, o loop.Options) {
	t.Helper()
	if o.Load == nil {
		def := o.Definition
		o.Load = func() (workflow.Definition, error) { return def, nil }
	}
	if o.Precheck == nil {
		o.Precheck = noProblems
	}
	if o.Store == nil {
		o.Store = func(workflow.Definition) loop.Store {
			return memoryStore{observe: func() ([]target.Item, error) { return nil, nil }}.store()
		}
	}
	if o.Workspaces == nil {
		o.Workspaces = func(workflow.Definition) loop.Workspaces { return noWorkspaces{} }
	}
	if o.Launch == nil {
		o.Launch = r.launch
	}
	if o.NewSessionID == nil {
		o.NewSessionID = func() (string, error) { return "session-1", nil }
	}
	if o.ScopeKeyOf == nil {
		o.ScopeKeyOf = sameScope
	}
	if o.ScopeKey == "" {
		o.ScopeKey = startScope
	}
	if o.Log == nil {
		o.Log = &r.log
	}
	if o.Output == nil {
		o.Output = loop.Appender{W: &r.stdout}
	}
	if o.Signals == nil {
		o.Signals = r.signals
	}
	if o.Publish != nil || o.Now != nil || o.After != nil {
		t.Fatal("rig.run: Publish・Now・After は rig が持つので渡さない")
	}
	o.Publish, o.Now, o.After = r.publish, r.clock, r.after
	exit := loop.Run(o)
	close(r.done)
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
}

func (r *rig) clock() time.Time { return r.now }

// setClock は時計を rig の起動時刻から elapsed だけ進んだ時刻に合わせる。
func (r *rig) setClock(elapsed time.Duration) { r.now = rigStart.Add(elapsed) }

// launch は script に従う worker を起動する。
func (r *rig) launch(_ workflow.Definition, job worker.Job, events chan<- worker.Event) loop.Worker {
	r.jobs = append(r.jobs, job)
	s := exits(0)
	if r.script != nil {
		s = r.script(job)
	}
	ref := job.Item.Ref()
	if s.untilStopped {
		r.send(events, worker.Started{Target: ref, PID: 1})
		return &loop.FakeWorker{OnStop: func() { r.send(events, worker.Ended{Target: ref, Result: worker.Result{Stopped: true}}) }}
	}
	r.send(events, worker.Started{Target: ref, PID: 1})
	if s.unstopped != "" {
		// 終わる worker の claude は、終わりより先に終わっている
		exited := make(chan struct{})
		if !s.neverEnds {
			close(exited)
		}
		r.send(events, worker.Unstopped{Target: ref, StopError: s.unstopped, Exited: exited})
	}
	if !s.neverEnds {
		r.send(events, worker.Ended{Target: ref, Result: s.result})
	}
	return &loop.FakeWorker{}
}

// send は evs を、送る番を待つ event の後ろに積む。
func (r *rig) send(events chan<- worker.Event, evs ...worker.Event) {
	for _, ev := range evs {
		r.events = append(r.events, queued{events: events, ev: ev})
	}
}

// after は timer の扱いに従う channel を返す。待った周期は waits に残す。
func (r *rig) after(d time.Duration) <-chan time.Time {
	r.waits = append(r.waits, d)
	kind := wakeTimer
	if r.period == nil {
		kind = periodTimer
	}
	t := timing{fate: neverFires}
	if r.timer != nil {
		t = r.timer(kind, d)
	}
	a := &armed{ch: make(chan time.Time, 1), at: r.now.Add(d), timing: t}
	if kind == periodTimer {
		r.period = a
	} else {
		r.wake = a
	}
	return a.ch
}

// publish は書き出した状態を残し、loop が select で受け取るものを 1 つだけ用意する。
func (r *rig) publish(s status.Snapshot) error {
	r.published = append(r.published, s)
	if len(r.events) > 0 {
		q := r.events[0]
		r.events = r.events[1:]
		go func() {
			select {
			case q.events <- q.ev:
			case <-r.done:
			}
		}()
		return nil
	}
	if r.stopped || exiting(s) {
		return nil
	}
	a := r.due()
	if a == nil {
		return nil
	}
	if a.timing.fate == stopsTheLoop {
		r.stopped = true
		sig := r.stopSignal
		if sig == nil {
			sig = syscall.SIGINT
		}
		r.signals <- sig
		return nil
	}
	if a == r.period {
		r.period = nil
	} else {
		r.wake = nil
	}
	if !a.timing.keepClock && a.at.After(r.now) {
		r.now = a.at
	}
	a.ch <- r.now
	return nil
}

// exiting は、loop が select に入らずに止まる直前の Publish か (停止要求を受け、走っている worker が無い)。
func exiting(s status.Snapshot) bool {
	if !s.Stopping {
		return false
	}
	for _, w := range s.Workers {
		if w.Phase == status.Running || w.Phase == status.Stopping {
			return false
		}
	}
	return true
}

// due は次に届ける timer。明けない timer は選ばない。周期と wake が同じ時刻なら wake を先にする。
func (r *rig) due() *armed {
	var best *armed
	for _, a := range []*armed{r.wake, r.period} {
		if a == nil || a.timing.fate == neverFires {
			continue
		}
		if best == nil || a.at.Before(best.at) {
			best = a
		}
	}
	return best
}

// logEvents は log.jsonl に書いた行のうち、event が name のもの。
func (r *rig) logEvents(t *testing.T, name string) []map[string]any {
	t.Helper()
	var found []map[string]any
	for line := range strings.Lines(r.log.String()) {
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("log.jsonl の行 %q: %v", line, err)
		}
		if fields["event"] == name {
			found = append(found, fields)
		}
	}
	return found
}
