package blackbox_test

import (
	"os"
	"syscall"
	"testing"
)

// flock による単一実行 (system.md §9, formats.md §3)。前 tick が lock を持つ間の tick は locked で見送る。

func TestTickIsSkippedAsLockedWhileAnotherTickHoldsTheLock(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	lock, err := os.OpenFile(s.lockFile(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	r := s.tick()

	assertExit(t, r, 3)
	line := s.onlyTickLine()
	assertResult(t, line, "locked")
	if _, ok := line["error"]; !ok {
		t.Fatalf("locked の行に error が無い: %v", line)
	}
	if calls := s.observationCalls(); len(calls) != 0 {
		t.Fatalf("lock を取れないのに観測した: %v", calls)
	}
	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("lock を取れないのに claude を起動した: %v", calls)
	}
	assertCronLogLine(t, r.stderr, s.project, asString(t, line["ts"]), "locked")
}

func TestLockIsReleasedWhenTheTickEnds(t *testing.T) {
	s := newSandbox(t)
	assertExit(t, s.tick(), 0)

	lock, err := os.OpenFile(s.lockFile(), os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("tick が lock file を %s に置いていない: %v", s.lockFile(), err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("tick の終了後も lock が残っている: %v", err)
	}
}
