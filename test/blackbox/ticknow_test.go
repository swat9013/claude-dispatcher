package blackbox_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// tick が走っている間だけ置く `tick.now` と、それを読む status の見出しの状態欄と orchestrator の行 (formats.md §1 / §10)。

const orchestratorAgentID = "orch1"

func (s *sandbox) tickNowFile() string { return filepath.Join(s.stateDir(), "tick.now") }

func (s *sandbox) tickNowExists() bool {
	_, err := os.Stat(s.tickNowFile())
	return err == nil
}

// dispatcherProcess は ps が binary の process (pid と command 行) を返すようにする。
func (s *sandbox) dispatcherProcess(pid int, args ...string) {
	s.setProcesses(fmt.Sprintf("%d %s %s", pid, dispatcherBin, strings.Join(args, " ")))
}

// agentsListOrchestrator は `claude agents --json` が orchestrator の session を返すようにする。orchestrator の rule は
// 何にでも当たるので、agents の rule を先頭に置いて書き直す。
func (s *sandbox) agentsListOrchestrator(sessionID string) {
	s.t.Helper()
	agents := []map[string]string{{"id": orchestratorAgentID, "status": "busy", "state": "working", "sessionId": sessionID}}
	rules := []stubwire.Rule{{ArgsPrefix: []string{"agents", "--json"}, Stdout: mustJSON(s.t, agents)}, s.workerRule}
	if s.orchestratorRule != nil {
		rules = append(rules, *s.orchestratorRule)
	}
	s.writeRules("claude", rules)
}

// observationWaits は観測 (`gh issue list`) が release("observation") まで返らないようにする。後片付けで release する。
func (s *sandbox) observationWaits() {
	s.respond("gh", stubwire.Rule{ArgsPrefix: []string{"issue", "list"}, Stdout: "[]", ReleaseFile: s.releaseFile("observation")})
	s.t.Cleanup(func() { s.release("observation") })
}

func (s *sandbox) waitObservationCall() {
	s.t.Helper()
	waitFor(s.t, func() bool {
		return len(s.callsMatching("gh", func(c stubCall) bool { return c.hasPrefix("issue", "list") })) > 0
	}, "観測の gh issue list が撃たれない")
}

// headingLine は status の stdout のうち project の見出し行を返す。
func headingLine(t *testing.T, stdout, project string) string {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, project+"  ") {
			return line
		}
	}
	t.Fatalf("見出し行が無い:\n%s", stdout)
	return ""
}

// orchestratorRow は表のうち KIND が orch の行を返す (無ければ nil)。
func orchestratorRow(stdout string) []string {
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[1] == "orch" {
			return fields
		}
	}
	return nil
}

func TestStatusShowsTheTickAndAnOrchestratorRowWhileTheOrchestratorRuns(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.orchestratorWaitsAfterWriting(startDecisions(playbookPath(s.defaultInstallPath(), "playbook-implementation"), 42))
	s.t.Cleanup(func() { s.release("orchestrator") })
	p := s.startLoop()
	sessionID, _ := s.waitOrchestratorCalls(1)[0].flagValue("--session-id")
	s.agentsListOrchestrator(sessionID)
	s.dispatcherProcess(p.cmd.Process.Pid, "loop", s.project, loopInterval)

	r := s.statusPS()

	heading := regexp.MustCompile(`^` + regexp.QuoteMeta(s.project) + `  loop 稼働中  tick 実行中 · orchestrator \d+s \(上限 15m\)  最終 tick なし$`)
	if !heading.MatchString(headingLine(t, r.stdout, s.project)) {
		t.Fatalf("見出しに orchestrator 実行中の状態欄が無い:\n%s", r.stdout)
	}
	row := orchestratorRow(r.stdout)
	want := regexp.MustCompile(`^- orch running \d+s ` + orchestratorAgentID + ` busy/working - - - \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z( |$)`)
	if !want.MatchString(strings.Join(row, " ")) {
		t.Fatalf("orchestrator の行 = %v:\n%s", row, r.stdout)
	}
}

func TestStatusShowsTheTickWithoutAnOrchestratorRowWhileTheTickObserves(t *testing.T) {
	s := newSandbox(t)
	s.observationWaits()
	p := s.startProcess(s.command("tick", s.project))
	s.waitObservationCall()
	s.dispatcherProcess(p.cmd.Process.Pid, "tick", s.project)

	r := s.statusPS()

	heading := regexp.MustCompile(`^` + regexp.QuoteMeta(s.project) + `  loop なし  tick 実行中 · \d+s  最終 tick なし$`)
	if !heading.MatchString(headingLine(t, r.stdout, s.project)) {
		t.Fatalf("見出しに tick 実行中の状態欄が無い:\n%s", r.stdout)
	}
	if row := orchestratorRow(r.stdout); row != nil {
		t.Fatalf("orchestrator を起動していないのに orchestrator の行がある: %v", row)
	}
	s.release("observation")
	assertExit(t, p.wait(), 0)
}

func TestStatusShowsNoRunningTickWhenItCannotReadTheProcessList(t *testing.T) {
	s := newSandbox(t)
	s.observationWaits()
	p := s.startProcess(s.command("tick", s.project))
	s.waitObservationCall()
	s.respond("ps", stubwire.Rule{Exit: 1, Stderr: "ps: boom\n"})

	r := s.statusPS()

	s.release("observation")
	assertExit(t, p.wait(), 0)
	if strings.Contains(r.stdout, "tick 実行中") {
		t.Fatalf("process の一覧を読めないのに走っている tick を出した:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "  ! process の一覧を読めない") {
		t.Fatalf("process の一覧を読めない注記が無い:\n%s", r.stdout)
	}
}

func TestStatusNotesATickNowItCannotRead(t *testing.T) {
	s := newSandbox(t)
	mustWrite(t, s.tickNowFile(), "{not json\n")
	s.setProcesses()

	r := s.statusPS()

	assertExit(t, r, 0)
	if strings.Contains(r.stdout, "tick 実行中") || !strings.Contains(r.stdout, "  ! tick.now を読めない") {
		t.Fatalf("読めない tick.now を注記して、走っている tick を出さない:\n%s", r.stdout)
	}
}

func TestLoopSecondSignalLeavesNoTickNow(t *testing.T) {
	s := newSandbox(t)

	r := s.secondSignalDuringTheOrchestrator()

	assertExit(t, r, 0)
	if s.tickNowExists() {
		t.Fatal("停止要求で止めた tick が tick.now を残した")
	}
}

func TestTickLeavesNoTickNowWhenItEnds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(s *sandbox)
		exit    int
	}{
		{"正常終了", func(s *sandbox) {}, 0},
		{"観測の失敗", func(s *sandbox) { s.ghFails([]string{"issue", "list"}, 1, "boom\n") }, 1},
		{"orchestrator の異常終了", func(s *sandbox) {
			s.setIssues(readyIssue(42))
			s.orchestratorFailsAfterWriting(3, startDecisions(playbookPath(s.defaultInstallPath(), "playbook-implementation"), 42))
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			tc.arrange(s)

			r := s.tick()

			assertExit(t, r, tc.exit)
			if s.tickNowExists() {
				t.Fatal("tick が終わった後に tick.now が残っている")
			}
		})
	}
}

func TestStatusIgnoresATickNowLeftByAProcessThatIsGone(t *testing.T) {
	for _, tc := range []struct {
		name      string
		processes []string
	}{
		{"pid の process が居ない", nil},
		{"pid が別の process に再利用された", []string{"4343 /bin/sleep 100"}},
		{"pid が別の project の loop に再利用された", []string{"4343 " + dispatcherBin + " loop otherproj 5m"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			mustWrite(t, s.tickNowFile(), `{"pid":4343,"ts":"2026-09-26T03:00:00.000000Z","stage":"orchestrator",`+
				`"orchestrator":{"started":"2026-09-26T03:00:05.000000Z","session_id":"left-behind"}}`+"\n")
			s.setProcesses(tc.processes...)
			s.respond("claude", stubwire.Rule{ArgsPrefix: []string{"agents", "--json"}, Stdout: "[]"})

			r := s.statusPS()

			assertExit(t, r, 0)
			if strings.Contains(r.stdout, "tick 実行中") {
				t.Fatalf("残った tick.now から状態欄を出した:\n%s", r.stdout)
			}
			if row := orchestratorRow(r.stdout); row != nil {
				t.Fatalf("残った tick.now から orchestrator の行を出した: %v", row)
			}
			if !strings.Contains(r.stdout, "  ! ") || !strings.Contains(r.stdout, "tick.now") {
				t.Fatalf("残った tick.now を無視したことを注記していない:\n%s", r.stdout)
			}
		})
	}
}

func TestDryRunWritesNoTickNowWhileItObserves(t *testing.T) {
	s := newSandbox(t)
	s.observationWaits()
	p := s.startProcess(s.command("tick", s.project, "--dry-run"))
	s.waitObservationCall()

	exists := s.tickNowExists()

	s.release("observation")
	assertExit(t, p.wait(), 0)
	if exists {
		t.Fatal("tick --dry-run が観測の間に tick.now を書いた")
	}
}
