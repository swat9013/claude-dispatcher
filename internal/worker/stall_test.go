package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// stall の判定 (formats.md §6 の progress)。stall の時計は progress の行で戻り、活動の表示とは別に読む。

const heartbeat = `{"type":"tool_progress","tool_name":"Bash","elapsed_time_seconds":30,"heartbeat":true}` + "\n"

const taskProgress = `{"type":"system","subtype":"task_progress"}` + "\n"

// stallClock は空の stream を started から見始めた streamWatch と、stall の上限が stall の Runner を作る。
func stallClock(t *testing.T, started time.Time, stall time.Duration) (*streamWatch, Runner) {
	t.Helper()
	return newStreamWatch(streamFile(t, "").Name(), 0, started, "/work"), Runner{Definition: workflow.Definition{StallTimeout: stall}}
}

// write は stream に line を追記する (worker が書くのと同じく、loop とは別に開いて書く)。
func write(t *testing.T, w *streamWatch, line string) {
	t.Helper()
	f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

// appendAt は stream に line を追記し、now の時点で w に確かめさせる。
func appendAt(t *testing.T, w *streamWatch, line string, now time.Time) {
	t.Helper()
	write(t, w, line)
	w.check(now)
}

// replaceWith は stream の path を、content を書いた別の file に置き換える。
func replaceWith(t *testing.T, w *streamWatch, content string) {
	t.Helper()
	next := filepath.Join(t.TempDir(), "next.log")
	if err := os.WriteFile(next, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, w.path); err != nil {
		t.Fatal(err)
	}
}

// unreadableUntilRestored は stream の path を、大きさは確かめられるが中身を読み出せない dir に置き換え、元へ戻す関数を返す。
func unreadableUntilRestored(t *testing.T, w *streamWatch) (restore func()) {
	t.Helper()
	aside := w.path + ".aside"
	if err := os.Rename(w.path, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(w.path, 0o755); err != nil {
		t.Fatal(err)
	}
	// dir の大きさは file system で違う。前に読めた大きさと同じだと、確かめても伸びていないとして読み出さない
	if info, err := os.Stat(w.path); err != nil || info.Size() == w.lines.size {
		t.Fatalf("dir の大きさ %v が前に読めた大きさ %d と同じか、確かめられない (%v)。stream の中身の大きさを変える", info, w.lines.size, err)
	}
	return func() {
		t.Helper()
		if err := os.Remove(w.path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(aside, w.path); err != nil {
			t.Fatal(err)
		}
	}
}

// removed は stream の path を消し、元へ戻す関数を返す (大きさも確かめられない)。
func removed(t *testing.T, w *streamWatch) (restore func()) {
	t.Helper()
	aside := w.path + ".aside"
	if err := os.Rename(w.path, aside); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := os.Rename(aside, w.path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkerWritingOnlyHeartbeatsStallsAfterTheStallTimeout(t *testing.T) {
	started := time.Now()
	w, r := stallClock(t, started, time.Minute)
	for s := 30; s <= 90; s += 30 {
		appendAt(t, w, heartbeat, started.Add(time.Duration(s)*time.Second))
	}

	reason := r.overdue(90*time.Second, w.sinceProgress(started.Add(90*time.Second)), w.readError)

	if !strings.Contains(reason, "stall") {
		t.Fatalf("heartbeat だけが 90s 続いて stall の上限 1m を過ぎたのに止めない: %q", reason)
	}
}

func TestProgressThatIsNotActivityResetsTheStallClock(t *testing.T) {
	started := time.Now()
	w, _ := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前"), started.Add(10*time.Second))

	appendAt(t, w, taskProgress, started.Add(50*time.Second))

	if got := w.sinceProgress(started.Add(70 * time.Second)); got != 20*time.Second {
		t.Fatalf("progress が途絶えた時間 = %s, want task_progress で時計が戻って 20s", got)
	}
}

func TestProgressDoesNotChangeTheActivity(t *testing.T) {
	started := time.Now()
	w, _ := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前"), started.Add(10*time.Second))
	write(t, w, taskProgress)

	activity, changed := w.check(started.Add(50 * time.Second))

	if changed {
		t.Fatalf("活動 = %q; want 活動として数えない task_progress は活動を変えない", activity)
	}
}

func TestProgressFollowedByMoreThan64KiBOfHeartbeatsInOneCheckResetsTheStallClock(t *testing.T) {
	started := time.Now()
	w, _ := stallClock(t, started, time.Minute)
	read := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/work/big.go"}}]}}` + "\n"

	// 1 回の確認のあいだに、progress の後へ 64 KiB を超える heartbeat が続く
	appendAt(t, w, read+strings.Repeat(heartbeat, progressLineLimit/len(heartbeat)+1), started.Add(50*time.Second))

	if got := w.sinceProgress(started.Add(70 * time.Second)); got != 20*time.Second {
		t.Fatalf("progress が途絶えた時間 = %s, want heartbeat の前の tool_use で時計が戻って 20s", got)
	}
}

func TestProgressWithTheSameSummaryResetsTheStallClock(t *testing.T) {
	started := time.Now()
	w, _ := stallClock(t, started, time.Minute)
	appendAt(t, w, text("同じ"), started.Add(10*time.Second))
	appendAt(t, w, heartbeat, started.Add(40*time.Second))

	appendAt(t, w, text("同じ"), started.Add(50*time.Second))

	if got := w.sinceProgress(started.Add(70 * time.Second)); got != 20*time.Second {
		t.Fatalf("progress が途絶えた時間 = %s, want 直前と同じ要約の行で時計が戻って 20s", got)
	}
}

func TestShrinkingTheStreamDoesNotResetTheStallClock(t *testing.T) {
	started := time.Now()
	w, _ := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前"), started.Add(10*time.Second))
	if err := os.Truncate(w.path, 0); err != nil {
		t.Fatal(err)
	}

	w.check(started.Add(30 * time.Second))

	if got := w.sinceProgress(started.Add(30 * time.Second)); got != 20*time.Second {
		t.Fatalf("progress が途絶えた時間 = %s, want 縮んだこと自体では戻らず 20s", got)
	}
}

func TestProgressWrittenAfterTheStreamShrankResetsTheStallClock(t *testing.T) {
	started := time.Now()
	w, _ := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前の長い行"), started.Add(10*time.Second))
	if err := os.Truncate(w.path, 0); err != nil {
		t.Fatal(err)
	}
	w.check(started.Add(20 * time.Second))

	appendAt(t, w, text("後"), started.Add(30*time.Second))

	if got := w.sinceProgress(started.Add(30 * time.Second)); got != 0 {
		t.Fatalf("progress が途絶えた時間 = %s, want 縮んだ後に書かれた行で時計が戻って 0s", got)
	}
}

func TestProgressInASmallerReplacementResetsTheStallClock(t *testing.T) {
	started := time.Now()
	w, _ := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前の長い行"), started.Add(10*time.Second))
	replaceWith(t, w, text("後"))

	w.check(started.Add(30 * time.Second))

	if got := w.sinceProgress(started.Add(30 * time.Second)); got != 0 {
		t.Fatalf("progress が途絶えた時間 = %s, want 小さい file に置き換わったら頭から見て 0s", got)
	}
}

func TestActivityAfterTheStreamShrankComesFromItsHead(t *testing.T) {
	started := time.Now()
	w, _ := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前の長い行"), started.Add(10*time.Second))
	if err := os.Truncate(w.path, 0); err != nil {
		t.Fatal(err)
	}
	w.check(started.Add(20 * time.Second))
	write(t, w, text("後"))

	activity, changed := w.check(started.Add(30 * time.Second))

	if !changed || activity != "後" {
		t.Fatalf("活動 = %q, %v; want 縮んだ後の行を頭から見て 後", activity, changed)
	}
}

func TestStallClockDoesNotResetWhileTheStreamCannotBeRead(t *testing.T) {
	started := time.Now()
	w, _ := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前"), started.Add(10*time.Second))
	write(t, w, text("後"))
	unreadableUntilRestored(t, w)

	w.check(started.Add(30 * time.Second))

	if got := w.sinceProgress(started.Add(30 * time.Second)); got != 20*time.Second {
		t.Fatalf("progress が途絶えた時間 = %s, want 中身を読み出せないあいだは戻らず 20s", got)
	}
}

func TestStallClockDoesNotResetWhileTheStreamCannotBeStatted(t *testing.T) {
	started := time.Now()
	w, _ := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前"), started.Add(10*time.Second))
	removed(t, w)

	w.check(started.Add(30 * time.Second))

	if got := w.sinceProgress(started.Add(30 * time.Second)); got != 20*time.Second {
		t.Fatalf("progress が途絶えた時間 = %s, want 大きさを確かめられないあいだは戻らず 20s", got)
	}
}

func TestActivityTellsWhyTheStreamCannotBeRead(t *testing.T) {
	started := time.Now()
	w, _ := stallClock(t, started, time.Minute)
	write(t, w, text("後"))
	unreadableUntilRestored(t, w)

	activity, changed := w.check(started.Add(10 * time.Second))

	if !changed || !strings.HasPrefix(activity, "stream を読めない") {
		t.Fatalf("活動 = %q, %v; want stream を読めない", activity, changed)
	}
}

func TestProgressWrittenWhileTheStreamCouldNotBeReadResetsTheStallClockOnceItCanBeRead(t *testing.T) {
	started := time.Now()
	w, _ := stallClock(t, started, time.Minute)
	write(t, w, text("後"))
	restore := removed(t, w)
	w.check(started.Add(10 * time.Second))
	restore()

	// 読めなかった確認から、file は伸びていない
	w.check(started.Add(20 * time.Second))

	if got := w.sinceProgress(started.Add(20 * time.Second)); got != 0 {
		t.Fatalf("progress が途絶えた時間 = %s, want 読み直して、読めないあいだに書かれた行で時計が戻って 0s", got)
	}
}

func TestStreamThatCouldNotBeReadIsReadAgainEvenWithoutGrowing(t *testing.T) {
	started := time.Now()
	w, r := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前の行")+text("前の行"), started.Add(10*time.Second))
	// 中身を読み出せなくしてから、読めた時と同じ file に戻す (戻した後の確認では、file は伸びていない)
	restore := unreadableUntilRestored(t, w)
	w.check(started.Add(20 * time.Second))
	restore()

	w.check(started.Add(30 * time.Second))

	if reason := r.overdue(time.Hour, w.sinceProgress(started.Add(80*time.Second)), w.readError); strings.Contains(reason, "読めない") {
		t.Fatalf("理由 = %q, want 伸びていなくても読み直して読めたので、stall の理由に読めない理由を添えない", reason)
	}
}

// watchAt は watch を別の goroutine で走らせ、ticks の時刻で確かめさせる。watch が途中で終われば、その時点で返す。
func watchAt(t *testing.T, r Runner, w *streamWatch, ticks ...time.Time) (result Result, ended bool, returned bool) {
	t.Helper()
	tick := make(chan time.Time)
	type outcome struct {
		result Result
		ended  bool
	}
	out := make(chan outcome, 1)
	run := &Run{stop: make(chan struct{})}
	go func() {
		result, ended := r.watch(make(chan error), run, w, tick)
		out <- outcome{result, ended}
	}()
	for _, now := range ticks {
		select {
		case tick <- now:
		case o := <-out:
			return o.result, o.ended, true
		}
	}
	// 最後の確認を済ませたかを、もう 1 回送れるかで確かめる (送れれば最後の確認では終わっていない)
	select {
	case o := <-out:
		return o.result, o.ended, true
	case tick <- ticks[len(ticks)-1]:
		run.Stop()
		<-out
		return Result{}, false, false
	}
}

func TestWatchStopsAWorkerThatWritesOnlyHeartbeats(t *testing.T) {
	started := time.Now()
	w, r := stallClock(t, started, time.Minute)
	write(t, w, heartbeat)

	result, ended, returned := watchAt(t, r, w, started.Add(30*time.Second), started.Add(61*time.Second))

	if !returned || ended || !strings.Contains(result.Failure, "stall") {
		t.Fatalf("watch = %+v, ended %v, 返った %v; want heartbeat だけが続くので stall で止める", result, ended, returned)
	}
}

func TestWatchWaitsForTheStallTimeoutWhenTheStreamCannotBeStatted(t *testing.T) {
	started := time.Now()
	w, r := stallClock(t, started, time.Minute)
	removed(t, w)

	_, _, returned := watchAt(t, r, w, started.Add(250*time.Millisecond), started.Add(59*time.Second))

	if returned {
		t.Fatal("大きさを確かめられないだけで、stall の上限を待たずに止めた")
	}
}

func TestWatchStallsWithTheReasonWhenTheStreamCannotBeStattedPastTheTimeout(t *testing.T) {
	started := time.Now()
	w, r := stallClock(t, started, time.Minute)
	removed(t, w)

	result, _, returned := watchAt(t, r, w, started.Add(250*time.Millisecond), started.Add(61*time.Second))

	if !returned || !strings.Contains(result.Failure, "stall") || !strings.Contains(result.Failure, "stream を読めない") {
		t.Fatalf("watch = %+v, 返った %v; want stall で止め、読めない理由を添える", result, returned)
	}
}

func TestStallReasonCarriesWhyTheStreamCannotBeRead(t *testing.T) {
	r := Runner{Definition: workflow.Definition{StallTimeout: time.Minute, RunTimeout: time.Hour}}

	reason := r.overdue(2*time.Minute, 2*time.Minute, "stream を読めない: 消えた")

	if !strings.Contains(reason, "stall") || !strings.Contains(reason, "stream を読めない: 消えた") {
		t.Fatalf("理由 = %q, want stall に読めない理由を添える", reason)
	}
}

func TestRunTimeoutReasonDoesNotCarryWhyTheStreamCannotBeRead(t *testing.T) {
	r := Runner{Definition: workflow.Definition{StallTimeout: time.Hour, RunTimeout: time.Minute}}

	reason := r.overdue(2*time.Minute, 2*time.Minute, "stream を読めない: 消えた")

	if strings.Contains(reason, "読めない") {
		t.Fatalf("理由 = %q, want 上限時間の理由には読めない理由を添えない", reason)
	}
}
