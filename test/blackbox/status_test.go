package blackbox_test

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
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
	before := map[string]map[string]string{}
	for _, dir := range []string{s.stateDir(), s.configRoot, s.home, s.clone} {
		before[dir] = fileFingerprints(t, dir)
	}

	r := s.statusPS()

	assertExit(t, r, 0)
	for dir, fingerprints := range before {
		if !maps.Equal(fingerprints, fileFingerprints(t, dir)) {
			t.Fatalf("status が %s の下に書いた", dir)
		}
	}
	if _, err := os.Stat(s.lockFile()); !os.IsNotExist(err) {
		t.Fatal("status が lock file を作った")
	}
	readOnly := map[string][][]string{
		"gh":     {{"issue", "list"}, {"api", "graphql"}},
		"git":    {{"remote", "get-url"}, {"worktree", "list"}, {"rev-parse"}, {"rev-list"}},
		"claude": {{"agents", "--json"}},
		"ps":     {{"-A"}},
	}
	for name, prefixes := range readOnly {
		for _, c := range s.calls(name) {
			if !slices.ContainsFunc(prefixes, func(p []string) bool { return c.hasPrefix(p...) }) {
				t.Fatalf("status が読み取り以外の %s を撃った: %v", name, c.args())
			}
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
	appendFile(t, s.logFile(), mustJSON(t, later)+"\n")
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

func TestStatusWithoutAProjectListsEveryProject(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.workerAlive()
	s.setWip(42)
	other := filepath.Join(s.configRoot, "other")
	mustMkdir(t, other)
	mustWrite(t, filepath.Join(other, "config.toml"), s.defaultConfig())

	r := s.run("status", "ps")

	assertExit(t, r, 0)
	var headers []string
	for _, block := range strings.Split(r.stdout, "\n\n") {
		headers = append(headers, strings.Fields(block)[0])
	}
	if !slices.Equal(headers, []string{"other", s.project}) || workerRow(r.stdout, 42) == nil {
		t.Fatalf("全 project を名前順に並べていない (%v):\n%s", headers, r.stdout)
	}
}

func TestStatusWithNoProjectsIsAUsageError(t *testing.T) {
	s := newSandbox(t)
	if err := os.RemoveAll(s.configRoot); err != nil {
		t.Fatal(err)
	}

	r := s.run("status", "ps")

	assertExit(t, r, 2)
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
	// ISSUE KIND STATE ELAPSED SESSION BRANCH WIP CL TICK (SESSION は claude agents に居ないので `-` の 1 語)
	if row := workerRow(r.stdout, 42); len(row) < 7 || row[6] != "?" {
		t.Fatalf("読めなかった WIP 列が ? になっていない: %v\n%s", row, r.stdout)
	}
	noted := slices.ContainsFunc(strings.Split(r.stdout, "\n"), func(line string) bool {
		return strings.HasPrefix(line, "  ! ") && strings.Contains(line, "wip")
	})
	if !noted {
		t.Fatalf("wip を読めなかった注記の行が無い:\n%s", r.stdout)
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
