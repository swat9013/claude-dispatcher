package loop_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"strings"
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

// 起動の直前の読み直し (formats.md §6 の tick の手順 7)。open な一覧の検索が古い結果を返しても、完了した直後の作業対象を
// 起動し直さない。

// readyTrigger は ready の label の付いた issue に当たる trigger
var readyTrigger = trigger.Trigger{Name: "implement", On: target.KindIssue, Issue: trigger.IssuePredicate{LabelsAll: []string{"ready"}}}

func readyIssue(number int) target.Issue {
	return target.Issue{Number: number, Labels: []string{"ready"}}
}

// oneTick は 1 回だけ回す tick の置き場と workflow 定義。
type oneTick struct {
	triggers []trigger.Trigger
	// open は open な一覧が返す作業対象、read は作業対象の読み直しが返すもの
	open []target.Item
	read func(target.Ref) (target.Item, error)
	// maxConcurrent は並列上限。0 なら 1
	maxConcurrent int
	// branches は claim した作業対象の workspace で checkout されている branch
	branches map[target.Ref]string
}

// branchWorkspaces は、作業対象ごとに決まった branch を checkout している workspace の口。
type branchWorkspaces map[target.Ref]string

func (branchWorkspaces) Existing() ([]target.Ref, error)         { return nil, nil }
func (branchWorkspaces) Remove(target.Ref) error                 { return nil }
func (b branchWorkspaces) Branch(ref target.Ref) (string, error) { return b[ref], nil }

// run は tick を 1 回だけ回し、起動した worker と人が読む行を返す。
func (o oneTick) run(t *testing.T) ([]worker.Job, string) {
	t.Helper()
	return o.runLogging(t, io.Discard)
}

// recheckSkips は tick を 1 回だけ回し、log.jsonl に書いた recheck_skip の行を返す。
func (o oneTick) recheckSkips(t *testing.T) []map[string]any {
	t.Helper()
	var log bytes.Buffer
	o.runLogging(t, &log)
	var skips []map[string]any
	for line := range strings.Lines(log.String()) {
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("log.jsonl の行 %q: %v", line, err)
		}
		if fields["event"] == "recheck_skip" {
			skips = append(skips, fields)
		}
	}
	return skips
}

// assertOneRecheckSkip は、recheck_skip の行が 1 行だけで、その target・trigger・reason の値が want と同じことを確かめる
// (ほかの key は見ない)。
func assertOneRecheckSkip(t *testing.T, skips []map[string]any, want map[string]any) {
	t.Helper()
	if len(skips) != 1 || !maps.Equal(map[string]any{"target": skips[0]["target"], "trigger": skips[0]["trigger"], "reason": skips[0]["reason"]}, want) {
		t.Fatalf("recheck_skip の行 = %v, want %v を 1 行", skips, want)
	}
}

// runLogging は tick を 1 回だけ回し、log.jsonl の行を log に書く。
func (o oneTick) runLogging(t *testing.T, log io.Writer) ([]worker.Job, string) {
	t.Helper()
	def := workflow.Definition{Interval: time.Minute, MaxConcurrent: max(o.maxConcurrent, 1), MaxAttempts: 1, MaxRetryBackoff: 5 * time.Minute, Triggers: o.triggers}
	var stdout bytes.Buffer
	var jobs []worker.Job
	// tick の後に受ける停止要求で、起動した worker の終わりを待って止まる
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGINT
	exit := loop.Run(loop.Options{
		Load:       func() (workflow.Definition, error) { return def, nil },
		Definition: def,
		Precheck:   noProblems,
		Store: func(workflow.Definition) loop.Store {
			return memoryStore{
				scopeKey: func() string { return startScope },
				observe:  func() ([]target.Item, error) { return o.open, nil },
				reread:   o.read,
			}
		},
		Workspaces: func(workflow.Definition) loop.Workspaces { return branchWorkspaces(o.branches) },
		Launch: func(_ workflow.Definition, job worker.Job, events chan<- worker.Event) loop.Worker {
			jobs = append(jobs, job)
			go func() {
				events <- worker.Started{Target: job.Item.Ref(), PID: 1}
				code := 0
				events <- worker.Ended{Target: job.Item.Ref(), Result: worker.Result{ExitCode: &code}}
			}()
			return endedWorker{}
		},
		NewSessionID: func() (string, error) { return "session-1", nil },
		ScopeKey:     startScope,
		Log:          log,
		Output:       loop.Appender{W: &stdout},
		Publish:      func(status.Snapshot) error { return nil },
		Signals:      signals,
		Now:          func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
		After:        func(time.Duration) <-chan time.Time { return nil },
	})
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
	return jobs, stdout.String()
}

func closedWhenReadAgain() oneTick {
	return oneTick{triggers: []trigger.Trigger{readyTrigger}, open: []target.Item{readyIssue(1)}, read: func(target.Ref) (target.Item, error) {
		closed := readyIssue(1)
		closed.Closed = true
		return closed, nil
	}}
}

func TestCandidateClosedWhenReadAgainIsNotLaunched(t *testing.T) {
	jobs, out := closedWhenReadAgain().run(t)

	if len(jobs) != 0 {
		t.Fatalf("起動 = %+v, 出力:\n%s", jobs, out)
	}
}

func TestCandidateClosedWhenReadAgainLeavesAPassLine(t *testing.T) {
	_, out := closedWhenReadAgain().run(t)

	if !strings.Contains(out, "読み直しで起動せず issue#1 (implement): 終端") {
		t.Fatalf("出力:\n%s", out)
	}
}

func TestCandidateClosedWhenReadAgainLeavesARecheckSkipEvent(t *testing.T) {
	skips := closedWhenReadAgain().recheckSkips(t)

	assertOneRecheckSkip(t, skips, map[string]any{"target": "issue#1", "trigger": "implement", "reason": "終端"})
}

func leftTheTriggerWhenReadAgain() oneTick {
	return oneTick{triggers: []trigger.Trigger{readyTrigger}, open: []target.Item{readyIssue(1)},
		read: func(ref target.Ref) (target.Item, error) { return target.Issue{Number: ref.Number}, nil }}
}

func TestCandidateThatLeftTheTriggerWhenReadAgainIsNotLaunched(t *testing.T) {
	jobs, out := leftTheTriggerWhenReadAgain().run(t)

	if len(jobs) != 0 {
		t.Fatalf("起動 = %+v, 出力:\n%s", jobs, out)
	}
}

func TestCandidateThatLeftTheTriggerWhenReadAgainLeavesAPassLine(t *testing.T) {
	_, out := leftTheTriggerWhenReadAgain().run(t)

	if !strings.Contains(out, "読み直しで起動せず issue#1 (implement): trigger から外れた") {
		t.Fatalf("出力:\n%s", out)
	}
}

func TestCandidateThatLeftTheTriggerWhenReadAgainLeavesARecheckSkipEvent(t *testing.T) {
	skips := leftTheTriggerWhenReadAgain().recheckSkips(t)

	assertOneRecheckSkip(t, skips, map[string]any{"target": "issue#1", "trigger": "implement", "reason": "trigger から外れた"})
}

func cannotBeReadAgain() oneTick {
	return oneTick{triggers: []trigger.Trigger{readyTrigger}, open: []target.Item{readyIssue(1)},
		read: func(target.Ref) (target.Item, error) { return nil, errors.New("gh の失敗") }}
}

func TestCandidateThatCannotBeReadAgainIsNotLaunched(t *testing.T) {
	jobs, out := cannotBeReadAgain().run(t)

	if len(jobs) != 0 {
		t.Fatalf("起動 = %+v, 出力:\n%s", jobs, out)
	}
}

func TestCandidateThatCannotBeReadAgainLeavesAnErrorLine(t *testing.T) {
	_, out := cannotBeReadAgain().run(t)

	if !strings.Contains(out, "error issue#1: 起動しようとした作業対象を読み直せない (次の tick で試み直す): gh の失敗") {
		t.Fatalf("出力:\n%s", out)
	}
}

func TestCandidateThatCannotBeReadAgainLeavesNoRecheckSkipEvent(t *testing.T) {
	skips := cannotBeReadAgain().recheckSkips(t)

	if len(skips) != 0 {
		t.Fatalf("recheck_skip の行 = %v, want 無し (error の行だけ)", skips)
	}
}

func conflictStillBeingComputedWhenReadAgain() oneTick {
	conflicting := true
	fixConflict := trigger.Trigger{Name: "fix-conflict", On: target.KindCL, CL: trigger.CLPredicate{Conflict: &conflicting}}
	return oneTick{triggers: []trigger.Trigger{fixConflict},
		open: []target.Item{target.CL{Number: 7, Head: "worktree-issue-1", SameRepo: true, Mergeable: target.MergeConflict}},
		read: func(ref target.Ref) (target.Item, error) {
			return target.CL{Number: ref.Number, Head: "worktree-issue-1", SameRepo: true, Mergeable: target.MergeUnknown}, nil
		}}
}

func TestCandidateCLWhoseConflictIsStillBeingComputedWhenReadAgainIsNotLaunched(t *testing.T) {
	jobs, out := conflictStillBeingComputedWhenReadAgain().run(t)

	if len(jobs) != 0 {
		t.Fatalf("起動 = %+v, 出力:\n%s", jobs, out)
	}
}

func TestCandidateCLWhoseConflictIsStillBeingComputedWhenReadAgainLeavesAPassLine(t *testing.T) {
	_, out := conflictStillBeingComputedWhenReadAgain().run(t)

	if !strings.Contains(out, "読み直しで起動せず cl#7 (fix-conflict): 当たるかをまだ決められない") {
		t.Fatalf("出力:\n%s", out)
	}
}

func TestCandidateCLWhoseConflictIsStillBeingComputedWhenReadAgainLeavesARecheckSkipEvent(t *testing.T) {
	skips := conflictStillBeingComputedWhenReadAgain().recheckSkips(t)

	assertOneRecheckSkip(t, skips, map[string]any{"target": "cl#7", "trigger": "fix-conflict", "reason": "当たるかをまだ決められない"})
}

func matchesAnEarlierTriggerWhenReadAgain() oneTick {
	review := trigger.Trigger{Name: "review", On: target.KindIssue, Issue: trigger.IssuePredicate{LabelsAll: []string{"needs-review"}}}
	return oneTick{triggers: []trigger.Trigger{review, readyTrigger}, open: []target.Item{readyIssue(1)},
		read: func(ref target.Ref) (target.Item, error) {
			return target.Issue{Number: ref.Number, Labels: []string{"ready", "needs-review"}}, nil
		}}
}

func TestCandidateThatMatchesAnEarlierTriggerWhenReadAgainIsNotLaunched(t *testing.T) {
	jobs, out := matchesAnEarlierTriggerWhenReadAgain().run(t)

	if len(jobs) != 0 {
		t.Fatalf("起動 = %+v, want 後ろの implement で起動しない。出力:\n%s", jobs, out)
	}
}

func TestCandidateThatMatchesAnEarlierTriggerWhenReadAgainLeavesAPassLine(t *testing.T) {
	_, out := matchesAnEarlierTriggerWhenReadAgain().run(t)

	if !strings.Contains(out, "読み直しで起動せず issue#1 (implement): 宣言順で先の trigger に当たる") {
		t.Fatalf("出力:\n%s", out)
	}
}

func TestCandidateThatMatchesAnEarlierTriggerWhenReadAgainLeavesARecheckSkipEvent(t *testing.T) {
	skips := matchesAnEarlierTriggerWhenReadAgain().recheckSkips(t)

	assertOneRecheckSkip(t, skips, map[string]any{"target": "issue#1", "trigger": "implement", "reason": "宣言順で先の trigger に当たる"})
}

func TestCandidateCLWhoseHeadIsHeldByAClaimWhenReadAgainIsNotLaunched(t *testing.T) {
	fixCI := trigger.Trigger{Name: "fix-ci", On: target.KindCL}
	issue := target.Ref{Kind: target.KindIssue, Number: 1}
	// 一覧では fork の CL (branch を重ねない)。読み直すと、issue#1 の workspace が checkout している同じ repo の branch
	tick := oneTick{triggers: []trigger.Trigger{readyTrigger, fixCI}, maxConcurrent: 2,
		open:     []target.Item{readyIssue(1), target.CL{Number: 7, Head: "worktree-issue-1"}},
		branches: map[target.Ref]string{issue: "worktree-issue-1"},
		read: func(ref target.Ref) (target.Item, error) {
			if ref == issue {
				return readyIssue(1), nil
			}
			return target.CL{Number: ref.Number, Head: "worktree-issue-1", SameRepo: true}, nil
		}}

	jobs, out := tick.run(t)

	if len(jobs) != 1 || jobs[0].Item.Ref() != issue {
		t.Fatalf("起動 = %+v, want issue#1 だけ。出力:\n%s", jobs, out)
	}
}

func TestSlotOfACandidateNotLaunchedGoesToTheNextCandidate(t *testing.T) {
	tick := oneTick{triggers: []trigger.Trigger{readyTrigger}, open: []target.Item{readyIssue(1), readyIssue(2)},
		read: func(ref target.Ref) (target.Item, error) {
			if ref.Number == 1 {
				return target.Issue{Number: 1, Closed: true}, nil
			}
			return readyIssue(ref.Number), nil
		}}

	jobs, out := tick.run(t)

	if len(jobs) != 1 || jobs[0].Item.Ref().Number != 2 {
		t.Fatalf("起動 = %+v, want issue#2 を 1 本。出力:\n%s", jobs, out)
	}
}

func TestLaunchedWorkerIsGivenTheIssueAsReadAgain(t *testing.T) {
	tick := oneTick{triggers: []trigger.Trigger{readyTrigger}, open: []target.Item{readyIssue(1)},
		read: func(ref target.Ref) (target.Item, error) {
			issue := readyIssue(ref.Number)
			issue.Title = "読み直した題名"
			return issue, nil
		}}

	jobs, out := tick.run(t)

	if len(jobs) != 1 || jobs[0].Item.Heading() != "読み直した題名" {
		t.Fatalf("起動 = %+v, 出力:\n%s", jobs, out)
	}
}
