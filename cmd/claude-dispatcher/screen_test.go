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
	sc.showStatus(snap)

	sc.Show(loop.Line{At: screenNow, Kind: loop.LineStart, Label: "起動", Rest: " issue#42 (implement, attempt 1, session x)"})

	want := status.Render(snap, status.LoopAlive, screenNow, tokyo, pipe) + "\n" + logRule() + "\n09:00:00 起動 issue#42 (implement, attempt 1, session x)\n"
	if got := lastFrame(&out); got != want {
		t.Fatalf("画面:\n%s\nwant:\n%s", got, want)
	}
}

func TestScreenLeavesActivityOutOfTheLog(t *testing.T) {
	var out bytes.Buffer
	sc := newScreen(&out, pipe)

	sc.Show(loop.Line{At: screenNow, Kind: loop.LineTickOK, Label: "tick ok", Rest: " · 候補 1"})
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

	sc.showStatus(status.Snapshot{Scope: "s", Stopping: true, Workers: []status.Worker{
		{Target: "issue#42", Phase: status.Running}, {Target: "issue#43", Phase: status.Running}, {Target: "issue#44", Phase: status.Verifying},
	}})

	want := "● loop 停止待ち  s\n次の tick なし\nworker の終了を待っている。もう一度 Ctrl+C で走っている worker 2 本を止めて終える\n\nworkers 3 "
	if got := lastFrame(&out); !strings.HasPrefix(got, want) {
		t.Fatalf("画面:\n%s\nwant 見出し:\n%s", got, want)
	}
}

func TestScreenHeadingShowsTheVersionOfTheLoopWhileItWaitsForWorkers(t *testing.T) {
	var out bytes.Buffer
	sc := newScreen(&out, pipe)

	sc.showStatus(status.Snapshot{Scope: "github.com/acme/widgets", Version: "v0.3.0", Commit: "0123abc", Stopping: true})

	if got := lastFrame(&out); !strings.HasPrefix(got, "● loop 停止待ち  github.com/acme/widgets · v0.3.0\n") {
		t.Fatalf("画面:\n%s", got)
	}
}

func TestScreenStopsTellingHowToStopOnceTheWorkersAreBeingStopped(t *testing.T) {
	// 2 回目の停止要求の後は、走っている worker が止めている段階へ移る
	var out bytes.Buffer
	sc := newScreen(&out, pipe)

	sc.showStatus(status.Snapshot{Scope: "s", Stopping: true, Workers: []status.Worker{{Target: "issue#42", Phase: status.Stopping}}})

	if got := lastFrame(&out); strings.Contains(got, "Ctrl+C") {
		t.Fatalf("画面:\n%s", got)
	}
}

func TestScreenPaintsTheHeadOfALogLine(t *testing.T) {
	var out bytes.Buffer
	sc := newScreen(&out, status.Display{Palette: status.ANSI})

	sc.Show(loop.Line{At: screenNow, Kind: loop.LineStart, Label: "起動", Rest: " issue#42"})

	if got, want := lastFrame(&out), "\033[90m09:00:00\033[0m \033[34m起動\033[0m issue#42\n"; !strings.HasSuffix(got, want) {
		t.Fatalf("起動の行の色:\n%q\nwant %q", got, want)
	}
}

func TestScreenFadesATickOKLine(t *testing.T) {
	var out bytes.Buffer
	sc := newScreen(&out, status.Display{Palette: status.ANSI})

	sc.Show(loop.Line{At: screenNow, Kind: loop.LineTickOK, Label: "tick ok", Rest: " · 候補 0"})

	if got, want := lastFrame(&out), "\033[2m09:00:00 tick ok · 候補 0\033[0m\n"; !strings.HasSuffix(got, want) {
		t.Fatalf("tick ok の行が薄くない:\n%q\nwant %q", got, want)
	}
}

func TestScreenPaintsAFailedTickLineRed(t *testing.T) {
	var out bytes.Buffer
	sc := newScreen(&out, status.Display{Palette: status.ANSI})

	sc.Show(loop.Line{At: screenNow, Kind: loop.LineTickError, Label: "tick error", Rest: " · gh の障害"})

	if got, want := lastFrame(&out), "\033[31m09:00:00 tick error · gh の障害\033[0m\n"; !strings.HasSuffix(got, want) {
		t.Fatalf("tick error の行が赤くない:\n%q\nwant %q", got, want)
	}
}

func TestScreenKeepsAMultiLineErrorOnOneRow(t *testing.T) {
	// hook の stderr のような複数行の文も、ログの 1 行に収める
	var out bytes.Buffer
	sc := newScreen(&out, pipe)

	sc.Show(loop.Line{At: screenNow, Kind: loop.LineError, Label: "error", Rest: " issue#42: after_run が失敗した\n1 行目\n2 行目"})

	if got := lastFrame(&out); !strings.HasSuffix(got, "09:00:00 error issue#42: after_run が失敗した 1 行目 2 行目\n") {
		t.Fatalf("画面:\n%s", got)
	}
}

func TestTerminalPaletteFollowsNoColor(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		palette status.Palette
	}{
		{"NO_COLOR が無ければ色を付ける", map[string]string{}, status.ANSI},
		{"NO_COLOR が空なら色を付ける", map[string]string{"NO_COLOR": ""}, status.ANSI},
		{"NO_COLOR が空でなければ色を付けない", map[string]string{"NO_COLOR": "1"}, status.Monochrome},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := terminalPalette(func(key string) string { return c.env[key] }); got != c.palette {
				t.Fatalf("palette = %v, want %v", got, c.palette)
			}
		})
	}
}
