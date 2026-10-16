package loop_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/loop"
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
	// tick 1 で起動した worker が stall で止められずに返る。tick 2 で作業対象は終端になるが、claim は止めている段階のまま
	// 持ち、workspace を消さない。停止要求はその worker を待たずに loop を止める
	r := newRig()
	periods := 0
	r.timer = func(time.Duration) timing {
		periods++
		if periods == 1 {
			return timing{fate: fires}
		}
		return timing{fate: stopsTheLoop}
	}
	r.script = func(worker.Job) workerScript {
		return workerScript{result: worker.Result{Failure: "stall", StopError: "子を止められない: EPERM", Unstopped: true}}
	}
	reads := 0
	ws := &removals{}
	def := workflow.Definition{Tracker: githubTracker, Interval: time.Minute, MaxConcurrent: 1, MaxAttempts: 3, MaxRetryBackoff: 5 * time.Minute, Triggers: []trigger.Trigger{readyTrigger}}

	r.run(t, loop.Options{
		Definition: def,
		Store: func(workflow.Definition) loop.Store {
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
		if got := r.events(t, name); len(got) != 0 {
			t.Errorf("%s の行 = %v, want なし", name, got)
		}
	}
	var messages []string
	for _, e := range r.events(t, "error") {
		messages = append(messages, e["error"].(string))
	}
	joined := strings.Join(messages, "\n")
	for _, want := range []string{"claude を止められない (pid 1, workspace ", "子を止められない: EPERM", "claude を止められないまま止まる (pid 1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("error の行 = %q, want %q を含む", joined, want)
		}
	}
}
