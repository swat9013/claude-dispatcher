package loop_test

import (
	"maps"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
)

// worker 1 回分の終わり方の end の行 (formats.md §4)。reason は token で、worker 自身の失敗は failure に分けて載せる。
// 人が読む行は token ではなく画面の文を出す。

// workerFailure は worker 自身の失敗の理由
const workerFailure = "異常終了 (exit status 1)"

// endedThenReadAs は、起動の直前の読み直しでは ready のままで、worker が終わった後の読み直しで afterEnd を返す
// issue#1 を 1 tick 回す。failure は worker 自身の失敗の理由。
func endedThenReadAs(afterEnd target.Issue, failure string) oneTick {
	reads := 0
	return oneTick{triggers: []trigger.Trigger{readyTrigger}, open: []target.Item{readyIssue(1)}, failure: failure, read: func(target.Ref) (target.Item, error) {
		reads++
		if reads == 1 {
			return readyIssue(1), nil
		}
		return afterEnd, nil
	}}
}

// endLine は tick を 1 回だけ回し、log.jsonl に書いた end の行の outcome・reason・failure (無ければ key ごと無い) と、
// 人が読む行を返す。
func (o oneTick) endLine(t *testing.T) (map[string]any, string) {
	t.Helper()
	r := o.rig(t)
	ends := r.logEvents(t, "end")
	if len(ends) != 1 {
		t.Fatalf("end の行 = %v, want 1 行", ends)
	}
	got := map[string]any{}
	for _, key := range []string{"outcome", "reason", "failure"} {
		if v, ok := ends[0][key]; ok {
			got[key] = v
		}
	}
	return got, r.stdout.String()
}

func assertEnd(t *testing.T, got, want map[string]any) {
	t.Helper()
	if !maps.Equal(got, want) {
		t.Fatalf("end の行の outcome・reason・failure = %v, want %v", got, want)
	}
}

func TestEndOfAWorkerThatFailedButWhoseIssueClosedIsCompletedWithTheFailureApart(t *testing.T) {
	got, _ := endedThenReadAs(closedIssue(1), workerFailure).endLine(t)

	assertEnd(t, got, map[string]any{"outcome": "completed", "reason": "terminal", "failure": workerFailure})
}

func TestEndOfAWorkerThatFailedButWhoseIssueClosedShowsTheFailureAfterTheReason(t *testing.T) {
	_, out := endedThenReadAs(closedIssue(1), workerFailure).endLine(t)

	if !strings.Contains(out, "終了 issue#1 (implement): completed — 終端 (worker は "+workerFailure+")\n") {
		t.Fatalf("出力:\n%s", out)
	}
}

func TestEndOfAWorkerThatFailedWithTheIssueStillMatchingIsFailedWithTheFailureApart(t *testing.T) {
	got, _ := endedThenReadAs(readyIssue(1), workerFailure).endLine(t)

	assertEnd(t, got, map[string]any{"outcome": "failed", "reason": "still_matches", "failure": workerFailure})
}

func TestEndOfAWorkerThatFailedWithTheIssueStillMatchingShowsOnlyTheFailure(t *testing.T) {
	_, out := endedThenReadAs(readyIssue(1), workerFailure).endLine(t)

	if !strings.Contains(out, "終了 issue#1 (implement): failed — "+workerFailure+"\n") {
		t.Fatalf("出力:\n%s", out)
	}
}

func TestEndOfAWorkerThatDidNotFailHasNoFailure(t *testing.T) {
	got, _ := endedThenReadAs(closedIssue(1), "").endLine(t)

	assertEnd(t, got, map[string]any{"outcome": "completed", "reason": "terminal"})
}
