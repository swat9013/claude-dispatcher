package proc_test

import (
	"bufio"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync"
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
	// 子は SIGTERM で自分から終わるが、SIGTERM を無視する孫を group に残す。合図は孫が trap を張った後に自分の pid を書く
	cmd, line := startGroup(t, `trap 'exit 0' TERM; sh -c "trap '' TERM; echo \$\$; while :; do sleep 0.05; done" & while :; do sleep 0.05; done`)
	grandchild, err := strconv.Atoi(line)
	if err != nil {
		t.Fatalf("孫の pid を読めない: %q", line)
	}
	group := proc.Watch(cmd)

	if _, err := group.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	// 猶予切れの SIGKILL でなく、子が終わった後の SIGKILL で孫が止まる筋を確かめる
	if !cmd.ProcessState.Exited() || cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("子が SIGTERM で自分から終わっていない: %v", cmd.ProcessState)
	}

	waitGone(t, grandchild)
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

// waitGone は pid の process が消えるまで待つ。上限を過ぎたら止めて test を落とす。
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("孫 %d が残った", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWatchReapsAGrandchildLeftInTheGroupWhenTheChildExitsByItself(t *testing.T) {
	// 子は SIGTERM を無視する孫を group に残して exit 0 する。止める理由は無い
	cmd, line := startGroup(t, `sh -c "trap '' TERM; echo \$\$; while :; do sleep 0.05; done" & sleep 0.3; exit 0`)
	grandchild, err := strconv.Atoi(line)
	if err != nil {
		t.Fatalf("孫の pid を読めない: %q", line)
	}
	group := proc.Watch(cmd)

	if err := group.Err(); err != nil {
		t.Fatalf("子が exit 0 で終わっていない: %v", err)
	}
	if exited, err := group.Stop(time.Second); !exited || err != nil {
		t.Fatalf("exited = %v, err = %v", exited, err)
	}
	waitGone(t, grandchild)
}

func TestStopSendsNoSignalToTheGroupAfterTheChildIsReaped(t *testing.T) {
	var (
		mu        sync.Mutex
		group     *proc.Group
		afterReap []string
	)
	proc.SetKillGroup(t, func(pgid int, sig syscall.Signal) error {
		mu.Lock()
		defer mu.Unlock()
		if group == nil {
			return syscall.Kill(-pgid, sig)
		}
		select {
		case <-group.Done():
			afterReap = append(afterReap, sig.String())
		default:
		}
		return syscall.Kill(-pgid, sig)
	})
	for _, script := range []string{`echo ready; exit 0`, `trap '' TERM; echo ready; while :; do sleep 0.05; done`} {
		cmd, _ := startGroup(t, script)
		mu.Lock()
		// mu を握ったまま Watch するので、Watch の goroutine が送る signal は新しい group と照らされる
		group = proc.Watch(cmd)
		g := group
		mu.Unlock()
		_, _ = g.Stop(100 * time.Millisecond)
		<-g.Done()
		_, _ = g.Stop(100 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(afterReap) != 0 {
		t.Fatalf("回収後の group に signal を送った (pgid が再利用されうる): %v", afterReap)
	}
}

func TestStopReturnsTheReasonWithoutWaitingWhenSIGKILLCannotBeSent(t *testing.T) {
	proc.SetKillGroup(t, func(pgid int, sig syscall.Signal) error {
		if sig == syscall.SIGKILL {
			return syscall.EPERM
		}
		return syscall.Kill(-pgid, sig)
	})
	cmd, _ := startGroup(t, `trap '' TERM; echo ready; while :; do sleep 0.05; done`)
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	group := proc.Watch(cmd)

	returned := make(chan error, 1)
	go func() {
		exited, err := group.Stop(50 * time.Millisecond)
		if exited {
			err = errors.New("exited が true")
		}
		returned <- err
	}()
	select {
	case err := <-returned:
		if !errors.Is(err, syscall.EPERM) {
			t.Fatalf("err = %v; want SIGKILL を送れなかった理由 (EPERM)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SIGKILL を送れないのに Stop が子の終わりを待ち続けた")
	}
}

func TestWaitReturnsTheReasonWithoutWaitingWhenSIGKILLCannotBeSent(t *testing.T) {
	proc.SetKillGroup(t, func(int, syscall.Signal) error { return syscall.EPERM })
	cmd, _ := startGroup(t, `trap '' TERM; echo ready; while :; do sleep 0.05; done`)
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })

	type outcome struct {
		end proc.End
		err error
	}
	returned := make(chan outcome, 1)
	go func() {
		end, err := proc.Wait(cmd, 50*time.Millisecond)
		returned <- outcome{end, err}
	}()
	select {
	case o := <-returned:
		if o.end != proc.TimedOut || !errors.Is(o.err, syscall.EPERM) {
			t.Fatalf("end = %v, err = %v", o.end, o.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SIGKILL を送れないのに Wait が子の終わりを待ち続けた")
	}
}

func TestWaitLeavesAGrandchildInTheGroupWhenTheChildExitsByItself(t *testing.T) {
	// 外部 CLI と hook の経路は、自分で終わった子が残した process (credential helper 等) を止めない
	cmd, line := startGroup(t, `sh -c "echo \$\$; exec sleep 30" & sleep 0.3; exit 0`)
	grandchild, err := strconv.Atoi(line)
	if err != nil {
		t.Fatalf("孫の pid を読めない: %q", line)
	}
	t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })

	end, err := proc.Wait(cmd, 10*time.Second)

	if end != proc.Exited || err != nil {
		t.Fatalf("end = %v, err = %v", end, err)
	}
	if !alive(grandchild) {
		t.Fatalf("自分で終わった子の孫 %d を止めた", grandchild)
	}
}

func TestCommandReportsTheReasonWhenItCannotStopATimedOutChild(t *testing.T) {
	proc.SetKillGroup(t, func(int, syscall.Signal) error { return syscall.EPERM })
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	// 子は止められないまま 3 秒で自分から終わる
	c := proc.Command{Path: sh, Timeout: 50 * time.Millisecond}

	_, err = c.Output("-c", "sleep 3")

	if err == nil || !strings.Contains(err.Error(), syscall.EPERM.Error()) {
		t.Fatalf("err = %v; want SIGKILL を送れなかった理由を含む", err)
	}
}
