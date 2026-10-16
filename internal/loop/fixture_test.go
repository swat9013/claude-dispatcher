package loop_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"sync"
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

// fate は After が返した timer の行き先。
type fate int

const (
	// neverFires は明けない
	neverFires fate = iota
	// fires は、待つ条件が満ちたら時計を d だけ進めて明ける
	fires
	// stopsTheLoop は、待つ条件が満ちたら明ける代わりに停止要求を送る
	stopsTheLoop
)

// timing は 1 つの timer の扱い。
type timing struct {
	fate fate
	// until は明ける (または停止要求を送る) 前に待つ条件。rig の lock を持ったまま呼ぶ。nil なら settled
	until func(r *rig) bool
	// keepClock なら明けても時計を進めない (時計は harness が setClock で tick ごとに合わせる)
	keepClock bool
}

// workerScript は起動した worker の振る舞い。
type workerScript struct {
	// result は起動の直後に送る終わり。untilStopped なら送らず、Stop されてから Stopped の終わりを送る
	result       worker.Result
	untilStopped bool
}

// exits は起動するとすぐに exit code で終わる worker。
func exits(code int) workerScript { return workerScript{result: worker.Result{ExitCode: &code}} }

// rig は loop を 1 回回す fixture。loop の goroutine だけが書く field (jobs・waits など) は Run から戻った後に読む。
type rig struct {
	// timer は After(d) の扱いを決める。nil なら、どの timer も明けない
	timer func(d time.Duration) timing
	// script は起動した worker の振る舞いを決める。nil なら、起動するとすぐに正常に終わる
	script func(job worker.Job) workerScript
	// stopSignal は stopsTheLoop の timer が送る停止要求。nil なら SIGINT
	stopSignal os.Signal
	// signals は loop の停止要求。Run の前に積めば、最初の tick の後に受ける
	signals chan os.Signal

	mu      sync.Mutex
	changed *sync.Cond
	now     time.Time
	// pending は、起動した worker の event のうち loop へ送り終えていない数
	pending int
	// fired は loop が受け取った timer の数
	fired    int
	finished bool
	// done は loop が止まると閉じる
	done     chan struct{}
	stopOnce sync.Once

	// 観測の結果
	jobs      []worker.Job
	waits     []time.Duration
	stdout    bytes.Buffer
	log       bytes.Buffer
	published []status.Snapshot
}

var rigStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newRig() *rig {
	r := &rig{now: rigStart, signals: make(chan os.Signal, 4), done: make(chan struct{})}
	r.changed = sync.NewCond(&r.mu)
	return r
}

// run は o の空の field を rig の既定で埋めて loop を回し、止まるまで待つ。Load が nil なら毎 tick Definition を返す。
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
	if o.Publish == nil {
		o.Publish = func(s status.Snapshot) error {
			r.published = append(r.published, s)
			return nil
		}
	}
	if o.Signals == nil {
		o.Signals = r.signals
	}
	if o.Now == nil {
		o.Now = r.clock
	}
	if o.After == nil {
		o.After = r.after
	}
	exit := loop.Run(o)
	r.mu.Lock()
	r.finished = true
	r.changed.Broadcast()
	r.mu.Unlock()
	close(r.done)
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
}

func (r *rig) clock() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

// setClock は時計を rig の起動時刻から elapsed だけ進んだ時刻に合わせる。
func (r *rig) setClock(elapsed time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = rigStart.Add(elapsed)
}

// settled は、起動した worker の event を loop がすべて受け取り終えたか。lock を持ったまま呼ぶ。
func (r *rig) settled() bool { return r.pending == 0 }

// waitUntil は until が満ちるか loop が止まるまで待ち、満ちたかを返す。worker の終わりと timer の明けを待つ唯一の口。
func (r *rig) waitUntil(until func(*rig) bool) bool {
	if until == nil {
		until = (*rig).settled
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for !until(r) && !r.finished {
		r.changed.Wait()
	}
	return !r.finished
}

// launch は script に従う worker を起動する。
func (r *rig) launch(_ workflow.Definition, job worker.Job, events chan<- worker.Event) loop.Worker {
	r.jobs = append(r.jobs, job)
	s := workerScript{result: exits(0).result}
	if r.script != nil {
		s = r.script(job)
	}
	ref := job.Item.Ref()
	w := &loop.FakeWorker{}
	if s.untilStopped {
		r.send(events, worker.Started{Target: ref, PID: 1})
		w.OnStop = func() { r.send(events, worker.Ended{Target: ref, Result: worker.Result{Stopped: true}}) }
		return w
	}
	r.send(events, worker.Started{Target: ref, PID: 1}, worker.Ended{Target: ref, Result: s.result})
	return w
}

// send は evs を順に loop へ送る。送り終えるまで pending に数える (呼んだ時点で数えるので、loop の goroutine から呼べば
// 次に待つ条件が必ずそれを見る)。
func (r *rig) send(events chan<- worker.Event, evs ...worker.Event) {
	r.mu.Lock()
	r.pending++
	r.mu.Unlock()
	go func() {
		for _, ev := range evs {
			events <- ev
		}
		r.mu.Lock()
		r.pending--
		r.changed.Broadcast()
		r.mu.Unlock()
	}()
}

// after は timer の扱いに従う channel を返す。待った周期は waits に残す。
func (r *rig) after(d time.Duration) <-chan time.Time {
	r.waits = append(r.waits, d)
	t := timing{fate: neverFires}
	if r.timer != nil {
		t = r.timer(d)
	}
	ch := make(chan time.Time)
	if t.fate == neverFires {
		return ch
	}
	go func() {
		if !r.waitUntil(t.until) {
			return
		}
		if t.fate == stopsTheLoop {
			r.stopOnce.Do(func() {
				sig := r.stopSignal
				if sig == nil {
					sig = syscall.SIGINT
				}
				r.signals <- sig
			})
			return
		}
		r.mu.Lock()
		if !t.keepClock {
			r.now = r.now.Add(d)
		}
		at := r.now
		r.mu.Unlock()
		// loop が置き直して捨てた timer は受け取られないので、loop が止まるまでで諦める
		select {
		case ch <- at:
			r.mu.Lock()
			r.fired++
			r.changed.Broadcast()
			r.mu.Unlock()
		case <-r.done:
		}
	}()
	return ch
}

// events は log.jsonl に書いた行のうち、event が name のもの (name が空なら全部)。
func (r *rig) events(t *testing.T, name string) []map[string]any {
	t.Helper()
	var found []map[string]any
	for line := range strings.Lines(r.log.String()) {
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("log.jsonl の行 %q: %v", line, err)
		}
		if name == "" || fields["event"] == name {
			found = append(found, fields)
		}
	}
	return found
}
