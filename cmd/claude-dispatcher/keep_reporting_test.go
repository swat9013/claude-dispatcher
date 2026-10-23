package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/coord"
	"github.com/swat9013/claude-dispatcher/internal/coord/coordtest"
	"github.com/swat9013/claude-dispatcher/internal/target"
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

// 単発起動は、置いた着手宣言が他の loop の着手宣言に置き換わっていて消せなかったときも、消せなかった理由を stderr に出す。
func TestLeaveOnceWritesALostDeleteOfTheClaimToStderr(t *testing.T) {
	ref := target.Ref{Kind: target.KindIssue, Number: 42}
	mem := coordtest.NewMemory("me-00000001")
	board := coord.NewBoard(mem, time.Minute)
	if _, err := board.Observe(time.Now, nil); err != nil {
		t.Fatal(err)
	}
	if out := board.Claim(ref); out != (coord.ClaimPlaced{}) {
		t.Fatalf("Claim = %#v", out)
	}
	mem.Put(&ref, coord.Message{Kind: coord.KindClaim, Format: coord.Format, Loop: "other-00000002", Target: ref.String()})
	var stderr strings.Builder

	leaveOnce(board, &stderr)

	if want := "issue#42 の着手宣言を消せない: " + coord.ErrLost.Error() + "\n"; stderr.String() != want {
		t.Fatalf("stderr %q, want %q", stderr.String(), want)
	}
}
