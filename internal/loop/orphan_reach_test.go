package loop_test

import (
	"errors"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/places"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// orphan が workspace の一覧の外や、別の作業対象の起動に及ぼす扱い (formats.md §6 の orphan)。

// orphanWorkspaces は refs の workspace が在り、branches の branch を checkout している掃除の口。existingErr があれば一覧を
// 読めない。
type orphanWorkspaces struct {
	refs        []target.Ref
	branches    map[target.Ref]string
	existingErr error
}

func (w orphanWorkspaces) Existing() ([]target.Ref, error) { return w.refs, w.existingErr }
func (orphanWorkspaces) Remove(target.Ref) error           { return nil }
func (w orphanWorkspaces) Branch(ref target.Ref) (string, error) {
	return w.branches[ref], nil
}

// readyStore は issue#1 を ready のまま返す置き場。
func readyStore(workflow.Definition) places.Store {
	return memoryStore{
		observe: func() ([]target.Item, error) { return []target.Item{readyIssue(1)}, nil },
		reread:  func(target.Ref) (target.Item, error) { return readyIssue(1), nil },
	}.store()
}

func TestTickThatCannotListWorkspacesDoesNotLaunch(t *testing.T) {
	r := newRig()
	r.timer = periodsThenStop(0)

	r.run(t, loop.Options{
		Definition: orphanDefinition(),
		Store:      readyStore,
		Workspaces: func(workflow.Definition) loop.Workspaces {
			return orphanWorkspaces{existingErr: errors.New("EACCES")}
		},
	})

	if len(r.jobs) != 0 {
		t.Fatalf("起動 = %d, want 0 (workspace の一覧を読めない tick は orphan を決められない)", len(r.jobs))
	}
	if ticks := r.logEvents(t, "tick"); len(ticks) != 1 || ticks[0]["result"] != "error" {
		t.Fatalf("tick の行 = %v, want error", ticks)
	}
}

func TestCandidateWithALiveOrphanButNoWorkspaceIsNotLaunched(t *testing.T) {
	// workspace.root を変えた・人が workspace を消した、などで、orphan の作業対象の workspace が一覧に無い
	r := newRig()
	r.timer = periodsThenStop(0)

	r.run(t, loop.Options{
		Definition: orphanDefinition(),
		Store:      readyStore,
		Orphaned:   func(target.Ref) (bool, error) { return true, nil },
	})

	if len(r.jobs) != 0 {
		t.Fatalf("起動 = %d, want 0", len(r.jobs))
	}
	if found := r.logEvents(t, "orphan"); len(found) != 1 || found[0]["target"] != "issue#1" {
		t.Fatalf("orphan の行 = %v, want issue#1", found)
	}
}

func TestCLWhoseHeadIsCheckedOutInTheWorkspaceOfALiveOrphanIsNotLaunched(t *testing.T) {
	fixCI := trigger.Trigger{Name: "fix-ci", On: target.KindCL}
	issue := target.Ref{Kind: target.KindIssue, Number: 1}
	cl := target.CL{Number: 7, Head: "worktree-issue-1", SameRepo: true}
	r := newRig()
	r.timer = periodsThenStop(0)
	def := orphanDefinition()
	def.Triggers = []trigger.Trigger{fixCI}

	r.run(t, loop.Options{
		Definition: def,
		Store: func(workflow.Definition) places.Store {
			return memoryStore{
				observe: func() ([]target.Item, error) { return []target.Item{cl}, nil },
				reread:  func(target.Ref) (target.Item, error) { return cl, nil },
			}.store()
		},
		Workspaces: func(workflow.Definition) loop.Workspaces {
			return orphanWorkspaces{refs: []target.Ref{issue}, branches: map[target.Ref]string{issue: "worktree-issue-1"}}
		},
		Orphaned: func(ref target.Ref) (bool, error) { return ref == issue, nil },
	})

	if len(r.jobs) != 0 {
		t.Fatalf("起動 = %+v, want なし (orphan の branch に CL の worker を重ねない)", r.jobs)
	}
}
