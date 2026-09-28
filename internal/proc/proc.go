// Package proc は起動済みの子プロセスを上限時間つきで待つ。gh の観測と orchestrator の起動が共有する。
package proc

import (
	"os/exec"
	"syscall"
	"time"
)

// End は待った子プロセスの終わり方。
type End int

const (
	// Exited は子が自分で終わった
	Exited End = iota
	// TimedOut は上限時間を超えたので止めた
	TimedOut
	// Stopped は停止要求が届いたので止めた
	Stopped
)

// Wait は開始済みの cmd の終了を待つ。timeout を超えるか stop が閉じたら process group ごと SIGKILL して、終わるまで待つ。
// 子が起こした孫 (credential helper 等) も道連れにするため、cmd は Setpgid か Setsid で起動しておく。stop が nil なら
// 停止要求は届かない。
func Wait(cmd *exec.Cmd, timeout time.Duration, stop <-chan struct{}) (End, error) {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	end := Exited
	select {
	case err := <-done:
		return Exited, err
	case <-timer.C:
		end = TimedOut
	case <-stop:
		end = Stopped
	}
	// 子の終了と同時に届いたら終了を優先する (select はどちらも選びうる)。正常終了した orchestrator を止めたことにすると、
	// 決定ファイルが読まれず、付いた wip が worker の無いまま残る
	select {
	case err := <-done:
		return Exited, err
	default:
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	return end, <-done
}
