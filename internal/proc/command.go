package proc

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Command は 1 回撃って stdout を受け取る外部 CLI の呼び出し口。Path は解決済みの絶対 path。
type Command struct {
	Path    string
	Env     []string
	Dir     string
	Timeout time.Duration
}

// Error は外部 CLI の失敗。起動できなかった / timeout なら Exit は -1。
type Error struct {
	Name   string
	Args   []string
	Exit   int
	Stderr string
}

func (e *Error) Error() string {
	head := strings.Join(e.Args[:min(2, len(e.Args))], " ")
	return fmt.Sprintf("%s %s failed (exit %d): %s", e.Name, head, e.Exit, strings.TrimSpace(e.Stderr))
}

// Output は args で撃ち、stdout を返す。exit 0 以外は *Error。timeout を超えたら process group ごと止める。
func (c Command) Output(args ...string) (string, error) { return c.OutputWithInput("", args...) }

// OutputWithInput は input を stdin に流して撃つ。それ以外は Output と同じ。
func (c Command) OutputWithInput(input string, args ...string) (string, error) {
	cmd := exec.Command(c.Path, args...)
	cmd.Env, cmd.Dir = c.Env, c.Dir
	cmd.Stdin = strings.NewReader(input)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// process group の外へ逃げた孫が pipe を握ったままでも Wait が戻るよう、pipe の読み切りを待つ上限を置く
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	name := filepath.Base(c.Path)
	if err := cmd.Start(); err != nil {
		return "", &Error{Name: name, Args: args, Exit: -1, Stderr: err.Error()}
	}
	timedOut, err := Wait(cmd, c.Timeout)
	if timedOut {
		return "", &Error{Name: name, Args: args, Exit: -1, Stderr: fmt.Sprintf("%s を超えても終わらない", c.Timeout)}
	}
	if err != nil {
		return "", &Error{Name: name, Args: args, Exit: cmd.ProcessState.ExitCode(), Stderr: stderr.String()}
	}
	return stdout.String(), nil
}
