package state_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/state"
)

// fd の flock で process の生死を確かめる (system.md §7)。flock は開いた file ごとに掛かるので、同じ process の別の open でも
// 取り合う。

func TestLockedIsTrueWhileAnotherOpenHoldsTheExclusiveLock(t *testing.T) {
	file := filepath.Join(t.TempDir(), "worker.log")
	holder, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	held, err := state.Locked(file)
	if err != nil || !held {
		t.Fatalf("Locked = %v, %v, want true (持ち主が生きている)", held, err)
	}

	holder.Close()
	held, err = state.Locked(file)
	if err != nil || held {
		t.Fatalf("持ち主が閉じた後の Locked = %v, %v, want false", held, err)
	}
}

func TestLockedIsFalseForAMissingFileAndDoesNotCreateIt(t *testing.T) {
	file := filepath.Join(t.TempDir(), "worker.log")

	held, err := state.Locked(file)

	if err != nil || held {
		t.Fatalf("Locked = %v, %v, want false", held, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("Locked が file を作った: %v", err)
	}
}
