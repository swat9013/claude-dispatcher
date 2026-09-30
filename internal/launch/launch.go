// Package launch は Claude Code セッションの起動を 1 つの部品に閉じる (ADR 0005)。
//
// 今の実装は `claude -p` (ClaudePrint と、その worker の生死を見分ける ClaudePrintCensus) の 1 つだけ。
// `claude --bg` 等へ変えるときは Launcher と Census の実装を 1 つずつ足す。
package launch

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/proc"
)

// Launcher は orchestrator と worker を起動する seam。
type Launcher interface {
	// RunOrchestrator は orchestrator を起動して終了を待つ。timeout を超えるか stop が閉じたら process group ごと止め、
	// どちらで止めたかを返す。stop が nil なら停止要求は届かない。出力は logFile へ落とす。起動できなかったときだけ error を返す。
	// onStart は起動できた直後に、終了を待つ前に session id を渡して呼ぶ (走っている間の orchestrator を status に見せるため)。
	RunOrchestrator(prompt, logFile string, timeout time.Duration, stop <-chan struct{}, onStart func(sessionID string)) (OrchestratorRun, error)
	// SpawnWorker は worker を新しい process session として起動し、待たずに返す。出力は logFile へ落とす。
	// worker の終了は起動した process の中で回収する (長く生きる loop に zombie を溜めない)。
	SpawnWorker(prompt, logFile string) (WorkerLaunch, error)
}

type OrchestratorRun struct {
	ExitCode int
	Seconds  float64
	// End は自分で終わったか、上限時間か停止要求で止めたか
	End       proc.End
	SessionID string
}

type WorkerLaunch struct {
	PID       int
	SessionID string
}

// PermissionMode は orchestrator / worker の permission posture。permission 層を外す起動は採らない (ADR 0002 / 0005)
const PermissionMode = "auto"

// ClaudePrint は `claude -p … --permission-mode auto --session-id <UUID>` で起動する Launcher。
type ClaudePrint struct {
	Claude string   // claude の絶対 path
	Env    []string // 子プロセスの env
	Cwd    string   // 実装 repo の clone
}

func (c ClaudePrint) command(prompt, sessionID string, log *os.File) *exec.Cmd {
	cmd := exec.Command(c.Claude, "-p", prompt, "--permission-mode", PermissionMode, "--session-id", sessionID)
	cmd.Env = c.Env
	cmd.Dir = c.Cwd
	cmd.Stdout, cmd.Stderr = log, log
	// 新しい process session にする: orchestrator は timeout と停止要求のときに process group ごと止めるため、
	// worker は loop の端末の signal と process group に巻き込まれないため
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

func (c ClaudePrint) start(prompt, logFile string) (*exec.Cmd, string, error) {
	log, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, "", fmt.Errorf("起動 log を開けない (%s): %w", logFile, err)
	}
	defer log.Close() // 子が複製した fd を持つので、親の分は起動後に閉じてよい
	sessionID, err := newSessionID()
	if err != nil {
		return nil, "", err
	}
	cmd := c.command(prompt, sessionID, log)
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("claude を起動できない (%s): %w", c.Claude, err)
	}
	return cmd, sessionID, nil
}

func (c ClaudePrint) RunOrchestrator(prompt, logFile string, timeout time.Duration, stop <-chan struct{}, onStart func(sessionID string)) (OrchestratorRun, error) {
	started := time.Now()
	cmd, sessionID, err := c.start(prompt, logFile)
	if err != nil {
		return OrchestratorRun{}, err
	}
	onStart(sessionID)
	// 異常終了は exit code で log に残すので、Wait の error は見ない
	end, _ := proc.Wait(cmd, timeout, stop)
	return OrchestratorRun{
		ExitCode: cmd.ProcessState.ExitCode(), Seconds: time.Since(started).Seconds(), End: end, SessionID: sessionID,
	}, nil
}

func (c ClaudePrint) SpawnWorker(prompt, logFile string) (WorkerLaunch, error) {
	cmd, sessionID, err := c.start(prompt, logFile)
	if err != nil {
		return WorkerLaunch{}, err
	}
	// 待たずに返し、終了は裏で回収する。loop は長く生きるので、回収しないと終わった worker が zombie として残る。
	// worker の成否は issue の label と引き渡しコメントが持つので、Wait の error は見ない
	go func() { _ = cmd.Wait() }()
	return WorkerLaunch{PID: cmd.Process.Pid, SessionID: sessionID}, nil
}

// newSessionID は UUID v4 を返す (claude の --session-id に渡し、log から transcript へ辿る鍵にする)。
// 要るのは乱数 16 byte に version / variant の bit を立てる 1 関数だけなので、UUID の library への依存は足さない。
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("session id を作れない: " + err.Error())
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
