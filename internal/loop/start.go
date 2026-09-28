package loop

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/paths"
)

// interval の範囲 (formats.md §13)
const (
	MinInterval = time.Minute
	MaxInterval = 24 * time.Hour
)

// Interval は loop の周期と、撃たれたときの綴り (見出しに出す)。
type Interval struct {
	Duration time.Duration
	Text     string
}

// ParseInterval は Go の duration の綴りの interval を読み、範囲 (1m 以上 24h 以下) の外を拒む。
func ParseInterval(text string) (Interval, error) {
	d, err := time.ParseDuration(text)
	if err != nil {
		return Interval{}, fmt.Errorf("interval は Go の duration の綴り (90s / 5m / 1h30m): %q", text)
	}
	if d < MinInterval || d > MaxInterval {
		return Interval{}, fmt.Errorf("interval は %s 以上 %s 以下: %q", MinInterval, MaxInterval, text)
	}
	return Interval{Duration: d, Text: text}, nil
}

// 起動時の検査のうち、lock を取るときの exit code (formats.md §13)
const (
	ExitFailed = 1
	ExitLocked = 3
)

// LockError は loop の lock を取れなかった理由。Exit は formats.md §13 の exit code。
type LockError struct {
	Exit int
	Msg  string
}

func (e *LockError) Error() string { return e.Msg }

// AcquireLock は state dir に loop の生存期間の lock を取る。返した file を閉じると lock が外れる — loop が終わるまで開いておく。
// state dir が無ければ lock の置き場も無いので、それも lock を取れない理由として返す。
func AcquireLock(project paths.Project) (*os.File, *LockError) {
	if info, err := os.Stat(project.StateDir); err != nil || !info.IsDir() {
		return nil, &LockError{ExitFailed, fmt.Sprintf("state dir が無い: %s (`claude-dispatcher setup %s` が作る)", project.StateDir, project.Name)}
	}
	lock, err := os.OpenFile(project.LoopLockFile(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, &LockError{ExitFailed, fmt.Sprintf("loop の lock file を開けない (%s): %v", project.LoopLockFile(), err)}
	}
	// flock は process が消えれば外れるので、落ちた loop の lock が残って次の起動を塞ぐことはない
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &LockError{ExitLocked, fmt.Sprintf("%s の loop がもう走っている (%s)", project.Name, project.LoopLockFile())}
		}
		return nil, &LockError{ExitFailed, fmt.Sprintf("loop の lock を取れない (%s): %v", project.LoopLockFile(), err)}
	}
	return lock, nil
}
