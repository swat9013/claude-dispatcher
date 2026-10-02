package blackbox_test

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"syscall"
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
	s.writeWorkflowWithCommands(s.workerWorkflow(fastRetry))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})

	s.startLoop()

	retry := s.waitEvents("retry", 1)[0]
	if retry["target"] != "issue#42" || retry["next_attempt"] != float64(2) || retry["backoff"] != float64(1) {
		t.Fatalf("retry の行 = %v", retry)
	}
	if start := s.waitEvents("start", 2)[1]; start["attempt"] != float64(2) {
		t.Fatalf("2 本目の start の行 = %v, want attempt 2", start)
	}
}

func TestFirstBackoffIsTenSeconds(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow("limits:\n  max_retry_backoff: 30s\n"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})

	s.startLoop()

	if retry := s.waitEvents("retry", 1)[0]; retry["backoff"] != float64(10) {
		t.Fatalf("retry の行 = %v, want backoff 10 秒", retry)
	}
}

func TestRetriedAttemptResumesTheSameSession(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(fastRetry))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})

	s.startLoop()

	starts := s.waitEvents("start", 2)
	session := asString(starts[0]["session_id"])
	calls := s.waitCalls("claude", 2, "2 回目の attempt の claude の呼び出しが記録されない")
	if starts[1]["session_id"] != session || argValueAfter(calls[1].Argv, "--resume") != session || slices.Contains(calls[1].Argv, "--session-id") {
		t.Fatalf("2 回目の argv = %q, want --resume %s", calls[1].Argv, session)
	}
}

func TestEndLineOfARetriedAttemptDoesNotCarryTheResultOfThePreviousAttempt(t *testing.T) {
	// worker log は attempt を跨いで追記するので、attempt 2 の終わりにも attempt 1 の result が file に残っている
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(fastRetry))
	s.setIssues(readyIssue(42))
	s.respondAll("claude", []stubwire.Rule{
		{ArgsContain: []string{"attempt 1"}, Stdout: `{"type":"result","subtype":"success","is_error":false,"num_turns":4,"permission_denials":[]}` + "\n"},
		{},
	})

	s.startLoop()

	ends := s.waitEvents("end", 2)
	if ends[0]["num_turns"] != float64(4) {
		t.Fatalf("attempt 1 の end の行 = %v, want num_turns 4", ends[0])
	}
	if _, ok := ends[1]["num_turns"]; ok {
		t.Fatalf("attempt 2 の end の行 = %v, want attempt 1 の result を載せない", ends[1])
	}
}

func TestRetriedAttemptRunsInTheSameWorkspace(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(fastRetry))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})

	s.startLoop()

	s.waitEvents("start", 2)
	if calls := s.waitCalls("claude", 2, "2 回目の attempt の claude の呼び出しが記録されない"); calls[0].Cwd != calls[1].Cwd || calls[1].Cwd != s.workspace(42) {
		t.Fatalf("cwd = %s, %s, want どちらも %s", calls[0].Cwd, calls[1].Cwd, s.workspace(42))
	}
}

func TestRetryAfterAnAttemptThatNeverStartedClaudeStartsTheSession(t *testing.T) {
	// before_run は 1 回目だけ失敗する (marks に印を残して 2 回目は通る)
	s := newSandbox(t)
	once := s.mark("before_run-failed")
	s.writeWorkflowWithCommands(regexp.MustCompile(`(?m)^  before_run: .*$`).ReplaceAllString(s.workerWorkflow(fastRetry),
		`  before_run: '[ -e "`+once+`" ] || { : > "`+once+`"; exit 1; }'`))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})

	s.startLoop()

	start := s.waitEvents("start", 1)[0]
	argv := s.waitCalls("claude", 1, "before_run が通った attempt の claude の呼び出しが記録されない")[0].Argv
	if start["attempt"] != float64(2) || argValueAfter(argv, "--session-id") != asString(start["session_id"]) || slices.Contains(argv, "--resume") {
		t.Fatalf("attempt %v の argv = %q, want 前に始めた session が無いので --session-id", start["attempt"], argv)
	}
}

func TestIssueIsAbandonedWhenItsLastAttemptFails(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(fastRetry + "  max_attempts: 2\n"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})
	s.startLoop()

	abandon := s.waitEvents("abandon", 1)[0]

	if abandon["target"] != "issue#42" || abandon["attempt"] != float64(2) {
		t.Fatalf("abandon の行 = %v", abandon)
	}
}

func TestAbandonedIssueIsNotLaunchedAgainWhileItStillMatchesTheTrigger(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(fastRetry + "  max_attempts: 2\n"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})
	s.startLoop()
	s.waitEvents("abandon", 1)

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
	s.writeWorkflowWithCommands(s.abandonedWorkflow())
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
	s.writeWorkflowWithCommands(s.abandonedWorkflow())
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
	s.writeWorkflowWithCommands(s.workerWorkflow(fastRetry + "  stall_timeout: 1s\n"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})

	s.startLoop()

	end := s.waitEvents("end", 1)[0]
	if end["outcome"] != "failed" || !strings.Contains(asString(end["reason"]), "stall") {
		t.Fatalf("end の行 = %v, want stall の失敗", end)
	}
}

func TestStalledAttemptIsRetried(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(fastRetry + "  stall_timeout: 1s\n"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})

	s.startLoop()

	if retry := s.waitEvents("retry", 1)[0]; retry["next_attempt"] != float64(2) {
		t.Fatalf("retry の行 = %v, want stall の後に attempt 2", retry)
	}
}

func TestWorkerThatRunsPastItsTimeLimitIsStoppedAndCountedAsAFailure(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(fastRetry + "  stall_timeout: 0s\n  run_timeout: 1s\n"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})

	s.startLoop()

	end := s.waitEvents("end", 1)[0]
	if end["outcome"] != "failed" || !strings.Contains(asString(end["reason"]), "上限時間") {
		t.Fatalf("end の行 = %v, want 上限時間の失敗", end)
	}
}

func TestAttemptPastItsTimeLimitIsRetried(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(fastRetry + "  stall_timeout: 0s\n  run_timeout: 1s\n"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})

	s.startLoop()

	if retry := s.waitEvents("retry", 1)[0]; retry["next_attempt"] != float64(2) {
		t.Fatalf("retry の行 = %v, want 上限時間の後に attempt 2", retry)
	}
}

func TestWorkerThatExitsAbnormallyIsRetried(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(fastRetry))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{Exit: 1})

	s.startLoop()

	if retry := s.waitEvents("retry", 1)[0]; retry["next_attempt"] != float64(2) {
		t.Fatalf("retry の行 = %v, want 異常終了の後に attempt 2", retry)
	}
}

func TestRetryWaitingForAFreeSlotDoesNotAdvanceTheAttempt(t *testing.T) {
	// issue#42 は失敗して再起動を待ち、issue#43 は走り続ける。並列上限を 1 に下げると、#42 の再起動は空きを待つ
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(fastRetry + "  max_concurrent: 2\n"))
	s.setIssues(readyIssue(42), readyIssue(43))
	s.respondAll("claude", []stubwire.Rule{
		{ArgsContain: []string{"issue #43"}, Stdout: `{"type":"result"}` + "\n", ReleaseFile: s.releaseFile()},
		{Stdout: `{"type":"result"}` + "\n", ReleaseFile: s.mark("never")}, // #42 は never を書くまで走り、書いたら当たったまま終わる
	})
	s.t.Cleanup(func() { _ = os.WriteFile(s.releaseFile(), nil, 0o644); _ = os.WriteFile(s.mark("never"), nil, 0o644) })
	s.startLoop()
	s.waitEvents("start", 2)
	s.writeWorkflowWithCommands(s.workerWorkflow(fastRetry + "  max_concurrent: 1\n"))
	ticks := len(s.events("tick"))
	s.waitEvents("tick", ticks+1)
	mustWrite(t, s.mark("never"), "")
	s.waitEvents("retry", 1)
	ticks = len(s.events("tick"))
	s.waitEvents("tick", ticks+3)
	if starts := s.startsOf("issue#42"); len(starts) != 1 {
		t.Fatalf("issue#42 の start の行 = %v, want 空きが出るまで再起動しない", starts)
	}
	if waits := s.events("wait_slot"); len(waits) != 1 || waits[0]["target"] != "issue#42" {
		t.Fatalf("wait_slot の行 = %v, want 空き待ちを 1 行", waits)
	}

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

// slowRetry は backoff を 3s にして、再起動を待つ間に作業対象を書き換えられるようにする
const slowRetry = "limits:\n  max_retry_backoff: 3s\n"

func TestWaitingRetryIsReleasedWhenTheIssueLeavesTheTrigger(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(slowRetry))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})
	s.startLoop()
	s.waitEvents("retry", 1)
	offTrigger := readyIssue(42)
	offTrigger.labels = nil

	s.setIssues(offTrigger)

	if release := s.waitEvents("release", 1)[0]; release["reason"] != "trigger から外れた" {
		t.Fatalf("release の行 = %v", release)
	}
	if starts := s.events("start"); len(starts) != 1 {
		t.Fatalf("start の行 = %v, want trigger から外れた issue を再起動しない", starts)
	}
}

func TestWaitingRetryOfAClosedIssueIsReleasedAndItsWorkspaceRemoved(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(slowRetry))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})
	s.startLoop()
	s.waitEvents("retry", 1)
	closed := readyIssue(42)
	closed.closed = true

	s.setIssues(closed)

	if release := s.waitEvents("release", 1)[0]; release["reason"] != "終端" {
		t.Fatalf("release の行 = %v", release)
	}
	if _, err := os.Stat(s.workspace(42)); !os.IsNotExist(err) {
		t.Fatalf("終端の issue の workspace が残っている: %v", err)
	}
}

func TestStopRequestDropsWaitingRetriesWithAnErrorLine(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow("limits:\n  max_retry_backoff: 1m\n"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})
	loop := s.startLoop()
	s.waitEvents("retry", 1)

	loop.signal(syscall.SIGINT)

	assertExit(t, loop.wait(), 0)
	if errs := s.events("error"); len(errs) != 1 || !strings.Contains(asString(errs[0]["error"]), "再起動を待ったまま止まる") {
		t.Fatalf("error の行 = %v", errs)
	}
}

func TestWaitingRetryIsReleasedWhenItsTriggerIsRemovedFromTheWorkflow(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(slowRetry))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})
	s.startLoop()
	s.waitEvents("retry", 1)

	s.writeWorkflowWithCommands(strings.Replace(s.workerWorkflow(slowRetry), "name: implement", "name: renamed", 1))

	if release := s.waitEvents("release", 1)[0]; release["reason"] != "trigger が workflow 定義から消えた" {
		t.Fatalf("release の行 = %v", release)
	}
}
