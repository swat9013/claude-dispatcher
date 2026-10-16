// Package proc は起動済みの子プロセスを process group ごと待って止める。gh の観測・orchestrator の起動・worker が共有する。
package proc

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
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
	err  error
	// sweepOnExit は子が自分で終わったときも group に残った孫を止めるか
	sweepOnExit bool
	// sweepErr は子の終了後の後始末 (回収せずに終了を待つ・group へ SIGKILL を送る) をできなかった理由
	sweepErr error

	// mu は「子が終わったか確かめて group へ signal を送る」(Stop) と「子が終わったと記録する」(Watch の goroutine) を排他にする。
	// 終わったと記録した後は Stop が signal を送らないので、回収した後の pgid へ届かない
	mu       sync.Mutex
	ended    bool
	stopping bool
}

// Watch は開始済みの cmd の終了を別の goroutine で待ち始める。
//
// 子が終わったら、回収 (reap) する前に process group に残った孫を SIGKILL で止めてから回収し、Done を閉じる。workspace を消すか
// 次が使う前に、そこを cwd にした process を残さないため。回収の前に送るのは、回収した後だと group が空になった pgid が別の
// process group に再利用されうるため (回収前は zombie の子が pgid を持ち続ける)。
func Watch(cmd *exec.Cmd) *Group { return watch(cmd, true) }

// watch は Watch と同じ。sweepOnExit が false なら、子が自分で終わったときの孫は止めない (Stop で止めたときだけ止める)。
func watch(cmd *exec.Cmd, sweepOnExit bool) *Group {
	g := &Group{cmd: cmd, done: make(chan struct{}), sweepOnExit: sweepOnExit}
	go func() {
		pid := cmd.Process.Pid
		if err := waitExit(pid); err != nil {
			// 回収せずに待てないなら、再利用されうる pgid へ送らないよう後始末を諦める
			g.markEnded()
			g.sweepErr = err
		} else if g.markEnded() {
			g.sweepErr = sweep(pid)
		}
		g.err = cmd.Wait()
		close(g.done)
	}()
	return g
}

// markEnded は子が終わったと記録し、group に残った孫を止めるかを返す。
func (g *Group) markEnded() (sweep bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ended = true
	return g.sweepOnExit || g.stopping
}

// signal は子がまだ終わっていなければ group へ sig を送る。終わっていれば何も送らず sent を false で返す (孫は Watch が止める)。
func (g *Group) signal(sig syscall.Signal) (sent bool, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ended {
		return false, nil
	}
	g.stopping = true
	return true, kill(g.cmd.Process.Pid, sig)
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
// SIGKILL を送れず子も終わらなければ、子の終わりを待たずに返す (exited は false。Done はまだ閉じていない)。
// err は後始末をできなかった理由 (signal を送れなかった・Watch が孫を止められなかった)。
func (g *Group) Stop(grace time.Duration) (exited bool, err error) {
	select {
	case <-g.done:
		return true, g.sweepErr
	default:
	}
	var errs []error
	if grace > 0 {
		if _, err := g.signal(syscall.SIGTERM); err != nil {
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
		if _, err := g.signal(syscall.SIGKILL); err != nil {
			// 届いた後でも失敗が返りうる (darwin は group に zombie が居ると EPERM を返す) ので、少しだけ終わりを待つ
			timer := time.NewTimer(killSettle)
			defer timer.Stop()
			select {
			case <-g.done:
			case <-timer.C:
				return false, errors.Join(append(errs, err)...)
			}
		}
		<-g.done
	}
	return false, errors.Join(append(errs, g.sweepErr)...)
}

// killSettle は SIGKILL の送信が失敗を返した後、子の終わりを待つ上限。
const killSettle = time.Second

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
// 止めるための signal を送れず子も終わらなければ、終わりを待たずに TimedOut と送れなかった理由を返す (*exec.ExitError ではない)。
// 子が自分で終わったときは group に残った孫を止めない (credential helper 等が後に残る使い方を妨げない)。
func Wait(cmd *exec.Cmd, timeout time.Duration) (End, error) {
	g := watch(cmd, false)
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
