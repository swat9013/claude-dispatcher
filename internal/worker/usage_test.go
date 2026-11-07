package worker

import (
	"os"
	"reflect"
	"slices"
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

// appendToStream は stream の file に、走っている claude が書くように content を足す。
func appendToStream(t *testing.T, stream *os.File, content string) {
	t.Helper()
	f, err := os.OpenFile(stream.Name(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

// watching は w を見る watch を別の goroutine で走らせ、test の終わりに止める。ticks へ送ると確かめ、渡す使用率は usage から受ける。
func watching(t *testing.T, w *streamWatch) (ticks chan<- time.Time, usage <-chan Event) {
	t.Helper()
	tick, handed := make(chan time.Time), make(chan Event)
	run := &Run{stop: make(chan struct{})}
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		watch(make(chan struct{}), run, w, tick, handed)
	}()
	t.Cleanup(func() {
		run.Stop()
		<-returned
	})
	return tick, handed
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
	case <-time.After(5 * time.Second):
		t.Fatal("使用率が渡されない")
		return 0
	}
}

func TestWatchHandsOverTheUsageOfEachNewLineOnceOldestFirst(t *testing.T) {
	started := time.Unix(0, 0)
	old := usageLine("0.1")
	stream := streamFile(t, old+usageLine("0.2")+usageLine("0.3"))
	ticks, usage := watching(t, newStreamWatch(stream, int64(len(old)), started, "/work", limits{}))

	ticks <- started.Add(time.Second)
	ticks <- started.Add(2 * time.Second)
	// 後から足した行が 3 番目に届くことで、先の 2 行を 2 回渡していないことを確かめる
	appendToStream(t, stream, usageLine("0.4"))
	ticks <- started.Add(3 * time.Second)

	got := []float64{nextWeeklyUsage(t, usage), nextWeeklyUsage(t, usage), nextWeeklyUsage(t, usage)}
	if want := []float64{0.2, 0.3, 0.4}; !slices.Equal(got, want) {
		t.Fatalf("使用率 = %v, want attempt の後の行を古い順に 1 回ずつ %v", got, want)
	}
}

func TestWatchHandsOverARateLimitEventOnlyOnceItsLineIsComplete(t *testing.T) {
	started := time.Unix(0, 0)
	line := usageLine("0.2")
	stream := streamFile(t, line[:len(line)/2])
	ticks, usage := watching(t, newStreamWatch(stream, 0, started, "/work", limits{}))

	ticks <- started.Add(time.Second)
	appendToStream(t, stream, line[len(line)/2:])
	ticks <- started.Add(2 * time.Second)

	if got := nextWeeklyUsage(t, usage); got != 0.2 {
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
	w := newStreamWatch(streamFile(t, usageLine("0.2")), 0, started, "/work", limits{run: time.Second})
	run := &Run{stop: make(chan struct{})}
	ticks := make(chan time.Time, 1)
	ticks <- started.Add(time.Minute)

	result, ended := watch(make(chan struct{}), run, w, ticks, make(chan Event))

	if ended || result.Failure == "" {
		t.Fatalf("watch = %+v, %v, want loop が受けなくても上限時間で止める", result, ended)
	}
}

func TestUsageNobodyTookIsHandedOverOnceSomebodyTakesIt(t *testing.T) {
	started := time.Unix(0, 0)
	w := newStreamWatch(streamFile(t, usageLine("0.2")), 0, started, "/work", limits{run: time.Second})
	ticks := make(chan time.Time, 1)
	ticks <- started.Add(time.Minute)
	watch(make(chan struct{}), &Run{stop: make(chan struct{})}, w, ticks, make(chan Event))

	_, usage := watching(t, w)

	if got := nextWeeklyUsage(t, usage); got != 0.2 {
		t.Fatalf("使用率 = %v, want 渡せなかった 0.2", got)
	}
}
