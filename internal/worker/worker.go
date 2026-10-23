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
	"github.com/swat9013/claude-dispatcher/internal/state"
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

// Event は worker の起動 (Started)・止められないことの知らせ (Unstopped)・終わり (Ended)。
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

// Unstopped は止めるときに SIGKILL を送れず、claude がまだ終わっていないこと (system.md §7)。worker は終わっておらず、
// claude が終わったときに Ended を送る。
type Unstopped struct {
	Target target.Ref
	// StopError は process group へ signal を送れなかった理由
	StopError string
	// Exited は claude が終わると閉じる。閉じた後も、worker は after_run を撃ってから Ended を送る
	Exited <-chan struct{}
}

func (Started) isEvent()   {}
func (Unstopped) isEvent() {}
func (Ended) isEvent()     {}

// UnstoppedAlive は、止められなかった claude がまだ終わっていないか。exited は Unstopped.Exited (nil なら止められなかった
// claude はいない)。
func UnstoppedAlive(exited <-chan struct{}) bool {
	if exited == nil {
		return false
	}
	select {
	case <-exited:
		return false
	default:
		return true
	}
}

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

// Start は job の worker を別の goroutine で進め、起動・止められないこと・終わりを events に送る。終わりは必ず 1 回送る
// (止められなければ、claude が終わったときに送る)。
func (r Runner) Start(job Job, events chan<- Event) *Run {
	run := &Run{stop: make(chan struct{})}
	ref := job.Item.Ref()
	go func() {
		result := r.attempt(job, run, notices{
			started: func(pid int, workspace string) {
				events <- Started{Target: ref, PID: pid, Workspace: workspace}
			},
			unstopped: func(stopError string, exited <-chan struct{}) {
				events <- Unstopped{Target: ref, StopError: stopError, Exited: exited}
			},
		})
		events <- Ended{Target: ref, Result: result}
	}()
	return run
}

// notices は worker が終わりの前に loop へ送る知らせ。
type notices struct {
	started   func(pid int, workspace string)
	unstopped func(stopError string, exited <-chan struct{})
}

func (r Runner) attempt(job Job, run *Run, n notices) Result {
	ref := job.Item.Ref()
	// stdout (stream-json) の worker log は hooks を撃つ前に開いて lock を取る。claude はこの fd を stdout として受け継ぐので、
	// loop が止まった後も claude (と fd を受け継いだ子孫) が生きている間は lock が外れない。次の loop は Orphaned でそれを
	// 確かめる (system.md §7 の orphan)。取れなければ、別の process がまだその workspace を使いうるので hooks も撃たない
	stream, err := openStream(LogFile(r.StateDir, ref))
	if err != nil {
		return Result{Failure: fmt.Sprintf("worker log を開けない: %v", err)}
	}
	defer stream.Close()
	if err := syscall.Flock(int(stream.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return Result{Failure: "worker log の lock を別の process が持っている (前の claude か、その fd を受け継いだ process が生きている)"}
		}
		return Result{Failure: fmt.Sprintf("worker log の lock を取れない: %v", err)}
	}
	path, err := r.Workspaces.Prepare(ref)
	if err != nil {
		return Result{Failure: err.Error()}
	}
	result := r.launch(job, run, path, stream, n)
	if err := r.Workspaces.AfterRun(ref); err != nil {
		result.AfterRunError = err.Error()
	}
	return result
}

// launch は描画して claude を起動し、終わるか止められるまで待つ。stream は lock を取った worker log。
func (r Runner) launch(job Job, run *Run, workspacePath string, stream *os.File, n notices) Result {
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
	stderr, err := openAppend(workerFile(r.StateDir, job.Item.Ref()) + ".stderr.log")
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
	n.started(cmd.Process.Pid, workspacePath)
	watch := newStreamWatch(stream, before.Size(), time.Now(), workspacePath,
		limits{stall: r.Definition.StallTimeout, run: r.Definition.RunTimeout})
	result := r.wait(cmd, run, watch, n.unstopped)
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

// wait は起動した claude が終わるか、止められるか、stall か上限時間で止めるまで待つ。止めようとして SIGKILL を送れず
// claude が終わっていなければ、unstopped で知らせてから claude が終わるまで待つ。
func (r Runner) wait(cmd *exec.Cmd, run *Run, w *streamWatch, unstopped func(stopError string, exited <-chan struct{})) Result {
	group := proc.Watch(cmd)
	ticker := time.NewTicker(watchInterval)
	result, ended := watch(group.Done(), run, w, ticker.C)
	ticker.Stop()
	if ended {
		// 子が自分で終わった。group に残った孫は Watch が止めたので、止められなかった理由だけを拾う
		_, err := group.Stop(stopGrace)
		return r.exited(cmd, stopError(Result{}, err))
	}
	if run.stopped() {
		// stall や上限時間と停止要求が重なったら、loop が止めたことを優先する
		result = Result{Stopped: true}
	}
	exited, err := stopGroup(group, stopGrace)
	if exited {
		return r.exited(cmd, stopError(Result{}, err))
	}
	select {
	case <-group.Done():
		return r.exited(cmd, stopError(result, err))
	default:
		// SIGKILL を送れず claude はまだ終わっていない。Group.Done は claude が終われば閉じるので、知らせてから待ち続ける
		unstopped(stopError(Result{}, err).StopError, group.Done())
		<-group.Done()
		// 送れなかった理由は知らせが運んだので、終わりには claude が終わった後に孫を止められなかった理由だけを載せる
		_, err = group.Stop(stopGrace)
		return r.exited(cmd, stopError(result, err))
	}
}

// stopGroup は止める理由ができた claude の process group を止める。test が SIGKILL を送れない場合を差し込めるよう変数に置く
// (SIGKILL の失敗は proc の中にあり、worker の test からは起こせない)。
var stopGroup = func(g *proc.Group, grace time.Duration) (exited bool, err error) { return g.Stop(grace) }

// stopError は process group へ signal を送れなかった理由を result に足す。
func stopError(result Result, err error) Result {
	if err != nil {
		result.StopError = err.Error()
	}
	return result
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

// LogFile は作業対象の worker log (claude の stdout) の path (formats.md §1)。
func LogFile(stateDir string, ref target.Ref) string { return workerFile(stateDir, ref) + ".log" }

// workerFile は作業対象の worker log の置き場の、拡張子の前までの path (stdout と stderr で共有する)。
func workerFile(stateDir string, ref target.Ref) string {
	return filepath.Join(stateDir, "workers", ref.FileName())
}

// Orphaned は、作業対象の worker log の flock を、前の loop が起動した claude (orphan) がまだ持っているかを返す
// (system.md §7 の orphan)。claim している作業対象には使わない (loop 自身の worker の lock も持っているものと見える)。
func Orphaned(stateDir string, ref target.Ref) (bool, error) {
	return state.Locked(LogFile(stateDir, ref))
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
