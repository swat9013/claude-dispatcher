package loop_test

import (
	"bytes"
	"encoding/json"
	"errors"
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

// open な一覧が古いときの再起動と打ち切りの解除 (formats.md §6 の「再起動」と tick の手順 5)。一覧の検索は書き込みの直後に
// 古い結果を返しうるので、再起動と打ち切りの解除は置き場からの読み直しで確かめる。

// staleTicks は、tick ごとに open な一覧と置き場からの読み直しが別々のものを返す loop。issue#1 の worker は起動すると
// すぐに失敗で終わる (作業対象が trigger に当たったままなら failed)。backoff の明けを tick を待たずに知らせる wake は
// 届けないので、再起動は必ず tick の中で試みる。
type staleTicks struct {
	triggers []trigger.Trigger
	// maxAttempts は attempt の上限 (1 なら最初の失敗で打ち切る)
	maxAttempts int
	// ticks は回す tick の数
	ticks int
	// open は tick (1 始まり) ごとに open な一覧が返す作業対象
	open func(tick int) []target.Item
	// read は tick ごとに置き場からの読み直しが返す issue#1
	read func(tick int) target.Item
	// readFails は、置き場からの読み直しを失敗させる tick (0 なら失敗させない)
	readFails int
}

// run は loop を ticks 回の tick まで回して止め、起動した worker と log.jsonl の行を返す。
func (s staleTicks) run(t *testing.T) ([]worker.Job, []map[string]any) {
	t.Helper()
	const interval = time.Minute
	def := workflow.Definition{Interval: interval, MaxConcurrent: 1, MaxAttempts: s.maxAttempts, MaxRetryBackoff: 5 * time.Minute, Triggers: s.triggers}
	var (
		start      = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		tick       int
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
			tick++
			return def, nil
		},
		Definition: def,
		Precheck:   noProblems,
		Store: func(workflow.Definition) loop.Store {
			return memoryStore{
				scopeKey: func() string { return startScope },
				observe:  func() ([]target.Item, error) { return s.open(tick), nil },
				reread: func(target.Ref) (target.Item, error) {
					if tick == s.readFails {
						return nil, errors.New("gh の失敗")
					}
					return s.read(tick), nil
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
		// 時計は tick を始めるたびに周期だけ進む。loop の goroutine だけが読み書きするので、worker の終わりは必ず
		// それを起動した tick の時刻で受け取る
		Now: func() time.Time { return start.Add(time.Duration(max(tick-1, 0)) * interval) },
		After: func(d time.Duration) <-chan time.Time {
			if d != interval {
				return neverFires
			}
			periods++
			if periods >= s.ticks {
				signals <- syscall.SIGINT
				return neverFires
			}
			ch := make(chan time.Time, 1)
			go func() {
				// 起動した worker の終わりを loop が受け取ってから周期を明ける
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

// eventsNamed は log.jsonl の行のうち event が name のもの。
func eventsNamed(lines []map[string]any, name string) []map[string]any {
	var found []map[string]any
	for _, line := range lines {
		if line["event"] == name {
			found = append(found, line)
		}
	}
	return found
}

func closedIssue(number int) target.Issue {
	closed := readyIssue(number)
	closed.Closed = true
	return closed
}

func unlabeledIssue(number int) target.Issue {
	return target.Issue{Number: number}
}

// listing は、どの tick でも同じ作業対象を返す open な一覧。
func listing(items ...target.Item) func(int) []target.Item {
	return func(int) []target.Item { return items }
}

// --- 再起動 ---

// closedDuringTheBackoffWhileTheListIsStale は、issue#1 の worker が trigger に当たったまま終わり (再起動待ち)、
// backoff の間に閉じたが、2 回目の tick の一覧は古く issue#1 を open のまま返す loop。
func closedDuringTheBackoffWhileTheListIsStale() staleTicks {
	return staleTicks{triggers: []trigger.Trigger{readyTrigger}, maxAttempts: 2, ticks: 2, open: listing(readyIssue(1)),
		read: func(tick int) target.Item {
			if tick >= 2 {
				return closedIssue(1)
			}
			return readyIssue(1)
		}}
}

func TestRetryOfAnIssueClosedDuringTheBackoffIsNotLaunchedWhileTheListIsStale(t *testing.T) {
	jobs, _ := closedDuringTheBackoffWhileTheListIsStale().run(t)

	if len(jobs) != 1 {
		t.Fatalf("起動した数 = %d, want 1 回目の attempt だけ (古い一覧の issue#1 を再起動した)", len(jobs))
	}
}

func TestRetryOfAnIssueClosedDuringTheBackoffIsReleasedAsTerminal(t *testing.T) {
	_, lines := closedDuringTheBackoffWhileTheListIsStale().run(t)

	releases := eventsNamed(lines, "release")
	if len(releases) != 1 || releases[0]["target"] != "issue#1" || releases[0]["reason"] != "終端" {
		t.Fatalf("release の行 = %v, want issue#1 を 終端 で 1 行", releases)
	}
}

func TestRetryContinuesWithTheSameTriggerEvenIfAnEarlierTriggerNowMatches(t *testing.T) {
	review := trigger.Trigger{Name: "review", On: target.KindIssue, Issue: trigger.IssuePredicate{LabelsAll: []string{"needs-review"}}}
	both := readyIssue(1)
	both.Labels = append(both.Labels, "needs-review")
	s := staleTicks{triggers: []trigger.Trigger{review, readyTrigger}, maxAttempts: 2, ticks: 2, open: listing(readyIssue(1)),
		read: func(tick int) target.Item {
			if tick >= 2 {
				return both
			}
			return readyIssue(1)
		}}

	jobs, _ := s.run(t)

	if len(jobs) != 2 || jobs[1].Trigger.Name != "implement" || jobs[1].Attempt != 2 {
		t.Fatalf("起動 = %+v, want implement の attempt 2 (再起動は同じ trigger で続ける)", jobs)
	}
}

// --- 打ち切りの解除 ---

// abandonedThenListed は、1 回目の tick で起動した issue#1 の worker が trigger に当たったまま失敗して打ち切られ、
// 2 回目の tick の一覧が inSecondTick を返し (古い)、3 回目の tick の一覧が issue#1 を当たったまま返す loop。置き場からの
// 読み直しは、どの tick でも read を返す。
func abandonedThenListed(inSecondTick []target.Item, read target.Item) staleTicks {
	return staleTicks{triggers: []trigger.Trigger{readyTrigger}, maxAttempts: 1, ticks: 3,
		open: func(tick int) []target.Item {
			if tick == 2 {
				return inSecondTick
			}
			return []target.Item{readyIssue(1)}
		},
		read: func(tick int) target.Item {
			if tick == 1 {
				return readyIssue(1)
			}
			return read
		}}
}

func TestAbandonmentIsKeptWhenTheIssueDropsOutOfAStaleListButStillMatches(t *testing.T) {
	jobs, lines := abandonedThenListed(nil, readyIssue(1)).run(t)

	if len(jobs) != 1 || len(eventsNamed(lines, "unabandon")) != 0 {
		t.Fatalf("起動した数 = %d, unabandon の行 = %v, want 打ち切ったまま (古い一覧から抜けただけで解いた)", len(jobs), eventsNamed(lines, "unabandon"))
	}
}

func TestAbandonmentIsKeptWhenAStaleListShowsTheIssueOutOfTheTriggerButItStillMatches(t *testing.T) {
	jobs, lines := abandonedThenListed([]target.Item{unlabeledIssue(1)}, readyIssue(1)).run(t)

	if len(jobs) != 1 || len(eventsNamed(lines, "unabandon")) != 0 {
		t.Fatalf("起動した数 = %d, unabandon の行 = %v, want 打ち切ったまま (古い一覧で外れて見えただけで解いた)", len(jobs), eventsNamed(lines, "unabandon"))
	}
}

// unreadableAfterDroppingOut は、打ち切った issue#1 が 2 回目の tick の一覧から抜け、その tick の読み直しが失敗する
// loop。置き場では閉じている (読み直せれば打ち切りを解く)。
func unreadableAfterDroppingOut() staleTicks {
	s := abandonedThenListed(nil, closedIssue(1))
	s.ticks, s.readFails = 2, 2
	return s
}

func TestAbandonmentIsKeptWhenTheIssueThatDroppedOutOfTheListCannotBeReadAgain(t *testing.T) {
	_, lines := unreadableAfterDroppingOut().run(t)

	if unabandons := eventsNamed(lines, "unabandon"); len(unabandons) != 0 {
		t.Fatalf("unabandon の行 = %v, want 無し (読み直せないのに打ち切りを解いた)", unabandons)
	}
}

func TestAbandonedIssueThatCannotBeReadAgainLeavesAnErrorLine(t *testing.T) {
	_, lines := unreadableAfterDroppingOut().run(t)

	errs := eventsNamed(lines, "error")
	if len(errs) != 1 || errs[0]["target"] != "issue#1" || !strings.Contains(asText(errs[0]["error"]), "打ち切った作業対象を読み直せない") {
		t.Fatalf("error の行 = %v, want issue#1 の「打ち切った作業対象を読み直せない」を 1 行", errs)
	}
}

func asText(v any) string {
	s, _ := v.(string)
	return s
}

func TestAbandonmentOfACLIsKeptWhenAStaleListMakesItLookAmbiguous(t *testing.T) {
	fixCI := trigger.Trigger{Name: "fix-ci", On: target.KindCL}
	abandoned := target.CL{Number: 7, Head: "feature", HeadRepo: "acme/widgets", SameRepo: true}
	// 同じ head branch の #8 は close 済みだが、古い一覧はまだ open で返す
	closedSibling := target.CL{Number: 8, Head: "feature", HeadRepo: "acme/widgets", SameRepo: true}
	s := staleTicks{triggers: []trigger.Trigger{fixCI}, maxAttempts: 1, ticks: 2,
		open: func(tick int) []target.Item {
			if tick == 2 {
				return []target.Item{abandoned, closedSibling}
			}
			return []target.Item{abandoned}
		},
		read: func(int) target.Item { return abandoned }}

	_, lines := s.run(t)

	if abandons, unabandons := eventsNamed(lines, "abandon"), eventsNamed(lines, "unabandon"); len(abandons) != 1 || len(unabandons) != 0 {
		t.Fatalf("abandon の行 = %v, unabandon の行 = %v, want cl#7 を打ち切ったまま (古い一覧で曖昧に見えただけで解いた)", abandons, unabandons)
	}
}

func TestAbandonmentIsLiftedWhenTheIssueReadAgainLeftTheTrigger(t *testing.T) {
	_, lines := abandonedThenListed([]target.Item{unlabeledIssue(1)}, unlabeledIssue(1)).run(t)

	if unabandons := eventsNamed(lines, "unabandon"); len(unabandons) != 1 || unabandons[0]["target"] != "issue#1" {
		t.Fatalf("unabandon の行 = %v, want issue#1 を 1 行", unabandons)
	}
}

func TestAbandonmentIsLiftedWhenTheIssueReadAgainIsClosed(t *testing.T) {
	_, lines := abandonedThenListed(nil, closedIssue(1)).run(t)

	if unabandons := eventsNamed(lines, "unabandon"); len(unabandons) != 1 || unabandons[0]["target"] != "issue#1" {
		t.Fatalf("unabandon の行 = %v, want issue#1 を 1 行", unabandons)
	}
}
