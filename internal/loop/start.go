package loop

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/config"
	"github.com/swat9013/claude-dispatcher/internal/paths"
)

// interval の範囲 (formats.md §13)
const (
	MinInterval = time.Minute
	MaxInterval = 24 * time.Hour
)

// 起動時の検査の exit code (formats.md §13)
const (
	ExitUsage  = 2
	ExitFailed = 1
	ExitLocked = 3
)

// ParseInterval は Go の duration の綴りの interval を読み、範囲 (1m 以上 24h 以下) の外を拒む。
func ParseInterval(text string) (time.Duration, error) {
	d, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("interval は Go の duration の綴り (90s / 5m / 1h30m): %q", text)
	}
	if d < MinInterval || d > MaxInterval {
		return 0, fmt.Errorf("interval は %s 以上 %s 以下: %q", MinInterval, MaxInterval, text)
	}
	return d, nil
}

// StartError は起動時の検査の失敗。Exit は formats.md §13 の exit code。
type StartError struct {
	Exit int
	Msg  string
}

func (e *StartError) Error() string { return e.Msg }

// Acquire は起動時の検査 (config と state dir の実在) を通し、loop の生存期間の lock を取る。
// 返した file を閉じると lock が外れる — loop が終わるまで開いておく。config の中身は検査しない (tick ごとに読み直す)。
func Acquire(project paths.Project) (*os.File, error) {
	if err := config.RequireFile(project.ConfigFile()); err != nil {
		return nil, &StartError{ExitUsage, err.Error()}
	}
	if info, err := os.Stat(project.StateDir); err != nil || !info.IsDir() {
		return nil, &StartError{ExitFailed, fmt.Sprintf("state dir が無い: %s (`claude-dispatcher setup %s` が作る)", project.StateDir, project.Name)}
	}
	lock, err := os.OpenFile(project.LoopLockFile(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, &StartError{ExitFailed, fmt.Sprintf("loop の lock file を開けない (%s): %v", project.LoopLockFile(), err)}
	}
	// flock は process が消えれば外れるので、落ちた loop の lock が残って次の起動を塞ぐことはない
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &StartError{ExitLocked, fmt.Sprintf("%s の loop がもう走っている (%s)", project.Name, project.LoopLockFile())}
		}
		return nil, &StartError{ExitFailed, fmt.Sprintf("loop の lock を取れない (%s): %v", project.LoopLockFile(), err)}
	}
	return lock, nil
}
