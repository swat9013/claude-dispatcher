package loop_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/places"
	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/worker"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// admission control (system.md §7、formats.md §6)。worker の stream が届けた使用率で、候補の起動と再起動を止める。

// usagePeriod は admission control の test の周期
const usagePeriod = time.Hour

// weekly は週の window だけの観測。reset は rig の起動時刻からのリセット時刻。
func weekly(utilization float64, reset time.Duration) worker.UsageObserved {
	return worker.UsageObserved{SevenDay: &worker.UsageWindow{Utilization: utilization, ResetsAt: rigStart.Add(reset)}}
}

// fiveHour は 5 時間の window だけの観測。
func fiveHour(utilization float64, reset time.Duration) worker.UsageObserved {
	return worker.UsageObserved{FiveHour: &worker.UsageWindow{Utilization: utilization, ResetsAt: rigStart.Add(reset)}}
}

// usageLoop は起動した worker が使用率を届ける loop。worker は正常に終わり、起動済みの作業対象は trigger から外れる。
type usageLoop struct {
	// issues は当たっている issue の番号 (宣言順に起動する)
	issues []int
	// thresholds は閾値。ゼロ値なら 100
	thresholds loop.UsageThresholds
	// usage は attempt 1 の worker が順に届ける観測 (issue の番号ごと)
	usage map[int][]worker.UsageObserved
	// failing は失敗で終わる issue の番号 (再起動を待つ)
	failing int
	// periods は回す周期の数
	periods int
	// maxConcurrent は並列上限。0 なら 1
	maxConcurrent int
}

// usageRun は回した loop の観測。
type usageRun struct {
	*rig
	// launchedAt は起動した worker の attempt と時刻 (起動の順)
	launchedAt []time.Time
}

func (u usageLoop) run(t *testing.T) *usageRun {
	t.Helper()
	if u.thresholds == (loop.UsageThresholds{}) {
		u.thresholds = loop.UsageThresholds{FiveHour: 100, Weekly: 100}
	}
	def := workflow.Definition{Tracker: githubTracker, Interval: usagePeriod, MaxConcurrent: max(u.maxConcurrent, 1), MaxAttempts: 5, MaxRetryBackoff: 5 * time.Minute, Triggers: []trigger.Trigger{readyTrigger}}
	r := &usageRun{rig: newRig()}
	periods := 0
	r.timer = func(kind timerKind, _ time.Duration) timing {
		if kind != periodTimer {
			return timing{fate: fires}
		}
		if periods++; periods > u.periods {
			return timing{fate: stopsTheLoop}
		}
		return timing{fate: fires}
	}
	r.script = func(job worker.Job) workerScript {
		r.launchedAt = append(r.launchedAt, r.now)
		s := exits(0)
		number := job.Item.Ref().Number
		if number == u.failing {
			s = exits(1)
		}
		if job.Attempt == 1 {
			s.usage = u.usage[number]
		}
		return s
	}
	finished := func(number int) bool {
		return number != u.failing && slices.ContainsFunc(r.jobs, func(j worker.Job) bool { return j.Item.Ref().Number == number })
	}
	item := func(number int) target.Item {
		if finished(number) {
			return target.Issue{Number: number}
		}
		return readyIssue(number)
	}
	r.run(t, loop.Options{
		Definition:      def,
		UsageThresholds: u.thresholds,
		Store: func(workflow.Definition) places.Store {
			return memoryStore{
				observe: func() ([]target.Item, error) {
					var open []target.Item
					for _, number := range u.issues {
						open = append(open, item(number))
					}
					return open, nil
				},
				reread: func(ref target.Ref) (target.Item, error) { return item(ref.Number), nil },
			}.store()
		},
	})
	return r
}

// launched は起動した worker の issue の番号 (起動の順)。
func (r *usageRun) launched() []int {
	numbers := make([]int, len(r.jobs))
	for i, j := range r.jobs {
		numbers[i] = j.Item.Ref().Number
	}
	return numbers
}

// lastUsage は最後に書き出した状態 file の usage。
func (r *usageRun) lastUsage(t *testing.T) status.Usage {
	t.Helper()
	last := r.published[len(r.published)-1]
	if last.Usage == nil {
		t.Fatal("状態 file に usage が無い")
	}
	return *last.Usage
}

func TestUsageAtTheThresholdClosesAdmissionToTheNextCandidate(t *testing.T) {
	r := usageLoop{
		issues:     []int{1, 2},
		thresholds: loop.UsageThresholds{FiveHour: 100, Weekly: 80},
		usage:      map[int][]worker.UsageObserved{1: {weekly(0.8, 100*time.Hour)}},
		periods:    3,
	}.run(t)

	if got := r.launched(); !slices.Equal(got, []int{1}) {
		t.Fatalf("起動 = %v, want 閾値に達した後の issue#2 は起動しない", got)
	}
}

func TestUsageBelowTheThresholdLeavesAdmissionOpen(t *testing.T) {
	r := usageLoop{
		issues:     []int{1, 2},
		thresholds: loop.UsageThresholds{FiveHour: 100, Weekly: 80},
		usage:      map[int][]worker.UsageObserved{1: {weekly(0.79, 100*time.Hour)}},
		periods:    3,
	}.run(t)

	if got := r.launched(); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("起動 = %v, want 閾値の手前なら issue#2 も起動する", got)
	}
}

func TestUsageThatEqualsAThresholdWithoutAnExactBinaryValueClosesAdmission(t *testing.T) {
	// 0.57 × 100 は浮動小数で 57 に届かない
	r := usageLoop{
		issues:     []int{1, 2},
		thresholds: loop.UsageThresholds{FiveHour: 100, Weekly: 57},
		usage:      map[int][]worker.UsageObserved{1: {weekly(0.57, 100*time.Hour)}},
		periods:    3,
	}.run(t)

	if got := r.launched(); !slices.Equal(got, []int{1}) {
		t.Fatalf("起動 = %v, want 使用率 57%% は閾値 57%% に達している", got)
	}
}

func TestDefaultThresholdClosesAdmissionOnlyAtTheLimit(t *testing.T) {
	below := usageLoop{issues: []int{1, 2}, usage: map[int][]worker.UsageObserved{1: {weekly(0.99, 100*time.Hour)}}, periods: 3}.run(t)
	limit := usageLoop{issues: []int{1, 2}, usage: map[int][]worker.UsageObserved{1: {weekly(1, 100*time.Hour)}}, periods: 3}.run(t)

	if got := below.launched(); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("99%% の起動 = %v, want 既定の閾値 100%% の手前なら起動する", got)
	}
	if got := limit.launched(); !slices.Equal(got, []int{1}) {
		t.Fatalf("100%% の起動 = %v, want 上限に達した window では起動しない", got)
	}
}

func TestEitherWindowAtItsThresholdClosesAdmission(t *testing.T) {
	r := usageLoop{
		issues:     []int{1, 2},
		thresholds: loop.UsageThresholds{FiveHour: 50, Weekly: 100},
		usage:      map[int][]worker.UsageObserved{1: {fiveHour(0.5, 100*time.Hour)}},
		periods:    3,
	}.run(t)

	if got := r.launched(); !slices.Equal(got, []int{1}) {
		t.Fatalf("起動 = %v, want 5 時間の閾値でも止まる", got)
	}
}

func TestCandidateIsLaunchedOnTheFirstTickAfterTheResetWithoutAnOperator(t *testing.T) {
	r := usageLoop{
		issues:     []int{1, 2},
		thresholds: loop.UsageThresholds{FiveHour: 100, Weekly: 80},
		usage:      map[int][]worker.UsageObserved{1: {weekly(0.9, 150*time.Minute)}},
		periods:    4,
	}.run(t)

	if got := r.launched(); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("起動 = %v, want リセットの後に issue#2 を起動する", got)
	}
	if got := r.launchedAt[1]; !got.Equal(rigStart.Add(3 * usagePeriod)) {
		t.Fatalf("issue#2 の起動の時刻 = %s, want リセットの後の最初の tick", got)
	}
}

func TestClosingAndReopeningEachLeaveOneLine(t *testing.T) {
	r := usageLoop{
		issues:     []int{1, 2},
		thresholds: loop.UsageThresholds{FiveHour: 100, Weekly: 80},
		usage:      map[int][]worker.UsageObserved{1: {weekly(0.81, 150*time.Minute), weekly(0.82, 150*time.Minute)}},
		periods:    4,
	}.run(t)

	closes, opens := r.logEvents(t, "admission_close"), r.logEvents(t, "admission_open")
	if len(closes) != 1 || len(opens) != 1 {
		t.Fatalf("admission_close = %v, admission_open = %v, want 1 行ずつ", closes, opens)
	}
	c := closes[0]
	wantReset := rigStart.Add(150 * time.Minute).UTC().Format(time.RFC3339)
	if c["reason"] != "usage" || c["window"] != "seven_day" || c["utilization"] != float64(81) || c["threshold"] != float64(80) || c["resets_at"] != wantReset {
		t.Fatalf("admission_close の行 = %v", c)
	}
	if out := r.stdout.String(); !strings.Contains(out, "起動を止める: 週の使用率 81% が閾値 80% 以上 (01/01 02:30 に再開)\n") ||
		!strings.Contains(out, " 起動を再開する\n") {
		t.Fatalf("人が読む行:\n%s", out)
	}
}

func TestUtilizationTooLargeForAnIntStaysPositiveInTheLine(t *testing.T) {
	r := usageLoop{issues: []int{1}, usage: map[int][]worker.UsageObserved{1: {weekly(1e300, 100*time.Hour)}}, periods: 1}.run(t)

	if c := r.logEvents(t, "admission_close"); len(c) != 1 || c[0]["utilization"].(float64) <= 0 {
		t.Fatalf("admission_close = %v, want 正の百分率", c)
	}
}

func TestFiveHourWindowIsNamedInTheLineThatCloses(t *testing.T) {
	r := usageLoop{
		issues:     []int{1},
		thresholds: loop.UsageThresholds{FiveHour: 50, Weekly: 100},
		usage:      map[int][]worker.UsageObserved{1: {fiveHour(0.5, 100*time.Hour)}},
		periods:    1,
	}.run(t)

	if c := r.logEvents(t, "admission_close"); len(c) != 1 || c[0]["window"] != "five_hour" {
		t.Fatalf("admission_close = %v, want five_hour", c)
	}
	if out := r.stdout.String(); !strings.Contains(out, "起動を止める: 5 時間の使用率 50% が閾値 50% 以上") {
		t.Fatalf("人が読む行:\n%s", out)
	}
}

func TestCandidatesAreStillCountedWhileClosed(t *testing.T) {
	r := usageLoop{
		issues:     []int{1, 2},
		thresholds: loop.UsageThresholds{FiveHour: 100, Weekly: 80},
		usage:      map[int][]worker.UsageObserved{1: {weekly(0.9, 100*time.Hour)}},
		periods:    2,
	}.run(t)

	ticks := r.logEvents(t, "tick")
	last := ticks[len(ticks)-1]
	if last["candidates"] != float64(1) || len(last["launched"].([]any)) != 0 {
		t.Fatalf("閉じている間の tick の行 = %v, want 候補 1・起動 0", last)
	}
}

func TestWaitingRetryDoesNotAdvanceItsAttemptWhileClosed(t *testing.T) {
	r := usageLoop{
		issues:     []int{1},
		failing:    1,
		thresholds: loop.UsageThresholds{FiveHour: 100, Weekly: 80},
		usage:      map[int][]worker.UsageObserved{1: {weekly(0.9, 150*time.Minute)}},
		periods:    3,
	}.run(t)

	if len(r.jobs) < 2 {
		t.Fatalf("起動 = %d 回, want リセットの後に再起動する", len(r.jobs))
	}
	if r.jobs[1].Attempt != 2 || !r.launchedAt[1].Equal(rigStart.Add(3*usagePeriod)) {
		t.Fatalf("2 本目 = attempt %d, 時刻 %s, want リセットの後の tick で attempt 2", r.jobs[1].Attempt, r.launchedAt[1])
	}
}

func TestLaterResetDecidesWhenAdmissionReopens(t *testing.T) {
	r := usageLoop{
		issues:     []int{1, 2},
		thresholds: loop.UsageThresholds{FiveHour: 80, Weekly: 80},
		usage:      map[int][]worker.UsageObserved{1: {fiveHour(0.9, 30*time.Minute), weekly(0.9, 150*time.Minute)}},
		periods:    4,
	}.run(t)

	if got := r.launchedAt[1]; !got.Equal(rigStart.Add(3 * usagePeriod)) {
		t.Fatalf("issue#2 の起動の時刻 = %s, want 遅い方のリセットの後", got)
	}
}

func TestReopenTimeIsTheLaterResetWhenBothWindowsAreAtTheirThresholds(t *testing.T) {
	for name, usage := range map[string][]worker.UsageObserved{
		"5 時間が先": {fiveHour(0.9, 100*time.Hour), weekly(0.9, 200*time.Hour)},
		"週が先":    {weekly(0.9, 200*time.Hour), fiveHour(0.9, 100*time.Hour)},
	} {
		t.Run(name, func(t *testing.T) {
			r := usageLoop{
				issues:     []int{1},
				thresholds: loop.UsageThresholds{FiveHour: 80, Weekly: 80},
				usage:      map[int][]worker.UsageObserved{1: usage},
				periods:    1,
			}.run(t)

			if got := r.lastUsage(t); got.ReopenAt == nil || !got.ReopenAt.Equal(rigStart.Add(200*time.Hour)) {
				t.Fatalf("usage = %+v, want 再開の時刻は遅い方のリセット時刻", got)
			}
		})
	}
}

func TestStatusFileCarriesTheObservationsAndTheReopenTimeWhileClosed(t *testing.T) {
	r := usageLoop{
		issues:     []int{1, 2},
		thresholds: loop.UsageThresholds{FiveHour: 70, Weekly: 80},
		usage:      map[int][]worker.UsageObserved{1: {fiveHour(0.41, 100*time.Hour), weekly(0.81, 200*time.Hour)}},
		periods:    1,
	}.run(t)

	got := r.lastUsage(t)
	if !got.Closed || got.Unreadable || got.ReopenAt == nil || !got.ReopenAt.Equal(rigStart.Add(200*time.Hour)) {
		t.Fatalf("usage = %+v, want 閉じていて再開の時刻は週のリセット時刻", got)
	}
	if got.FiveHour.Threshold != 70 || got.FiveHour.Utilization == nil || *got.FiveHour.Utilization != 41 {
		t.Fatalf("5 時間 = %+v", got.FiveHour)
	}
	if got.SevenDay.Threshold != 80 || got.SevenDay.Utilization == nil || *got.SevenDay.Utilization != 81 || !got.SevenDay.ResetsAt.Equal(rigStart.Add(200*time.Hour)) {
		t.Fatalf("週 = %+v", got.SevenDay)
	}
}

func TestWindowMissingFromAnEventKeepsItsPreviousObservation(t *testing.T) {
	r := usageLoop{
		issues:  []int{1},
		usage:   map[int][]worker.UsageObserved{1: {fiveHour(0.41, 100*time.Hour), weekly(0.2, 100*time.Hour)}},
		periods: 1,
	}.run(t)

	if got := r.lastUsage(t); got.FiveHour.Utilization == nil || *got.FiveHour.Utilization != 41 || got.SevenDay.Utilization == nil {
		t.Fatalf("usage = %+v, want 5 時間の値を残して週を足す", got)
	}
}

func TestWindowWithoutAnObservationIsNullAndDoesNotCloseAdmission(t *testing.T) {
	r := usageLoop{issues: []int{1, 2}, periods: 3}.run(t)

	got := r.lastUsage(t)
	if got.Closed || got.FiveHour.Utilization != nil || got.SevenDay.Utilization != nil || got.SevenDay.ResetsAt != nil {
		t.Fatalf("usage = %+v, want 未観測で閉じない", got)
	}
	if launched := r.launched(); !slices.Equal(launched, []int{1, 2}) {
		t.Fatalf("起動 = %v, want 未観測なら起動を止めない", launched)
	}
}

func TestObservationPastItsResetIsNoLongerShown(t *testing.T) {
	r := usageLoop{
		issues:     []int{1},
		thresholds: loop.UsageThresholds{FiveHour: 100, Weekly: 80},
		usage:      map[int][]worker.UsageObserved{1: {weekly(0.9, 30*time.Minute)}},
		periods:    2,
	}.run(t)

	if got := r.lastUsage(t); got.Closed || got.ReopenAt != nil || got.SevenDay.Utilization != nil {
		t.Fatalf("usage = %+v, want リセットの後は未観測に戻る", got)
	}
}

func TestEventWithNoReadableWindowClosesAdmissionUntilALoopRestart(t *testing.T) {
	r := usageLoop{
		issues:  []int{1, 2},
		usage:   map[int][]worker.UsageObserved{1: {{}}},
		periods: 3,
	}.run(t)

	if got := r.launched(); !slices.Equal(got, []int{1}) {
		t.Fatalf("起動 = %v, want 読めない event の後は起動しない", got)
	}
	closes := r.logEvents(t, "admission_close")
	if len(closes) != 1 || closes[0]["reason"] != "unreadable" || closes[0]["window"] != nil {
		t.Fatalf("admission_close = %v, want reason unreadable の 1 行", closes)
	}
	if got := r.lastUsage(t); !got.Closed || !got.Unreadable || got.ReopenAt != nil {
		t.Fatalf("usage = %+v, want 閉じていて再開の時刻は無い", got)
	}
	if out := r.stdout.String(); !strings.Contains(out, "起動を止める: rate_limit_event を読めない\n") {
		t.Fatalf("人が読む行:\n%s", out)
	}
}

func TestReadableEventReopensAdmissionClosedByAnUnreadableOne(t *testing.T) {
	// issue#1 の worker の終わりの判定で閉じた後、並んで走っていた issue#2 の worker が読める event を届ける
	r := usageLoop{
		issues:        []int{1, 2, 3},
		usage:         map[int][]worker.UsageObserved{1: {{}}, 2: {weekly(0.2, 100*time.Hour)}},
		periods:       3,
		maxConcurrent: 2,
	}.run(t)

	if got := r.launched(); !slices.Equal(got, []int{1, 2, 3}) {
		t.Fatalf("起動 = %v, want 読める event で解ける", got)
	}
	if opens := r.logEvents(t, "admission_open"); len(opens) != 1 {
		t.Fatalf("admission_open = %v, want 1 行", opens)
	}
	if got := r.lastUsage(t); got.Unreadable {
		t.Fatalf("usage = %+v, want 読めない印は消える", got)
	}
}
