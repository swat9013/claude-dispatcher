package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// 単発起動は worker の終わりを待つ間、生存報告を間隔ごとに置き直す (formats.md §11・§12)。
func TestKeepReportingRenewsHeartbeatEveryInterval(t *testing.T) {
	clock := make(chan time.Time)
	beats := make(chan time.Duration)
	var mu sync.Mutex
	var waits []time.Duration
	after := func(d time.Duration) <-chan time.Time {
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, d)
		return clock
	}
	heartbeat := func(d time.Duration) error {
		beats <- d
		return nil
	}
	stop := keepReporting(heartbeat, time.Minute, after, &strings.Builder{})
	for i := range 2 {
		clock <- time.Time{}
		if d := <-beats; d != time.Minute {
			t.Fatalf("%d 回目の置き直しに書く間隔 %v, want 1m", i+1, d)
		}
	}
	stop()
	mu.Lock()
	defer mu.Unlock()
	for i, d := range waits {
		if d != time.Minute {
			t.Fatalf("%d 回目の待ち %v, want 1m", i+1, d)
		}
	}
}

// 単発起動は落ちた置き直しだけを stderr に 1 行出す。
func TestKeepReportingWritesOnlyFailedHeartbeatsToStderr(t *testing.T) {
	clock := make(chan time.Time)
	var mu sync.Mutex
	var stderr strings.Builder
	results := []error{nil, errors.New("負けた")}
	done := make(chan struct{}, len(results))
	heartbeat := func(time.Duration) error {
		err := results[0]
		results = results[1:]
		done <- struct{}{}
		return err
	}
	stop := keepReporting(heartbeat, time.Minute, func(time.Duration) <-chan time.Time { return clock }, lockedWriter{&mu, &stderr})
	for range 2 {
		clock <- time.Time{}
		<-done
	}
	stop()
	mu.Lock()
	defer mu.Unlock()
	if got := stderr.String(); got != "生存報告を置けない: 負けた\n" {
		t.Fatalf("stderr %q, want 落ちた 2 回目の 1 行だけ", got)
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
