// Package proc は起動済みの子プロセスを process group ごと待って止める。gh の観測・orchestrator の起動・worker が共有する。
package proc

import (
	"errors"
	"fmt"
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
)

// Group は開始済みの子とその process group。子が起こした孫 (credential helper・claude の tool 等) も道連れにするため、
// cmd は Setpgid か Setsid で起動しておく。
type Group struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// Watch は開始済みの cmd の終了を別の goroutine で待ち始める。
func Watch(cmd *exec.Cmd) *Group {
	g := &Group{cmd: cmd, done: make(chan struct{})}
	go func() {
		g.err = cmd.Wait()
		close(g.done)
	}()
	return g
}

// Done は子が終わると閉じる。
func (g *Group) Done() <-chan struct{} { return g.done }

// Err は終わった子の cmd.Wait の結果。Done が閉じた後に呼ぶ。
func (g *Group) Err() error { return g.err }

// Stop は子を止め、終わるまで待つ。子がもう終わっていれば何も送らず exited を true で返す。
//
// 子の終了と止める理由が同時に来たら、終了を優先する (呼び出し側の select はどちらも選びうる)。自分で終わった子を止めたことに
// すると、終わり方 (exit code・決定ファイル) を確かめないまま後始末に進んでしまう。
//
// 止めるときは process group に SIGTERM を送り、grace のうちに子が終わらなければ SIGKILL を送って、終わるまで待つ (grace が 0 以下なら SIGTERM を送らず SIGKILL だけ)。子が
// 終わった後も group に残った孫を SIGKILL で止める (workspace を消すか次が使う前に、そこを cwd にした process を残さないため)。
// err は signal を送れなかった理由。
func (g *Group) Stop(grace time.Duration) (exited bool, err error) {
	select {
	case <-g.done:
		return true, nil
	default:
	}
	pid := g.cmd.Process.Pid
	var errs []error
	if grace > 0 {
		if err := kill(pid, syscall.SIGTERM); err != nil {
			errs = append(errs, err)
		}
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-g.done:
		case <-timer.C:
		}
	}
	select {
	case <-g.done:
	default:
		if err := kill(pid, syscall.SIGKILL); err != nil {
			errs = append(errs, err)
		}
		<-g.done
	}
	if err := kill(pid, syscall.SIGKILL); err != nil {
		errs = append(errs, err)
	}
	return false, errors.Join(errs...)
}

// kill は process group に sig を送る。group がもう無い (ESRCH) のは失敗と数えない。
func kill(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("process group %d に %v を送れない: %w", pid, sig, err)
	}
	return nil
}

// Wait は開始済みの cmd の終了を待つ。timeout を超えたら猶予なしで process group ごと止めて、終わるまで待つ。
func Wait(cmd *exec.Cmd, timeout time.Duration) (End, error) {
	g := Watch(cmd)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-g.Done():
		return Exited, g.Err()
	case <-timer.C:
	}
	if exited, _ := g.Stop(0); exited {
		return Exited, g.Err()
	}
	return TimedOut, g.Err()
}
