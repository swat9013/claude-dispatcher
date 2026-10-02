package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/status"
)

// 端末の画面の描き方 (formats.md §6)。

var (
	screenNow = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// tokyo は端末の local time (UTC と食い違うことを確かめる)
	tokyo = time.FixedZone("JST", 9*60*60)
	// pipe は色も幅も無い描き方
	pipe = status.Display{Palette: status.Monochrome}
)

func newScreen(out *bytes.Buffer, d status.Display) *screen {
	return &screen{out: out, now: func() time.Time { return screenNow }, loc: tokyo, display: func() status.Display { return d }}
}

// lastFrame は画面に最後に描いたもの。
func lastFrame(out *bytes.Buffer) string {
	frames := strings.Split(out.String(), "\033[H\033[2J")
	return frames[len(frames)-1]
}

func logRule() string { return "ログ " + strings.Repeat("─", 55) }

func TestScreenDrawsTheStatusAboveTheLog(t *testing.T) {
	var out bytes.Buffer
	sc := newScreen(&out, pipe)
	started := screenNow.Add(-time.Minute)
	snap := status.Snapshot{Scope: "s", Workers: []status.Worker{{
		Target: "issue#42", Trigger: "implement", Attempt: 1, Phase: status.Running, StartedAt: &started,
		Activity: &status.Activity{At: screenNow, Summary: "Bash go test ./..."},
	}}}
	sc.show(snap)

	sc.Show(loop.Line{At: screenNow, Kind: loop.LineStart, Label: "起動", Rest: " issue#42 (implement, attempt 1, session x)"})

	want := status.Render(snap, status.LoopAlive, screenNow, tokyo, pipe) + "\n" + logRule() + "\n09:00:00 起動 issue#42 (implement, attempt 1, session x)\n"
	if got := lastFrame(&out); got != want {
		t.Fatalf("画面:\n%s\nwant:\n%s", got, want)
	}
}

func TestScreenLeavesActivityOutOfTheLog(t *testing.T) {
	var out bytes.Buffer
	sc := newScreen(&out, pipe)

	sc.Show(loop.Line{At: screenNow, Kind: loop.LineTickOK, Label: "tick ok · 候補 1"})
	sc.Show(loop.Line{At: screenNow, Kind: loop.LineActivity, Label: "活動", Rest: " issue#42: thinking"})

	if got := lastFrame(&out); strings.Contains(got, "活動") || !strings.HasSuffix(got, "09:00:00 tick ok · 候補 1\n") {
		t.Fatalf("画面:\n%s", got)
	}
}

func TestScreenKeepsOnlyTheLastTenLines(t *testing.T) {
	var out bytes.Buffer
	sc := newScreen(&out, pipe)

	for i := range 12 {
		sc.Show(loop.Line{At: screenNow, Label: fmt.Sprintf("line %d", i)})
	}

	if got := lastFrame(&out); !strings.Contains(got, logRule()+"\n09:00:00 line 2\n") || !strings.HasSuffix(got, "09:00:00 line 11\n") {
		t.Fatalf("画面:\n%s\nwant line 2 から line 11 まで", got)
	}
}

func TestScreenTellsHowToStopWhileTheLoopWaitsForWorkers(t *testing.T) {
	var out bytes.Buffer
	sc := newScreen(&out, pipe)

	sc.show(status.Snapshot{Scope: "s", Stopping: true, Workers: []status.Worker{
		{Target: "issue#42", Phase: status.Running}, {Target: "issue#43", Phase: status.Stopping}, {Target: "issue#44", Phase: status.Verifying},
	}})

	want := "● loop 停止待ち  s\n次の tick なし\n走っている worker 2 本の終了を待っている。もう一度 Ctrl+C で worker を止めて終える\n\nworkers 3 "
	if got := lastFrame(&out); !strings.HasPrefix(got, want) {
		t.Fatalf("画面:\n%s\nwant 見出し:\n%s", got, want)
	}
}

func TestScreenPaintsTheHeadOfALogLine(t *testing.T) {
	var out bytes.Buffer
	sc := newScreen(&out, status.Display{Palette: status.ANSI})

	sc.Show(loop.Line{At: screenNow, Kind: loop.LineStart, Label: "起動", Rest: " issue#42"})
	sc.Show(loop.Line{At: screenNow, Kind: loop.LineTickOK, Label: "tick ok · 候補 0"})

	got := lastFrame(&out)
	if want := "\033[90m09:00:00\033[0m \033[34m起動\033[0m issue#42\n"; !strings.Contains(got, want) {
		t.Errorf("起動の行の色:\n%q\nwant %q", got, want)
	}
	if want := "\033[2m09:00:00 tick ok · 候補 0\033[0m\n"; !strings.Contains(got, want) {
		t.Errorf("tick ok の行が薄くない:\n%q\nwant %q", got, want)
	}
}
