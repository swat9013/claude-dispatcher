//go:build blackbox

package main

import (
	"fmt"
	"os"
	"time"
)

// slowAfterEnv は、black-box テストが遅い一覧の閾値を差し替える env (time.ParseDuration の書式)。build tag `blackbox` で
// build した binary だけが読むので、配布する binary の利用者には閾値を差し替える口が無い (#374・#384)。
const slowAfterEnv = "CLAUDE_DISPATCHER_TEST_SLOW_AFTER"

func init() {
	raw, ok := os.LookupEnv(slowAfterEnv)
	if !ok {
		return
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s を読めない: %v\n", slowAfterEnv, err)
		os.Exit(2)
	}
	slowAfter = func(time.Duration) <-chan time.Time { return time.After(d) }
}
