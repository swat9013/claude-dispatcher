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
	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/render"
	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
	"github.com/swat9013/claude-dispatcher/internal/workspace"
)

// Job は起動する worker 1 回分の入力。
type Job struct {
	Item      target.Item
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
	Target    target.Ref
	PID       int
	Workspace string
}

// Ended は worker 1 回分が終わったこと。
type Ended struct {
	Target target.Ref
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
	// ResultSummary は attempt の最後の stream-json の result の行の要約。claude を起動しなかった・result の行が無い・worker log を
	// 最後まで読めなかったなら nil。項目の型が違えば、その項目を除いた要約
	ResultSummary *ResultSummary
	// ResultSummaryError は要約を作れなかったか、一部の項目を読めなかった理由
	ResultSummaryError string
}

// Runner は worker を起動する。
type Runner struct {
	Workspaces workspace.Manager
	// Definition は起動の仕方 (claude.command / args) と、stall と上限時間の上限を持つ workflow 定義
	Definition workflow.Definition
	// StateDir は描画した共通 prompt と worker log を置く state dir
	StateDir string
}

// stopGrace は停止のときに SIGTERM から SIGKILL までに待つ時間
const stopGrace = 5 * time.Second

// watchInterval は stream の file (活動と progress) と、stall・上限時間を確かめる間隔
const watchInterval = 250 * time.Millisecond

// Run は走っている worker 1 回分。
type Run struct {
	stop chan struct{}
	once sync.Once
	// activity は最新の活動 (formats.md §6)。loop の goroutine が読むので mu で守る
	mu       sync.Mutex
	activity status.Activity
}

// Activity は最新の活動。まだ無ければ At がゼロ値。
func (r *Run) Activity() status.Activity {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activity
}

func (r *Run) setActivity(a status.Activity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activity = a
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
	ref := job.Item.Ref()
	go func() {
		result := r.attempt(job, run, func(pid int, workspace string) {
			events <- Started{Target: ref, PID: pid, Workspace: workspace}
		})
		events <- Ended{Target: ref, Result: result}
	}()
	return run
}

func (r Runner) attempt(job Job, run *Run, started func(pid int, workspace string)) Result {
	ref := job.Item.Ref()
	path, err := r.Workspaces.Prepare(ref)
	if err != nil {
		return Result{Failure: err.Error()}
	}
	result := r.launch(job, run, path, started)
	if err := r.Workspaces.AfterRun(ref); err != nil {
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
	command, err := deps.Lookup(claude.Command, r.Workspaces.Env)
	if err != nil {
		return Result{Failure: err.Error()}
	}
	// stdout (stream-json) と stderr を別の file に追記する。stall は stdout に progress が書かれなくなったことで見る。
	// claude に file をそのまま渡すので、loop が死んでも claude の出力は途切れない
	name := filepath.Join(r.StateDir, "workers", job.Item.Ref().FileName())
	stream, err := openStream(name + ".log")
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
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = workspacePath, r.Workspaces.Env, stream, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// 前の attempt が追記した行は、この attempt の活動でない
	before, err := stream.Stat()
	if err != nil {
		return Result{Failure: fmt.Sprintf("stream の file を読めない: %v", err)}
	}
	if err := cmd.Start(); err != nil {
		return Result{Failure: fmt.Sprintf("%s を起動できない: %v", claude.Command, err)}
	}
	started(cmd.Process.Pid, workspacePath)
	watch := newStreamWatch(stream, before.Size(), time.Now(), workspacePath,
		limits{stall: r.Definition.StallTimeout, run: r.Definition.RunTimeout})
	result := r.wait(cmd, run, watch)
	// claude の process が終わった後に読むので、claude が書く result の行はそろっている
	result.ResultSummary, err = watch.lastResult()
	if err != nil {
		result.ResultSummaryError = fmt.Sprintf("attempt の最後の result の要約を (一部) 作れない: %v", err)
	}
	return result
}

// sessionArgs は session の渡し方。前の attempt で始めた session があれば同じ id で続け、無ければ発行した id で始める。
func sessionArgs(job Job) []string {
	if job.Resume {
		return []string{"--resume", job.SessionID}
	}
	return []string{"--session-id", job.SessionID}
}

// render は action を描画し、共通 prompt を描画して state dir の file に書く。
func (r Runner) render(job Job, workspacePath string) (action, promptFile string, err error) {
	vars := render.Vars{Item: job.Item, Trigger: job.Trigger.Name, Attempt: job.Attempt, Workspace: workspacePath}
	action, err = render.Render("action", job.Trigger.Action, vars)
	if err != nil {
		return "", "", err
	}
	prompt, err := render.Render("共通 prompt", job.Prompt, vars)
	if err != nil {
		return "", "", err
	}
	promptFile = filepath.Join(r.StateDir, "prompts", job.Item.Ref().FileName()+".md")
	if err := writeFile(promptFile, prompt); err != nil {
		return "", "", fmt.Errorf("共通 prompt を書けない: %w", err)
	}
	return action, promptFile, nil
}

// wait は起動した claude が終わるか、止められるか、stall か上限時間で止めるまで待つ。
func (r Runner) wait(cmd *exec.Cmd, run *Run, w *streamWatch) Result {
	group := proc.Watch(cmd)
	ticker := time.NewTicker(watchInterval)
	result, ended := watch(group.Done(), run, w, ticker.C)
	ticker.Stop()
	if ended {
		return r.exited(cmd, Result{})
	}
	if run.stopped() {
		// stall や上限時間と停止要求が重なったら、loop が止めたことを優先する
		result = Result{Stopped: true}
	}
	exited, err := group.Stop(stopGrace)
	if exited {
		return r.exited(cmd, Result{})
	}
	if err != nil {
		result.StopError = err.Error()
	}
	return r.exited(cmd, result)
}

// watch は claude が終わるまで待つ (ended が true)。先に止める理由ができたら、その理由を持った result を返す。
// ticks が届くたびに、その時刻で stream を確かめ、活動を更新し、確認が返した理由で止める。
func watch(done <-chan struct{}, run *Run, w *streamWatch, ticks <-chan time.Time) (result Result, ended bool) {
	for {
		select {
		case <-done:
			return Result{}, true
		case <-run.stop:
			return Result{Stopped: true}, false
		case now := <-ticks:
			seen := w.check(now)
			if seen.changed {
				run.setActivity(status.Activity{At: now, Summary: seen.activity})
			}
			if seen.stop != "" {
				return Result{Failure: seen.stop}, false
			}
		}
	}
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

func writeFile(file, content string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(content), 0o644)
}

// openStream は stream の worker log を追記で開く。claude が書き、監視と要約が同じ fd で読むので、読み書きで開く
// (formats.md §6)。
func openStream(file string) (*os.File, error) { return openFile(file, os.O_RDWR) }

// openAppend は file を書き込みだけの追記で開く。
func openAppend(file string) (*os.File, error) { return openFile(file, os.O_WRONLY) }

func openFile(file string, access int) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(file, os.O_CREATE|access|os.O_APPEND, 0o644)
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
