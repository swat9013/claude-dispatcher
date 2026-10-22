package state_test

import (
	"errors"
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

// 単発起動は loop.lock だけを取るので、止まった loop の後に撃った単発起動の間も status は loop なしと描く (formats.md §11)。
func TestLockRunDoesNotMakeTheLoopAlive(t *testing.T) {
	dir := t.TempDir()
	loop, err := state.Lock(dir, "scope")
	if err != nil {
		t.Fatal(err)
	}
	loop.Close() // loop を止める

	run, err := state.LockRun(dir, "scope")
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	alive, err := state.Alive(dir)
	if err != nil || alive {
		t.Fatalf("単発起動の間の Alive = %v, %v, want false (○ loop なし)", alive, err)
	}
}

// 単発起動が loop.lock を持つ間は、同じ scope key の loop と単発起動が起動時に止まる (exit 3 の元)。
func TestLockStopsWhileARunHoldsTheScope(t *testing.T) {
	dir := t.TempDir()
	run, err := state.LockRun(dir, "scope")
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()

	if _, err := state.Lock(dir, "scope"); !errors.Is(err, state.ErrAlreadyRunning) {
		t.Fatalf("単発起動の間の Lock = %v, want ErrAlreadyRunning", err)
	}
}

func TestLockRunStopsWhileARunHoldsTheScope(t *testing.T) {
	dir := t.TempDir()
	run, err := state.LockRun(dir, "scope")
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()

	if _, err := state.LockRun(dir, "scope"); !errors.Is(err, state.ErrAlreadyRunning) {
		t.Fatalf("単発起動の間の LockRun = %v, want ErrAlreadyRunning", err)
	}
}

func TestLockRunSucceedsAfterTheRunReleased(t *testing.T) {
	dir := t.TempDir()
	run, err := state.LockRun(dir, "scope")
	if err != nil {
		t.Fatal(err)
	}
	run.Close()

	again, err := state.LockRun(dir, "scope")
	if err != nil {
		t.Fatalf("外れた後の LockRun = %v", err)
	}
	again.Close()
}

func TestLockRunStopsWhileALoopHoldsTheScope(t *testing.T) {
	dir := t.TempDir()
	loop, err := state.Lock(dir, "scope")
	if err != nil {
		t.Fatal(err)
	}
	defer loop.Close()

	if _, err := state.LockRun(dir, "scope"); !errors.Is(err, state.ErrAlreadyRunning) {
		t.Fatalf("loop の間の LockRun = %v, want ErrAlreadyRunning", err)
	}
}

func TestLockMakesTheLoopAlive(t *testing.T) {
	dir := t.TempDir()
	loop, err := state.Lock(dir, "scope")
	if err != nil {
		t.Fatal(err)
	}
	defer loop.Close()

	alive, err := state.Alive(dir)
	if err != nil || !alive {
		t.Fatalf("loop の間の Alive = %v, %v, want true", alive, err)
	}
}

// max-concurrent が渡す並列上限の file (formats.md §13)。

func TestReadMaxConcurrentRejectsALimitBelowOne(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, state.MaxConcurrentFile), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, ok, err := state.ReadMaxConcurrent(dir)

	if err == nil || ok {
		t.Fatalf("ReadMaxConcurrent = %v, %v, want 1 未満を誤りにする", ok, err)
	}
}

func TestConcurrentWritesOfMaxConcurrentLeaveOneOfTheWrittenLimits(t *testing.T) {
	dir := t.TempDir()
	errs := make(chan error, 20)
	for n := 1; n <= 20; n++ {
		go func() { errs <- state.WriteMaxConcurrent(dir, n) }()
	}
	for range 20 {
		if err := <-errs; err != nil {
			t.Fatalf("同時に撃った WriteMaxConcurrent が落ちた: %v", err)
		}
	}

	n, ok, err := state.ReadMaxConcurrent(dir)

	if err != nil || !ok || n < 1 || n > 20 {
		t.Fatalf("ReadMaxConcurrent = %d, %v, %v, want 書いた値のどれか", n, ok, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, state.MaxConcurrentFile+".*.tmp")); len(leftovers) != 0 {
		t.Fatalf("一時 file が残った: %v", leftovers)
	}
}

func TestLockDiscardsTheLimitLeftByThePreviousLoop(t *testing.T) {
	dir := t.TempDir()
	if err := state.WriteMaxConcurrent(dir, 5); err != nil {
		t.Fatal(err)
	}

	held, err := state.Lock(dir, "s")

	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if _, ok, err := state.ReadMaxConcurrent(dir); ok || err != nil {
		t.Fatalf("Lock の後の ReadMaxConcurrent = %v, %v, want 前の loop の値が消えている", ok, err)
	}
}
