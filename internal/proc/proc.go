// Package proc は起動済みの子プロセスを process group ごと待って止める。gh の観測・orchestrator の起動・worker が共有する。
package proc

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync/atomic"
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
	// ended は子が終わると閉じる (回収の前)。done より先に閉じうる
	ended chan struct{}
	err   error
	// sweepErr は子の終了後に group へ SIGKILL を送れなかった理由
	sweepErr error
}

// Watch は開始済みの cmd の終了を別の goroutine で待ち始める。
//
// 子が終わったら、回収 (reap) する前に process group に残った孫を SIGKILL で止めてから回収し、Done を閉じる。workspace を消すか
// 次が使う前に、そこを cwd にした process を残さないため。回収の前に送るのは、回収した後だと group が空になった pgid が別の
// process group に再利用されうるため (回収前は zombie の子が pgid を持ち続ける)。
func Watch(cmd *exec.Cmd) *Group {
	g := &Group{cmd: cmd, done: make(chan struct{}), ended: make(chan struct{})}
	go func() {
		pid := cmd.Process.Pid
		if err := waitExit(pid); err != nil {
			// 回収せずに待てないなら、再利用されうる pgid へ送らないよう後始末を諦める
			g.sweepErr = err
			g.err = cmd.Wait()
			close(g.ended)
		} else {
			close(g.ended)
			g.sweepErr = sweep(pid)
			g.err = cmd.Wait()
		}
		close(g.done)
	}()
	return g
}

// Done は子が終わり、group に残った孫を止めてから閉じる。
func (g *Group) Done() <-chan struct{} { return g.done }

// Err は子の終了を待ち、cmd.Wait の結果を返す。Stop が SIGKILL を送れずに返った後は、子が終わるまで戻らない。
func (g *Group) Err() error {
	<-g.done
	return g.err
}

// Stop は子を止め、終わるまで待つ。子がもう終わっていれば何も送らず exited を true で返す。
//
// 子の終了と止める理由が同時に来たら、終了を優先する (呼び出し側の select はどちらも選びうる)。自分で終わった子を止めたことに
// すると、終わり方 (exit code・決定ファイル) を確かめないまま後始末に進んでしまう。
//
// 止めるときは process group に SIGTERM を送り、grace のうちに子が終わらなければ SIGKILL を送って、終わるまで待つ (grace が 0 以下なら SIGTERM を送らず SIGKILL だけ)。
// SIGKILL を送れなければ、子の終わりを待たずに返す (exited は false)。group に残った孫は Watch が止める。
// err は signal を送れなかった理由。
func (g *Group) Stop(grace time.Duration) (exited bool, err error) {
	select {
	case <-g.done:
		return true, g.sweepErr
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
			select {
			case <-g.ended:
				// 送る前に子が終わっていた (darwin は zombie だけの group への signal を EPERM で拒む)。孫は Watch が止める
			default:
				return false, errors.Join(append(errs, err)...)
			}
		}
		<-g.done
	}
	return false, errors.Join(append(errs, g.sweepErr)...)
}

// killGroup は process group pgid に sig を送る関数。test が失敗を差し込めるよう atomic に置く (Watch の goroutine も読む)。
var killGroup atomic.Pointer[func(pgid int, sig syscall.Signal) error]

func init() {
	f := func(pgid int, sig syscall.Signal) error { return syscall.Kill(-pgid, sig) }
	killGroup.Store(&f)
}

// sweep は終わった子 (回収前) の process group に残った孫を SIGKILL で止める。
func sweep(pid int) error {
	err := kill(pid, syscall.SIGKILL)
	if runtime.GOOS == "darwin" && errors.Is(err, syscall.EPERM) {
		// darwin は zombie の子だけが残った group への signal を EPERM で拒む。生きた孫には届いた後でも同じ EPERM が返るので、
		// 送れなかった孫 (権限の違う process) と区別できない。後者は配布の想定 (孫は claude が同じ user で起こす) の外なので失敗と数えない
		return nil
	}
	return err
}

// kill は process group に sig を送る。group がもう無い (ESRCH) のは失敗と数えない。
func kill(pid int, sig syscall.Signal) error {
	if err := (*killGroup.Load())(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("process group %d に %v を送れない: %w", pid, sig, err)
	}
	return nil
}

// Wait は開始済みの cmd の終了を待つ。timeout を超えたら猶予なしで process group ごと止めて、終わるまで待つ。
// 止めるための signal を送れなければ、終わりを待たずに TimedOut と送れなかった理由を返す。
func Wait(cmd *exec.Cmd, timeout time.Duration) (End, error) {
	g := Watch(cmd)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-g.Done():
		return Exited, g.Err()
	case <-timer.C:
	}
	exited, err := g.Stop(0)
	if exited {
		return Exited, g.Err()
	}
	select {
	case <-g.Done():
		return TimedOut, g.Err()
	default:
		return TimedOut, err
	}
}
