package loop_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/places"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// 前の loop が残した claude (orphan) の扱い (system.md §7、formats.md §6 の orphan)。

// orphanTicks は tick ごとに orphan を確かめる fake。aliveUntil 回目の確かめまで orphan が生きている。
type orphanTicks struct {
	checks     int
	aliveUntil int
	err        error
}

func (o *orphanTicks) orphaned(target.Ref) (bool, error) {
	o.checks++
	if o.err != nil {
		return false, o.err
	}
	return o.checks <= o.aliveUntil, nil
}

// periodsThenStop は n 回の周期を明けさせ、その次の周期で loop を止める timer。
func periodsThenStop(n int) func(kind timerKind, _ time.Duration) timing {
	periods := 0
	return func(kind timerKind, _ time.Duration) timing {
		if kind != periodTimer {
			return timing{fate: neverFires}
		}
		periods++
		if periods <= n {
			return timing{fate: fires}
		}
		return timing{fate: stopsTheLoop}
	}
}

func orphanDefinition() workflow.Definition {
	return workflow.Definition{Tracker: githubTracker, Interval: time.Minute, MaxConcurrent: 2, MaxAttempts: 3, MaxRetryBackoff: 5 * time.Minute, Triggers: []trigger.Trigger{readyTrigger}}
}

func TestIssueWithALiveOrphanIsNotLaunchedUntilTheOrphanEnds(t *testing.T) {
	// workspace issue#1 は claim が無く、tick 1・2 では前の loop の claude が生きている。tick 3 で終わっている
	r := newRig()
	r.timer = periodsThenStop(2)
	o := &orphanTicks{aliveUntil: 2}
	ready := memoryStore{
		observe: func() ([]target.Item, error) { return []target.Item{readyIssue(1)}, nil },
		reread:  func(target.Ref) (target.Item, error) { return readyIssue(1), nil },
	}

	r.run(t, loop.Options{
		Definition: orphanDefinition(),
		Store:      func(workflow.Definition) places.Store { return ready.store() },
		Workspaces: func(workflow.Definition) loop.Workspaces { return &removals{} },
		Orphaned:   o.orphaned,
	})

	if len(r.jobs) != 1 {
		t.Fatalf("起動した worker = %d, want 1 (orphan が終わった tick 3 だけ)", len(r.jobs))
	}
	found, ended := r.logEvents(t, "orphan"), r.logEvents(t, "orphan_end")
	if len(found) != 1 || found[0]["target"] != "issue#1" || len(ended) != 1 || ended[0]["target"] != "issue#1" {
		t.Errorf("orphan の行 = %v, orphan_end の行 = %v, want issue#1 で 1 行ずつ", found, ended)
	}
	ticks := r.logEvents(t, "tick")
	if len(ticks) < 3 || ticks[0]["orphans"] != float64(1) || ticks[1]["orphans"] != float64(1) || ticks[2]["orphans"] != float64(0) {
		t.Errorf("tick の行 = %v, want orphans が 1・1・0", ticks)
	}
	if got := r.published[1].Orphans; len(got) != 1 || got[0].Target != "issue#1" {
		t.Errorf("tick 1 の後の状態 file の orphans = %v, want issue#1", got)
	}
	if got := r.published[len(r.published)-1].Orphans; len(got) != 0 {
		t.Errorf("orphan が終わった後の状態 file の orphans = %v, want なし", got)
	}
}

func TestWorkspaceOfATerminalIssueWithALiveOrphanIsNotRemovedUntilTheOrphanEnds(t *testing.T) {
	r := newRig()
	r.timer = periodsThenStop(1)
	o := &orphanTicks{aliveUntil: 1}
	ws := &removals{}
	closed := readyIssue(1)
	closed.Closed = true
	terminal := memoryStore{
		observe: func() ([]target.Item, error) { return nil, nil },
		reread:  func(target.Ref) (target.Item, error) { return closed, nil },
	}

	r.run(t, loop.Options{
		Definition: orphanDefinition(),
		Store:      func(workflow.Definition) places.Store { return terminal.store() },
		Workspaces: func(workflow.Definition) loop.Workspaces { return ws },
		Orphaned:   o.orphaned,
	})

	if len(ws.removed) != 1 || o.checks != 2 {
		t.Fatalf("消した workspace = %v (orphan を確かめた回数 %d), want orphan が終わった tick 2 で issue#1 を 1 回", ws.removed, o.checks)
	}
}

func TestLiveOrphanTakesASlotOfTheConcurrencyLimit(t *testing.T) {
	// 並列上限 2 のうち、orphan の issue#1 が 1 つ使う。候補の issue#2・#3 は 1 本だけ起動する
	r := newRig()
	r.timer = periodsThenStop(0)
	open := memoryStore{
		observe: func() ([]target.Item, error) {
			return []target.Item{readyIssue(1), readyIssue(2), readyIssue(3)}, nil
		},
		reread: func(ref target.Ref) (target.Item, error) { return readyIssue(ref.Number), nil },
	}

	r.run(t, loop.Options{
		Definition: orphanDefinition(),
		Store:      func(workflow.Definition) places.Store { return open.store() },
		Workspaces: func(workflow.Definition) loop.Workspaces { return &removals{} },
		Orphaned:   func(ref target.Ref) (bool, error) { return ref.Number == 1, nil },
	})

	if len(r.jobs) != 1 || r.jobs[0].Item.Ref().Number != 2 {
		t.Fatalf("起動した worker = %v, want issue#2 の 1 本", r.jobs)
	}
}

func TestTickThatCannotCheckForOrphansNeitherSweepsNorLaunches(t *testing.T) {
	r := newRig()
	r.timer = periodsThenStop(0)
	ws := &removals{}
	closed := readyIssue(1)
	closed.Closed = true
	store := memoryStore{
		observe: func() ([]target.Item, error) { return []target.Item{readyIssue(2)}, nil },
		reread: func(ref target.Ref) (target.Item, error) {
			if ref.Number == 1 {
				return closed, nil
			}
			return readyIssue(ref.Number), nil
		},
	}

	r.run(t, loop.Options{
		Definition: orphanDefinition(),
		Store:      func(workflow.Definition) places.Store { return store.store() },
		Workspaces: func(workflow.Definition) loop.Workspaces { return ws },
		Orphaned:   (&orphanTicks{err: errors.New("EIO")}).orphaned,
	})

	if len(r.jobs) != 0 || len(ws.removed) != 0 {
		t.Fatalf("起動 = %d・消した workspace = %v, want どちらもなし", len(r.jobs), ws.removed)
	}
	ticks := r.logEvents(t, "tick")
	if len(ticks) != 1 || ticks[0]["result"] != "error" || !strings.Contains(ticks[0]["error"].(string), "EIO") {
		t.Fatalf("tick の行 = %v, want 確かめられない理由を持つ error", ticks)
	}
}
