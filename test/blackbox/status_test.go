package blackbox_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// `status ps` / `status watch` (formats.md §10)。log.jsonl の spawned を起点に、process の生死・wip・作業ツリー・CL を
// 読み直して今の worker を並べる。読み取り専用。

const (
	workerPID     = 4242
	workerSession = "3f2b8c1e-0000-4000-8000-000000000042"
	spawnedTS     = "2026-09-26T02:48:00.000000Z"
)

// writeSpawnedTickLine は issue 42 の worker を起動した tick 行を log.jsonl に置く。
func (s *sandbox) writeSpawnedTickLine() {
	s.t.Helper()
	line := map[string]any{
		"ts": spawnedTS, "project": s.project, "cwd": s.clone, "result": "ok",
		"observed": map[string]int{"issues": 1, "cls": 0}, "candidates": 1, "wip": 0, "instructions": map[string]int{"start": 1},
		"instruction_file": "/x", "orchestrator": map[string]any{"exit_code": 0, "seconds": 1.0, "timed_out": false, "session_id": "o"},
		"spawned": []map[string]any{{"issue": 42, "kind": "start", "pid": workerPID, "log": "/x.log", "session_id": workerSession}},
	}
	mustWrite(s.t, s.logFile(), mustJSON(s.t, line)+"\n")
}

// workerAlive は ps が worker の process を返すようにする。
func (s *sandbox) workerAlive() {
	s.setProcesses(fmt.Sprintf("%d /usr/local/bin/claude -p prompt --permission-mode auto --session-id %s", workerPID, workerSession))
}

func (s *sandbox) setProcesses(lines ...string) {
	s.respond("ps", stubwire.Rule{Stdout: strings.Join(append([]string{"    1 /sbin/launchd"}, lines...), "\n") + "\n"})
}

func (s *sandbox) setWip(numbers ...int) {
	listed := []map[string]int{}
	for _, n := range numbers {
		listed = append(listed, map[string]int{"number": n})
	}
	s.respond("gh", stubwire.Rule{ArgsPrefix: []string{"issue", "list"}, ArgContains: wipLabel, Stdout: mustJSON(s.t, listed)})
}

// statusScenario は issue 42 の worker が起動済みで、clone に作業ツリーがあり CL #57 が open の状況を作る。
func (s *sandbox) statusScenario() {
	s.writeSpawnedTickLine()
	s.respond("claude", stubwire.Rule{ArgsPrefix: []string{"agents", "--json"}, Stdout: "[]"})
	s.respond("git", stubwire.Rule{ArgsPrefix: []string{"remote", "get-url", "origin"}, Stdout: "git@github.com:" + defaultIssueRepo + ".git\n"})
	s.respond("git", stubwire.Rule{ArgsPrefix: []string{"worktree", "list"}, Stdout: "worktree " + s.clone + "\nbranch refs/heads/main\n\nworktree " + s.clone + "/.claude/worktrees/issue-42\nbranch refs/heads/" + workerBranch(42) + "\n"})
	s.respond("git", stubwire.Rule{ArgsPrefix: []string{"rev-parse"}, Stdout: "origin/main\n"})
	s.respond("git", stubwire.Rule{ArgsPrefix: []string{"rev-list"}, Stdout: "3\n"})
	s.respond("gh", stubwire.Rule{ArgsPrefix: []string{"api", "graphql"}, ArgContains: "headRefName",
		Stdout: `{"data":{"repository":{"i42":{"nodes":[{"number":57,"state":"OPEN"}]}}}}`})
}

func (s *sandbox) statusPS(args ...string) runResult {
	s.t.Helper()
	return s.run(append([]string{"status", "ps", s.project}, args...)...)
}

// workerRow は表のうち #<issue> で始まる行を返す (無ければ "")。
func workerRow(stdout string, issue int) []string {
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == fmt.Sprintf("#%d", issue) {
			return fields
		}
	}
	return nil
}

func TestStatusWritesNothing(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.workerAlive()
	s.setWip(42)
	before := fileFingerprints(t, s.stateDir())
	configBefore := fileFingerprints(t, s.configRoot)

	r := s.statusPS()

	assertExit(t, r, 0)
	if !maps.Equal(before, fileFingerprints(t, s.stateDir())) || !maps.Equal(configBefore, fileFingerprints(t, s.configRoot)) {
		t.Fatal("status が state dir か config root に書いた")
	}
	if _, err := os.Stat(s.lockFile()); !os.IsNotExist(err) {
		t.Fatal("status が lock file を作った")
	}
	for _, c := range s.calls("gh") {
		if !c.hasPrefix("issue", "list") && !c.hasPrefix("api", "graphql") {
			t.Fatalf("status が読み取り以外の gh を撃った: %v", c.args())
		}
	}
}

func TestStatusListsARunningWorkerWithItsWipBranchAndCL(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.workerAlive()
	s.setWip(42)

	r := s.statusPS()

	row := workerRow(r.stdout, 42)
	for _, want := range []string{"start", "running", "+3", "yes", "#57", "OPEN", "2026-09-26T02:48:00Z"} {
		if !strings.Contains(strings.Join(row, " "), want) {
			t.Fatalf("#42 の行に %q が無い: %v\n%s", want, row, r.stdout)
		}
	}
}

func TestStatusShowsAWorkerWhoseProcessIsGoneButWipRemainsAsExited(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.setWip(42)

	r := s.statusPS()

	if row := workerRow(r.stdout, 42); len(row) == 0 || row[2] != "exited" || !strings.Contains(strings.Join(row, " "), "yes") {
		t.Fatalf("stale wip の行 = %v\n%s", row, r.stdout)
	}
}

func TestStatusDoesNotMistakeAReusedPidForTheWorker(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.setProcesses(fmt.Sprintf("%d /usr/bin/vim notes.txt", workerPID))
	s.setWip(42)

	r := s.statusPS()

	if row := workerRow(r.stdout, 42); len(row) < 3 || row[2] != "exited" {
		t.Fatalf("pid を再利用した別 process を worker と見た: %v\n%s", row, r.stdout)
	}
}

func TestStatusLeavesOutAWorkerThatExitedAndHasNoWip(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.setWip()

	r := s.statusPS()

	assertExit(t, r, 0)
	if row := workerRow(r.stdout, 42); row != nil {
		t.Fatalf("終わって wip も剥がれた worker を載せた: %v", row)
	}
}

func TestStatusListsOnlyTheLatestSpawnOfAnIssueWhoseEarlierWorkerExited(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	later := map[string]any{
		"ts": "2026-09-26T03:30:00.000000Z", "project": s.project, "cwd": s.clone, "result": "ok",
		"spawned": []map[string]any{{"issue": 42, "kind": "reenter", "pid": workerPID + 1, "log": "/y.log", "session_id": "later-session"}},
	}
	f, err := os.OpenFile(s.logFile(), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, mustJSON(t, later))
	f.Close()
	s.setProcesses(fmt.Sprintf("%d claude -p x --session-id later-session", workerPID+1))
	s.setWip(42)

	r := s.statusPS()

	if n := strings.Count(r.stdout, "\n#42 "); n != 1 || !strings.Contains(strings.Join(workerRow(r.stdout, 42), " "), "reenter running") {
		t.Fatalf("#42 の行が最新の起動 1 行でない (%d 行):\n%s", n, r.stdout)
	}
}

func TestStatusMatchesTheCloneToTheCLRepoIgnoringCase(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.respond("git", stubwire.Rule{ArgsPrefix: []string{"remote", "get-url", "origin"}, Stdout: "https://github.com/ACME/Widgets/\n"})
	s.workerAlive()
	s.setWip(42)

	r := s.statusPS()

	if row := workerRow(r.stdout, 42); !strings.Contains(strings.Join(row, " "), "+3") {
		t.Fatalf("大文字小文字違いの origin を別の repo と見た: %v\n%s", row, r.stdout)
	}
}

func TestStatusJSONCarriesTheWorkers(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.workerAlive()
	s.setWip(42)

	r := s.statusPS("--json")

	var doc struct {
		Projects []struct {
			Project string
			Tick    struct {
				LastTS string `json:"last_ts"`
			}
			Workers []map[string]any
		}
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatalf("stdout が JSON でない: %v\n%s", err, r.stdout)
	}
	if len(doc.Projects) != 1 || len(doc.Projects[0].Workers) != 1 || doc.Projects[0].Tick.LastTS != spawnedTS {
		t.Fatalf("status --json = %s", r.stdout)
	}
	w := doc.Projects[0].Workers[0]
	if number(t, w["issue"]) != 42 || w["state"] != "running" || w["wip"] != true || number(t, asMap(t, w["cl"])["number"]) != 57 {
		t.Fatalf("worker = %v", w)
	}
}

func TestStatusReadsARunningTickFromTheProcessList(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.setWip()
	s.setProcesses("777 /home/me/go/bin/claude-dispatcher tick " + s.project)

	r := s.statusPS()

	if first := strings.SplitN(r.stdout, "\n", 2)[0]; !strings.Contains(first, "tick 実行中") {
		t.Fatalf("1 行目 = %q", first)
	}
}

func TestStatusMarksWhatItCouldNotReadWithAQuestionMarkAndANote(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.workerAlive()
	s.ghFails([]string{"issue", "list"}, 1, "HTTP 502: Bad Gateway\n")

	r := s.statusPS()

	assertExit(t, r, 0)
	if row := workerRow(r.stdout, 42); len(row) == 0 || !strings.Contains(strings.Join(row, " "), "?") {
		t.Fatalf("読めなかった wip が ? になっていない: %v\n%s", row, r.stdout)
	}
	if !strings.Contains(r.stdout, "! ") || !strings.Contains(r.stdout, "wip") {
		t.Fatalf("読めなかった理由の注記が無い:\n%s", r.stdout)
	}
}

func TestStatusOfAnUnknownProjectIsAUsageError(t *testing.T) {
	s := newSandbox(t)

	r := s.run("status", "ps", "nosuch")

	assertExit(t, r, 2)
}

func TestStatusWatchRejectsAnIntervalOutsideItsRange(t *testing.T) {
	for _, interval := range []string{"0", "0.5", "NaN", "Inf", "1e10"} {
		t.Run(interval, func(t *testing.T) {
			s := newSandbox(t)

			r := s.run("status", "watch", s.project, "--interval", interval)

			assertExit(t, r, 2)
		})
	}
}
