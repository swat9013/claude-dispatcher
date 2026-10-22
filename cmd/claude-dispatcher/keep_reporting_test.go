package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// 単発起動は worker の終わりを待つ間、生存報告を間隔ごとに置き直し、落ちた置き直しを stderr に出す (formats.md §11・§12)。
func TestKeepReportingRenewsHeartbeatEveryInterval(t *testing.T) {
	clock := make(chan time.Time)
	beats := make(chan time.Duration)
	var mu sync.Mutex
	var stderr strings.Builder
	fails := []error{nil, errors.New("負けた")}
	heartbeat := func(d time.Duration) error {
		beats <- d
		mu.Lock()
		defer mu.Unlock()
		err := fails[0]
		fails = fails[1:]
		return err
	}
	stop := keepReporting(heartbeat, time.Minute, func(time.Duration) <-chan time.Time { return clock }, lockedWriter{&mu, &stderr})
	for i := range 2 {
		clock <- time.Time{}
		if d := <-beats; d != time.Minute {
			t.Fatalf("%d 回目の置き直しの間隔 %v, want 1m", i+1, d)
		}
	}
	stop()
	mu.Lock()
	defer mu.Unlock()
	if got := stderr.String(); got != "生存報告を置けない: 負けた\n" {
		t.Fatalf("stderr %q, want 2 回目の失敗だけ", got)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}
