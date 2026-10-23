package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/coord"
	"github.com/swat9013/claude-dispatcher/internal/coord/coordtest"
)

// beatingRefs は、生存報告の置き直しの結果を results の順に返し、置き直すたびに beats へ知らせる coordination board。
type beatingRefs struct {
	*coordtest.Memory
	beats   chan time.Duration
	results []error
}

func (b *beatingRefs) Heartbeat(interval time.Duration) error {
	err := b.Memory.Heartbeat(interval)
	if len(b.results) > 0 {
		err, b.results = b.results[0], b.results[1:]
	}
	b.beats <- interval
	return err
}

// 単発起動は worker の終わりを待つ間、生存報告を間隔ごとに置き直す (formats.md §11・§12)。
func TestKeepReportingRenewsHeartbeatEveryInterval(t *testing.T) {
	clock := make(chan time.Time)
	refs := &beatingRefs{Memory: coordtest.NewMemory("me-00000001"), beats: make(chan time.Duration)}
	var mu sync.Mutex
	var waits []time.Duration
	after := func(d time.Duration) <-chan time.Time {
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, d)
		return clock
	}
	stop := keepReporting(coord.NewBoard(refs, time.Minute), after, &strings.Builder{})
	for i := range 2 {
		clock <- time.Time{}
		if d := <-refs.beats; d != time.Minute {
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
	refs := &beatingRefs{Memory: coordtest.NewMemory("me-00000001"), beats: make(chan time.Duration), results: []error{nil, errors.New("負けた")}}
	var mu sync.Mutex
	var stderr strings.Builder
	stop := keepReporting(coord.NewBoard(refs, time.Minute), func(time.Duration) <-chan time.Time { return clock }, lockedWriter{&mu, &stderr})
	for range 2 {
		clock <- time.Time{}
		<-refs.beats
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
