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

// ErrAlreadyRunning は同じ project の loop が走っていて lock を取れなかったことを表す。
var ErrAlreadyRunning = errors.New("同じ project の loop がもう走っている")

// AcquireLock は state dir に loop の生存期間の lock を取る (state dir の実在は呼び出し側が先に確かめる)。返した file を
// 閉じると lock が外れる — loop が終わるまで開いておく。走っている loop が持っていれば ErrAlreadyRunning を包んで返す。
func AcquireLock(project paths.Project) (*os.File, error) {
	lock, err := os.OpenFile(project.LoopLockFile(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("loop の lock file を開けない (%s): %w", project.LoopLockFile(), err)
	}
	// flock は process が消えれば外れるので、落ちた loop の lock が残って次の起動を塞ぐことはない
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%s: %w (%s)", project.Name, ErrAlreadyRunning, project.LoopLockFile())
		}
		return nil, fmt.Errorf("loop の lock を取れない (%s): %w", project.LoopLockFile(), err)
	}
	return lock, nil
}
