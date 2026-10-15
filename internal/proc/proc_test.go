package proc_test

import (
	"bufio"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/proc"
)

// startGroup は sh -c script を新しい process group で起動し、script が最初に書いた 1 行 (準備ができた合図) まで待つ。
func startGroup(t *testing.T, script string) (*exec.Cmd, string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("準備の合図が来ない: %v", err)
	}
	return cmd, strings.TrimSpace(line)
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestStopKillsAChildThatIgnoresSIGTERMOnceTheGraceRunsOut(t *testing.T) {
	cmd, _ := startGroup(t, `trap '' TERM; echo ready; while :; do sleep 0.05; done`)
	group := proc.Watch(cmd)

	begin := time.Now()
	exited, err := group.Stop(200 * time.Millisecond)

	if exited || err != nil {
		t.Fatalf("exited = %v, err = %v", exited, err)
	}
	if elapsed := time.Since(begin); elapsed < 200*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("猶予を待たずに、または猶予を大きく過ぎて止まった: %v", elapsed)
	}
	if status := cmd.ProcessState.Sys().(syscall.WaitStatus); !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("SIGKILL で止まっていない: %v", cmd.ProcessState)
	}
}

func TestStopReapsAGrandchildLeftInTheGroupAfterTheChildEnds(t *testing.T) {
	// 子は SIGTERM で終わるが、SIGTERM を無視する孫を group に残す
	cmd, line := startGroup(t, `sh -c "trap '' TERM; while :; do sleep 0.05; done" & echo $!; trap 'exit 0' TERM; while :; do sleep 0.05; done`)
	grandchild, err := strconv.Atoi(line)
	if err != nil {
		t.Fatalf("孫の pid を読めない: %q", line)
	}
	group := proc.Watch(cmd)

	if _, err := group.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for alive(grandchild) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(grandchild, syscall.SIGKILL)
			t.Fatalf("孫 %d が残った", grandchild)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStopReportsExitedAndKeepsTheExitCodeOfAChildThatAlreadyEnded(t *testing.T) {
	cmd, _ := startGroup(t, `echo ready; exit 3`)
	group := proc.Watch(cmd)
	<-group.Done()

	exited, err := group.Stop(time.Second)

	var exit *exec.ExitError
	if !exited || err != nil || !errors.As(group.Err(), &exit) || exit.ExitCode() != 3 {
		t.Fatalf("exited = %v, err = %v, wait = %v", exited, err, group.Err())
	}
}

func TestWaitReportsTimedOutWhenTheTimeoutPasses(t *testing.T) {
	cmd, _ := startGroup(t, `trap '' TERM; echo ready; while :; do sleep 0.05; done`)

	end, _ := proc.Wait(cmd, 100*time.Millisecond)

	if end != proc.TimedOut {
		t.Fatalf("end = %v", end)
	}
}
