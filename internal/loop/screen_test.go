package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/tick"
)

func TestScreenWhileWaitingShowsTheNextTickTheLastTickAndTheGuide(t *testing.T) {
	v := view{
		project: "myproj", interval: "5m", phase: waiting, now: start, next: start.Add(3 * time.Minute),
		last:   &tick.Outcome{TS: "2026-09-26T02:58:00.123456Z", Result: tick.ResultOK, Instructions: map[string]int{"start": 1}, Spawned: []int{42}},
		report: status.Report{Notes: []string{"wip を読めない"}},
	}

	got := strings.Join(v.lines(true), "\n")

	want := "myproj  loop 5m  待機 · 次の tick 2026-09-26T03:03:00Z (あと 3m)\n" +
		"最終 tick 2026-09-26T02:58:00Z ok · 指示 start 1 · 起動 #42\n" +
		"  ! wip を読めない\n" +
		"\n" +
		"Ctrl+C で停止"
	if got != want {
		t.Fatalf("画面 =\n%s\nwant\n%s", got, want)
	}
}

func TestScreenPutsTheErrorOfTheLastTickFirstAmongTheNotes(t *testing.T) {
	v := view{
		project: "myproj", interval: "5m", phase: waiting, now: start, next: start,
		last:   &tick.Outcome{TS: "2026-09-26T02:58:00Z", Result: tick.ResultConfigError, Error: "未知の key"},
		report: status.Report{Notes: []string{"wip を読めない"}},
	}

	lines := v.lines(false)

	if lines[1] != "最終 tick 2026-09-26T02:58:00Z config_error · 指示 0" || lines[2] != "  ! 未知の key" || lines[3] != "  ! wip を読めない" {
		t.Fatalf("lines = %q", lines)
	}
}

func TestScreenBeforeTheFirstTickSaysThereIsNoLastTick(t *testing.T) {
	v := view{project: "myproj", interval: "5m", phase: ticking, now: start.Add(12 * time.Second), tickStarted: start}

	lines := v.lines(true)

	if lines[0] != "myproj  loop 5m  tick 実行中 · 12s" || lines[1] != "最終 tick なし" || lines[len(lines)-1] != "Ctrl+C: この tick を終えてから停止" {
		t.Fatalf("lines = %q", lines)
	}
}

func TestScreenWhileTheOrchestratorRunsShowsItsElapsedAndLimit(t *testing.T) {
	v := view{project: "myproj", interval: "5m", phase: stopping, now: start.Add(3 * time.Minute), tickStarted: start, orchestratorStarted: start.Add(time.Minute)}

	lines := v.lines(true)

	if lines[0] != "myproj  loop 5m  停止待ち · orchestrator 2m (上限 15m)" {
		t.Fatalf("1 行目 = %q", lines[0])
	}
	if !strings.HasPrefix(lines[len(lines)-1], "停止待ち: この tick を終えたら止まる。もう一度 Ctrl+C で orchestrator を止めて止まる") {
		t.Fatalf("操作案内 = %q", lines[len(lines)-1])
	}
}
