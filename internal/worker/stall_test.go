package worker

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// stall の判定 (formats.md §6 の worker の起動と活動)。stall の時計は活動として数える行でだけ戻る。

const heartbeat = `{"type":"tool_progress","tool_name":"Bash","elapsed_time_seconds":30,"heartbeat":true}` + "\n"

// stallClock は空の stream を started から見始めた streamWatch と、stall の上限が stall の Runner を作る。
func stallClock(t *testing.T, started time.Time, stall time.Duration) (*streamWatch, Runner) {
	t.Helper()
	w := &streamWatch{file: streamFile(t, ""), active: started, workspace: "/work"}
	return w, Runner{Definition: workflow.Definition{StallTimeout: stall}}
}

// appendAt は stream に line を追記し、now の時点で w に確かめさせる。
func appendAt(t *testing.T, w *streamWatch, line string, now time.Time) {
	t.Helper()
	f, err := os.OpenFile(w.file.Name(), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	observeAt(t, w, now)
}

// observeAt は now の時点で w に stream の file を確かめさせ、活動を返す。活動が変わらなければ ok が false。
func observeAt(t *testing.T, w *streamWatch, now time.Time) (summary string, ok bool) {
	t.Helper()
	grew, err := w.observe()
	if err != nil {
		t.Fatal(err)
	}
	if !grew {
		return "", false
	}
	return w.activity(now)
}

func TestWorkerWritingOnlyHeartbeatsStallsAfterTheStallTimeout(t *testing.T) {
	started := time.Now()
	w, r := stallClock(t, started, time.Minute)
	for s := 30; s <= 90; s += 30 {
		appendAt(t, w, heartbeat, started.Add(time.Duration(s)*time.Second))
	}

	reason := r.overdue(90*time.Second, w.inactiveFor(started.Add(90*time.Second)))

	if !strings.Contains(reason, "stall") {
		t.Fatalf("heartbeat だけが 90s 続いて stall の上限 1m を過ぎたのに止めない: %q", reason)
	}
}

func TestWatchStopsAWorkerThatWritesOnlyHeartbeats(t *testing.T) {
	stream := streamFile(t, "")
	writer, err := os.OpenFile(stream.Name(), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	// heartbeat を stall の上限より細かく書き続ける (file は伸び続ける)
	quit := make(chan struct{})
	defer close(quit)
	go func() {
		for {
			select {
			case <-quit:
				return
			case <-time.After(20 * time.Millisecond):
				_, _ = writer.WriteString(heartbeat)
			}
		}
	}()
	r := Runner{Definition: workflow.Definition{StallTimeout: 500 * time.Millisecond}}
	run := &Run{stop: make(chan struct{})}
	time.AfterFunc(10*time.Second, run.Stop)

	result, ended := r.watch(make(chan error), run, stream, 0, "/work")

	if ended || !strings.Contains(result.Failure, "stall") {
		t.Fatalf("watch = %+v, ended %v; want heartbeat だけが続くので stall で止める", result, ended)
	}
}

func TestLineCountedAsActivityResetsTheStallClockEvenWithTheSameSummary(t *testing.T) {
	started := time.Now()
	w, _ := stallClock(t, started, time.Minute)
	appendAt(t, w, text("同じ"), started.Add(10*time.Second))
	appendAt(t, w, heartbeat, started.Add(40*time.Second))

	appendAt(t, w, text("同じ"), started.Add(50*time.Second))

	if got := w.inactiveFor(started.Add(70 * time.Second)); got != 20*time.Second {
		t.Fatalf("活動が途絶えた時間 = %s, want 直前と同じ要約の行で時計が戻って 20s", got)
	}
}

func TestStallClockDoesNotResetWhileTheStreamCannotBeRead(t *testing.T) {
	started := time.Now()
	w, _ := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前"), started.Add(10*time.Second))
	// 名前を消した file は、開いたままの書き手では伸びるが、名前で開き直して読めない
	writer, err := os.OpenFile(w.file.Name(), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := os.Remove(w.file.Name()); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString(text("後")); err != nil {
		t.Fatal(err)
	}

	summary, ok := observeAt(t, w, started.Add(30*time.Second))

	if !ok || !strings.HasPrefix(summary, "stream を読めない") {
		t.Fatalf("活動 = %q, %v; want stream を読めない", summary, ok)
	}
	if got := w.inactiveFor(started.Add(30 * time.Second)); got != 20*time.Second {
		t.Fatalf("活動が途絶えた時間 = %s, want 読めないあいだは戻らず 20s", got)
	}
}
