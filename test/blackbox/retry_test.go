package blackbox_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// retry・backoff・打ち切り・stall・上限時間 (formats.md §4 / §6 の再起動、system.md §7 の失敗の扱い)。
// backoff の上限を 1s にして、再起動を実時間で待てるようにする。

const fastRetry = "limits:\n  max_retry_backoff: 1s\n"

// startsOf は log.jsonl の start の行のうち、target の行を返す。
func (s *sandbox) startsOf(target string) []map[string]any {
	var found []map[string]any
	for _, line := range s.events("start") {
		if line["target"] == target {
			found = append(found, line)
		}
	}
	return found
}

func TestWorkerThatEndsStillMatchingTheTriggerIsRetriedAfterABackoff(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(fastRetry))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})

	s.startLoop()

	retry := s.waitEvents("retry", 1)[0]
	if retry["target"] != "issue#42" || retry["attempt"] != float64(2) || retry["backoff"] != float64(1) {
		t.Fatalf("retry の行 = %v", retry)
	}
	if start := s.waitEvents("start", 2)[1]; start["attempt"] != float64(2) {
		t.Fatalf("2 本目の start の行 = %v, want attempt 2", start)
	}
}

func TestRetriedAttemptResumesTheSameSession(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(fastRetry))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})

	s.startLoop()

	starts := s.waitEvents("start", 2)
	session := asString(starts[0]["session_id"])
	calls := s.calls("claude")
	if starts[1]["session_id"] != session || argValueAfter(calls[1].Argv, "--resume") != session || slices.Contains(calls[1].Argv, "--session-id") {
		t.Fatalf("2 回目の argv = %q, want --resume %s", calls[1].Argv, session)
	}
}

func TestIssueIsAbandonedWhenItsLastAttemptFails(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(fastRetry + "  max_attempts: 2\n"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})
	s.startLoop()

	abandon := s.waitEvents("abandon", 1)[0]

	if abandon["target"] != "issue#42" || abandon["attempts"] != float64(2) {
		t.Fatalf("abandon の行 = %v", abandon)
	}
	ticks := len(s.events("tick"))
	s.waitEvents("tick", ticks+2)
	if starts := s.events("start"); len(starts) != 2 {
		t.Fatalf("start の行 = %d 行, want 打ち切った後は起動しない 2 行", len(starts))
	}
}

// abandonedWorkflow は、宣言順で先の trigger (label urgent) と、打ち切られる trigger (label ready-for-agent) を持つ。
func (s *sandbox) abandonedWorkflow() string {
	return strings.Replace(s.workerWorkflow(fastRetry+"  max_attempts: 1\n"), "triggers:\n", `triggers:
  - name: urgent
    on: issue
    when: {labels: {all: [urgent]}}
    action: /urgent
`, 1)
}

func TestAbandonedIssueIsNotLaunchedByAnotherTrigger(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.abandonedWorkflow())
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})
	s.startLoop()
	s.waitEvents("abandon", 1)
	both := readyIssue(42)
	both.labels = append(both.labels, "urgent")

	s.setIssues(both)

	ticks := len(s.events("tick"))
	s.waitEvents("tick", ticks+2)
	if starts := s.events("start"); len(starts) != 1 {
		t.Fatalf("start の行 = %v, want 打ち切った issue を別の trigger で起動しない", starts)
	}
}

func TestAbandonedIssueComesBackAfterLeavingTheTriggerOnce(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.abandonedWorkflow())
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})
	s.startLoop()
	s.waitEvents("abandon", 1)
	offTrigger := readyIssue(42)
	offTrigger.labels = nil
	s.setIssues(offTrigger)
	s.waitEvents("unabandon", 1)

	s.setIssues(readyIssue(42))

	if start := s.waitEvents("start", 2)[1]; start["attempt"] != float64(1) {
		t.Fatalf("start の行 = %v, want 打ち切りが解けて attempt 1 から", start)
	}
}

func TestStalledWorkerIsStoppedAndCountedAsAFailure(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(fastRetry + "  stall_timeout: 1s\n"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})

	s.startLoop()

	end := s.waitEvents("end", 1)[0]
	if end["outcome"] != "failed" || !strings.Contains(asString(end["reason"]), "stall") {
		t.Fatalf("end の行 = %v, want stall の失敗", end)
	}
	s.waitEvents("retry", 1)
}

func TestWorkerThatRunsPastItsTimeLimitIsStoppedAndCountedAsAFailure(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(fastRetry + "  stall_timeout: 0s\n  run_timeout: 1s\n"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})

	s.startLoop()

	end := s.waitEvents("end", 1)[0]
	if end["outcome"] != "failed" || !strings.Contains(asString(end["reason"]), "上限時間") {
		t.Fatalf("end の行 = %v, want 上限時間の失敗", end)
	}
	s.waitEvents("retry", 1)
}

func TestRetryWaitingForAFreeSlotDoesNotAdvanceTheAttempt(t *testing.T) {
	// issue#42 は失敗して再起動を待ち、issue#43 は走り続ける。並列上限を 1 に下げると、#42 の再起動は空きを待つ
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(fastRetry + "  max_concurrent: 2\n"))
	s.setIssues(readyIssue(42), readyIssue(43))
	s.respondAll("claude", []stubwire.Rule{
		{ArgContains: "issue #43", Stdout: `{"type":"result"}` + "\n", ReleaseFile: s.releaseFile()},
		{Stdout: `{"type":"result"}` + "\n", ReleaseFile: s.mark("never")}, // #42 は never を書くまで走り、書いたら当たったまま終わる
	})
	s.t.Cleanup(func() { _ = os.WriteFile(s.releaseFile(), nil, 0o644); _ = os.WriteFile(s.mark("never"), nil, 0o644) })
	s.startLoop()
	s.waitEvents("start", 2)
	s.writeWorkflow(s.workerWorkflow(fastRetry + "  max_concurrent: 1\n"))
	ticks := len(s.events("tick"))
	s.waitEvents("tick", ticks+1)
	mustWrite(t, s.mark("never"), "")
	s.waitEvents("retry", 1)
	ticks = len(s.events("tick"))
	s.waitEvents("tick", ticks+3)

	s.release()

	start := waitStart(t, s, "issue#42", 2)
	if start["attempt"] != float64(2) {
		t.Fatalf("空きを待った後の start の行 = %v, want attempt 2", start)
	}
}

// waitStart は target の start の行が n 行になるまで待ち、n 行目を返す。
func waitStart(t *testing.T, s *sandbox, target string, n int) map[string]any {
	t.Helper()
	waitFor(t, func() bool { return len(s.startsOf(target)) >= n }, target+" の start の行が増えない")
	return s.startsOf(target)[n-1]
}
