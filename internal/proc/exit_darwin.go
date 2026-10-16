package proc

import (
	"errors"
	"fmt"
	"syscall"
)

// waitExit は子 pid が終わるまで待つ。子を回収 (reap) しないので、戻った時点でも pid と pgid は子 (zombie) が持ったまま。
// darwin の waitid は Go から呼べないので、kqueue の EVFILT_PROC / NOTE_EXIT で終了を知る。
func waitExit(pid int) error {
	kq, err := syscall.Kqueue()
	if err != nil {
		return fmt.Errorf("子 %d の終了を待つ kqueue を作れない: %w", pid, err)
	}
	defer func() { _ = syscall.Close(kq) }()
	var change syscall.Kevent_t
	syscall.SetKevent(&change, pid, syscall.EVFILT_PROC, syscall.EV_ADD|syscall.EV_ONESHOT)
	change.Fflags = syscall.NOTE_EXIT
	events := make([]syscall.Kevent_t, 1)
	for {
		n, err := syscall.Kevent(kq, []syscall.Kevent_t{change}, events, nil)
		switch {
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.ESRCH):
			// 登録の前に終わっていた。回収はまだなので zombie として残っている
			return nil
		case err != nil:
			return fmt.Errorf("子 %d の終了を待てない: %w", pid, err)
		case n == 0:
			continue
		}
		if ev := events[0]; ev.Flags&syscall.EV_ERROR != 0 {
			if errno := syscall.Errno(ev.Data); errno != syscall.ESRCH {
				return fmt.Errorf("子 %d の終了を待てない: %w", pid, errno)
			}
		}
		return nil
	}
}
