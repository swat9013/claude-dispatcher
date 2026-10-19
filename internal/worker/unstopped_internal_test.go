package worker

import (
	"errors"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/proc"
)

// SIGKILL を送れず claude が終わっていない worker の終わり方 (system.md §7 の tick の手順 1)。

func TestAClaudeThatCannotBeKilledIsReportedUnstoppedAndEndsOnceWhenItExits(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	restore := stopGroup
	stopGroup = func(*proc.Group, time.Duration) (bool, error) {
		return false, errors.Join(proc.ErrNotStopped, errors.New("EPERM"))
	}
	t.Cleanup(func() { stopGroup = restore })
	stream, err := openStream(filepath.Join(t.TempDir(), "worker.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	run := &Run{stop: make(chan struct{})}
	run.Stop()
	var reported []string

	// 止められないと知らせた後に、claude が自分で死ぬ (SIGKILL が遅れて届いた形)
	result := Runner{}.wait(cmd, run, newStreamWatch(stream, 0, time.Now(), t.TempDir(), limits{}), func(stopError string) {
		reported = append(reported, stopError)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	})

	if len(reported) != 1 || reported[0] == "" {
		t.Errorf("止められない知らせ = %q, want 理由付きで 1 回", reported)
	}
	if !result.Stopped || result.StopError == "" {
		t.Errorf("result = %+v, want claude が終わった後の Stopped・StopError あり", result)
	}
}
