package blackbox_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// 遅い一覧 (formats.md §6・§5・§7.4)。閾値の 15 秒を実時間で待たず、build tag `blackbox` の binary だけが読む env で閾値を
// 縮め、gh の応答をそれより遅らせる。

// slowAfterEnv は cmd/claude-dispatcher/slow_blackbox.go が読む env。名前がずれると閾値が 15 秒のままになり、遅い run の
// 行が出ずにテストが落ちる
const slowAfterEnv = "CLAUDE_DISPATCHER_TEST_SLOW_AFTER"

// slowEnv は閾値を 100ms に縮める。gh の応答はそれより長い slowGhDelay だけ遅らせる
var slowEnv = map[string]string{slowAfterEnv: "100ms"}

const slowGhDelay = 500 * time.Millisecond

const slowIssueListingLine = "issue 置き場 acme/widgets の一覧の読み出しが 15 秒を超えた"

// makeSlow は、以後この sandbox で names の stub が返す応答をすべて slowGhDelay 遅らせる (元の rule は書き換えない)。
func (s *sandbox) makeSlow(names ...string) {
	s.t.Helper()
	for _, name := range names {
		rules := append([]stubwire.Rule(nil), s.rules[name]...)
		for i := range rules {
			rules[i].Delay = slowGhDelay
		}
		s.respondAll(name, rules)
	}
}

// assertSlowLineOnlyOnStderr は、遅い run が stderr に遅い一覧の行を 1 行出し、stdout と exit code は速い run と同じことを
// 確かめる。
func assertSlowLineOnlyOnStderr(t *testing.T, fast, slow runResult) {
	t.Helper()
	if n := strings.Count(slow.stderr, slowIssueListingLine+"\n"); n != 1 {
		t.Errorf("stderr の遅い一覧の行 = %d 行, want 1:\n%s", n, slow.stderr)
	}
	if slow.stdout != fast.stdout {
		t.Errorf("stdout が知らせの無いときと違う:\n%s\nwant:\n%s", slow.stdout, fast.stdout)
	}
	if slow.exit != fast.exit {
		t.Errorf("exit code = %d, 知らせの無いときは %d", slow.exit, fast.exit)
	}
}

func TestDryRunPrintsTheSlowListingLineOnlyOnStderr(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	fast := s.dryRun()
	s.makeSlow("gh")

	slow := s.runWithEnv(slowEnv, "loop", "--dry-run")

	assertSlowLineOnlyOnStderr(t, fast, slow)
}

func TestDoctorPrintsTheSlowListingLineOnlyOnStderr(t *testing.T) {
	s := newSandbox(t)
	s.setStore(nil, nil)
	fast := s.run("doctor")
	s.makeSlow("gh")

	slow := s.runWithEnv(slowEnv, "doctor")

	assertSlowLineOnlyOnStderr(t, fast, slow)
}

// 一覧を確かめる手順を持つのは jira (と cl: の CL 置き場) なので、起動時の確認の遅い一覧は jira の workflow 定義で見る。

// jiraSlowLines は jira の issue 置き場と cl: の CL 置き場の遅い一覧の行。
var jiraSlowLines = []string{
	"issue 置き場 " + jiraSite + "/" + jiraKey + " の一覧の読み出しが 15 秒を超えた",
	"CL 置き場 acme/widgets の一覧の読み出しが 15 秒を超えた",
}

func TestJiraDoctorPrintsTheSlowListingLinesOfBothPlacesOnlyOnStderr(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.jiraWorkerWorkflow())
	s.setJiraPlaces(readyJiraIssue(42))
	fast := s.run("doctor")
	s.makeSlow("acli", "gh")

	slow := s.runWithEnv(slowEnv, "doctor")

	for _, line := range jiraSlowLines {
		if !strings.Contains(slow.stderr, line+"\n") {
			t.Errorf("stderr に %q が無い:\n%s", line, slow.stderr)
		}
	}
	if slow.stdout != fast.stdout || slow.exit != fast.exit {
		t.Errorf("stdout・exit が知らせの無いときと違う: %d\n%s\nwant: %d\n%s", slow.exit, slow.stdout, fast.exit, fast.stdout)
	}
}

func TestRunPrintsTheSlowListingLinesOfTheConfirmationOnStderr(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.jiraWorkerWorkflow())
	s.setJiraPlaces(readyJiraIssue(42))
	s.onClaude(stubwire.Rule{})
	s.makeSlow("acli", "gh")

	r := s.runWithEnv(slowEnv, "run", "implement", "issue#42")

	for _, line := range jiraSlowLines {
		if !strings.Contains(r.stderr, line+"\n") {
			t.Errorf("stderr に %q が無い:\n%s", line, r.stderr)
		}
		if strings.Contains(r.stdout, line) {
			t.Errorf("stdout に %q が出た:\n%s", line, r.stdout)
		}
	}
}

func TestLoopShowsTheSlowListingLinesOfTheStartupConfirmationBeforeItStarts(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.jiraWorkerWorkflow())
	s.setJiraPlaces()
	s.makeSlow("acli", "gh")

	loop := s.startLoopWithEnv(slowEnv)

	loop.waitForOutput(loopStartedLine)
	before, _, _ := strings.Cut(loop.stdout.String(), "loop を始めた: ")
	for _, line := range jiraSlowLines {
		if !strings.Contains(before, " "+line+"\n") {
			t.Errorf("loop を始めた行より前の stdout に %q が無い:\n%s", line, loop.stdout)
		}
		if strings.Contains(loop.stderr.String(), line) {
			t.Errorf("stderr に %q が出た:\n%s", line, loop.stderr)
		}
	}
}

func TestReleaseBuildDoesNotReadTheSlowListingThresholdFromTheEnvironment(t *testing.T) {
	// 配布する binary は build tag を付けずに build する (.goreleaser.yaml)。閾値の差し替えの口は持たない (#374)
	bin := filepath.Join(t.TempDir(), "claude-dispatcher")
	if out, err := exec.Command("go", "build", "-o", bin, dispatcherPackage).CombinedOutput(); err != nil {
		t.Fatalf("tag 無しで build できない: %v\n%s", err, out)
	}
	s := newSandbox(t)
	s.setStore(nil, nil)
	s.makeSlow("gh")
	cmd := exec.Command(bin, "doctor")
	cmd.Dir = s.clone
	cmd.Env = s.environ(slowEnv)

	out, _ := cmd.CombinedOutput()

	if strings.Contains(string(out), slowIssueListingLine) {
		t.Fatalf("tag 無しの binary が env の閾値で遅い一覧の行を出した:\n%s", out)
	}
}
