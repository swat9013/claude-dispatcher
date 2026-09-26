package blackbox_test

import (
	"os"
	"syscall"
	"testing"
)

// flock による単一実行 (system.md §9, formats.md §3)。前 tick が lock を持つ間の tick は locked で見送る。

func holdLock(t *testing.T, file string) *os.File {
	t.Helper()
	lock, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("lock を取れない: %v", err)
	}
	return lock
}

func TestTickIsSkippedAsLockedWhileAnotherTickHoldsTheLock(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	holdLock(t, s.lockFile())

	r := s.tick()

	line := s.assertOutcome(r, outcomeLocked)
	if _, ok := line["error"]; !ok {
		t.Fatalf("locked の行に error が無い: %v", line)
	}
	if calls := s.observationCalls(); len(calls) != 0 {
		t.Fatalf("lock を取れないのに観測した: %v", calls)
	}
	s.assertNoClaude("lock を取れないのに")
	assertCronLogLine(t, r.stderr, s.project, asString(t, line["ts"]), "locked")
}

func TestLockIsReleasedWhenTheTickEnds(t *testing.T) {
	s := newSandbox(t)

	assertExit(t, s.tick(), 0)

	if _, err := os.Stat(s.lockFile()); err != nil {
		t.Fatalf("tick が lock file を %s に置いていない: %v", s.lockFile(), err)
	}
	holdLock(t, s.lockFile())
}
