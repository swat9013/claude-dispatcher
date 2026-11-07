package loop

import (
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/worker"
)

func TestStatusFileFollowsTheLatestJudgementPastTheReset(t *testing.T) {
	// 判定は起動を試みるときだけなので、リセット時刻を過ぎても次の判定までは log.jsonl と同じく閉じたままと出す
	reset := time.Unix(1000, 0)
	a := newAdmission(UsageThresholds{FiveHour: 100, Weekly: 80})
	a.observe(worker.UsageObserved{SevenDay: &worker.UsageWindow{Utilization: 0.9, ResetsAt: reset}})
	a.closed, a.reopenAt = true, &reset

	got := a.snapshot(reset.Add(time.Minute))

	if !got.Closed || got.ReopenAt == nil || !got.ReopenAt.Equal(reset) {
		t.Fatalf("usage = %+v, want 直近の判定のまま閉じている", got)
	}
}
