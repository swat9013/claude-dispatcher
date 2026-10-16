package proc

import (
	"syscall"
	"testing"
)

// SetKillGroup は process group へ signal を送る関数を test の間だけ f に差し替える。Watch より前に呼ぶ。
func SetKillGroup(t *testing.T, f func(pgid int, sig syscall.Signal) error) {
	t.Helper()
	orig := killGroup.Swap(&f)
	t.Cleanup(func() { killGroup.Store(orig) })
}
