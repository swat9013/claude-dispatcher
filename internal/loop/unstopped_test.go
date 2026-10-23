package loop_test

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/places"
	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/worker"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// SIGKILL を送れず claude が終わっていない worker (system.md §7 の tick の手順 1、formats.md §6 の「止められない worker」)。

// removals は Remove を数え、workspace は作業対象 issue#1 の分だけ在るとする掃除の口。
type removals struct {
	mu      sync.Mutex
	removed []target.Ref
}

func (w *removals) Existing() ([]target.Ref, error) {
	return []target.Ref{{Kind: target.KindIssue, Number: 1}}, nil
}

func (w *removals) Remove(ref target.Ref) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.removed = append(w.removed, ref)
	return nil
}

func (*removals) Branch(target.Ref) (string, error) { return "", nil }

func TestAWorkerWhoseClaudeCannotBeStoppedIsNeitherVerifiedNorRestartedAndKeepsItsWorkspace(t *testing.T) {
	// tick 1 で起動した worker が stall で止められないと知らせ、loop の終了まで終わらない。tick 2 で作業対象は終端になるが、claim は止めている段階のまま
	// 持ち、workspace を消さない。停止要求はその worker を待たずに loop を止める
	r := newRig()
	periods := 0
	r.timer = func(kind timerKind, _ time.Duration) timing {
		if kind != periodTimer {
			return timing{fate: neverFires}
		}
		periods++
		if periods == 1 {
			return timing{fate: fires}
		}
		return timing{fate: stopsTheLoop}
	}
	r.script = func(worker.Job) workerScript {
		return workerScript{unstopped: "子を止められない: EPERM\nEPERM", neverEnds: true}
	}
	reads := 0
	ws := &removals{}
	def := workflow.Definition{Tracker: githubTracker, Interval: time.Minute, MaxConcurrent: 1, MaxAttempts: 3, MaxRetryBackoff: 5 * time.Minute, Triggers: []trigger.Trigger{readyTrigger}}

	r.run(t, loop.Options{
		Definition: def,
		Store: func(workflow.Definition) places.Store {
			return memoryStore{
				observe: func() ([]target.Item, error) {
					if reads > 0 {
						return nil, nil
					}
					return []target.Item{readyIssue(1)}, nil
				},
				reread: func(target.Ref) (target.Item, error) {
					reads++
					if reads == 1 {
						// 起動の直前の読み直し
						return readyIssue(1), nil
					}
					closed := readyIssue(1)
					closed.Closed = true
					return closed, nil
				},
			}.store()
		},
		Workspaces: func(workflow.Definition) loop.Workspaces { return ws },
	})

	if len(r.jobs) != 1 {
		t.Errorf("起動した worker = %d, want 1 (止められない worker の作業対象は再起動しない)", len(r.jobs))
	}
	if reads != 1 {
		t.Errorf("作業対象の読み直し = %d, want 1 (起動の直前だけ。終わりを確かめず、止めている claim は突き合わせない)", reads)
	}
	if len(ws.removed) != 0 {
		t.Errorf("消した workspace = %v, want なし", ws.removed)
	}
	for _, name := range []string{"end", "retry", "abandon"} {
		if got := r.logEvents(t, name); len(got) != 0 {
			t.Errorf("%s の行 = %v, want なし", name, got)
		}
	}
	var messages []string
	for _, e := range r.logEvents(t, "error") {
		messages = append(messages, e["error"].(string))
	}
	joined := strings.Join(messages, "\n")
	for _, want := range []string{"claude を止められない (pid 1, workspace ", "子を止められない: EPERM / EPERM", "claude を止められないまま止まる (pid 1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("error の行 = %q, want %q を含む", joined, want)
		}
	}
	// 状態 file では普通に止めている worker と見分けられ、pid と workspace が載る (formats.md §7.1)
	var unstopped *status.Worker
	for _, s := range r.published {
		for _, w := range s.Workers {
			if w.Phase == status.Unstopped {
				unstopped = &w
			}
		}
	}
	if unstopped == nil || unstopped.PID != 1 {
		t.Errorf("状態 file の止められない worker = %+v, want 段階 unstopped・pid 1", unstopped)
	}
}

func TestAnUnstoppedWorkerWhoseClaudeLaterExitsIsVerifiedAndNotReportedAtLoopEnd(t *testing.T) {
	// stall で止められないと知らせた worker の claude が後で終わる。loop は終わりを受けて作業対象を確かめ、
	// loop の終了の行にその pid を残っているとは書かない
	r := newRig()
	r.timer = func(kind timerKind, _ time.Duration) timing {
		if kind != periodTimer {
			return timing{fate: neverFires}
		}
		return timing{fate: stopsTheLoop}
	}
	r.script = func(worker.Job) workerScript {
		return workerScript{unstopped: "子を止められない: EPERM", result: worker.Result{Failure: "stall"}}
	}
	reads := 0
	def := workflow.Definition{Tracker: githubTracker, Interval: time.Minute, MaxConcurrent: 1, MaxAttempts: 3, MaxRetryBackoff: 5 * time.Minute, Triggers: []trigger.Trigger{readyTrigger}}

	r.run(t, loop.Options{
		Definition: def,
		Store: func(workflow.Definition) places.Store {
			return memoryStore{
				observe: func() ([]target.Item, error) { return []target.Item{readyIssue(1)}, nil },
				reread: func(target.Ref) (target.Item, error) {
					reads++
					if reads == 1 {
						return readyIssue(1), nil
					}
					closed := readyIssue(1)
					closed.Closed = true
					return closed, nil
				},
			}.store()
		},
		Workspaces: func(workflow.Definition) loop.Workspaces { return &removals{} },
	})

	// claude が終わった後は、after_run の間も止められない worker として出さない (終わった pid を止めさせない)
	for _, s := range r.published {
		for _, w := range s.Workers {
			if w.Phase == status.Unstopped || w.PID != 0 {
				t.Errorf("claude が終わった後の状態 file の worker = %+v, want unstopped でなく pid なし", w)
			}
		}
	}
	if reads != 2 {
		t.Errorf("作業対象の読み直し = %d, want 2 (起動の直前と、終わりの確かめ)", reads)
	}
	if got := r.logEvents(t, "end"); len(got) != 1 || got[0]["outcome"] != "completed" {
		t.Errorf("end の行 = %v, want completed の 1 行", got)
	}
	for _, e := range r.logEvents(t, "error") {
		if strings.Contains(e["error"].(string), "止められないまま止まる") {
			t.Errorf("error の行 = %q, want 終わった claude を残っているとは書かない", e["error"])
		}
	}
}

func TestTheStopWaitsForAnUnstoppedWorkerWhoseClaudeHasExited(t *testing.T) {
	// 停止要求が、止められないと知らせた worker の claude が終わった後、worker の終わりより前に届く。loop は worker の
	// 終わり (after_run の後) を待ってから止まり、終わった claude を残っているとは書かない
	r := newRig()
	r.signals = make(chan os.Signal, 1)
	r.signals <- os.Interrupt
	r.script = func(worker.Job) workerScript {
		return workerScript{unstopped: "子を止められない: EPERM", result: worker.Result{Failure: "stall"}}
	}
	def := workflow.Definition{Tracker: githubTracker, Interval: time.Minute, MaxConcurrent: 1, MaxAttempts: 3, MaxRetryBackoff: 5 * time.Minute, Triggers: []trigger.Trigger{readyTrigger}}

	r.run(t, loop.Options{
		Definition: def,
		Store: func(workflow.Definition) places.Store {
			return memoryStore{
				observe: func() ([]target.Item, error) { return []target.Item{readyIssue(1)}, nil },
				reread:  func(target.Ref) (target.Item, error) { return readyIssue(1), nil },
			}.store()
		},
		Workspaces: func(workflow.Definition) loop.Workspaces { return &removals{} },
	})

	if got := r.logEvents(t, "end"); len(got) != 1 {
		t.Errorf("end の行 = %v, want 1 行 (worker の終わりを待って止まる)", got)
	}
	for _, e := range r.logEvents(t, "error") {
		if strings.Contains(e["error"].(string), "止められないまま止まる") {
			t.Errorf("error の行 = %q, want 終わった claude を残っているとは書かない", e["error"])
		}
	}
}
