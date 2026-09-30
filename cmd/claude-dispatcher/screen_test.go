package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/status"
)

// 端末の画面の描き方 (formats.md §6)。

var screenNow = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// lastFrame は画面に最後に描いたもの。
func lastFrame(out *bytes.Buffer) string {
	frames := strings.Split(out.String(), "\033[H\033[2J")
	return frames[len(frames)-1]
}

func TestScreenDrawsTheStatusTableAboveTheRecentLines(t *testing.T) {
	var out bytes.Buffer
	sc := &screen{out: &out, now: func() time.Time { return screenNow }}
	started := screenNow.Add(-time.Minute)
	snap := status.Snapshot{Scope: "s", Workers: []status.Worker{{
		Target: "issue#42", Trigger: "implement", Attempt: 1, Phase: status.Running, StartedAt: &started,
		Activity: &status.Activity{At: screenNow, Summary: "tool Bash"},
	}}}
	sc.show(snap)

	fmt.Fprint(sc, "t0 起動 issue#42\n")

	want := status.Render(snap, status.LoopAlive, screenNow, time.Local) + "\nt0 起動 issue#42\n"
	if got := lastFrame(&out); got != want || !strings.Contains(got, "\ttool Bash\n") {
		t.Fatalf("画面 = %q, want %q", got, want)
	}
}

func TestScreenKeepsOnlyTheLastTenLines(t *testing.T) {
	var out bytes.Buffer
	sc := &screen{out: &out, now: func() time.Time { return screenNow }}

	for i := range 12 {
		fmt.Fprintf(sc, "line %d\n", i)
	}

	if got := lastFrame(&out); !strings.HasPrefix(got, "\nline 2\n") || !strings.HasSuffix(got, "line 11\n") {
		t.Fatalf("画面 = %q, want line 2 から line 11 まで", got)
	}
}

func TestScreenWaitsForTheEndOfALine(t *testing.T) {
	var out bytes.Buffer
	sc := &screen{out: &out, now: func() time.Time { return screenNow }}

	fmt.Fprint(sc, "書きかけ")

	if got := lastFrame(&out); got != "\n" {
		t.Fatalf("画面 = %q, want 書きかけの行を出さない", got)
	}
}
