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
	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/worker"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// 再起動の直前の読み直し (formats.md §6 の「再起動」)。tick の中で再起動するときも、open な一覧を使わずに置き場から
// 読み直す。

// retryInTheSecondTick は、1 回目の tick で起動した issue#1 の worker が trigger に当たったまま終わり (再起動待ち)、
// 2 回目の tick の中で再起動を試みる loop。open な一覧はどの tick でも open (古い)、置き場からの読み直しは、2 回目の
// tick からは readInSecondTick を返す。backoff の明けを tick を待たずに知らせる wake は届けないので、再起動は必ず
// 2 回目の tick の中で試みる。
type retryInTheSecondTick struct {
	triggers []trigger.Trigger
	// open は open な一覧が返す issue#1
	open target.Issue
	// readInSecondTick は、2 回目の tick からの読み直しが返す issue#1 (それまでは open を返す)
	readInSecondTick target.Issue
}

// run は loop を 2 回目の tick まで回して止め、起動した worker と log.jsonl の行を返す。
func (r retryInTheSecondTick) run(t *testing.T) ([]worker.Job, []map[string]any) {
	t.Helper()
	const interval = time.Minute
	def := workflow.Definition{Interval: interval, MaxConcurrent: 1, MaxAttempts: 2, MaxRetryBackoff: 5 * time.Minute, Triggers: r.triggers}
	var (
		start      = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		ticks      int
		jobs       []worker.Job
		workers    sync.WaitGroup
		periods    int
		log        bytes.Buffer
		stdout     bytes.Buffer
		signals    = make(chan os.Signal, 1)
		neverFires = make(chan time.Time)
	)
	exit := loop.Run(loop.Options{
		Load: func() (workflow.Definition, error) {
			ticks++
			return def, nil
		},
		Definition: def,
		Precheck:   noProblems,
		Store: func(workflow.Definition) loop.Store {
			return memoryStore{
				scopeKey: func() string { return startScope },
				observe:  func() ([]target.Item, error) { return []target.Item{r.open}, nil },
				reread: func(target.Ref) (target.Item, error) {
					if ticks >= 2 {
						return r.readInSecondTick, nil
					}
					return r.open, nil
				},
			}
		},
		Workspaces: func(workflow.Definition) loop.Workspaces { return noWorkspaces{} },
		Launch: func(_ workflow.Definition, job worker.Job, events chan<- worker.Event) loop.Worker {
			jobs = append(jobs, job)
			workers.Add(1)
			go func() {
				defer workers.Done()
				events <- worker.Started{Target: job.Item.Ref(), PID: 1}
				code := 1
				events <- worker.Ended{Target: job.Item.Ref(), Result: worker.Result{ExitCode: &code}}
			}()
			return endedWorker{}
		},
		NewSessionID: func() (string, error) { return "session-1", nil },
		ScopeKey:     startScope,
		Log:          &log,
		Output:       loop.Appender{W: &stdout},
		Publish:      func(status.Snapshot) error { return nil },
		Signals:      signals,
		// 時計は tick を始めるたびに周期だけ進む。loop の goroutine だけが読み書きするので、1 回目の worker の終わりは
		// 必ず 1 回目の tick の時刻で受け取る
		Now: func() time.Time { return start.Add(time.Duration(max(ticks-1, 0)) * interval) },
		After: func(d time.Duration) <-chan time.Time {
			if d != interval {
				return neverFires
			}
			periods++
			if periods > 1 {
				// 2 回目の tick の後は止まる
				signals <- syscall.SIGINT
				return neverFires
			}
			ch := make(chan time.Time, 1)
			go func() {
				// 1 回目の worker の終わりを loop が受け取ってから (再起動待ちになってから) 周期を明ける
				workers.Wait()
				ch <- time.Time{}
			}()
			return ch
		},
	})
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
	var lines []map[string]any
	for line := range strings.Lines(log.String()) {
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("log.jsonl の行 %q: %v", line, err)
		}
		lines = append(lines, fields)
	}
	return jobs, lines
}

func closedDuringTheBackoffWhileTheListIsStale() retryInTheSecondTick {
	closed := readyIssue(1)
	closed.Closed = true
	return retryInTheSecondTick{triggers: []trigger.Trigger{readyTrigger}, open: readyIssue(1), readInSecondTick: closed}
}

func TestRetryOfAnIssueClosedDuringTheBackoffIsNotLaunchedWhileTheListIsStale(t *testing.T) {
	jobs, _ := closedDuringTheBackoffWhileTheListIsStale().run(t)

	if len(jobs) != 1 {
		t.Fatalf("起動した数 = %d, want 1 回目の attempt だけ (古い一覧の issue#1 を再起動した)", len(jobs))
	}
}

func TestRetryOfAnIssueClosedDuringTheBackoffIsReleasedAsTerminal(t *testing.T) {
	_, lines := closedDuringTheBackoffWhileTheListIsStale().run(t)

	var releases []map[string]any
	for _, line := range lines {
		if line["event"] == "release" {
			releases = append(releases, line)
		}
	}
	if len(releases) != 1 || releases[0]["target"] != "issue#1" || releases[0]["reason"] != "終端" {
		t.Fatalf("release の行 = %v, want issue#1 を 終端 で 1 行", releases)
	}
}

func TestRetryContinuesWithTheSameTriggerEvenIfAnEarlierTriggerNowMatches(t *testing.T) {
	review := trigger.Trigger{Name: "review", On: target.KindIssue, Issue: trigger.IssuePredicate{LabelsAll: []string{"needs-review"}}}
	both := readyIssue(1)
	both.Labels = append(both.Labels, "needs-review")
	r := retryInTheSecondTick{triggers: []trigger.Trigger{review, readyTrigger}, open: readyIssue(1), readInSecondTick: both}

	jobs, _ := r.run(t)

	if len(jobs) != 2 || jobs[1].Trigger.Name != "implement" || jobs[1].Attempt != 2 {
		t.Fatalf("起動 = %+v, want implement の attempt 2 (再起動は同じ trigger で続ける)", jobs)
	}
}
