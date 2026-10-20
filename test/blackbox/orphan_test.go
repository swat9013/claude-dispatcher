package blackbox_test

import (
	"os"
	"regexp"
	"syscall"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// loop が落ちて残った claude (orphan) を、起動し直した loop がどう扱うか (formats.md §6 の orphan、system.md §7)。

// crashWithAnOrphan は issue#42 の worker を起動した loop を SIGKILL で落とし、claude の stub (release を待つ) を残す。
func (s *sandbox) crashWithAnOrphan() {
	s.t.Helper()
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	crashed := s.startLoop()
	s.waitEvents("start", 1)
	crashed.signal(syscall.SIGKILL)
	crashed.wait()
	// 後片付けで stub を終わらせる (TempDir の削除と並んで書かないよう、test の最後に release する)
	s.t.Cleanup(s.release)
}

// waitTicks は、今ある tick の行に加えて n 行の tick の行が書かれるまで待つ。
func (s *sandbox) waitTicks(n int) { s.waitEvents("tick", len(s.events("tick"))+n) }

func TestRestartedLoopDoesNotLaunchIntoTheWorkspaceOfALiveOrphanUntilItEnds(t *testing.T) {
	s := newSandbox(t)
	s.crashWithAnOrphan()

	loop := s.startLoop()

	if got := s.waitEvents("orphan", 1)[0]; got["target"] != "issue#42" {
		t.Fatalf("orphan の行 = %v, want issue#42", got)
	}
	loop.waitForOutput(regexp.MustCompile(` orphan issue#42: 前の loop の claude がまだ動いている$`))
	s.waitTicks(2)
	if starts := s.events("start"); len(starts) != 1 {
		t.Fatalf("start の行 = %d, want 1 (orphan が生きている間は起動しない)", len(starts))
	}
	s.release()
	s.waitEvents("orphan_end", 1)
	s.waitEvents("start", 2)
}

func TestRestartedLoopKeepsTheWorkspaceOfATerminalIssueWhileItsOrphanLives(t *testing.T) {
	s := newSandbox(t)
	s.crashWithAnOrphan()
	closed := readyIssue(42)
	closed.closed = true
	s.setIssues(closed)

	s.startLoop()

	s.waitEvents("orphan", 1)
	s.waitTicks(2)
	if _, err := os.Stat(s.workspace(42)); err != nil {
		t.Fatalf("orphan が生きている workspace を消した: %v", err)
	}
	s.release()
	s.waitEvents("orphan_end", 1)
	waitFor(t, func() bool {
		_, err := os.Stat(s.workspace(42))
		return os.IsNotExist(err)
	}, "orphan が終わった後も、終端の issue の workspace が残る")
}
