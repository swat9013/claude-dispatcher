package worker

import (
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestCheckReportsTheUsageOfEachNewLineOnce(t *testing.T) {
	started := time.Unix(0, 0)
	old := usageLine("0.1")
	w := newStreamWatch(streamFile(t, old+usageLine("0.2")+usageLine("0.3")), int64(len(old)), started, "/work", limits{})

	w.check(started.Add(time.Second))
	w.check(started.Add(2 * time.Second))

	if len(w.unsent) != 2 || w.unsent[0].SevenDay.Utilization != 0.2 || w.unsent[1].SevenDay.Utilization != 0.3 {
		t.Fatalf("usage = %+v, want attempt の後の 2 行を古い順に 1 回ずつ", w.unsent)
	}
}

func TestCheckDoesNotReportAHalfWrittenRateLimitEvent(t *testing.T) {
	started := time.Unix(0, 0)
	w := newStreamWatch(streamFile(t, strings.TrimSuffix(usageLine("0.2"), "\n")), 0, started, "/work", limits{})

	w.check(started.Add(time.Second))

	if len(w.unsent) != 0 {
		t.Fatalf("usage = %+v, want 書きかけの行は読まない", w.unsent)
	}
}

func TestARateLimitEventDoesNotBecomeTheActivity(t *testing.T) {
	started := time.Unix(0, 0)
	w := newStreamWatch(streamFile(t, usageLine("0.2")), 0, started, "/work", limits{})

	if got := w.check(started.Add(time.Second)); got.changed {
		t.Fatalf("活動 = %q, want rate_limit_event は活動にしない", got.activity)
	}
}

func TestWatchHandsOverTheUsageOfTheLinesItReads(t *testing.T) {
	started := time.Unix(0, 0)
	w := newStreamWatch(streamFile(t, usageLine("0.2")), 0, started, "/work", limits{})
	run := &Run{stop: make(chan struct{})}
	ticks := make(chan time.Time, 1)
	ticks <- started.Add(time.Second)
	usage := make(chan Event)
	returned := make(chan struct{})

	go func() {
		defer close(returned)
		watch(make(chan struct{}), run, w, ticks, usage)
	}()
	got := <-usage
	run.Stop()
	<-returned

	if u, ok := got.(UsageObserved); !ok || u.SevenDay.Utilization != 0.2 {
		t.Fatalf("渡した event = %+v, want 使用率 0.2", got)
	}
	if len(w.unsent) != 0 {
		t.Fatalf("未送 = %+v, want 渡した分は残らない", w.unsent)
	}
}

func TestWatchStopsAWorkerEvenIfNobodyTakesTheUsage(t *testing.T) {
	started := time.Unix(0, 0)
	w := newStreamWatch(streamFile(t, usageLine("0.2")), 0, started, "/work", limits{run: time.Second})
	run := &Run{stop: make(chan struct{})}
	ticks := make(chan time.Time, 1)
	ticks <- started.Add(time.Minute)

	result, ended := watch(make(chan struct{}), run, w, ticks, make(chan Event))

	if ended || result.Failure == "" {
		t.Fatalf("watch = %+v, %v, want loop が受けなくても上限時間で止める", result, ended)
	}
	if len(w.unsent) != 1 {
		t.Fatalf("未送 = %+v, want 渡せなかった使用率を残す", w.unsent)
	}
}
