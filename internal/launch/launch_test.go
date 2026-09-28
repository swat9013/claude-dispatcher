package launch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestRunOrchestratorStopsTheProcessGroupOnAStopRequestAndSaysSo(t *testing.T) {
	c := fakeClaude(t, "sleep 30")
	stop := make(chan struct{})
	time.AfterFunc(100*time.Millisecond, func() { close(stop) })
	started := time.Now()

	run, err := c.RunOrchestrator("prompt", filepath.Join(t.TempDir(), "o.log"), time.Minute, stop)

	if err != nil || !run.Stopped || run.TimedOut {
		t.Fatalf("run = %+v, err = %v, want 停止要求で止めた (timed_out ではない)", run, err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("停止要求から止まるまで %s 掛かった", elapsed)
	}
}

func TestRunOrchestratorTellsATimeoutApartFromAStopRequest(t *testing.T) {
	c := fakeClaude(t, "sleep 30")

	run, err := c.RunOrchestrator("prompt", filepath.Join(t.TempDir(), "o.log"), 100*time.Millisecond, nil)

	if err != nil || !run.TimedOut || run.Stopped {
		t.Fatalf("run = %+v, err = %v, want 上限時間で止めた (stopped ではない)", run, err)
	}
}

func TestSpawnWorkerReapsTheWorkerWhenItEnds(t *testing.T) {
	c := fakeClaude(t, "exit 0")

	w, err := c.SpawnWorker("prompt", filepath.Join(t.TempDir(), "w.log"))

	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _ := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(w.PID)).Output()
		state := strings.TrimSpace(string(out))
		if state == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("終わった worker (pid %d) が回収されずに残っている (state %q)", w.PID, state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
