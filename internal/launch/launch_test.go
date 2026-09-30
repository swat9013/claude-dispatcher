package launch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/proc"
)

// fakeClaude は claude の代わりに script を置いた ClaudePrint を返す。
func fakeClaude(t *testing.T, script string) ClaudePrint {
	t.Helper()
	dir := t.TempDir()
	claude := filepath.Join(dir, "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return ClaudePrint{Claude: claude, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: dir}
}

func closed() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// processState は pid の process の状態 (ps の STAT)。process が無ければ ""。
func processState(pid int) string {
	out, _ := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return strings.TrimSpace(string(out))
}

// ignoreStart は起動の通知を受け流す
func ignoreStart(string, time.Time) {}

func TestRunOrchestratorStopsItOnAStopRequestAndSaysSo(t *testing.T) {
	c := fakeClaude(t, "sleep 30")

	run, err := c.RunOrchestrator("prompt", filepath.Join(t.TempDir(), "o.log"), time.Minute, closed(), ignoreStart)

	if err != nil || run.End != proc.Stopped {
		t.Fatalf("run = %+v, err = %v, want 停止要求で止めた", run, err)
	}
}

func TestRunOrchestratorTellsATimeoutApartFromAStopRequest(t *testing.T) {
	c := fakeClaude(t, "sleep 30")

	run, err := c.RunOrchestrator("prompt", filepath.Join(t.TempDir(), "o.log"), 100*time.Millisecond, nil, ignoreStart)

	if err != nil || run.End != proc.TimedOut {
		t.Fatalf("run = %+v, err = %v, want 上限時間で止めた", run, err)
	}
}

func TestStoppingTheOrchestratorLeavesAWorkerRunning(t *testing.T) {
	c := fakeClaude(t, "sleep 30")
	w, err := c.SpawnWorker("prompt", filepath.Join(t.TempDir(), "w.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Kill(-w.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("worker (pid %d) を止められない: %v", w.PID, err)
		}
	})

	if _, err := c.RunOrchestrator("prompt", filepath.Join(t.TempDir(), "o.log"), time.Minute, closed(), ignoreStart); err != nil {
		t.Fatal(err)
	}

	if state := processState(w.PID); state == "" || strings.HasPrefix(state, "Z") {
		t.Fatalf("orchestrator を止めたら worker (pid %d) も止まった (state %q)", w.PID, state)
	}
}

func TestSpawnWorkerReapsTheWorkerWhenItEnds(t *testing.T) {
	c := fakeClaude(t, "exit 0")

	w, err := c.SpawnWorker("prompt", filepath.Join(t.TempDir(), "w.log"))

	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for state := processState(w.PID); state != ""; state = processState(w.PID) {
		if time.Now().After(deadline) {
			t.Fatalf("終わった worker (pid %d) が回収されずに残っている (state %q)", w.PID, state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
