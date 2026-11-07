package blackbox_test

import (
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// usage limit の閾値 (formats.md §6 の admission control、system.md §7)。使用率は worker の stream の rate_limit_event で届く。

// usageEvent は 5 時間と週の使用率を持つ rate_limit_event の行。
func usageEvent(fiveHour, weekly float64, reset time.Time) string {
	return fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","unifiedWindows":{`+
		`"five_hour":{"utilization":%g,"resetsAt":%d},"seven_day":{"utilization":%g,"resetsAt":%d}}}}`+"\n", fiveHour, reset.Unix(), weekly, reset.Unix())
}

func TestWorkerAtTheThresholdFinishesWhileTheNextCandidateWaits(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42), readyIssue(43))
	done := readyIssue(42)
	done.labels = nil
	s.onClaude(stubwire.Rule{
		Stdout: usageEvent(0.41, 0.81, time.Now().Add(time.Hour)), ReleaseFile: s.releaseFile(),
		Writes: []stubwire.FileWrite{s.ghResponses(done, readyIssue(43))},
	})
	loop := s.startLoop("--max-weekly-usage", "80")

	closes := s.waitEvents("admission_close", 1)

	if closes[0]["reason"] != "usage" || closes[0]["window"] != "seven_day" || closes[0]["utilization"] != float64(81) || closes[0]["threshold"] != float64(80) {
		t.Fatalf("admission_close の行 = %v", closes[0])
	}
	loop.waitForOutput(regexp.MustCompile(`起動を止める: 週の使用率 81% が閾値 80% 以上 \(\d\d/\d\d \d\d:\d\d に再開\)`))
	s.waitStatusRow("issue#42", "implement", "1", "running")
	out := s.waitStatus("使用率 5 時間 41% · 週 81% (閾値 80%) · 起動を止めている · ")
	if !regexp.MustCompile(`起動を止めている · \d\d/\d\d \d\d:\d\d に再開\n`).MatchString(out) {
		t.Fatalf("status:\n%s", out)
	}
	s.release()
	if end := s.waitEvents("end", 1)[0]; end["target"] != "issue#42" || end["outcome"] != "completed" {
		t.Fatalf("end の行 = %v, want 走っていた worker は完走する", end)
	}
	ticks := len(s.events("tick"))
	s.waitEvents("tick", ticks+2)
	if starts := s.events("start"); len(starts) != 1 {
		t.Fatalf("start の行 = %v, want 空きができても issue#43 を起動しない", starts)
	}
}

func TestUsageWrittenRightBeforeTheWorkerEndsIsNotLost(t *testing.T) {
	// claude が書いてすぐ終わった行は監視の周期に当たらないので、終わりを受ける前に読み切る
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{Stdout: usageEvent(0.1, 1, time.Now().Add(time.Hour))})

	s.startLoop()

	s.waitEvents("admission_close", 1)
}

func TestStatusShowsNoObservationBeforeTheFirstEvent(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues()
	s.startLoop()

	s.waitStatus("使用率 5 時間 未観測 · 週 未観測\n")
}

// unreadableEvent はどの window も読めない rate_limit_event の行。
const unreadableEvent = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}` + "\n"

func TestEventWithNoReadableWindowStopsLaunching(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42), readyIssue(43))
	done := readyIssue(42)
	done.labels = nil
	s.onClaude(stubwire.Rule{Stdout: unreadableEvent, ReleaseFile: s.releaseFile(), Writes: []stubwire.FileWrite{s.ghResponses(done, readyIssue(43))}})
	s.startLoop()

	if c := s.waitEvents("admission_close", 1)[0]; c["reason"] != "unreadable" {
		t.Fatalf("admission_close の行 = %v", c)
	}
	s.release()
	s.waitEvents("end", 1)
	ticks := s.waitEvents("tick", len(s.events("tick"))+2)

	if last := ticks[len(ticks)-1]; last["candidates"] != float64(1) {
		t.Fatalf("tick の行 = %v, want issue#43 は候補のまま", last)
	}
	if starts := s.events("start"); len(starts) != 1 {
		t.Fatalf("start の行 = %v, want 空きができても issue#43 を起動しない", starts)
	}
}

func TestEventWithNoReadableWindowAsksForAnOperator(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{Stdout: unreadableEvent, ReleaseFile: s.releaseFile()})
	s.startLoop()

	s.waitStatus("使用率を読めない: rate_limit_event の形式が変わった。dispatcher を上げて loop を起動し直す\n")
}

func TestRunIgnoresTheUsageLimit(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	done := readyIssue(42)
	done.labels = nil
	s.onClaude(stubwire.Rule{Stdout: usageEvent(1, 1, time.Now().Add(time.Hour)), Writes: []stubwire.FileWrite{s.ghResponses(done)}})

	r := s.run("run", "implement", "issue#42")

	assertExit(t, r, 0)
	if lines := s.logLines(); len(lines) != 0 {
		t.Fatalf("log.jsonl の行 = %v, want 無し", lines)
	}
}

func TestAWrongUsageThresholdIsAnArgumentError(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))

	r := s.run("loop", "--max-weekly-usage", "101")

	assertExit(t, r, 2)
}

func TestDryRunAcceptsTheUsageThresholds(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))

	assertExit(t, s.dryRun("--max-five-hour-usage=50", "--max-weekly-usage", "80"), 0)
}
