package blackbox_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

// `loop` の起動時の検査・scope key の lock・停止 (formats.md §6)。
// 周期 (1m 以上) を待つ振る舞い (tick ごとの読み直し) は internal/loop の単体テストが持つ。

var (
	loopStartedLine = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z loop を始めた: `)
	firstTickLine   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z tick `)
	loopStoppedLine = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z loop を止めた \(停止要求 SIGINT\)$`)
)

func TestLoopTicksRightAfterStartingAndShowsTheCandidates(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42), readyIssue(43))

	loop := s.startLoop()

	line := loop.waitForOutput(firstTickLine)
	if !strings.HasSuffix(line, " tick ok · 候補 2: implement issue #42, implement issue #43") {
		t.Fatalf("tick の行 = %q", line)
	}
}

func TestLoopNamesItsScopeKeyAndStateDirWhenItStarts(t *testing.T) {
	s := newSandbox(t)

	loop := s.startLoop()

	line := loop.waitForOutput(loopStartedLine)
	want := "scope github.com/acme/widgets · state dir " + s.stateDir("github.com/acme/widgets") + " · workflow " + s.workflowFile()
	if !strings.HasSuffix(line, want) {
		t.Fatalf("起動の行 = %q, want 末尾 %q", line, want)
	}
	if info, err := os.Stat(s.stateDir("github.com/acme/widgets")); err != nil || !info.IsDir() {
		t.Fatalf("loop が state dir を作っていない: %v", err)
	}
}

func TestSecondLoopOnTheSameIssueRepoIsRefusedAtStart(t *testing.T) {
	s := newSandbox(t)
	first := s.startLoop()
	first.waitForOutput(firstTickLine)
	// 別の path の workflow 定義でも、issue 置き場が同じ (綴りの大文字と小文字だけ違う) なら同じ scope key
	other := filepath.Join(s.root, "other-clone", "WORKFLOW.md")
	mustWrite(t, other, strings.Replace(defaultWorkflow, "repo: acme/widgets", "repo: Acme/Widgets", 1))

	r := s.run("loop", other)

	assertExit(t, r, 3)
	if !strings.Contains(r.stderr, "github.com/acme/widgets") {
		t.Fatalf("stderr が scope key を名指ししていない: %q", r.stderr)
	}
	if !first.running() {
		t.Fatal("2 本目の起動で 1 本目が止まった")
	}
}

func TestLoopOnAnotherIssueRepoRunsAlongside(t *testing.T) {
	s := newSandbox(t)
	first := s.startLoop()
	first.waitForOutput(firstTickLine)
	other := filepath.Join(s.root, "other-clone", "WORKFLOW.md")
	mustWrite(t, other, strings.Replace(defaultWorkflow, "repo: acme/widgets", "repo: acme/gadgets", 1))

	second := s.startLoop(other)

	second.waitForOutput(firstTickLine)
}

func TestLoopStopsOnAStopRequest(t *testing.T) {
	s := newSandbox(t)
	loop := s.startLoop()
	loop.waitForOutput(firstTickLine)

	loop.signal(syscall.SIGINT)

	r := loop.wait()
	assertExit(t, r, 0)
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if !loopStoppedLine.MatchString(lines[len(lines)-1]) {
		t.Fatalf("最後の行 = %q, want 停止の行", lines[len(lines)-1])
	}
}

func TestLoopReleasesTheLockWhenItStops(t *testing.T) {
	s := newSandbox(t)
	first := s.startLoop()
	first.waitForOutput(firstTickLine)
	first.signal(syscall.SIGINT)
	assertExit(t, first.wait(), 0)

	second := s.startLoop()

	second.waitForOutput(firstTickLine)
}

func TestLoopWithABrokenWorkflowDefinitionFailsToStartWithoutWritingAnything(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(strings.Replace(defaultWorkflow, "on: issue", "on: pr", 1))

	r := s.run("loop")

	s.assertRejected(r, "triggers[0].on")
	if _, err := os.Stat(s.stateRoot); !os.IsNotExist(err) {
		t.Fatalf("起動に失敗した loop が state root を作った: %v", err)
	}
}

func TestLoopThatCannotCreateItsStateDirFailsToStart(t *testing.T) {
	s := newSandbox(t)
	// state root の親を file で塞ぐ
	mustWrite(t, filepath.Dir(s.stateRoot), "")

	r := s.run("loop")

	assertExit(t, r, 1)
	if !strings.Contains(r.stderr, s.stateRoot) {
		t.Fatalf("stderr が state dir を名指ししていない: %q", r.stderr)
	}
}
