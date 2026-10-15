package worker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stall の判定 (formats.md §6 の progress)。stall の時計は progress の行で戻り、活動の表示とは別に読む。

const heartbeat = `{"type":"tool_progress","tool_name":"Bash","elapsed_time_seconds":30,"heartbeat":true}` + "\n"

const taskProgress = `{"type":"system","subtype":"task_progress"}` + "\n"

// errReadFails は fake の file handle が返す、読めない理由。
var errReadFails = errors.New("読み出せない")

// flakyHandle は claude に渡した worker log の fd を包み、Stat と読み出しを落とせる fake の file handle。
type flakyHandle struct {
	file *os.File
	// statFails が true なら Stat が落ちる
	statFails bool
	// readFails が true なら、offset readFrom から先の読み出しが落ちる
	readFails bool
	readFrom  int64
}

func (h *flakyHandle) Stat() (os.FileInfo, error) {
	if h.statFails {
		return nil, errReadFails
	}
	return h.file.Stat()
}

func (h *flakyHandle) ReadAt(p []byte, off int64) (int, error) {
	if !h.readFails {
		return h.file.ReadAt(p, off)
	}
	if off >= h.readFrom {
		return 0, errReadFails
	}
	if limit := h.readFrom - off; int64(len(p)) > limit {
		n, err := h.file.ReadAt(p[:limit], off)
		if err != nil {
			return n, err
		}
		return n, errReadFails
	}
	return h.file.ReadAt(p, off)
}

// watched は test が claude の代わりに fd へ追記する worker log と、その fd を読む streamWatch。
type watched struct {
	*streamWatch
	// path は worker log の path。fd とは別に、path の rename・置き換えを試すのに使う
	path   string
	handle *flakyHandle
}

// check は活動だけを返す (止める理由は stopReason で読む)。
func (w *watched) check(now time.Time) (activity string, changed bool) {
	return w.streamWatch.check(now).seen()
}

func (o observation) seen() (activity string, changed bool) { return o.activity, o.changed }

// stallClock は空の worker log を、stall の上限を stall にして started から見始める。
func stallClock(t *testing.T, started time.Time, stall time.Duration) *watched {
	t.Helper()
	return watchFrom(t, "", started, limits{stall: stall})
}

// watchFrom は content を書いた worker log を、content の後 (今の attempt が書く行) から started で見始める。
func watchFrom(t *testing.T, content string, started time.Time, l limits) *watched {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stream.log")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := openAppend(path, os.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	h := &flakyHandle{file: f}
	return &watched{streamWatch: newStreamWatch(h, int64(len(content)), started, "/work", l), path: path, handle: h}
}

// write は claude と同じく、worker log の fd へ line を追記する。
func write(t *testing.T, w *watched, line string) {
	t.Helper()
	if _, err := w.handle.file.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

// appendAt は worker log に line を追記し、now の時点で確かめさせる。
func appendAt(t *testing.T, w *watched, line string, now time.Time) {
	t.Helper()
	write(t, w, line)
	w.check(now)
}

// shrinkTo は worker log を fd で切り詰め、content を書き直す。
func shrinkTo(t *testing.T, w *watched, content string) {
	t.Helper()
	if err := w.handle.file.Truncate(0); err != nil {
		t.Fatal(err)
	}
	write(t, w, content)
}

// unreadableFrom は worker log の offset from から先を読み出せなくし、元へ戻す関数を返す。from より前の行は each へ渡った
// 後に、読み出しが落ちる。
func unreadableFrom(w *watched, from int64) (restore func()) {
	w.handle.readFails, w.handle.readFrom = true, from
	return func() { w.handle.readFails = false }
}

// unreadable は worker log の中身を読み出せなくし (大きさは確かめられる)、元へ戻す関数を返す。
func unreadable(w *watched) (restore func()) { return unreadableFrom(w, 0) }

// unstattable は worker log の大きさを確かめられなくし、元へ戻す関数を返す。
func unstattable(w *watched) (restore func()) {
	w.handle.statFails = true
	return func() { w.handle.statFails = false }
}

func size(t *testing.T, w *watched) int64 {
	t.Helper()
	info, err := w.handle.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func TestWorkerWritingOnlyHeartbeatsStallsAfterTheStallTimeout(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	for s := 30; s <= 90; s += 30 {
		appendAt(t, w, heartbeat, started.Add(time.Duration(s)*time.Second))
	}

	reason := w.stopReason(started.Add(90 * time.Second))

	if !strings.Contains(reason, "stall") {
		t.Fatalf("heartbeat だけが 90s 続いて stall の上限 1m を過ぎたのに止めない: %q", reason)
	}
}

func TestProgressThatIsNotActivityResetsTheStallClock(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前"), started.Add(10*time.Second))

	appendAt(t, w, taskProgress, started.Add(50*time.Second))

	if got := w.sinceProgress(started.Add(70 * time.Second)); got != 20*time.Second {
		t.Fatalf("progress が途絶えた時間 = %s, want task_progress で時計が戻って 20s", got)
	}
}

func TestProgressDoesNotChangeTheActivity(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前"), started.Add(10*time.Second))
	write(t, w, taskProgress)

	activity, changed := w.check(started.Add(50 * time.Second))

	if changed {
		t.Fatalf("活動 = %q; want 活動として数えない task_progress は活動を変えない", activity)
	}
}

func TestProgressPushedOutOfTheActivityTailStillResetsTheStallClock(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	read := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/work/big.go"}}]}}` + "\n"

	// 1 回の確認のあいだに、progress の後へ 64 KiB を超える heartbeat が続く
	appendAt(t, w, read+strings.Repeat(heartbeat, tailLimit/len(heartbeat)+1), started.Add(50*time.Second))

	if got := w.sinceProgress(started.Add(70 * time.Second)); got != 20*time.Second {
		t.Fatalf("progress が途絶えた時間 = %s, want 末尾の窓から押し出された tool_use で時計が戻って 20s", got)
	}
}

func TestProgressWithTheSameSummaryResetsTheStallClock(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	appendAt(t, w, text("同じ"), started.Add(10*time.Second))
	appendAt(t, w, heartbeat, started.Add(40*time.Second))

	appendAt(t, w, text("同じ"), started.Add(50*time.Second))

	if got := w.sinceProgress(started.Add(70 * time.Second)); got != 20*time.Second {
		t.Fatalf("progress が途絶えた時間 = %s, want 直前と同じ要約の行で時計が戻って 20s", got)
	}
}

func TestShrinkingTheStreamDoesNotResetTheStallClock(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前"), started.Add(10*time.Second))
	shrinkTo(t, w, "")

	w.check(started.Add(30 * time.Second))

	if got := w.sinceProgress(started.Add(30 * time.Second)); got != 20*time.Second {
		t.Fatalf("progress が途絶えた時間 = %s, want 縮んだこと自体では戻らず 20s", got)
	}
}

func TestProgressWrittenAfterTheStreamShrankResetsTheStallClock(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前の長い行"), started.Add(10*time.Second))
	shrinkTo(t, w, "")
	w.check(started.Add(20 * time.Second))

	appendAt(t, w, text("後"), started.Add(30*time.Second))

	if got := w.sinceProgress(started.Add(30 * time.Second)); got != 0 {
		t.Fatalf("progress が途絶えた時間 = %s, want 縮んだ後に書かれた行で時計が戻って 0s", got)
	}
}

func TestProgressInAStreamThatShrankBetweenChecksResetsTheStallClock(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前の長い行"), started.Add(10*time.Second))
	shrinkTo(t, w, text("後"))

	w.check(started.Add(30 * time.Second))

	if got := w.sinceProgress(started.Add(30 * time.Second)); got != 0 {
		t.Fatalf("progress が途絶えた時間 = %s, want 縮んだら頭から見て 0s", got)
	}
}

func TestActivityAfterTheStreamShrankComesFromItsHead(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前の長い行"), started.Add(10*time.Second))
	shrinkTo(t, w, "")
	w.check(started.Add(20 * time.Second))
	write(t, w, text("後"))

	activity, changed := w.check(started.Add(30 * time.Second))

	if !changed || activity != "後" {
		t.Fatalf("活動 = %q, %v; want 縮んだ後の行を頭から見て 後", activity, changed)
	}
}

func TestStreamThatShrankAndCouldNotBeReadIsReadFromItsHeadAfterGrowingPastItsOldSize(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	appendAt(t, w, text(strings.Repeat("前", 50)), started.Add(10*time.Second))
	old := size(t, w)
	shrinkTo(t, w, text("後"))
	restore := unreadable(w)
	w.check(started.Add(20 * time.Second))
	restore()
	// 読めるようになるまでに、縮む前の大きさを超えて伸びる。後に続くのは progress と数えない行だけ
	for size(t, w) <= old {
		write(t, w, heartbeat)
	}

	w.check(started.Add(30 * time.Second))

	if got := w.sinceProgress(started.Add(30 * time.Second)); got != 0 {
		t.Fatalf("progress が途絶えた時間 = %s, want 縮んだことを覚えて頭から読み、後 の行で時計が戻って 0s", got)
	}
}

func TestRenamingOrRemovingThePathDoesNotStopWatchingTheStream(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前"), started.Add(10*time.Second))
	if err := os.Rename(w.path, w.path+".moved"); err != nil {
		t.Fatal(err)
	}
	appendAt(t, w, text("動かした後"), started.Add(20*time.Second))
	if err := os.Remove(w.path + ".moved"); err != nil {
		t.Fatal(err)
	}
	write(t, w, text("消した後"))

	activity, changed := w.check(started.Add(30 * time.Second))

	if !changed || activity != "消した後" || w.sinceProgress(started.Add(30*time.Second)) != 0 {
		t.Fatalf("活動 = %q, %v, progress が途絶えた時間 %s; want path を動かしても消しても fd の行を読んで 消した後・0s",
			activity, changed, w.sinceProgress(started.Add(30*time.Second)))
	}
}

func TestReplacingThePathWithALargerFileDoesNotChangeWhatIsWatched(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前"), started.Add(10*time.Second))
	other := filepath.Join(t.TempDir(), "other.log")
	if err := os.WriteFile(other, []byte(strings.Repeat(heartbeat, 10)+text("置き換えた file")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, w.path); err != nil {
		t.Fatal(err)
	}
	write(t, w, text("後"))

	activity, changed := w.check(started.Add(20 * time.Second))

	if !changed || activity != "後" {
		t.Fatalf("活動 = %q, %v; want path の置き換えに関わらず fd に書かれた 後", activity, changed)
	}
}

func TestStallClockDoesNotResetWhileTheStreamCannotBeRead(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前"), started.Add(10*time.Second))
	write(t, w, text("後"))
	unreadable(w)

	w.check(started.Add(30 * time.Second))

	if got := w.sinceProgress(started.Add(30 * time.Second)); got != 20*time.Second {
		t.Fatalf("progress が途絶えた時間 = %s, want 中身を読み出せないあいだは戻らず 20s", got)
	}
}

func TestStallClockDoesNotResetWhileTheStreamCannotBeStatted(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前"), started.Add(10*time.Second))
	write(t, w, text("後"))
	unstattable(w)

	w.check(started.Add(30 * time.Second))

	if got := w.sinceProgress(started.Add(30 * time.Second)); got != 20*time.Second {
		t.Fatalf("progress が途絶えた時間 = %s, want 大きさを確かめられないあいだは戻らず 20s", got)
	}
}

func TestActivityTellsWhyTheStreamCannotBeRead(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	write(t, w, text("後"))
	unreadable(w)

	activity, changed := w.check(started.Add(10 * time.Second))

	if !changed || !strings.HasPrefix(activity, "stream を読めない") {
		t.Fatalf("活動 = %q, %v; want stream を読めない", activity, changed)
	}
}

func TestProgressWrittenWhileTheStreamCouldNotBeReadResetsTheStallClockOnceItCanBeRead(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	write(t, w, text("後"))
	restore := unstattable(w)
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
	w := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前の行")+text("前の行"), started.Add(10*time.Second))
	restore := unreadable(w)
	w.check(started.Add(20 * time.Second))
	restore()

	w.check(started.Add(30 * time.Second))

	if reason := w.stopReason(started.Add(80 * time.Second)); strings.Contains(reason, "読めない") {
		t.Fatalf("理由 = %q, want 伸びていなくても読み直して読めたので、stall の理由に読めない理由を添えない", reason)
	}
}

// writeProgressThenUnreadable は worker log に progress の行と progress に数えない heartbeat の行を追記し、heartbeat の
// 行から先を読めなくして、元へ戻す関数を返す。1 回の確認の中で、progress の行を読んだ後に読み出しが落ちる。
func writeProgressThenUnreadable(t *testing.T, w *watched) (restore func()) {
	t.Helper()
	from := size(t, w) + int64(len(taskProgress))
	write(t, w, taskProgress+heartbeat)
	return unreadableFrom(w, from)
}

func TestStallClockDoesNotResetWhenTheStreamBecomesUnreadablePartway(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前"), started.Add(10*time.Second))
	writeProgressThenUnreadable(t, w)

	w.check(started.Add(30 * time.Second))

	if got := w.sinceProgress(started.Add(30 * time.Second)); got != 20*time.Second {
		t.Fatalf("progress が途絶えた時間 = %s, want 途中で読めなくなった確認では、読めた分の progress でも戻らず 20s", got)
	}
}

func TestProgressReadBeforeTheStreamBecameUnreadablePartwayResetsTheStallClockOnceItCanBeRead(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	appendAt(t, w, text("前"), started.Add(10*time.Second))
	restore := writeProgressThenUnreadable(t, w)
	if activity, _ := w.check(started.Add(20 * time.Second)); !strings.Contains(activity, "読めない") {
		t.Fatalf("活動 = %q, want 途中で読めなくなったことを活動にする (読めなくする仕掛けが働いていない)", activity)
	}
	restore()

	// 読めなかった確認から、file は伸びていない
	w.check(started.Add(30 * time.Second))

	if got := w.sinceProgress(started.Add(30 * time.Second)); got != 0 {
		t.Fatalf("progress が途絶えた時間 = %s, want 読み終えた offset は進んでおらず、読めた分の progress を読み直して 0s", got)
	}
}

// watchAt は watch を別の goroutine で走らせ、ticks の時刻で確かめさせる。watch が途中で終われば、その時点で返す。
func watchAt(t *testing.T, w *watched, ticks ...time.Time) (result Result, ended bool, returned bool) {
	t.Helper()
	tick := make(chan time.Time)
	type outcome struct {
		result Result
		ended  bool
	}
	out := make(chan outcome, 1)
	run := &Run{stop: make(chan struct{})}
	go func() {
		result, ended := Runner{}.watch(make(chan struct{}), run, w.streamWatch, tick)
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
	w := stallClock(t, started, time.Minute)
	write(t, w, heartbeat)

	result, ended, returned := watchAt(t, w, started.Add(30*time.Second), started.Add(61*time.Second))

	if !returned || ended || !strings.Contains(result.Failure, "stall") {
		t.Fatalf("watch = %+v, ended %v, 返った %v; want heartbeat だけが続くので stall で止める", result, ended, returned)
	}
}

func TestWatchWaitsForTheStallTimeoutWhenTheStreamCannotBeStatted(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	unstattable(w)

	_, _, returned := watchAt(t, w, started.Add(250*time.Millisecond), started.Add(59*time.Second))

	if returned {
		t.Fatal("大きさを確かめられないだけで、stall の上限を待たずに止めた")
	}
}

func TestWatchStallsWithTheReasonWhenTheStreamCannotBeStattedPastTheTimeout(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	unstattable(w)

	result, _, returned := watchAt(t, w, started.Add(250*time.Millisecond), started.Add(61*time.Second))

	if !returned || !strings.Contains(result.Failure, "stall") || !strings.Contains(result.Failure, "stream を読めない") {
		t.Fatalf("watch = %+v, 返った %v; want stall で止め、読めない理由を添える", result, returned)
	}
}

func TestWatchStopsAtTheRunTimeoutWithoutWhyTheStreamCannotBeRead(t *testing.T) {
	started := time.Now()
	w := watchFrom(t, "", started, limits{stall: time.Hour, run: time.Minute})
	unstattable(w)

	result, _, returned := watchAt(t, w, started.Add(250*time.Millisecond), started.Add(2*time.Minute))

	if !returned || !strings.Contains(result.Failure, "上限時間") || strings.Contains(result.Failure, "読めない") {
		t.Fatalf("watch = %+v, 返った %v; want 上限時間で止め、読めない理由を添えない", result, returned)
	}
}
