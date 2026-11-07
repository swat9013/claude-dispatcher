package loop

import (
	"math"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/worker"
)

// admission control (system.md §7、formats.md §6): worker の stream が届けた usage limit の使用率が閾値以上なら、候補の起動と
// 再起動を止める。走っている worker は止めない。観測は loop の memory だけに持つ。

// UsageThresholds は usage limit の window ごとの閾値 (百分率)。
type UsageThresholds struct {
	FiveHour, Weekly int
}

// usageWindow は window 1 つの閾値と観測。
type usageWindow struct {
	// token は log.jsonl の window の値、label は人が読む行の window の呼び名
	token, label string
	threshold    int
	// latest は最新の観測。まだ無ければ nil
	latest *worker.UsageWindow
}

// current は now の時点で効いている観測。リセット時刻を過ぎた観測は、今の window の使用率でないので nil。
func (w usageWindow) current(now time.Time) *worker.UsageWindow {
	if w.latest == nil || !w.latest.ResetsAt.After(now) {
		return nil
	}
	return w.latest
}

// atThreshold は now の時点で使用率が閾値以上か。百分率へ掛けると 0.57 が 57 に届かないので、閾値の側を割って比べる。
func (w usageWindow) atThreshold(now time.Time) bool {
	c := w.current(now)
	return c != nil && c.Utilization >= float64(w.threshold)/100
}

// maxPercentInput は percent が受ける使用率の上限。stream の値が int に収まらない桁でも負の百分率を出さない
const maxPercentInput = 1e6

// percent は使用率を整数の百分率にする。
func percent(utilization float64) int {
	return int(math.Round(min(utilization, maxPercentInput) * 100))
}

// statusWindow は状態 file の window 1 つ。
func (w usageWindow) statusWindow(now time.Time) status.UsageWindow {
	s := status.UsageWindow{Threshold: w.threshold}
	if c := w.current(now); c != nil {
		utilization, resetsAt := percent(c.Utilization), c.ResetsAt
		s.Utilization, s.ResetsAt = &utilization, &resetsAt
	}
	return s
}

// admission は usage limit の観測と、admission control の判定。
type admission struct {
	fiveHour, sevenDay usageWindow
	// unreadable は、直近の rate_limit_event がどの window も読めなかったか
	unreadable bool
	// closed と reopenAt は直近の判定の結果。状態 file はこれを出すので、閉じているかが log.jsonl の行と食い違わない
	closed   bool
	reopenAt *time.Time
}

func newAdmission(t UsageThresholds) admission {
	return admission{
		fiveHour: usageWindow{token: "five_hour", label: "5 時間", threshold: t.FiveHour},
		sevenDay: usageWindow{token: "seven_day", label: "週", threshold: t.Weekly},
	}
}

// observe は rate_limit_event 1 行の観測を受ける。event に無い window は前の値のまま残す。
func (a *admission) observe(u worker.UsageObserved) {
	a.unreadable = u.Unreadable()
	if u.FiveHour != nil {
		a.fiveHour.latest = u.FiveHour
	}
	if u.SevenDay != nil {
		a.sevenDay.latest = u.SevenDay
	}
}

// blocking は now の時点で閾値以上の window のうち、リセットが遅い方。両方が解けるまで閉じているので、これが再開の時刻を決める。
// 無ければ nil。
func (a *admission) blocking(now time.Time) *usageWindow {
	var later *usageWindow
	for _, w := range []*usageWindow{&a.fiveHour, &a.sevenDay} {
		if w.atThreshold(now) && (later == nil || w.latest.ResetsAt.After(later.latest.ResetsAt)) {
			later = w
		}
	}
	return later
}

// snapshot は状態 file の usage。
func (a *admission) snapshot(now time.Time) *status.Usage {
	return &status.Usage{
		FiveHour: a.fiveHour.statusWindow(now), SevenDay: a.sevenDay.statusWindow(now),
		Closed: a.closed, ReopenAt: a.reopenAt, Unreadable: a.unreadable,
	}
}

// judgeAdmission は admission control を判定し、結果を記憶して、閉じているかを返す。閉じたか解けたときだけ、そのことを 1 行残す。
func (l *loop) judgeAdmission() (closed bool) {
	now := l.o.Now()
	a := &l.admission
	blocking := a.blocking(now)
	closed = a.unreadable || blocking != nil
	switch {
	case closed && !a.closed:
		l.logAdmissionClose(now)
	case !closed && a.closed:
		l.rec.event("admission_open", map[string]any{}, LineNote, "起動を再開する", "")
	}
	a.closed, a.reopenAt = closed, nil
	if blocking != nil && !a.unreadable {
		a.reopenAt = &blocking.latest.ResetsAt
	}
	return closed
}

// logAdmissionClose は閉じた理由の行を書く。読めない event はリセットを待っても解けないので、使用率より先に出す。
func (l *loop) logAdmissionClose(now time.Time) {
	if l.admission.unreadable {
		l.rec.event("admission_close", map[string]any{"reason": reasonUnreadable}, LineError, "起動を止める", ": %s", reasonUnreadable.screen())
		return
	}
	w := l.admission.blocking(now)
	utilization := percent(w.latest.Utilization)
	l.rec.event("admission_close", map[string]any{
		"reason": reasonUsage, "window": w.token, "utilization": utilization, "threshold": w.threshold,
		"resets_at": w.latest.ResetsAt.UTC().Format(time.RFC3339),
	}, LineNote, "起動を止める", ": %sの使用率 %d%% が閾値 %d%% 以上 (%s)", w.label, utilization, w.threshold, status.ReopenText(w.latest.ResetsAt, now.Location()))
}
