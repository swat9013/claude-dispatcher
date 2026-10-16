package proc

import (
	"fmt"
	"syscall"
	"unsafe"
)

// pPID は waitid の idtype の P_PID。
const pPID = 1

// waitExit は子 pid が終わるまで待つ。子を回収 (reap) しないので、戻った時点でも pid と pgid は子 (zombie) が持ったまま。
func waitExit(pid int) error {
	// siginfo_t は 128 byte。中身は読まない
	var siginfo [16]uint64
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, pPID, uintptr(pid), uintptr(unsafe.Pointer(&siginfo[0])), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		switch errno {
		case 0:
			return nil
		case syscall.EINTR:
			continue
		default:
			return fmt.Errorf("子 %d の終了を回収せずに待てない: %w", pid, errno)
		}
	}
}
