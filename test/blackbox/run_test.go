package blackbox_test

import (
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// 単発起動 `run` (formats.md §11、system.md §7)。

func TestRunLaunchesTheNamedTargetOnceAndExitsZeroWhenItLeavesTheTrigger(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42), readyIssue(43))
	done := readyIssue(42)
	done.labels = nil
	s.onClaude(stubwire.Rule{Writes: []stubwire.FileWrite{s.ghResponses(done, readyIssue(43))}})

	r := s.run("run", "implement", "issue#42")

	assertExit(t, r, 0)
	calls := s.calls("claude")
	if len(calls) != 1 || calls[0].Cwd != s.workspace(42) {
		t.Fatalf("claude の呼び出し = %v, want workspace %s で 1 回", calls, s.workspace(42))
	}
	if !strings.Contains(r.stdout, "終了: issue#42 完了 (trigger から外れた)") {
		t.Fatalf("stdout = %q", r.stdout)
	}
	if lines := s.logLines(); len(lines) != 0 {
		t.Fatalf("log.jsonl の行 = %v, want 無し", lines)
	}
}

func TestRunExitsSixWithoutRetryWhenTheTargetStillMatchesAfterTheWorker(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})

	r := s.run("run", "implement", "issue#42")

	assertExit(t, r, 6)
	if calls := s.calls("claude"); len(calls) != 1 {
		t.Fatalf("claude の呼び出し = %d 回, want 1 (retry しない)", len(calls))
	}
	if !strings.Contains(r.stdout, "未完了 (trigger に当たったまま)") {
		t.Fatalf("stdout = %q", r.stdout)
	}
}

func TestRunDoesNotLaunchATargetThatDoesNotMatchTheTrigger(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	other := readyIssue(42)
	other.labels = nil
	s.setIssues(other)

	r := s.run("run", "implement", "issue#42")

	assertExit(t, r, 5)
	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("claude の呼び出し = %d 回, want 0", len(calls))
	}
}

func TestRunRejectsAnUnknownTriggerAWrongKindAndAMisspelledTarget(t *testing.T) {
	for name, args := range map[string][]string{
		"未知の trigger": {"run", "deploy", "issue#42"},
		"種類が違う":       {"run", "implement", "cl#42"},
		"綴りの誤り":       {"run", "implement", "#42"},
		"番号が 0":       {"run", "implement", "issue#0"},
		"作業対象が無い":     {"run", "implement"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeWorkflowWithCommands(s.workerWorkflow(""))
			s.setIssues(readyIssue(42))

			r := s.run(args...)

			assertExit(t, r, 2)
			if calls := s.calls("claude"); len(calls) != 0 {
				t.Fatalf("claude の呼び出し = %d 回, want 0", len(calls))
			}
		})
	}
}

func TestRunStopsAtStartupWhileALoopOfTheSameScopeIsRunning(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues()
	s.startLoop()
	s.waitEvents("tick", 1)

	r := s.run("run", "implement", "issue#42")

	assertExit(t, r, 3)
}

func TestRunDoesNotLaunchIntoTheWorkspaceOfALiveOrphan(t *testing.T) {
	s := newSandbox(t)
	s.crashWithAnOrphan()

	r := s.run("run", "implement", "issue#42")

	assertExit(t, r, 5)
	if calls := s.calls("claude"); len(calls) != 1 {
		t.Fatalf("claude の呼び出し = %d 回, want 1 (orphan の分だけ)", len(calls))
	}
}
