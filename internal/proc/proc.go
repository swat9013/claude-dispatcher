// Package proc は起動済みの子プロセスを上限時間つきで待つ。gh の観測と orchestrator の起動が共有する。
package proc

import (
	"os/exec"
	"syscall"
	"time"
)

// Wait は開始済みの cmd の終了を待つ。timeout を超えたら process group ごと SIGKILL して、終わるまで待つ。
// 子が起こした孫 (credential helper 等) も道連れにするため、cmd は Setpgid か Setsid で起動しておく。
func Wait(cmd *exec.Cmd, timeout time.Duration) (timedOut bool, err error) {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return false, err
	case <-time.After(timeout):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return true, <-done
	}
}
