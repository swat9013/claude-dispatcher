package loop_test

import (
	"bytes"
	"errors"
	"io"
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

// launchesInOneTick は、open な一覧が open を返し、作業対象の読み直しが read を返す置き場で 1 回だけ tick を回し、起動した
// worker と人が読む行を返す。並列上限は 1。
func launchesInOneTick(t *testing.T, triggers []trigger.Trigger, open []target.Item, read func(target.Ref) (target.Item, error)) ([]worker.Job, string) {
	t.Helper()
	def := workflow.Definition{Interval: time.Minute, MaxConcurrent: 1, MaxAttempts: 1, MaxRetryBackoff: 5 * time.Minute, Triggers: triggers}
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
				observe:  func() ([]target.Item, error) { return open, nil },
				reread:   read,
			}
		},
		Workspaces: func(workflow.Definition) loop.Workspaces { return noWorkspaces{} },
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
		Log:          io.Discard,
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

func TestCandidateClosedWhenReadAgainIsNotLaunched(t *testing.T) {
	read := func(target.Ref) (target.Item, error) {
		closed := readyIssue(1)
		closed.Closed = true
		return closed, nil
	}

	jobs, out := launchesInOneTick(t, []trigger.Trigger{readyTrigger}, []target.Item{readyIssue(1)}, read)

	if len(jobs) != 0 || !strings.Contains(out, "起動しない issue#1 (implement): 終端 (起動の直前に読み直した)") {
		t.Fatalf("起動 = %+v, 出力:\n%s", jobs, out)
	}
}

func TestCandidateThatLeftTheTriggerWhenReadAgainIsNotLaunched(t *testing.T) {
	read := func(ref target.Ref) (target.Item, error) { return target.Issue{Number: ref.Number}, nil }

	jobs, out := launchesInOneTick(t, []trigger.Trigger{readyTrigger}, []target.Item{readyIssue(1)}, read)

	if len(jobs) != 0 || !strings.Contains(out, "起動しない issue#1 (implement): trigger から外れた (起動の直前に読み直した)") {
		t.Fatalf("起動 = %+v, 出力:\n%s", jobs, out)
	}
}

func cannotBeRead(target.Ref) (target.Item, error) { return nil, errors.New("gh の失敗") }

func TestCandidateThatCannotBeReadAgainIsNotLaunched(t *testing.T) {
	jobs, out := launchesInOneTick(t, []trigger.Trigger{readyTrigger}, []target.Item{readyIssue(1)}, cannotBeRead)

	if len(jobs) != 0 {
		t.Fatalf("起動 = %+v, 出力:\n%s", jobs, out)
	}
}

func TestCandidateThatCannotBeReadAgainLeavesAnErrorLine(t *testing.T) {
	_, out := launchesInOneTick(t, []trigger.Trigger{readyTrigger}, []target.Item{readyIssue(1)}, cannotBeRead)

	if !strings.Contains(out, "error issue#1: 起動しようとした作業対象を読み直せない (次の tick で試み直す): gh の失敗") {
		t.Fatalf("出力:\n%s", out)
	}
}

func TestCandidateCLWhoseConflictIsStillBeingComputedWhenReadAgainIsNotLaunched(t *testing.T) {
	conflicting := true
	fixConflict := trigger.Trigger{Name: "fix-conflict", On: target.KindCL, CL: trigger.CLPredicate{Conflict: &conflicting}}
	read := func(ref target.Ref) (target.Item, error) {
		return target.CL{Number: ref.Number, Head: "worktree-issue-1", SameRepo: true, Mergeable: target.MergeUnknown}, nil
	}

	jobs, out := launchesInOneTick(t, []trigger.Trigger{fixConflict},
		[]target.Item{target.CL{Number: 7, Head: "worktree-issue-1", SameRepo: true, Mergeable: target.MergeConflict}}, read)

	if len(jobs) != 0 || !strings.Contains(out, "起動しない cl#7 (fix-conflict): 当たるかをまだ決められない (起動の直前に読み直した)") {
		t.Fatalf("起動 = %+v, 出力:\n%s", jobs, out)
	}
}

func TestSlotOfACandidateNotLaunchedGoesToTheNextCandidate(t *testing.T) {
	read := func(ref target.Ref) (target.Item, error) {
		if ref.Number == 1 {
			return target.Issue{Number: 1, Closed: true}, nil
		}
		return readyIssue(ref.Number), nil
	}

	jobs, out := launchesInOneTick(t, []trigger.Trigger{readyTrigger}, []target.Item{readyIssue(1), readyIssue(2)}, read)

	if len(jobs) != 1 || jobs[0].Item.Ref().Number != 2 {
		t.Fatalf("起動 = %+v, want issue#2 を 1 本。出力:\n%s", jobs, out)
	}
}

func TestLaunchedWorkerIsGivenTheIssueAsReadAgain(t *testing.T) {
	read := func(ref target.Ref) (target.Item, error) {
		issue := readyIssue(ref.Number)
		issue.Title = "読み直した題名"
		return issue, nil
	}

	jobs, out := launchesInOneTick(t, []trigger.Trigger{readyTrigger}, []target.Item{readyIssue(1)}, read)

	if len(jobs) != 1 || jobs[0].Item.Heading() != "読み直した題名" {
		t.Fatalf("起動 = %+v, 出力:\n%s", jobs, out)
	}
}
