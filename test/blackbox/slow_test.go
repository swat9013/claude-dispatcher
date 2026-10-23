package blackbox_test

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// 遅い一覧 (formats.md §6・§5・§7.4)。閾値の 15 秒を実時間で待たず、build tag `blackbox` の binary だけが読む env で閾値を
// 縮め、stub の応答をそれより遅らせる。

// slowAfterEnv は cmd/claude-dispatcher/slow_blackbox.go が読む env。名前がずれると閾値が 15 秒のままになり、遅い run の
// 行が出ずにテストが落ちる
const slowAfterEnv = "CLAUDE_DISPATCHER_TEST_SLOW_AFTER"

// slowEnv は閾値を 100ms に縮める。stub の応答はそれより長い slowStubDelay だけ遅らせる
var slowEnv = map[string]string{slowAfterEnv: "100ms"}

const slowStubDelay = 500 * time.Millisecond

const slowIssueListingLine = "issue 置き場 acme/widgets の一覧の読み出しが 15 秒を超えた"

// makeSlow は、呼んだ時点で names の stub に登録済みの rule を写し、slowStubDelay の遅延を付けて登録し直す。後から登録した
// rule は遅れない
func (s *sandbox) makeSlow(names ...string) {
	s.t.Helper()
	for _, name := range names {
		rules := append([]stubwire.Rule(nil), s.rules[name]...)
		for i := range rules {
			rules[i].Delay = slowStubDelay
		}
		s.respondAll(name, rules)
	}
}

// assertSlowLinesOnlyOnStderr は、遅い run が stderr に lines を 1 行ずつちょうど出し、速い run は出さず、stdout と exit code は
// 速い run (wantExit で終わる) と同じことを確かめる。
func assertSlowLinesOnlyOnStderr(t *testing.T, fast, slow runResult, wantExit int, lines ...string) {
	t.Helper()
	assertExit(t, fast, wantExit)
	for _, line := range lines {
		if n := strings.Count(slow.stderr, line+"\n"); n != 1 {
			t.Errorf("stderr の %q = %d 行, want 1:\n%s", line, n, slow.stderr)
		}
		if strings.Contains(fast.stderr+fast.stdout+slow.stdout, line) {
			t.Errorf("stderr の外か速い run に %q が出た", line)
		}
	}
	if slow.stdout != fast.stdout {
		t.Errorf("stdout が知らせの無いときと違う:\n%s\nwant:\n%s", slow.stdout, fast.stdout)
	}
	if slow.exit != fast.exit {
		t.Errorf("exit code = %d, 知らせの無いときは %d", slow.exit, fast.exit)
	}
}

func TestDryRunPrintsTheSlowListingLineOnlyOnStderr(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	fast := s.dryRun()
	s.makeSlow("gh")

	slow := s.runWithEnv(slowEnv, "loop", "--dry-run")

	assertSlowLinesOnlyOnStderr(t, fast, slow, 0, slowIssueListingLine)
}

func TestDoctorPrintsTheSlowListingLineOnlyOnStderr(t *testing.T) {
	s := newSandbox(t)
	s.setStore(nil, nil)
	fast := s.run("doctor")
	s.makeSlow("gh")

	slow := s.runWithEnv(slowEnv, "doctor")

	assertSlowLinesOnlyOnStderr(t, fast, slow, 0, slowIssueListingLine)
}

// 一覧を確かめる手順を持つのは jira (と cl: の CL 置き場) なので、起動時の確認の遅い一覧は jira の workflow 定義で見る。

// jiraSlowLines は jira の issue 置き場と cl: の CL 置き場の遅い一覧の行。
var jiraSlowLines = []string{
	"issue 置き場 " + jiraSite + "/" + jiraKey + " の一覧の読み出しが 15 秒を超えた",
	"CL 置き場 acme/widgets の一覧の読み出しが 15 秒を超えた",
}

func TestJiraDoctorPrintsTheSlowListingLinesOfBothPlacesOnlyOnStderr(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.jiraWorkerWorkflow())
	s.setJiraPlaces(readyJiraIssue(42))
	fast := s.run("doctor")
	s.makeSlow("acli", "gh")

	slow := s.runWithEnv(slowEnv, "doctor")

	assertSlowLinesOnlyOnStderr(t, fast, slow, 0, jiraSlowLines...)
}

func TestRunPrintsTheSlowListingLinesOfTheConfirmationOnStderr(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.jiraWorkerWorkflow())
	s.setJiraPlaces(readyJiraIssue(42))
	s.onClaude(stubwire.Rule{})
	s.makeSlow("acli", "gh")

	r := s.runWithEnv(slowEnv, "run", "implement", "issue#42")

	// worker は trigger から外さないので、知らせがあっても起動して未完了の 6 で終わる (stdout は時刻を含むので比べない)
	assertExit(t, r, 6)
	if calls := s.calls("claude"); len(calls) != 1 {
		t.Errorf("claude の呼び出し = %d 回, want 1", len(calls))
	}
	for _, line := range jiraSlowLines {
		if n := strings.Count(r.stderr, line+"\n"); n != 1 {
			t.Errorf("stderr の %q = %d 行, want 1:\n%s", line, n, r.stderr)
		}
		if strings.Contains(r.stdout, line) {
			t.Errorf("stdout に %q が出た:\n%s", line, r.stdout)
		}
	}
}

func TestLoopShowsTheSlowListingLinesOfTheStartupConfirmationBeforeItStarts(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.jiraWorkerWorkflow())
	s.setJiraPlaces()
	s.makeSlow("acli", "gh")

	loop := s.startLoopWithEnv(slowEnv)

	started := loop.waitForOutput(loopStartedLine)
	before, _, found := strings.Cut(loop.stdout.String(), started)
	if !found {
		t.Fatalf("stdout に loop を始めた行 %q が無い:\n%s", started, loop.stdout)
	}
	for _, line := range jiraSlowLines {
		if n := strings.Count(before, " "+line+"\n"); n != 1 {
			t.Errorf("loop を始めた行より前の stdout の %q = %d 行, want 1:\n%s", line, n, loop.stdout)
		}
		if strings.Contains(loop.stderr.String(), line) {
			t.Errorf("stderr に %q が出た:\n%s", line, loop.stderr)
		}
	}
}

func TestLoopWritesTheSlowListingsOfTheStartupConfirmationRightAfterLoopStart(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.jiraWorkerWorkflow())
	s.setJiraPlaces()
	s.makeSlow("acli", "gh")

	loop := s.startLoopWithEnv(slowEnv)
	loop.waitForOutput(loopStartedLine)
	loop.waitForOutput(regexp.MustCompile(`tick ok`))

	// 最初の tick の読み出しも遅いので、loop_start から最初の tick の行までに、起動時の確認の 2 行 (issue・cl) と tick の 1 行 (issue) が並ぶ
	lines := s.logLinesIn(s.jiraStateDir())
	start := slices.IndexFunc(lines, func(l map[string]any) bool { return l["event"] == "loop_start" })
	tick := slices.IndexFunc(lines, func(l map[string]any) bool { return l["event"] == "tick" })
	if start < 0 || tick < start {
		t.Fatalf("log.jsonl = %v, want loop_start の後に tick の行", lines)
	}
	var slow []map[string]any
	for _, l := range lines[start+1 : tick] {
		if l["event"] == "slow_listing" {
			slow = append(slow, l)
		}
	}
	if len(slow) != 3 || lines[start+1]["event"] != "slow_listing" {
		t.Fatalf("loop_start から最初の tick までの行 = %v, want loop_start の直後から slow_listing 3 行 (起動時の確認 2 行・tick 1 行)", lines[start:tick+1])
	}
	issue := struct{ kind, place string }{"issue", jiraSite + "/" + jiraKey}
	cl := struct{ kind, place string }{"cl", "acme/widgets"}
	want := []struct{ kind, place string }{issue, cl, issue}
	for i, w := range want {
		got := slow[i]
		if got["event"] != "slow_listing" || got["kind"] != w.kind || got["place"] != w.place {
			t.Errorf("%d 本目の slow_listing = %v, want kind %s, place %s", i+1, got, w.kind, w.place)
		}
	}
}

func TestReleaseBuildDoesNotReadTheSlowListingThresholdFromTheEnvironment(t *testing.T) {
	// 配布する binary は build tag を付けずに build する (.goreleaser.yaml)。閾値の差し替えの口は持たない (#374)
	bin := filepath.Join(t.TempDir(), "claude-dispatcher")
	if out, err := exec.Command("go", "build", "-o", bin, dispatcherPackage).CombinedOutput(); err != nil {
		t.Fatalf("tag 無しで build できない: %v\n%s", err, out)
	}
	s := newSandbox(t)
	s.setStore(nil, nil)
	s.makeSlow("gh")
	cmd := exec.Command(bin, "doctor")
	cmd.Dir = s.clone
	cmd.Env = s.environ(slowEnv)

	out, err := cmd.CombinedOutput()

	if err != nil || !strings.Contains(string(out), "ok   issue 置き場 acme/widgets\n") {
		t.Fatalf("tag 無しの doctor が通らない (%v):\n%s", err, out)
	}
	if strings.Contains(string(out), slowIssueListingLine) {
		t.Fatalf("tag 無しの binary が env の閾値で遅い一覧の行を出した:\n%s", out)
	}
}
