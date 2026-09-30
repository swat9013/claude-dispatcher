package status_test

import (
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/status"
)

// 状態の描き方 (formats.md §7.2)。

var base = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func at(d time.Duration) *time.Time { t := base.Add(d); return &t }

func TestRunningWorkerShowsItsElapsedTimeAndActivity(t *testing.T) {
	s := &status.Snapshot{Scope: "s", Workers: []status.Worker{{
		Target: "issue#42", Trigger: "implement", Attempt: 2, Phase: status.Running, StartedAt: at(0),
		Activity: &status.Activity{At: base, Summary: "tool Bash"},
	}}}

	got := status.Render(s, "s", true, base.Add(5*time.Minute+10*time.Second), time.UTC)

	if want := "issue#42\timplement\tattempt 2\t走っている\t5m10s\ttool Bash"; !strings.Contains(got, want) {
		t.Fatalf("描画:\n%s\nwant 行 %q", got, want)
	}
}

func TestWaitingRetryShowsWhenItRestarts(t *testing.T) {
	s := &status.Snapshot{Scope: "s", Workers: []status.Worker{{Target: "issue#42", Trigger: "implement", Attempt: 1, Phase: status.WaitingRetry, RetryAt: at(90 * time.Second)}}}

	got := status.Render(s, "s", true, base, time.UTC)

	if want := "issue#42\timplement\tattempt 1\t再起動待ち\t00:01:30 に再起動\t"; !strings.Contains(got, want) {
		t.Fatalf("描画:\n%s\nwant 行 %q", got, want)
	}
}

func TestHeadingOfAStoppingLoopShowsTheLastTickError(t *testing.T) {
	s := &status.Snapshot{Scope: "s", Stopping: true, LastTick: &status.Tick{At: base, Result: "error", Error: "workflow 定義の誤り"}}

	got := status.Render(s, "s", true, base, time.UTC)

	if want := "loop 停止待ち · scope s · 直近の tick 00:00:00 error: workflow 定義の誤り\n"; got != want {
		t.Fatalf("描画 = %q, want %q", got, want)
	}
}

func TestHeadingOfAStoppedLoopHidesItsWorkers(t *testing.T) {
	s := &status.Snapshot{Scope: "s", NextTickAt: at(time.Minute), Workers: []status.Worker{{Target: "issue#42", Phase: status.Running}}}

	got := status.Render(s, "s", false, base, time.UTC)

	if got != "loop なし · scope s\n" {
		t.Fatalf("描画 = %q", got)
	}
}
