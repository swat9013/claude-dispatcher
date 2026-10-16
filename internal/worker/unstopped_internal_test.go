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

func TestStoppingAClaudeThatCannotBeKilledReturnsAnUnstoppedResult(t *testing.T) {
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

	result := Runner{}.wait(cmd, run, newStreamWatch(stream, 0, time.Now(), t.TempDir(), limits{}))

	if !result.Unstopped || !result.Stopped || result.StopError == "" || result.ExitCode != nil {
		t.Errorf("result = %+v, want Unstopped・Stopped・StopError あり・ExitCode なし", result)
	}
}
