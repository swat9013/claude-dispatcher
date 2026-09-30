package blackbox_test

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
	s.writeSpawnedTickLineWithTS(spawnedTS)
}

// writeSpawnedTickLineAt は issue 42 の worker を at に起動した tick 行を log.jsonl に置く。
func (s *sandbox) writeSpawnedTickLineAt(at time.Time) {
	s.t.Helper()
	s.writeSpawnedTickLineWithTS(at.UTC().Format("2006-01-02T15:04:05.000000Z"))
}

// writeSpawnedTickLineWithTS は issue 42 の worker を起動した tick 行を置く。ts は log.jsonl の綴り (2006-01-02T15:04:05.000000Z)。
func (s *sandbox) writeSpawnedTickLineWithTS(ts string) {
	s.t.Helper()
	line := map[string]any{
		"ts": ts, "project": s.project, "cwd": s.clone, "result": "ok",
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
	s.respondLatestCLs(map[int]latestCLPage{42: {cls: []latestCL{{number: 57, state: "OPEN"}}}})
}

// latestCL は status の CL 列の問い合わせ (branch ごとに新しい順の CL) が返す CL 1 本。
type latestCL struct {
	number int
	state  string
	fork   bool
}

// latestCLPage は 1 issue 分の応答。hasMore は cls の後にまだ古い CL が続く (totalCount が cls の本数を超える)
type latestCLPage struct {
	cls     []latestCL
	hasMore bool
}

// respondLatestCLs は `gh api graphql` の headRefName の問い合わせに、issue ごとの CL の列を返す (GitHub GraphQL の形)。
func (s *sandbox) respondLatestCLs(pages map[int]latestCLPage) {
	s.t.Helper()
	repository := map[string]any{}
	for issue, page := range pages {
		nodes := make([]map[string]any, 0, len(page.cls))
		for _, cl := range page.cls {
			nodes = append(nodes, map[string]any{"number": cl.number, "state": cl.state, "isCrossRepository": cl.fork})
		}
		total := len(page.cls)
		if page.hasMore {
			total++
		}
		repository[fmt.Sprintf("i%d", issue)] = map[string]any{"totalCount": total, "nodes": nodes}
	}
	s.respond("gh", stubwire.Rule{ArgsPrefix: []string{"api", "graphql"}, ArgContains: "headRefName",
		Stdout: mustJSON(s.t, map[string]any{"data": map[string]any{"repository": repository}})})
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

func TestStatusCLColumnSkipsAForkCLOnTheWorkerBranchName(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.workerAlive()
	s.setWip(42)
	// 最新は fork から同じ branch 名で開かれた #60。worker の CL は #57
	s.respondLatestCLs(map[int]latestCLPage{42: {cls: []latestCL{{number: 60, state: "OPEN", fork: true}, {number: 57, state: "MERGED"}}}})

	r := s.statusPS()

	row := strings.Join(workerRow(r.stdout, 42), " ")
	if !strings.Contains(row, "#57 MERGED") || strings.Contains(row, "#60") {
		t.Fatalf("#42 の CL 列が fork でない最新の CL (#57 MERGED) でない: %s\n%s", row, r.stdout)
	}
}

func TestStatusCLColumnIsUnknownOnlyForAnIssueWhoseWindowHoldsOnlyForkCLs(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	line := map[string]any{
		"ts": spawnedTS, "project": s.project, "cwd": s.clone, "result": "ok",
		"spawned": []map[string]any{
			{"issue": 42, "kind": "start", "pid": workerPID, "log": "/x.log", "session_id": workerSession},
			{"issue": 43, "kind": "start", "pid": workerPID + 1, "log": "/y.log", "session_id": "43-session"},
		},
	}
	mustWrite(t, s.logFile(), mustJSON(t, line)+"\n")
	s.workerAlive()
	s.setWip(42, 43)
	forks := make([]latestCL, 0, 10)
	for n := range 10 {
		forks = append(forks, latestCL{number: 200 + n, state: "CLOSED", fork: true})
	}
	// 42 は新しい 10 本が fork の CL だけで、まだ続きがある。43 は worker の CL #58 がある
	s.respondLatestCLs(map[int]latestCLPage{
		42: {cls: forks, hasMore: true},
		43: {cls: []latestCL{{number: 58, state: "OPEN"}}},
	})

	r := s.statusPS()

	if row := workerRow(r.stdout, 42); len(row) < 2 || row[len(row)-2] != "?" {
		t.Fatalf("#42 の CL 列が ? でない: %v\n%s", row, r.stdout)
	}
	if row := strings.Join(workerRow(r.stdout, 43), " "); !strings.Contains(row, "#58 OPEN") {
		t.Fatalf("#43 の CL 列が #58 OPEN でない: %s\n%s", row, r.stdout)
	}
}

func TestStatusShowsAWorkerWhoseProcessIsGoneButWipRemainsAsStale(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.setWip(42)

	r := s.statusPS()

	if row := workerRow(r.stdout, 42); len(row) == 0 || row[2] != "stale" || !strings.Contains(strings.Join(row, " "), "yes") {
		t.Fatalf("stale wip の行 = %v\n%s", row, r.stdout)
	}
}

func TestStatusDoesNotMistakeAReusedPidForTheWorker(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.setProcesses(fmt.Sprintf("%d /usr/bin/vim notes.txt", workerPID))
	s.setWip(42)

	r := s.statusPS()

	if row := workerRow(r.stdout, 42); len(row) < 3 || row[2] != "stale" {
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
	if !slices.Equal(headers, []string{"other", s.project}) {
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

func TestStatusReadsTheLoopOfTheProjectFromTheProcessList(t *testing.T) {
	for _, tc := range []struct {
		name, process, want string
	}{
		{"running", "777 /home/me/go/bin/claude-dispatcher loop " + defaultProject + " 5m", "loop 稼働中"},
		{"other-project", "777 /home/me/go/bin/claude-dispatcher loop other 5m", "loop なし"},
		{"single-tick", "777 /home/me/go/bin/claude-dispatcher tick " + defaultProject, "loop なし"},
		// worker の command 行には spawn prompt が載る。prompt の中の綴りを loop と取り違えない
		{"in-a-prompt", "778 /usr/local/bin/claude -p run claude-dispatcher loop " + defaultProject + " 5m --session-id x", "loop なし"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.statusScenario()
			s.setWip()
			s.setProcesses(tc.process)

			r := s.statusPS()

			if first := strings.SplitN(r.stdout, "\n", 2)[0]; first != s.project+"  "+tc.want+"  最終 tick 2026-09-26T02:48:00Z ok" {
				t.Fatalf("1 行目 = %q, want 2 欄目が %s", first, tc.want)
			}
		})
	}
}

func TestStatusMarksTheLoopUnknownWhenTheProcessListCannotBeRead(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.setWip()
	s.respond("ps", stubwire.Rule{Exit: 1, Stderr: "ps: not permitted\n"})

	r := s.statusPS()

	if first := strings.SplitN(r.stdout, "\n", 2)[0]; !strings.Contains(first, "  loop ?  ") {
		t.Fatalf("1 行目 = %q, want loop ?", first)
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
