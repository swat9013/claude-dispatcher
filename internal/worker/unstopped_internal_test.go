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

func TestWaitReportsAnUnkillableClaudeOnceAndReturnsOnlyAfterItExits(t *testing.T) {
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
	var exited <-chan struct{}
	aliveWhenReported := false

	// 止められないと知らせた後に、claude が死ぬ (SIGKILL が遅れて届いた形)
	result := Runner{}.wait(cmd, run, newStreamWatch(stream, 0, time.Now(), t.TempDir(), limits{}), func(stopError string, done <-chan struct{}) {
		reported, exited = append(reported, stopError), done
		select {
		case <-done:
		default:
			aliveWhenReported = true
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	})

	if len(reported) != 1 || reported[0] == "" {
		t.Errorf("止められない知らせ = %q, want 理由付きで 1 回", reported)
	}
	if !aliveWhenReported {
		t.Error("知らせた時点で claude が終わっていた, want 生きている間に知らせる")
	}
	select {
	case <-exited:
	default:
		t.Error("wait が claude の終わりより先に戻った (呼び出し元の after_run が claude と重なる)")
	}
	if !result.Stopped || result.StopError != "" {
		t.Errorf("result = %+v, want Stopped・StopError なし (送れなかった理由は知らせが運ぶ)", result)
	}
}

func TestUnstoppedAliveIsTrueOnlyWhileAnUnstoppedClaudeHasNotExited(t *testing.T) {
	exited, closed := make(chan struct{}), make(chan struct{})
	close(closed)
	cases := []struct {
		name   string
		exited <-chan struct{}
		want   bool
	}{
		{"止められなかった claude がいない", nil, false},
		{"claude がまだ終わっていない", exited, true},
		{"claude が終わった", closed, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UnstoppedAlive(c.exited); got != c.want {
				t.Errorf("UnstoppedAlive = %v, want %v", got, c.want)
			}
		})
	}
}
