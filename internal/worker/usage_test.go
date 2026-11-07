package worker

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
	"github.com/swat9013/claude-dispatcher/internal/workspace"
)

// worker の stream の rate_limit_event から usage limit の使用率を読む (formats.md §6 の admission control)。

func TestRateLimitEventWithBothWindowsIsReadAsUsage(t *testing.T) {
	line := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","unifiedWindows":{` +
		`"five_hour":{"utilization":0.15,"resetsAt":1791868800},"seven_day":{"utilization":0.33,"resetsAt":1792188000}}}}`

	got, ok := parseUsage([]byte(line))

	want := UsageObserved{
		FiveHour: &UsageWindow{Utilization: 0.15, ResetsAt: time.Unix(1791868800, 0).UTC()},
		SevenDay: &UsageWindow{Utilization: 0.33, ResetsAt: time.Unix(1792188000, 0).UTC()},
	}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("parseUsage = %+v, %v, want %+v", got, ok, want)
	}
}

func TestRateLimitEventWithOneWindowIsReadable(t *testing.T) {
	line := `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","unifiedWindows":{` +
		`"seven_day":{"utilization":1,"resetsAt":1794002400,"status":"rejected"}}}}`

	got, ok := parseUsage([]byte(line))

	if !ok || got.Unreadable() || got.FiveHour != nil || got.SevenDay == nil || got.SevenDay.Utilization != 1 {
		t.Fatalf("parseUsage = %+v, %v, want seven_day だけの読める event", got, ok)
	}
}

func TestWindowWhoseValuesAreNotNumbersIsTreatedAsMissing(t *testing.T) {
	line := `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{` +
		`"five_hour":{"utilization":"15%","resetsAt":1791868800},"seven_day":{"utilization":0.33,"resetsAt":1792188000}}}}`

	got, ok := parseUsage([]byte(line))

	if !ok || got.FiveHour != nil || got.SevenDay == nil {
		t.Fatalf("parseUsage = %+v, %v, want 数でない five_hour を無いものとして seven_day を読む", got, ok)
	}
}

func TestRateLimitEventWithNoReadableWindowIsUnreadable(t *testing.T) {
	for _, line := range []string{
		`{"type":"rate_limit_event"}`,
		`{"type":"rate_limit_event","rate_limit_info":"limited"}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning"}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":[]}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"five_hour":{"utilization":0.1}}}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"resetsAt":"soon","utilization":0.1}}}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":0.1,"resetsAt":1792188000000}}}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":0.1,"resetsAt":-1}}}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":-0.1,"resetsAt":1792188000}}}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":1e999,"resetsAt":1792188000}}}}`,
	} {
		got, ok := parseUsage([]byte(line))
		if !ok || !got.Unreadable() {
			t.Errorf("parseUsage(%s) = %+v, %v, want 読めない event", line, got, ok)
		}
	}
}

func TestRateLimitEventThatIsNotValidJSONIsUnreadable(t *testing.T) {
	for _, line := range []string{
		`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":0.1,}}}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"pad":"` + strings.Repeat("x", lineHeadLimit),
	} {
		got, ok := parseUsage([]byte(line))
		if !ok || !got.Unreadable() {
			t.Errorf("parseUsage(%.80s) = %+v, %v, want 読めない event", line, got, ok)
		}
	}
}

func TestFieldOutsideTheWindowsDoesNotHideThem(t *testing.T) {
	line := `{"type":"rate_limit_event","rate_limit_info":{"overageResetsAt":1e999,"unifiedWindows":{"seven_day":{"utilization":0.1,"resetsAt":1792188000}}}}`

	got, ok := parseUsage([]byte(line))

	if !ok || got.SevenDay == nil {
		t.Fatalf("parseUsage = %+v, %v, want window の外の項目が崩れていても読む", got, ok)
	}
}

func TestUtilizationOverTheLimitIsRead(t *testing.T) {
	line := `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":1.2,"resetsAt":1792188000}}}}`

	got, ok := parseUsage([]byte(line))

	if !ok || got.SevenDay == nil || got.SevenDay.Utilization != 1.2 {
		t.Fatalf("parseUsage = %+v, %v, want 1 を超える使用率も読む", got, ok)
	}
}

func TestLinesOtherThanARateLimitEventAreNotUsage(t *testing.T) {
	for _, line := range []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"rate_limit_event"}]}}`,
		`{"type":42}`,
		`{"type":"rate_limit_ev`,
		`plain text`,
		``,
	} {
		if got, ok := parseUsage([]byte(line)); ok {
			t.Errorf("parseUsage(%q) = %+v, want 使用率の行でない", line, got)
		}
	}
}

// usageLine は週の使用率だけを持つ rate_limit_event の行。
func usageLine(utilization string) string {
	return `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":` + utilization + `,"resetsAt":1792188000}}}}` + "\n"
}

// usageWait は test が非同期の応答を待つ上限。応答が無ければ、go test の timeout を待たずに落とす。
const usageWait = 5 * time.Second

// runningWatch は別の goroutine で走る watch。
type runningWatch struct {
	t        *testing.T
	ticks    chan time.Time
	usage    chan Event
	returned chan struct{}
	// result と ended は watch が返した値。returned が閉じてから読む
	result Result
	ended  bool
}

// watching は w を見る watch を別の goroutine で走らせ、test の終わりに止める。
func watching(t *testing.T, w *streamWatch) *runningWatch {
	t.Helper()
	r := &runningWatch{t: t, ticks: make(chan time.Time), usage: make(chan Event), returned: make(chan struct{})}
	run := &Run{stop: make(chan struct{})}
	go func() {
		defer close(r.returned)
		r.result, r.ended = watch(make(chan struct{}), run, w, r.ticks, r.usage)
	}()
	t.Cleanup(func() {
		run.Stop()
		select {
		case <-r.returned:
		case <-time.After(usageWait):
			t.Error("watch が止まらない")
		}
	})
	return r
}

// check は watch に now の時点で確かめさせる。watch は 1 つの goroutine なので、返った時点で前の check の確認は終わっている。
func (r *runningWatch) check(now time.Time) {
	r.t.Helper()
	select {
	case r.ticks <- now:
	case <-r.returned:
		r.t.Fatalf("watch が先に終わった: %+v", r.result)
	case <-time.After(usageWait):
		r.t.Fatal("watch が確認を受けない")
	}
}

// nextWeeklyUsage は usage から次の event を受け、週の使用率を返す。週の使用率を持たない event なら落とす。
func nextWeeklyUsage(t *testing.T, usage <-chan Event) float64 {
	t.Helper()
	select {
	case got := <-usage:
		u, ok := got.(UsageObserved)
		if !ok || u.SevenDay == nil {
			t.Fatalf("渡した event = %+v, want 週の使用率", got)
		}
		return u.SevenDay.Utilization
	case <-time.After(usageWait):
		t.Fatal("使用率が渡されない")
		return 0
	}
}

func TestWatchHandsOverTheUsageOfEachNewLineOnceOldestFirst(t *testing.T) {
	started := time.Unix(0, 0)
	w := watchFrom(t, usageLine("0.1"), started, limits{})
	write(t, w, usageLine("0.2")+usageLine("0.3"))
	r := watching(t, w.streamWatch)

	r.check(started.Add(time.Second))
	r.check(started.Add(2 * time.Second))
	got := []float64{nextWeeklyUsage(t, r.usage), nextWeeklyUsage(t, r.usage)}
	// 渡し終えた後に足した行が次に届くことで、渡し終えた行を残していないことを確かめる
	write(t, w, usageLine("0.4"))
	r.check(started.Add(3 * time.Second))
	got = append(got, nextWeeklyUsage(t, r.usage))

	if want := []float64{0.2, 0.3, 0.4}; !slices.Equal(got, want) {
		t.Fatalf("使用率 = %v, want attempt の後の行を古い順に 1 回ずつ %v", got, want)
	}
}

func TestWatchHandsOverARateLimitEventOnlyOnceItsLineIsComplete(t *testing.T) {
	started := time.Unix(0, 0)
	line := usageLine("0.2")
	w := watchFrom(t, "", started, limits{})
	write(t, w, line[:len(line)/2])
	r := watching(t, w.streamWatch)

	r.check(started.Add(time.Second))
	// 次の check が通れば、書きかけの行を読んだ確認は終わっている
	r.check(started.Add(2 * time.Second))
	write(t, w, line[len(line)/2:])
	r.check(started.Add(3 * time.Second))

	if got := nextWeeklyUsage(t, r.usage); got != 0.2 {
		t.Fatalf("使用率 = %v, want 書きかけの行を読まず、完結した行の 0.2", got)
	}
}

func TestARateLimitEventDoesNotBecomeTheActivity(t *testing.T) {
	started := time.Unix(0, 0)
	w := newStreamWatch(streamFile(t, usageLine("0.2")), 0, started, "/work", limits{})

	if got := w.check(started.Add(time.Second)); got.changed {
		t.Fatalf("活動 = %q, want rate_limit_event は活動にしない", got.activity)
	}
}

func TestWatchStopsAWorkerEvenIfNobodyTakesTheUsage(t *testing.T) {
	started := time.Unix(0, 0)
	w := watchFrom(t, "", started, limits{run: time.Second})
	write(t, w, usageLine("0.2"))
	r := watching(t, w.streamWatch)

	// 1 回目で使用率を読み、誰も受けないまま 2 回目で上限時間を超える
	r.check(started.Add(time.Millisecond))
	r.check(started.Add(time.Minute))

	select {
	case <-r.returned:
	case <-time.After(usageWait):
		t.Fatal("watch が止まらない")
	}
	if r.ended || r.result.Failure == "" {
		t.Fatalf("watch = %+v, %v, want loop が受けなくても上限時間で止める", r.result, r.ended)
	}
}

func TestUsageStillUnsentWhenAWorkerIsStoppedIsHandedOverAtItsEnd(t *testing.T) {
	dir := t.TempDir()
	stub := "#!/bin/sh\nprintf '%s' '" + usageLine("0.2") + `{"type":"system","subtype":"init"}` + "\n'\nexec /bin/sleep 60\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	r := Runner{
		StateDir:   filepath.Join(dir, "state"),
		Workspaces: workspace.Manager{Root: filepath.Join(dir, "workspaces"), HookLock: &sync.Mutex{}, Env: []string{"PATH=" + dir}},
		Definition: workflow.Definition{Claude: workflow.Claude{Command: "claude"}},
	}
	job := Job{Item: target.Issue{Number: 1}, Trigger: trigger.Trigger{Action: "work"}}
	run := &Run{stop: make(chan struct{})}
	usage := make(chan Event)
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		r.attempt(job, run, notices{started: func(int, string) {}, usage: usage})
	}()
	t.Cleanup(func() {
		run.Stop()
		for {
			select {
			case <-usage:
			case <-ended:
				return
			case <-time.After(usageWait):
				t.Error("attempt が終わらない")
				return
			}
		}
	})
	// 活動が更新されたなら、同じ確認が使用率の行も読んでいる。誰も受けていないので、使用率は未送のまま
	for deadline := time.Now().Add(usageWait); run.Activity().At.IsZero(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("watch が claude の行を読まない")
		}
	}

	run.Stop()

	if got := nextWeeklyUsage(t, usage); got != 0.2 {
		t.Fatalf("使用率 = %v, want 止めた worker の 0.2", got)
	}
}
