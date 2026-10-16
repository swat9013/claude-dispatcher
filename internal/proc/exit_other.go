//go:build !linux && !darwin

package proc

import (
	"errors"
	"runtime"
)

// waitExit は配布対象 (linux・darwin) の外では使えない。子の終了後の group の後始末は行わない。
func waitExit(int) error {
	return errors.New(runtime.GOOS + " では子を回収せずに終了を待てない")
}
