package blackbox_test

import (
	"strings"
	"testing"
	"time"
)

// 遅い一覧 (formats.md §6・§5・§7.4)。閾値の 15 秒を実時間で待たず、build tag `blackbox` の binary だけが読む env で閾値を
// 縮め、gh の一覧の応答をそれより遅らせる。

const slowAfterEnv = "CLAUDE_DISPATCHER_TEST_SLOW_AFTER"

const slowIssueListingLine = "issue 置き場 acme/widgets の一覧の読み出しが 15 秒を超えた\n"

// slowRun は gh の応答をすべて閾値より遅らせて args を撃つ。
func (s *sandbox) slowRun(args ...string) runResult {
	s.t.Helper()
	rules := s.rules["gh"]
	for i := range rules {
		rules[i].Delay = time.Second
	}
	s.respondAll("gh", rules)
	return s.runWithEnv(map[string]string{slowAfterEnv: "100ms"}, args...)
}

// assertSlowLineOnlyOnStderr は、遅い run が stderr に遅い一覧の行を出し、stdout と exit code は速い run と同じことを確かめる。
func assertSlowLineOnlyOnStderr(t *testing.T, fast, slow runResult) {
	t.Helper()
	if !strings.Contains(slow.stderr, slowIssueListingLine) {
		t.Errorf("stderr に %q が無い:\n%s", slowIssueListingLine, slow.stderr)
	}
	if strings.Contains(fast.stderr, slowIssueListingLine) {
		t.Errorf("速い一覧で stderr に遅い一覧の行が出た:\n%s", fast.stderr)
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
	args := []string{"loop", "--dry-run"}

	fast := s.run(args...)
	slow := s.slowRun(args...)

	assertExit(t, fast, 0)
	assertSlowLineOnlyOnStderr(t, fast, slow)
}

func TestDoctorPrintsTheSlowListingLineOnlyOnStderr(t *testing.T) {
	s := newSandbox(t)
	s.setStore(nil, nil)

	fast := s.run("doctor")
	slow := s.slowRun("doctor")

	assertExit(t, fast, 0)
	assertSlowLineOnlyOnStderr(t, fast, slow)
}
