package loop_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/loop"
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

// run は loop を ticks 回の tick まで回して止め、起動した worker と、回した fixture (log.jsonl の行を引く) を返す。
func (s staleTicks) run(t *testing.T) ([]worker.Job, *rig) {
	t.Helper()
	const interval = time.Minute
	def := workflow.Definition{Tracker: githubTracker, Interval: interval, MaxConcurrent: 1, MaxAttempts: s.maxAttempts, MaxRetryBackoff: 5 * time.Minute, Triggers: s.triggers}
	r := newRig()
	r.script = func(worker.Job) workerScript { return exits(1) }
	tick, periods := 0, 0
	// 周期は、起動した worker の終わりを loop が処理し終えてから明ける。時計は workflow 定義を読むたびに周期だけ進むので、
	// worker の終わりは必ずそれを起動した tick の時刻で受け取る
	r.timer = func(kind timerKind, _ time.Duration) timing {
		if kind != periodTimer {
			return timing{fate: neverFires}
		}
		periods++
		if periods >= s.ticks {
			return timing{fate: stopsTheLoop}
		}
		return timing{fate: fires, keepClock: true}
	}
	r.run(t, loop.Options{
		Load: func() (workflow.Definition, error) {
			tick++
			r.setClock(time.Duration(tick-1) * interval)
			return def, nil
		},
		Definition: def,
		Store: func(workflow.Definition) loop.Store {
			return memoryStore{
				observe: func() ([]target.Item, error) { return s.open(tick), nil },
				reread: func(target.Ref) (target.Item, error) {
					if tick == s.readFails {
						return nil, errors.New("gh の失敗")
					}
					return s.read(tick), nil
				},
			}.store()
		},
	})
	return r.jobs, r
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
	_, r := closedDuringTheBackoffWhileTheListIsStale().run(t)

	releases := r.logEvents(t, "release")
	if len(releases) != 1 || releases[0]["target"] != "issue#1" || releases[0]["reason"] != "terminal" {
		t.Fatalf("release の行 = %v, want issue#1 を terminal で 1 行", releases)
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
	jobs, r := abandonedThenListed(nil, readyIssue(1)).run(t)

	if len(jobs) != 1 || len(r.logEvents(t, "unabandon")) != 0 {
		t.Fatalf("起動した数 = %d, unabandon の行 = %v, want 打ち切ったまま (古い一覧から抜けただけで解いた)", len(jobs), r.logEvents(t, "unabandon"))
	}
}

func TestAbandonmentIsKeptWhenAStaleListShowsTheIssueOutOfTheTriggerButItStillMatches(t *testing.T) {
	jobs, r := abandonedThenListed([]target.Item{unlabeledIssue(1)}, readyIssue(1)).run(t)

	if len(jobs) != 1 || len(r.logEvents(t, "unabandon")) != 0 {
		t.Fatalf("起動した数 = %d, unabandon の行 = %v, want 打ち切ったまま (古い一覧で外れて見えただけで解いた)", len(jobs), r.logEvents(t, "unabandon"))
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
	_, r := unreadableAfterDroppingOut().run(t)

	if unabandons := r.logEvents(t, "unabandon"); len(unabandons) != 0 {
		t.Fatalf("unabandon の行 = %v, want 無し (読み直せないのに打ち切りを解いた)", unabandons)
	}
}

func TestAbandonedIssueThatCannotBeReadAgainLeavesAnErrorLine(t *testing.T) {
	_, r := unreadableAfterDroppingOut().run(t)

	errs := r.logEvents(t, "error")
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

	_, r := s.run(t)

	if abandons, unabandons := r.logEvents(t, "abandon"), r.logEvents(t, "unabandon"); len(abandons) != 1 || len(unabandons) != 0 {
		t.Fatalf("abandon の行 = %v, unabandon の行 = %v, want cl#7 を打ち切ったまま (古い一覧で曖昧に見えただけで解いた)", abandons, unabandons)
	}
}

func TestAbandonmentOfACLIsLiftedWhenItIsMergedEvenIfItsConflictIsUnknown(t *testing.T) {
	conflicting := true
	fixConflict := trigger.Trigger{Name: "fix-conflict", On: target.KindCL, CL: trigger.CLPredicate{Conflict: &conflicting}}
	inConflict := target.CL{Number: 7, Head: "feature", HeadRepo: "acme/widgets", SameRepo: true, Mergeable: target.MergeConflict}
	// merge した CL は conflict を計算しない
	merged := target.CL{Number: 7, Head: "feature", HeadRepo: "acme/widgets", SameRepo: true, Closed: true, Mergeable: target.MergeUnknown}
	s := staleTicks{triggers: []trigger.Trigger{fixConflict}, maxAttempts: 1, ticks: 2,
		open: func(tick int) []target.Item {
			if tick == 2 {
				return nil
			}
			return []target.Item{inConflict}
		},
		read: func(tick int) target.Item {
			if tick == 2 {
				return merged
			}
			return inConflict
		}}

	_, r := s.run(t)

	if unabandons := r.logEvents(t, "unabandon"); len(unabandons) != 1 || unabandons[0]["target"] != "cl#7" {
		t.Fatalf("unabandon の行 = %v, want cl#7 を 1 行 (merge した CL の打ち切りを保った)", unabandons)
	}
}

func TestAbandonmentIsLiftedWhenTheIssueReadAgainLeftTheTrigger(t *testing.T) {
	_, r := abandonedThenListed([]target.Item{unlabeledIssue(1)}, unlabeledIssue(1)).run(t)

	if unabandons := r.logEvents(t, "unabandon"); len(unabandons) != 1 || unabandons[0]["target"] != "issue#1" {
		t.Fatalf("unabandon の行 = %v, want issue#1 を 1 行", unabandons)
	}
}

func TestAbandonmentIsLiftedWhenTheIssueReadAgainIsClosed(t *testing.T) {
	_, r := abandonedThenListed(nil, closedIssue(1)).run(t)

	if unabandons := r.logEvents(t, "unabandon"); len(unabandons) != 1 || unabandons[0]["target"] != "issue#1" {
		t.Fatalf("unabandon の行 = %v, want issue#1 を 1 行", unabandons)
	}
}
