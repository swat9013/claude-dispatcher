package blackbox_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// マシンを跨いだ分担: 調停 remote の着手宣言と生存報告 (formats.md §12、system.md §4、ADR 0011・0017・0023)。git は stub
// なので、確かめるのは git に渡す引数と、loop・単発起動・doctor の外から見える振る舞い。死んだ loop の判定は分の単位の経過を
// 待つので、internal/loop の単体テストが持つ。

const (
	emptyTree    = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	claimSHA     = "c1a1c1a1c1a1c1a1c1a1c1a1c1a1c1a1c1a1c1a1"
	heartbeatSHA = "beadbeadbeadbeadbeadbeadbeadbeadbeadbead"
	otherSHA     = "0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e"
	// otherHeartbeatSHA は他の loop の生存報告の sha。自分の生存報告 (heartbeatSHA) と取り違えないよう別の値にする
	otherHeartbeatSHA = "beadbeadbeadbeadbeadbeadbeadbeadbeadbea0"
	otherLoop         = "other-host-1a2b3c4d"
	originURL         = "git@github.com:acme/widgets.git"
)

// coordinated は coordination.remote: origin を書いた workflow 定義の extra (workerWorkflow の hooks の後に足す)。
const coordinated = "coordination:\n  remote: origin\n"

// refPrefix は既定の scope の調停 remote の ref の頭。scope dir は state dir の名前と同じ (formats.md §12)
func (s *sandbox) refPrefix() string {
	return "refs/dispatcher/" + filepath.Base(s.defaultStateDir()) + "/"
}

func (s *sandbox) claimRefOf(n int) string { return s.refPrefix() + "claims/issue-" + strconv.Itoa(n) }

// commitOf は `git cat-file commit` が返す、message を持つ commit。
func commitOf(message string) string {
	return "tree " + emptyTree + "\nauthor claude-dispatcher <claude-dispatcher@invalid> 0 +0000\ncommitter claude-dispatcher <claude-dispatcher@invalid> 0 +0000\n\n" + message
}

// remoteRef は ls-remote に載せる ref と、その commit の message。
type remoteRef struct {
	name, sha, message string
}

// gitRules は調停 remote の git の応答: ls-remote は refs を返し、その sha の commit を読ませ、commit を作り、push は通す。
// before は先頭に置く rule (push の負け・失敗など)。
func (s *sandbox) gitRules(refs []remoteRef, before ...stubwire.Rule) []stubwire.Rule {
	var listed strings.Builder
	rules := append([]stubwire.Rule{}, before...)
	for _, r := range refs {
		listed.WriteString(r.sha + "\t" + r.name + "\n")
		rules = append(rules, stubwire.Rule{ArgsPrefix: []string{"cat-file", "commit", r.sha}, Stdout: commitOf(r.message)})
	}
	return append(rules,
		// 利用者は ssh の撃ち方を決めていない (git は未設定の key に exit 1 を返す)
		stubwire.Rule{ArgsPrefix: []string{"config", "--get", "core.sshCommand"}, Exit: 1},
		stubwire.Rule{ArgsPrefix: []string{"remote", "get-url", "--all", "origin"}, Stdout: originURL + "\n"},
		stubwire.Rule{ArgsPrefix: []string{"remote", "get-url", "--push", "--all", "origin"}, Stdout: originURL + "\n"},
		stubwire.Rule{ArgsPrefix: []string{"ls-remote", "origin", s.refPrefix() + "*"}, Stdout: listed.String()},
		stubwire.Rule{ArgsPrefix: []string{"fetch", "--no-tags", "--no-write-fetch-head", "origin"}},
		stubwire.Rule{ArgsPrefix: []string{"mktree"}, Stdout: emptyTree + "\n"},
		stubwire.Rule{ArgsPrefix: []string{"commit-tree", emptyTree}, ArgsContain: []string{"claude-dispatcher claim"}, Stdout: claimSHA + "\n"},
		stubwire.Rule{ArgsPrefix: []string{"commit-tree", emptyTree}, ArgsContain: []string{"claude-dispatcher heartbeat"}, Stdout: heartbeatSHA + "\n"},
		stubwire.Rule{ArgsPrefix: []string{"push"}, Stdout: "To origin\nDone\n"},
	)
}

// rulesFile は git の応答 rule 列を書き換える FileWrite (push や worker の代役が remote を書き換えたことにする)。
func (s *sandbox) rulesFile(rules []stubwire.Rule) stubwire.FileWrite {
	raw, err := json.Marshal(rules)
	if err != nil {
		s.t.Fatal(err)
	}
	return stubwire.FileWrite{Path: stubwire.ResponsesFile(s.stubRoot, "git"), Content: string(raw)}
}

// claimingRemote は、issue n の着手宣言の push が通ると ls-remote にそれが載り、消す push が通ると載らなくなる git の応答。
func (s *sandbox) claimingRemote(n int) []stubwire.Rule {
	ref := s.claimRefOf(n)
	released := s.gitRules(nil)
	// 消す push は、自分の sha を期待値にした lease (`<ref>:<sha>`) で見分ける
	held := s.gitRules([]remoteRef{{ref, claimSHA, "claude-dispatcher claim\n\nformat: 1\n"}},
		stubwire.Rule{ArgsPrefix: []string{"push"}, ArgsContain: []string{ref + ":" + claimSHA}, Stdout: "To origin\nDone\n", Writes: []stubwire.FileWrite{s.rulesFile(released)}})
	return s.gitRules(nil,
		stubwire.Rule{ArgsPrefix: []string{"push"}, ArgsContain: []string{claimSHA + ":" + ref}, Stdout: "To origin\nDone\n", Writes: []stubwire.FileWrite{s.rulesFile(held)}})
}

// pushes は git の stub が受けた push の argv (argv[1:])。
func (s *sandbox) pushes() [][]string {
	var found [][]string
	for _, c := range s.calls("git") {
		if len(c.Argv) > 1 && c.Argv[1] == "push" {
			found = append(found, c.Argv[1:])
		}
	}
	return found
}

// assertEveryPushSkipsHooks は、調停 remote への push がどれも --no-verify と --porcelain を付けていることを確かめる (ADR 0017)。
func (s *sandbox) assertEveryPushSkipsHooks() {
	s.t.Helper()
	for _, argv := range s.pushes() {
		if !slices.Contains(argv, "--no-verify") || !slices.Contains(argv, "--porcelain") {
			s.t.Fatalf("--no-verify か --porcelain の無い push: %v", argv)
		}
	}
}

// pushed は、push の argv のうち、want の要素をすべて含むものがあるか。
func (s *sandbox) pushed(want ...string) bool {
	return slices.ContainsFunc(s.pushes(), func(argv []string) bool {
		for _, w := range want {
			if !slices.Contains(argv, w) {
				return false
			}
		}
		return true
	})
}

var loopNamePattern = regexp.MustCompile(`^[a-z0-9._-]+-[0-9a-f]{8}$`)

// runOneWorker は、issue 42 に worker を 1 回起動して終える loop を調停 remote を指して回し、止めた後の loop 名を返す。
func (s *sandbox) runOneWorker() string {
	s.t.Helper()
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.setIssues(readyIssue(42))
	done := readyIssue(42)
	done.labels = nil
	s.onClaude(stubwire.Rule{Writes: []stubwire.FileWrite{s.ghResponses(done)}})
	s.respondAll("git", s.claimingRemote(42))

	loop := s.startLoop()
	s.waitEvents("end", 1)
	loop.signal(syscall.SIGINT)
	assertExit(s.t, loop.wait(), 0)

	name, _ := s.events("loop_start")[0]["loop"].(string)
	return name
}

func TestLoopNameIsTheSanitizedHostnameAndEightHex(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.respondAll("git", s.gitRules(nil))

	loop := s.startLoop()
	s.waitEvents("loop_start", 1)
	loop.signal(syscall.SIGINT)
	assertExit(t, loop.wait(), 0)

	name, _ := s.events("loop_start")[0]["loop"].(string)
	host, _ := os.Hostname()
	if !loopNamePattern.MatchString(name) || !strings.HasPrefix(name, strings.ToLower(strings.Split(host, ".")[0])) {
		t.Fatalf("loop 名 = %q, want <hostname を無害化したもの>-<乱数 8 hex>", name)
	}
}

func TestLoopWithACoordinationRemoteClaimsBeforeLaunchingAndDeletesItsRefs(t *testing.T) {
	s := newSandbox(t)

	name := s.runOneWorker()

	claim, heartbeat := s.claimRefOf(42), s.refPrefix()+"loops/"+name
	for _, want := range [][]string{
		{"origin", "+" + heartbeatSHA + ":" + heartbeat},
		{"--force-with-lease=" + claim + ":", "origin", claimSHA + ":" + claim},
		{"--force-with-lease=" + claim + ":" + claimSHA, "origin", ":" + claim},
		{"--force-with-lease=" + heartbeat + ":" + heartbeatSHA, "origin", ":" + heartbeat},
	} {
		if !s.pushed(want...) {
			t.Fatalf("push %v が無い:\n%v", want, s.pushes())
		}
	}
	s.assertEveryPushSkipsHooks()
}

func TestLoopCallsGitFromTheCloneOfTheWorkflowDefinition(t *testing.T) {
	s := newSandbox(t)

	s.runOneWorker()

	for _, c := range s.calls("git") {
		if c.Cwd != s.clone {
			t.Fatalf("git を %s で撃った, want workflow 定義のある clone %s", c.Cwd, s.clone)
		}
	}
}

func TestEveryGitCallToTheCoordinationRemoteDisablesTheTerminalPrompt(t *testing.T) {
	s := newSandbox(t)

	s.runOneWorker()

	s.assertEveryGitCallDisablesTheTerminalPrompt()
}

// assertEveryGitCallDisablesTheTerminalPrompt は、git の stub が受けた呼び出しが 1 回以上あり、どれも
// GIT_TERMINAL_PROMPT=0 を付けていることを確かめる。
func (s *sandbox) assertEveryGitCallDisablesTheTerminalPrompt() {
	s.t.Helper()
	calls := s.calls("git")
	if len(calls) == 0 {
		s.t.Fatal("git を撃っていない")
	}
	for _, c := range calls {
		if c.Env["GIT_TERMINAL_PROMPT"] != "0" {
			s.t.Fatalf("GIT_TERMINAL_PROMPT=0 の無い git の呼び出し: %v (env %v)", c.Argv[1:], c.Env)
		}
		// 利用者が ssh の撃ち方を決めていない sandbox では、ssh の prompt も止める。その判定に撃つ config は ssh を撃たない
		if c.Argv[1] != "config" && c.Env["GIT_SSH_COMMAND"] != "ssh -o BatchMode=yes" {
			t.Fatalf("BatchMode の ssh の無い git の呼び出し: %v (env %v)", c.Argv[1:], c.Env)
		}
	}
}

func TestLoopThatLosesTheClaimDoesNotLaunchAndOnlyLogsIt(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.setIssues(readyIssue(42))
	claim := s.claimRefOf(42)
	s.respondAll("git", s.gitRules(nil, stubwire.Rule{ArgsPrefix: []string{"push"}, ArgsContain: []string{claimSHA + ":" + claim}, Exit: 1,
		Stdout: "To origin\n!\t" + claimSHA + ":" + claim + "\t[remote rejected] (cannot lock ref '" + claim + "': reference already exists)\nDone\n",
		Stderr: "error: failed to push some refs to 'origin'\n"}))

	s.startLoop()
	lost := s.waitEvents("claim_lost", 1)

	if lost[0]["target"] != "issue#42" || lost[0]["trigger"] != "implement" {
		t.Fatalf("claim_lost の行 = %v", lost[0])
	}
	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("負けた作業対象に worker を起動した: %v", calls)
	}
	for _, e := range s.events("error") {
		if e["target"] == "issue#42" {
			t.Fatalf("負けを error の行に書いた: %v", e)
		}
	}
	s.assertEveryPushSkipsHooks()
}

func TestLoopDoesNotLaunchATargetClaimedByAnotherLiveLoop(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.setIssues(readyIssue(42))
	s.respondAll("git", s.gitRules([]remoteRef{
		{s.claimRefOf(42), otherSHA, "claude-dispatcher claim\n\nformat: 1\nloop: " + otherLoop + "\ntarget: issue#42\n"},
		{s.refPrefix() + "loops/" + otherLoop, otherHeartbeatSHA, "claude-dispatcher heartbeat\n\nformat: 1\nloop: " + otherLoop + "\ninterval: 1m0s\nseq: 1\n"},
	}))

	s.startLoop()
	s.waitEvents("tick", 2)

	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("他の loop が着手している作業対象に worker を起動した: %v", calls)
	}
	if s.pushed("origin", claimSHA+":"+s.claimRefOf(42)) {
		t.Fatalf("他の loop の着手宣言がある作業対象に着手宣言を push した: %v", s.pushes())
	}
}

func TestLoopStopsTheWorkerWhoseClaimIsTakenOver(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.setIssues(readyIssue(42))
	claim := s.claimRefOf(42)
	takenOver := s.gitRules([]remoteRef{{claim, otherSHA, "claude-dispatcher claim\n\nformat: 1\nloop: " + otherLoop + "\ntarget: issue#42\n"}})
	// worker が走っている間に、他の loop が着手宣言を置き換えた
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile(), Writes: []stubwire.FileWrite{s.rulesFile(takenOver)}})
	s.respondAll("git", s.claimingRemote(42))

	s.startLoop()
	taken := s.waitEvents("taken_over", 1)
	end := s.waitEvents("end", 1)[0]

	if taken[0]["target"] != "issue#42" || taken[0]["by"] != otherLoop {
		t.Fatalf("taken_over の行 = %v", taken[0])
	}
	if end["outcome"] != "stopped" || end["reason"] != "taken_over" {
		t.Fatalf("end の行 = %v, want 乗っ取られて止めた", end)
	}
	if s.pushed("--force-with-lease="+claim+":"+claimSHA, "origin", ":"+claim) {
		t.Fatalf("乗っ取られた着手宣言を消した: %v", s.pushes())
	}
	waitFor(t, func() bool {
		return strings.Contains(s.statusJSON(), `"taken_over":[{"by":"`+otherLoop+`","target":"issue#42"}]`)
	},
		"状態 file に taken_over が出ない")
	s.assertEveryPushSkipsHooks()
}

func TestLoopNameIsNewOnEveryStart(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.respondAll("git", s.gitRules(nil))

	for range 2 {
		loop := s.startLoop()
		s.waitEvents("loop_start", len(s.events("loop_start"))+1)
		loop.signal(syscall.SIGINT)
		assertExit(t, loop.wait(), 0)
	}

	starts := s.events("loop_start")
	first, _ := starts[0]["loop"].(string)
	second, _ := starts[1]["loop"].(string)
	if !loopNamePattern.MatchString(first) || !loopNamePattern.MatchString(second) || first == second {
		t.Fatalf("loop 名 = %q と %q, want 起動ごとに新しい <hostname>-<乱数 8 hex>", first, second)
	}
}

func TestLoopFailsToStartWhenTheRemoteReadsAndWritesDifferentURLs(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.respondAll("git", s.gitRules(nil, stubwire.Rule{ArgsPrefix: []string{"remote", "get-url", "--push", "--all", "origin"}, Stdout: "git@github.com:me/widgets.git\n"}))

	r := s.run("loop")

	assertExit(t, r, 1)
	if !strings.Contains(r.stderr, "調停 remote origin の読む URL と書く URL が違う") {
		t.Fatalf("stderr = %q", r.stderr)
	}
	if pushes := s.pushes(); len(pushes) != 0 {
		t.Fatalf("起動を失敗させる前に push した: %v", pushes)
	}
}

func TestLoopDoesNotLaunchWhileARefOfANewerFormatIsOnTheRemote(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.setIssues(readyIssue(42))
	s.respondAll("git", s.gitRules([]remoteRef{
		{s.refPrefix() + "loops/" + otherLoop, otherSHA, "claude-dispatcher heartbeat\n\nformat: 2\nloop: " + otherLoop + "\ninterval: 1m0s\n"},
	}))

	s.startLoop()
	s.waitEvents("tick", 2)

	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("新しい版の ref があるのに worker を起動した: %v", calls)
	}
	waitFor(t, func() bool { return strings.Contains(s.statusJSON(), `"newer_format":{"format":2}`) }, "状態 file に newer_format が出ない")
}

func TestLoopWithoutACoordinationRemoteNeverCallsGit(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	done := readyIssue(42)
	done.labels = nil
	s.onClaude(stubwire.Rule{Writes: []stubwire.FileWrite{s.ghResponses(done)}})

	s.startLoop()
	s.waitEvents("end", 1)

	if calls := s.calls("git"); len(calls) != 0 {
		t.Fatalf("調停 remote を指さない loop が git を撃った: %v", calls[0].Argv)
	}
}

func TestDryRunWithACoordinationRemoteDoesNotTouchTheRemote(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.setIssues(readyIssue(42))
	s.respondAll("git", s.gitRules(nil))

	r := s.dryRun()

	assertExit(t, r, 0)
	if calls := s.calls("git"); len(calls) != 0 {
		t.Fatalf("試運転が git を撃った: %v", calls[0].Argv)
	}
}

func TestLoopFailsToStartWhenTheCoordinationRemoteCannotBeUsed(t *testing.T) {
	cases := []struct {
		name string
		rule stubwire.Rule
		want string
	}{
		{"remote を解けない", stubwire.Rule{ArgsPrefix: []string{"remote", "get-url"}, Exit: 2, Stderr: "error: No such remote 'origin'\n"}, "調停 remote origin を解けない"},
		{"ref を読めない", stubwire.Rule{ArgsPrefix: []string{"ls-remote"}, Exit: 128, Stderr: "fatal: Could not read from remote repository.\n"}, "調停 remote origin を読めない"},
		{"生存報告を置けない", stubwire.Rule{ArgsPrefix: []string{"push"}, Exit: 1, Stderr: "remote: Permission denied\n"}, "調停 remote origin に生存報告を置けない"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
			s.respondAll("git", s.gitRules(nil, c.rule))

			r := s.run("loop")

			assertExit(t, r, 1)
			if !strings.Contains(r.stderr, c.want) {
				t.Fatalf("stderr = %q, want %q", r.stderr, c.want)
			}
		})
	}
}

func TestRunWithACoordinationRemotePutsAndDeletesItsClaimAndHeartbeat(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.setIssues(readyIssue(42))
	done := readyIssue(42)
	done.labels = nil
	s.onClaude(stubwire.Rule{Writes: []stubwire.FileWrite{s.ghResponses(done)}})
	s.respondAll("git", s.claimingRemote(42))

	r := s.run("run", "implement", "issue#42")

	assertExit(t, r, 0)
	claim := s.claimRefOf(42)
	for _, want := range [][]string{
		{"--force-with-lease=" + claim + ":", "origin", claimSHA + ":" + claim},
		{"--force-with-lease=" + claim + ":" + claimSHA, "origin", ":" + claim},
	} {
		if !s.pushed(want...) {
			t.Fatalf("push %v が無い:\n%v", want, s.pushes())
		}
	}
	heartbeats := 0
	for _, argv := range s.pushes() {
		if strings.Contains(strings.Join(argv, " "), s.refPrefix()+"loops/") {
			heartbeats++
		}
	}
	if heartbeats != 2 {
		t.Fatalf("生存報告の push = %d 回, want 置くのと消すので 2 回:\n%v", heartbeats, s.pushes())
	}
	s.assertEveryPushSkipsHooks()
	s.assertEveryGitCallDisablesTheTerminalPrompt()
}

func TestRunDoesNotLaunchATargetClaimedByAnotherLoop(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.setIssues(readyIssue(42))
	s.respondAll("git", s.gitRules([]remoteRef{
		{s.claimRefOf(42), otherSHA, "claude-dispatcher claim\n\nformat: 1\nloop: " + otherLoop + "\ntarget: issue#42\n"},
	}))

	r := s.run("run", "implement", "issue#42")

	assertExit(t, r, 5)
	if !strings.Contains(r.stderr, "issue#42 は loop "+otherLoop+" が着手しているので起動しない") {
		t.Fatalf("stderr = %q", r.stderr)
	}
	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("worker を起動した: %v", calls)
	}
}

func TestDoctorShowsAReadableCoordinationRemoteWithoutPushing(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.respondAll("git", s.gitRules(nil))

	r := s.run("doctor")

	assertExit(t, r, 0)
	if !strings.Contains(r.stdout, "ok   調停 remote origin ("+originURL+")\n") {
		t.Fatalf("stdout = %q", r.stdout)
	}
	if pushes := s.pushes(); len(pushes) != 0 {
		t.Fatalf("doctor が push を撃った: %v", pushes)
	}
	s.assertEveryGitCallDisablesTheTerminalPrompt()
}

func TestDoctorFailsOnAnUnreadableCoordinationRemoteWithoutPushing(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.respondAll("git", s.gitRules(nil, stubwire.Rule{ArgsPrefix: []string{"ls-remote"}, Exit: 128, Stderr: "fatal: unable to access\n"}))

	r := s.run("doctor")

	assertExit(t, r, 1)
	if !strings.Contains(r.stdout, "NG   調停 remote origin を読めない: ") {
		t.Fatalf("stdout = %q", r.stdout)
	}
	if pushes := s.pushes(); len(pushes) != 0 {
		t.Fatalf("doctor が push を撃った: %v", pushes)
	}
}

func TestRunThatLosesTheClaimDoesNotLaunch(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.setIssues(readyIssue(42))
	claim := s.claimRefOf(42)
	s.respondAll("git", s.gitRules(nil, stubwire.Rule{ArgsPrefix: []string{"push"}, ArgsContain: []string{claimSHA + ":" + claim}, Exit: 1,
		Stdout: "To origin\n!\t" + claimSHA + ":" + claim + "\t[remote rejected] (cannot lock ref '" + claim + "': reference already exists)\nDone\n",
		Stderr: "error: failed to push some refs to 'origin'\n"}))

	r := s.run("run", "implement", "issue#42")

	assertExit(t, r, 5)
	if !strings.Contains(r.stderr, "issue#42 は他の loop が先に着手したので起動しない") {
		t.Fatalf("stderr = %q", r.stderr)
	}
	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("worker を起動した: %v", calls)
	}
}

func TestRunDoesNotLaunchWhileARefOfANewerFormatIsOnTheRemote(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.setIssues(readyIssue(42))
	s.respondAll("git", s.gitRules([]remoteRef{
		{s.refPrefix() + "loops/" + otherLoop, otherSHA, "claude-dispatcher heartbeat\n\nformat: 2\nloop: " + otherLoop + "\ninterval: 1m0s\n"},
	}))

	r := s.run("run", "implement", "issue#42")

	assertExit(t, r, 5)
	if !strings.Contains(r.stderr, "調停 remote に新しい版の ref があるので起動しない (dispatcher を上げる)") {
		t.Fatalf("stderr = %q", r.stderr)
	}
	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("worker を起動した: %v", calls)
	}
}

func TestLoopThatFailsToTakeTheLockPutsNoHeartbeat(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.respondAll("git", s.gitRules(nil))
	first := s.startLoop()
	s.waitEvents("loop_start", 1)
	before := len(s.pushes())

	second := s.run("loop")

	assertExit(t, second, 3)
	if after := len(s.pushes()); after != before {
		t.Fatalf("lock を取れずに失敗する起動が push した: %v", s.pushes()[before:])
	}
	first.signal(syscall.SIGINT)
	assertExit(t, first.wait(), 0)
}

func TestLoopThatCannotOpenItsLogPutsNoHeartbeat(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(coordinated))
	s.respondAll("git", s.gitRules(nil))
	// log.jsonl の置き場を dir で塞ぐ
	if err := os.MkdirAll(filepath.Join(s.defaultStateDir(), "log.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := s.run("loop")

	assertExit(t, r, 1)
	if !strings.Contains(r.stderr, "log.jsonl を開けない") {
		t.Fatalf("stderr = %q", r.stderr)
	}
	if pushes := s.pushes(); len(pushes) != 0 {
		t.Fatalf("log.jsonl を開けずに失敗する起動が push した: %v", pushes)
	}
}

// statusJSON は状態 file の中身 (空白を詰めた JSON)。無ければ ""。
func (s *sandbox) statusJSON() string {
	raw, err := os.ReadFile(filepath.Join(s.defaultStateDir(), "status.json"))
	if err != nil {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	compact, _ := json.Marshal(v)
	return string(compact)
}
