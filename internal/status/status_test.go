package status_test

import (
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/status"
)

// 状態の描き方 (formats.md §7.2)。

var base = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func at(d time.Duration) *time.Time { t := base.Add(d); return &t }

// pipe は stdout が端末でないときの描き方 (色も幅も無い)
var pipe = status.Display{Palette: status.Monochrome}

func TestHeadingOfARunningLoopShowsTheNextAndTheLastTick(t *testing.T) {
	s := status.Snapshot{Scope: "github.com/acme/widgets", NextTickAt: at(5 * time.Minute), LastTick: &status.Tick{At: base, Result: status.TickOK, Candidates: 1}}

	got := status.Render(s, status.LoopAlive, base, time.UTC, pipe)

	want := "● loop 稼働中  github.com/acme/widgets\n次の tick 00:05:00 · 直近の tick 00:00:00 ok · 候補 1\n"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("描画:\n%s\nwant 見出し:\n%s", got, want)
	}
}

func TestHeadingOfAStoppingLoopShowsTheLastTickError(t *testing.T) {
	s := status.Snapshot{Scope: "s", Stopping: true, LastTick: &status.Tick{At: base, Result: status.TickError, Error: "workflow 定義の誤り"}}

	got := status.Render(s, status.LoopAlive, base, time.UTC, pipe)

	want := "● loop 停止待ち  s\n次の tick なし · 直近の tick 00:00:00 error: workflow 定義の誤り\n"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("描画:\n%s\nwant 見出し:\n%s", got, want)
	}
}

func TestHeadingOfALoopBeforeItsFirstTickIsOneLine(t *testing.T) {
	got := status.Render(status.Snapshot{Scope: "s"}, status.LoopAlive, base, time.UTC, pipe)

	if !strings.HasPrefix(got, "● loop 稼働中  s\n\n") {
		t.Fatalf("描画:\n%s", got)
	}
}

func TestStoppedLoopShowsOnlyItsHeading(t *testing.T) {
	s := status.Snapshot{Scope: "s", NextTickAt: at(time.Minute), LastTick: &status.Tick{At: base, Result: status.TickOK},
		Workers: []status.Worker{{Target: "issue#42", Phase: status.Running}}, Abandoned: []status.Abandoned{{Target: "issue#43"}}}

	got := status.Render(s, status.LoopGone, base, time.UTC, pipe)

	if want := "○ loop なし  s\n直近の tick 00:00:00 ok · 候補 0\n"; got != want {
		t.Fatalf("描画 = %q, want %q", got, want)
	}
}

func TestUnrecordedHeadingFollowsTheLoop(t *testing.T) {
	if got := status.Unrecorded("s", status.LoopAlive, pipe); got != "● loop 稼働中  s\n記録なし\n" {
		t.Fatalf("見出し = %q", got)
	}
}

func TestWorkersAreATableWithColumnHeadings(t *testing.T) {
	s := status.Snapshot{Scope: "s", Workers: []status.Worker{
		{Target: "issue#42", Trigger: "implement", Attempt: 2, Phase: status.Running, StartedAt: at(0), Activity: &status.Activity{At: base, Summary: "Bash go test ./..."}},
		{Target: "issue#44", Trigger: "fix-ci", Attempt: 1, Phase: status.WaitingRetry, RetryAt: at(90 * time.Second)},
	}}

	got := status.Render(s, status.LoopAlive, base.Add(5*time.Minute+5*time.Second), time.UTC, pipe)

	want := strings.Join([]string{
		"workers 2 " + strings.Repeat("─", 50),
		"作業対象  trigger    attempt  段階           経過               活動",
		"issue#42  implement  2        running        5m05s              Bash go test ./...",
		"issue#44  fix-ci     1        waiting_retry  00:01:30 に再起動",
	}, "\n") + "\n"
	if !strings.HasSuffix(got, "\n\n"+want) {
		t.Fatalf("描画:\n%s\nwant 表:\n%s", got, want)
	}
}

func TestElapsedTimeOfAWorker(t *testing.T) {
	cases := []struct {
		elapsed time.Duration
		want    string
	}{
		{45 * time.Second, "45s"},
		{time.Minute, "1m00s"},
		{time.Hour + 2*time.Minute + 5*time.Second + 500*time.Millisecond, "1h02m05s"},
		{-65 * time.Second, "0s"},
	}
	for _, c := range cases {
		s := status.Snapshot{Scope: "s", Workers: []status.Worker{{Target: "issue#42", Trigger: "implement", Attempt: 1, Phase: status.Stopping, StartedAt: at(0)}}}

		got := status.Render(s, status.LoopAlive, base.Add(c.elapsed), time.UTC, pipe)

		if want := "issue#42  implement  1        stopping  " + c.want + "\n"; !strings.HasSuffix(got, want) {
			t.Errorf("経過 %v の描画:\n%s\nwant 行 %q", c.elapsed, got, want)
		}
	}
}

func TestNoWorkersShowsAnEmptySection(t *testing.T) {
	got := status.Render(status.Snapshot{Scope: "s"}, status.LoopAlive, base, time.UTC, pipe)

	if !strings.HasSuffix(got, "\n\nworkers 0 "+strings.Repeat("─", 50)+"\n") {
		t.Fatalf("描画:\n%s", got)
	}
}

func TestNeedsAttentionGathersWhatAPersonMustFix(t *testing.T) {
	s := status.Snapshot{Scope: "s",
		Abandoned: []status.Abandoned{{Target: "issue#43", Trigger: "implement"}},
		Ambiguous: []status.Ambiguous{{Head: "worktree-issue-7", Targets: []string{"cl#52", "cl#53"}}},
		Blocked:   []status.Blocked{{Trigger: "fix-ci", Error: "action の先頭の /fix が見つからない"}},
	}

	got := status.Render(s, status.LoopAlive, base, time.UTC, pipe)

	want := strings.Join([]string{
		"要対処 3 " + strings.Repeat("─", 51),
		"打ち切り issue#43 (implement): label を外して trigger から外し、1 周期待ってから付け直すと解ける",
		"曖昧な CL worktree-issue-7: cl#52, cl#53",
		"起動しない trigger fix-ci: action の先頭の /fix が見つからない",
	}, "\n") + "\n"
	if !strings.HasSuffix(got, "workers 0 "+strings.Repeat("─", 50)+"\n\n"+want) {
		t.Fatalf("描画:\n%s\nwant 要対処:\n%s", got, want)
	}
}

func TestNeedsAttentionIsLeftOutWhenNothingNeedsAPerson(t *testing.T) {
	got := status.Render(status.Snapshot{Scope: "s"}, status.LoopAlive, base, time.UTC, pipe)

	if strings.Contains(got, "要対処") {
		t.Fatalf("描画:\n%s", got)
	}
}

// running は走っている worker 1 本の状態
var running = status.Snapshot{Scope: "s", Workers: []status.Worker{{Target: "issue#42", Trigger: "implement", Attempt: 1, Phase: status.Running, StartedAt: at(0)}}}

func TestRunningIsPaintedCyanOnATerminal(t *testing.T) {
	got := status.Render(running, status.LoopAlive, base, time.UTC, status.Display{Palette: status.ANSI})

	if !strings.Contains(got, "\033[36mrunning\033[0m") {
		t.Fatalf("端末の描画で running が水色でない:\n%q", got)
	}
}

func TestNothingIsPaintedWithoutColors(t *testing.T) {
	got := status.Render(running, status.LoopAlive, base, time.UTC, pipe)

	if strings.Contains(got, "\033[") {
		t.Fatalf("色の無い描画に色が付いた:\n%q", got)
	}
}

// narrow は幅 20 の端末
var narrow = status.Display{Palette: status.Monochrome, Width: 20}

func TestLongLinesAreCutAtTheWidthOfTheTerminal(t *testing.T) {
	s := status.Snapshot{Scope: "s", Abandoned: []status.Abandoned{{Target: "issue#43", Trigger: "implement"}}}

	got := status.Render(s, status.LoopAlive, base, time.UTC, narrow)

	// 全角は 2 桁と数える: 「打ち切り」(8) + 「 issue#43 (」(11) で 19 桁、残る 1 桁に … を置く
	if want := "\n打ち切り issue#43 (…\n"; !strings.Contains(got, want) {
		t.Fatalf("描画:\n%s\nwant 行 %q", got, want)
	}
}

func TestEmojiTakeTwoColumns(t *testing.T) {
	s := status.Snapshot{Scope: "s", Blocked: []status.Blocked{{Trigger: "a", Error: "✅✅"}}}

	got := status.Render(s, status.LoopAlive, base, time.UTC, status.Display{Palette: status.Monochrome, Width: 25})

	// 「起動しない trigger a: ✅✅」は 18 + 4 + 2×2 で 26 桁。幅 25 に収まらないので、24 桁で切って … を置く
	if want := "\n起動しない trigger a: ✅…\n"; !strings.Contains(got, want) {
		t.Fatalf("描画:\n%s\nwant 行 %q", got, want)
	}
}

func TestSectionRulesStopAtTheWidthOfTheTerminal(t *testing.T) {
	got := status.Render(status.Snapshot{Scope: "s"}, status.LoopAlive, base, time.UTC, narrow)

	if want := "\nworkers 0 " + strings.Repeat("─", 10) + "\n"; !strings.Contains(got, want) {
		t.Fatalf("罫線が端末の幅で止まらない:\n%s", got)
	}
}
