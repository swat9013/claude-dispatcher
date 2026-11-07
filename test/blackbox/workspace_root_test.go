package blackbox_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// workspace root の専有 (formats.md §1・§5・§6・§7.4・§11、system.md §7)。読み書きの細部は internal/workspace の単体テストが
// 持ち、ここでは subcommand ごとの exit code と、worker を起動しないこと・workspace を消さないことを見る。

const otherScopeKey = "github.com/acme/gadgets"

func (s *sandbox) workspaceRoot() string {
	return filepath.Join(s.clone, ".claude-dispatcher", "workspaces")
}

func (s *sandbox) scopeKeyFile() string { return filepath.Join(s.workspaceRoot(), ".scope-key") }

// rootInUseLine は別の scope key が使っている root を名指しする文。
func (s *sandbox) rootInUseLine() string {
	return "workspace.root " + s.workspaceRoot() + " は scope key " + otherScopeKey + " が使っている"
}

// occupyRoot は別の scope key の loop が root を使っている状態を作る。
func (s *sandbox) occupyRoot() {
	s.t.Helper()
	mustWrite(s.t, s.scopeKeyFile(), otherScopeKey+"\n")
}

// breakScopeKeyFile は .scope-key の path に dir を置いて読めなくする。
func (s *sandbox) breakScopeKeyFile() {
	s.t.Helper()
	mustMkdir(s.t, s.scopeKeyFile())
}

func (s *sandbox) assertScopeKeyFile(want string) {
	s.t.Helper()
	got, err := os.ReadFile(s.scopeKeyFile())
	if err != nil {
		s.t.Fatal(err)
	}
	if string(got) != want {
		s.t.Fatalf(".scope-key = %q, want %q", got, want)
	}
}

func (s *sandbox) assertNoClaude() {
	s.t.Helper()
	if calls := s.calls("claude"); len(calls) != 0 {
		s.t.Fatalf("claude を起動した: %v", calls)
	}
}

// writeDefinition は clone に、issue 置き場が repo で、rootLine (空なら既定) の workflow 定義 name を書く。
func (s *sandbox) writeDefinition(name, repo, rootLine string) {
	s.t.Helper()
	content := strings.Replace(defaultWorkflow, "repo: acme/widgets\n", "repo: "+repo+"\n"+rootLine, 1)
	mustWrite(s.t, filepath.Join(s.clone, name), content)
}

var loopStarted = regexp.MustCompile("loop を始めた")

func TestLoopRefusesARootThatAnotherScopeKeyUsesAndLeavesItsWorkspaces(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.occupyRoot()
	// issue#7 は自分の issue 置き場に無い。掃除が走れば終端と読んで消す
	mustWrite(t, filepath.Join(s.workspace(7), "work"), "別の loop の作業\n")

	r := s.run("loop")

	s.assertRejected(r, s.rootInUseLine())
	s.assertNoClaude()
	if _, err := os.Stat(filepath.Join(s.workspace(7), "work")); err != nil {
		t.Fatalf("別の loop の workspace を消した: %v", err)
	}
	s.assertScopeKeyFile(otherScopeKey + "\n")
}

func TestRunRefusesARootThatAnotherScopeKeyUses(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.occupyRoot()

	r := s.run("run", "implement", "issue#42")

	s.assertRejected(r, s.rootInUseLine())
	s.assertNoClaude()
	s.assertScopeKeyFile(otherScopeKey + "\n")
}

func TestDryRunRefusesARootThatAnotherScopeKeyUsesWithoutWriting(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.occupyRoot()

	r := s.dryRun()

	s.assertRejected(r, s.rootInUseLine())
	s.assertScopeKeyFile(otherScopeKey + "\n")
}

func TestDryRunDoesNotWriteTheScopeKey(t *testing.T) {
	s := newSandbox(t)

	r := s.dryRun()

	assertExit(t, r, 0)
	if _, err := os.Stat(s.workspaceRoot()); !os.IsNotExist(err) {
		t.Fatalf("試運転が workspace root を作った: %v", err)
	}
}

func TestDryRunExitsOneWhenTheScopeKeyFileCannotBeRead(t *testing.T) {
	s := newSandbox(t)
	s.breakScopeKeyFile()

	r := s.dryRun()

	assertExit(t, r, 1)
	if !strings.Contains(r.stderr, s.scopeKeyFile()) {
		t.Fatalf("stderr が .scope-key を名指ししていない: %q", r.stderr)
	}
}

func TestDoctorReportsARootThatAnotherScopeKeyUsesWithoutWriting(t *testing.T) {
	s := newSandbox(t)
	s.occupyRoot()

	r := s.run("doctor")

	assertExit(t, r, 1)
	if !strings.Contains(r.stdout, "NG   "+s.rootInUseLine()+"\n") {
		t.Fatalf("stdout = %q", r.stdout)
	}
	s.assertScopeKeyFile(otherScopeKey + "\n")
}

func TestDoctorPassesARootWithoutAScopeKeyFileWithoutWriting(t *testing.T) {
	s := newSandbox(t)

	r := s.run("doctor")

	assertExit(t, r, 0)
	if !strings.Contains(r.stdout, "ok   workspace.root "+s.workspaceRoot()+"\n") {
		t.Fatalf("stdout = %q", r.stdout)
	}
	if _, err := os.Stat(s.workspaceRoot()); !os.IsNotExist(err) {
		t.Fatalf("doctor が workspace root を作った: %v", err)
	}
}

func TestDoctorReportsAScopeKeyFileItCannotRead(t *testing.T) {
	s := newSandbox(t)
	s.breakScopeKeyFile()

	r := s.run("doctor")

	assertExit(t, r, 1)
	if !regexp.MustCompile(`(?m)^NG   .*` + regexp.QuoteMeta(s.scopeKeyFile())).MatchString(r.stdout) {
		t.Fatalf("stdout = %q", r.stdout)
	}
}

func TestLoopWritesItsScopeKeyAndStartsAgainOnTheSameRoot(t *testing.T) {
	s := newSandbox(t)

	first := s.startLoop()
	first.waitForOutput(loopStarted)
	s.assertScopeKeyFile(defaultScopeKey + "\n")
	first.signal(syscall.SIGTERM)
	first.wait()

	again := s.startLoop()
	again.waitForOutput(loopStarted)
	s.assertScopeKeyFile(defaultScopeKey + "\n")
}

func TestRunWritesItsScopeKeyOnARootWithoutOne(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})

	s.run("run", "implement", "issue#42")

	s.assertScopeKeyFile(defaultScopeKey + "\n")
}

func TestOnlyOneOfTwoLoopsStartedTogetherOnAnEmptyRootKeepsRunning(t *testing.T) {
	s := newSandbox(t)
	s.writeDefinition("WORKFLOW.widgets.md", "acme/widgets", "")
	s.writeDefinition("WORKFLOW.gadgets.md", "acme/gadgets", "")

	widgets := s.startLoop("WORKFLOW.widgets.md")
	gadgets := s.startLoop("WORKFLOW.gadgets.md")

	waitFor(t, func() bool { return !widgets.running() || !gadgets.running() }, "どちらの loop も起動時に失敗しない")
	winner, loser, owner := gadgets, widgets, otherScopeKey
	if !gadgets.running() {
		winner, loser, owner = widgets, gadgets, defaultScopeKey
	}
	assertExit(t, loser.wait(), 2)
	winner.waitForOutput(loopStarted)
	s.assertScopeKeyFile(owner + "\n")
}

func TestLoopExitsOneWithoutAWorkerWhenTheScopeKeyFileCannotBeRead(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.breakScopeKeyFile()

	r := s.run("loop")

	assertExit(t, r, 1)
	s.assertNoClaude()
}

func TestRunExitsOneWithoutAWorkerWhenTheScopeKeyFileCannotBeRead(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.breakScopeKeyFile()

	r := s.run("run", "implement", "issue#42")

	assertExit(t, r, 1)
	s.assertNoClaude()
}

func TestLoopExitsOneWhenTheRootCannotBeCreated(t *testing.T) {
	s := newSandbox(t)
	mustWrite(t, filepath.Join(s.clone, "blocked"), "")
	s.writeDefinition("WORKFLOW.blocked.md", "acme/widgets", "workspace:\n  root: blocked/workspaces\n")

	r := s.run("loop", "WORKFLOW.blocked.md")

	assertExit(t, r, 1)
}

func TestTwoDefinitionsInOneDirStartTwoLoopsWhenTheirRootsDiffer(t *testing.T) {
	s := newSandbox(t)
	s.writeDefinition("WORKFLOW.widgets.md", "acme/widgets", "workspace:\n  root: workspaces/widgets\n")
	s.writeDefinition("WORKFLOW.gadgets.md", "acme/gadgets", "workspace:\n  root: workspaces/gadgets\n")

	widgets := s.startLoop("WORKFLOW.widgets.md")
	gadgets := s.startLoop("WORKFLOW.gadgets.md")

	widgets.waitForOutput(loopStarted)
	gadgets.waitForOutput(loopStarted)
	if !widgets.running() || !gadgets.running() {
		t.Fatalf("loop が止まった: widgets %v, gadgets %v", widgets.running(), gadgets.running())
	}
}

func TestLoopRefusesARootWithoutAScopeKeyThatHoldsAnotherProjectsWorkspace(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	mustWrite(t, filepath.Join(s.workspace(7), "work"), "別の loop の作業\n")

	r := s.run("loop")

	s.assertRejected(r, "workspace.root "+s.workspaceRoot()+" に別の project の workspace issue-7 がある")
	s.assertNoClaude()
	if _, err := os.Stat(filepath.Join(s.workspace(7), "work")); err != nil {
		t.Fatalf("別の loop の workspace を消した: %v", err)
	}
}

func TestDryRunAndDoctorReportAnotherProjectsWorkspaceWithoutWriting(t *testing.T) {
	s := newSandbox(t)
	mustMkdir(t, s.workspace(7))
	line := "workspace.root " + s.workspaceRoot() + " に別の project の workspace issue-7 がある"

	dry := s.dryRun()
	assertExit(t, dry, 2)
	if !strings.Contains(dry.stderr, line) {
		t.Fatalf("stderr = %q", dry.stderr)
	}

	doctor := s.run("doctor")

	assertExit(t, doctor, 1)
	if !strings.Contains(doctor.stdout, "NG   "+line+"\n") {
		t.Fatalf("stdout = %q", doctor.stdout)
	}
	if _, err := os.Stat(s.scopeKeyFile()); !os.IsNotExist(err) {
		t.Fatalf(".scope-key を書いた: %v", err)
	}
}

func TestLoopClaimsARootWhoseWorkspacesItLaunched(t *testing.T) {
	s := newSandbox(t)
	mustMkdir(t, s.workspace(7))
	mustWrite(t, filepath.Join(s.defaultStateDir(), "workers", "issue-7.log"), "")

	p := s.startLoop()

	p.waitForOutput(loopStarted)
	s.assertScopeKeyFile(defaultScopeKey + "\n")
}
