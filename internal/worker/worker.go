// Package worker は worker の 1 回分 (workspace の用意・描画・`claude -p` の起動・終了の待ち・after_run) を
// 1 つの goroutine に閉じ、起動と終わり方を event で返す (system.md §7 / §10 / §13 の起動部)。
package worker

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/render"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
	"github.com/swat9013/claude-dispatcher/internal/workspace"
)

// Job は起動する worker 1 回分の入力。
type Job struct {
	Issue     target.Issue
	Trigger   trigger.Trigger
	Attempt   int
	SessionID string
	// Resume は、前の attempt で SessionID の session を始めていて、その続きから起動するか
	Resume bool
	// Prompt は共通 prompt の template (workflow 定義の本文)
	Prompt string
}

// Event は worker の起動 (Started) か終わり (Ended)。
type Event interface{ isEvent() }

// Started は claude を起動したこと。
type Started struct {
	Number    int
	PID       int
	Workspace string
}

// Ended は worker 1 回分が終わったこと。
type Ended struct {
	Number int
	Result Result
}

func (Started) isEvent() {}
func (Ended) isEvent()   {}

// Result は worker 1 回分の終わり方。
type Result struct {
	// Failure は attempt の失敗の理由 (hook・描画・起動の失敗、異常終了)。失敗でなければ ""
	Failure string
	// ExitCode は claude の process が自分で終わったときの exit code。起動に至らないか、signal で終わったなら nil
	ExitCode *int
	// Stopped は Stop で止めたか
	Stopped bool
	// AfterRunError は after_run の失敗。処理は続けるので、失敗とは数えない
	AfterRunError string
	// StopError は止めるときに process group へ signal を送れなかった理由
	StopError string
}

// Runner は worker を起動する。
type Runner struct {
	Workspaces workspace.Manager
	// Definition は起動の仕方 (claude.command / args) と、stall と上限時間の上限を持つ workflow 定義
	Definition workflow.Definition
	// Env は worker に渡す環境 (loop の環境)
	Env []string
	// StateDir は描画した共通 prompt と worker log を置く state dir
	StateDir string
}

// stopGrace は停止のときに SIGTERM から SIGKILL までに待つ時間
const stopGrace = 5 * time.Second

// watchInterval は stall と上限時間を確かめる間隔
const watchInterval = 250 * time.Millisecond

// Run は走っている worker 1 回分。
type Run struct {
	stop chan struct{}
	once sync.Once
}

// Stop は worker を止める。起動の前なら起動せず、走っていれば process group を止める。何度呼んでもよい。
func (r *Run) Stop() { r.once.Do(func() { close(r.stop) }) }

func (r *Run) stopped() bool {
	select {
	case <-r.stop:
		return true
	default:
		return false
	}
}

// Start は job の worker を別の goroutine で進め、起動と終わりを events に送る。終わりは必ず 1 回送る。
func (r Runner) Start(job Job, events chan<- Event) *Run {
	run := &Run{stop: make(chan struct{})}
	n := job.Issue.Number
	go func() {
		result := r.attempt(job, run, func(pid int, workspace string) {
			events <- Started{Number: n, PID: pid, Workspace: workspace}
		})
		events <- Ended{Number: n, Result: result}
	}()
	return run
}

func (r Runner) attempt(job Job, run *Run, started func(pid int, workspace string)) Result {
	n := job.Issue.Number
	path, err := r.Workspaces.Prepare(n)
	if err != nil {
		return Result{Failure: err.Error()}
	}
	result := r.launch(job, run, path, started)
	if err := r.Workspaces.AfterRun(n); err != nil {
		result.AfterRunError = err.Error()
	}
	return result
}

// launch は描画して claude を起動し、終わるか止められるまで待つ。
func (r Runner) launch(job Job, run *Run, workspacePath string, started func(pid int, workspace string)) Result {
	if run.stopped() {
		return Result{Stopped: true}
	}
	action, promptFile, err := r.render(job, workspacePath)
	if err != nil {
		return Result{Failure: err.Error()}
	}
	claude := r.Definition.Claude
	// command は attempt ごとに PATH から引き直す (loop を止めずに claude を入れ替えても、次の attempt から追従する)
	command, err := deps.Lookup(claude.Command, r.Env)
	if err != nil {
		return Result{Failure: err.Error()}
	}
	// stdout (stream-json) と stderr を別の file に追記する。stall は stdout の file が伸びなくなったことで見る。
	// claude に file をそのまま渡すので、loop が死んでも claude の出力は途切れない
	name := filepath.Join(r.StateDir, "workers", target.FileName(job.Issue.Number))
	stream, err := openAppend(name + ".log")
	if err != nil {
		return Result{Failure: fmt.Sprintf("worker log を開けない: %v", err)}
	}
	defer stream.Close()
	stderr, err := openAppend(name + ".stderr.log")
	if err != nil {
		return Result{Failure: fmt.Sprintf("worker log を開けない: %v", err)}
	}
	defer stderr.Close()

	// action は `--` の後ろに置く (`-` で始まる action を claude が option と読まないように)
	args := append(append([]string{}, claude.Args...), "-p", "--output-format", "stream-json", "--verbose")
	args = append(args, sessionArgs(job)...)
	args = append(args, "--append-system-prompt-file", promptFile, "--", action)
	cmd := exec.Command(command, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = workspacePath, r.Env, stream, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return Result{Failure: fmt.Sprintf("%s を起動できない: %v", claude.Command, err)}
	}
	started(cmd.Process.Pid, workspacePath)
	return r.wait(cmd, run, stream)
}

// sessionArgs は session の渡し方。前の attempt で始めた session があれば同じ id で続け、無ければ発行した id で始める。
func sessionArgs(job Job) []string {
	if job.Resume {
		return []string{"--resume", job.SessionID}
	}
	return []string{"--session-id", job.SessionID}
}

// streamWatch は stream の file が最後に伸びた時刻を追う (stall の検知に使う)。
type streamWatch struct {
	file    *os.File
	size    int64
	changed time.Time
}

// observe は now の時点の file の大きさを見て、伸びていれば伸びた時刻を進める。file を読めなければ、その失敗を返す。
func (w *streamWatch) observe(now time.Time) error {
	info, err := w.file.Stat()
	if err != nil {
		return fmt.Errorf("stream の file を読めない: %w", err)
	}
	if info.Size() != w.size {
		w.size, w.changed = info.Size(), now
	}
	return nil
}

// silentFor は stream の file が最後に伸びてから now までの時間。
func (w *streamWatch) silentFor(now time.Time) time.Duration { return now.Sub(w.changed) }

// render は action を描画し、共通 prompt を描画して state dir の file に書く。
func (r Runner) render(job Job, workspacePath string) (action, promptFile string, err error) {
	vars := render.Vars{Issue: job.Issue, Trigger: job.Trigger.Name, Attempt: job.Attempt, Workspace: workspacePath}
	action, err = render.Render("action", job.Trigger.Action, vars)
	if err != nil {
		return "", "", err
	}
	prompt, err := render.Render("共通 prompt", job.Prompt, vars)
	if err != nil {
		return "", "", err
	}
	promptFile = filepath.Join(r.StateDir, "prompts", target.FileName(job.Issue.Number)+".md")
	if err := writeFile(promptFile, prompt); err != nil {
		return "", "", fmt.Errorf("共通 prompt を書けない: %w", err)
	}
	return action, promptFile, nil
}

// wait は起動した claude が終わるか、止められるか、stall か上限時間で止めるまで待つ。
func (r Runner) wait(cmd *exec.Cmd, run *Run, stream *os.File) Result {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	result, ended := r.watch(done, run, stream)
	if ended {
		return r.exited(cmd, Result{})
	}
	// 子の終了と同時に止める理由ができたら終了を優先する (select はどちらも選びうる)。自分で終わった worker を
	// 止めたことにすると、終わり方を確かめないまま claim を解いてしまう
	select {
	case <-done:
		return r.exited(cmd, Result{})
	default:
	}
	if run.stopped() {
		// stall や上限時間と停止要求が重なったら、loop が止めたことを優先する
		result = Result{Stopped: true}
	}
	if err := stopGroup(cmd.Process.Pid, done); err != nil {
		result.StopError = err.Error()
	}
	return r.exited(cmd, result)
}

// watch は claude が終わるまで待つ (ended が true)。先に止める理由ができたら、その理由を持った result を返す。
// stall と上限時間は、どちらかが有効なときだけ確かめる。
func (r Runner) watch(done <-chan error, run *Run, stream *os.File) (result Result, ended bool) {
	started := time.Now()
	w := &streamWatch{file: stream, changed: started}
	if err := w.observe(started); err != nil {
		return Result{Failure: err.Error()}, false
	}
	var tick <-chan time.Time
	if r.Definition.StallTimeout > 0 || r.Definition.RunTimeout > 0 {
		ticker := time.NewTicker(watchInterval)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		select {
		case <-done:
			return Result{}, true
		case <-run.stop:
			return Result{Stopped: true}, false
		case now := <-tick:
			if err := w.observe(now); err != nil {
				return Result{Failure: err.Error()}, false
			}
			if reason := r.overdue(now.Sub(started), w.silentFor(now)); reason != "" {
				return Result{Failure: reason}, false
			}
		}
	}
}

// overdue は stall か上限時間に当たったら、その理由を返す。当たらなければ ""。
func (r Runner) overdue(running, silent time.Duration) string {
	stall, limit := r.Definition.StallTimeout, r.Definition.RunTimeout
	switch {
	case stall > 0 && silent > stall:
		return fmt.Sprintf("stall (stream が %s 途絶えた)", stall)
	case limit > 0 && running > limit:
		return fmt.Sprintf("上限時間 %s を超えた", limit)
	}
	return ""
}

// exited は終わった claude の終わり方を result に足す。
func (r Runner) exited(cmd *exec.Cmd, result Result) Result {
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Exited() {
		code := status.ExitStatus()
		result.ExitCode = &code
	}
	if !result.Stopped && result.Failure == "" && (result.ExitCode == nil || *result.ExitCode != 0) {
		result.Failure = "異常終了 (" + cmd.ProcessState.String() + ")"
	}
	return result
}

// stopGroup は process group に SIGTERM を送り、stopGrace のうちに claude が終わらなければ SIGKILL を送って、終わるまで待つ。
// claude が終わった後も、group に残った子 (claude が起こした tool の process) を SIGKILL で止める。workspace を消すか
// 次の attempt が使う前に、そこを cwd にした process を残さないため。
func stopGroup(pid int, done <-chan error) error {
	var errs []error
	if err := kill(pid, syscall.SIGTERM); err != nil {
		errs = append(errs, err)
	}
	select {
	case <-done:
	case <-time.After(stopGrace):
		if err := kill(pid, syscall.SIGKILL); err != nil {
			errs = append(errs, err)
		}
		<-done
	}
	if err := kill(pid, syscall.SIGKILL); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// kill は process group に sig を送る。group がもう無い (ESRCH) のは失敗と数えない。
func kill(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("process group %d に %v を送れない: %w", pid, sig, err)
	}
	return nil
}

func writeFile(file, content string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(content), 0o644)
}

func openAppend(file string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// NewSessionID は Claude Code の session id に渡す UUID (version 4) を発行する。
func NewSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("session id の乱数を作れない: " + err.Error())
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
