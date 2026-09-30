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
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/render"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/workspace"
)

// Job は起動する worker 1 回分の入力。
type Job struct {
	Issue     target.Issue
	Trigger   trigger.Trigger
	Attempt   int
	SessionID string
	// Prompt は共通 prompt の template (workflow 定義の本文)
	Prompt string
}

// Event は worker の起動か終わり。どちらか一方だけを持つ。
type Event struct {
	Number  int
	Started *Started
	Ended   *Result
}

// Started は claude を起動したこと。
type Started struct {
	PID       int
	Workspace string
}

// Result は worker 1 回分の終わり方。
type Result struct {
	// Failure は attempt の失敗の理由 (hook・描画・起動の失敗、異常終了)。失敗でなければ ""
	Failure string
	// ExitCode は claude の process が終わったときの exit code。起動に至らなければ nil
	ExitCode *int
	// Stopped は Stop で止めたか
	Stopped bool
	// AfterRunError は after_run の失敗。処理は続けるので、失敗とは数えない
	AfterRunError string
}

// Runner は worker を起動する。
type Runner struct {
	Workspaces workspace.Manager
	// Command は claude の起動 command (PATH から探す)、Args はその後ろに dispatcher の引数より前に渡す引数
	Command string
	Args    []string
	// Env は worker に渡す環境 (loop の環境)
	Env []string
	// StateDir は描画した共通 prompt と worker log を置く state dir
	StateDir string
}

// stopGrace は停止のときに SIGTERM から SIGKILL までに待つ時間
const stopGrace = 5 * time.Second

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
	go func() {
		result := r.attempt(job, run, func(s Started) { events <- Event{Number: job.Issue.Number, Started: &s} })
		events <- Event{Number: job.Issue.Number, Ended: &result}
	}()
	return run
}

func (r Runner) attempt(job Job, run *Run, started func(Started)) Result {
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
func (r Runner) launch(job Job, run *Run, workspacePath string, started func(Started)) Result {
	if run.stopped() {
		return Result{Stopped: true}
	}
	vars := render.Vars{Issue: job.Issue, Trigger: job.Trigger.Name, Attempt: job.Attempt, Workspace: workspacePath}
	action, err := render.Render("action", job.Trigger.Action, vars)
	if err != nil {
		return Result{Failure: err.Error()}
	}
	prompt, err := render.Render("共通 prompt", job.Prompt, vars)
	if err != nil {
		return Result{Failure: err.Error()}
	}
	promptFile := filepath.Join(r.StateDir, "prompts", "issue-"+strconv.Itoa(job.Issue.Number)+".md")
	if err := writeFile(promptFile, prompt); err != nil {
		return Result{Failure: fmt.Sprintf("共通 prompt を書けない: %v", err)}
	}
	command, err := deps.Lookup(r.Command, r.Env)
	if err != nil {
		return Result{Failure: err.Error()}
	}
	logFile := filepath.Join(r.StateDir, "workers", "issue-"+strconv.Itoa(job.Issue.Number)+".log")
	log, err := openAppend(logFile)
	if err != nil {
		return Result{Failure: fmt.Sprintf("worker log を開けない: %v", err)}
	}
	defer log.Close()

	args := append(append([]string{}, r.Args...), "-p", "--output-format", "stream-json", "--verbose",
		"--session-id", job.SessionID, "--append-system-prompt-file", promptFile, action)
	cmd := exec.Command(command, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = workspacePath, r.Env, log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return Result{Failure: fmt.Sprintf("%s を起動できない: %v", r.Command, err)}
	}
	started(Started{PID: cmd.Process.Pid, Workspace: workspacePath})

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	select {
	case <-done:
	case <-run.stop:
		stopped = true
		stopGroup(cmd.Process.Pid, done)
	}
	code := cmd.ProcessState.ExitCode()
	result := Result{ExitCode: &code, Stopped: stopped}
	if !stopped && code != 0 {
		result.Failure = fmt.Sprintf("exit %d", code)
	}
	return result
}

// stopGroup は process group に SIGTERM を送り、stopGrace のうちに終わらなければ SIGKILL を送って、終わるまで待つ。
func stopGroup(pid int, done <-chan error) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(stopGrace):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-done
	}
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
