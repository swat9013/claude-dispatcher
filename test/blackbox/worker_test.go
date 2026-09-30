package blackbox_test

import (
	"bufio"
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

// worker の起動・完了の確認・停止 (formats.md §4 / §6、system.md §7)。claude は stub で、応答 rule の Writes で
// 外部 store (gh の応答) を書き換えて worker の成果の代わりにする。

// workerWorkflow は周期 1s で、hooks が sandbox の marks dir に印を残す workflow 定義。action と本文は template。
// sandbox の PATH は stub だけなので、hooks は shell の組み込みだけで書く。
func (s *sandbox) workerWorkflow(extra string) string {
	marks := s.marksDir()
	mustMkdir(s.t, marks)
	return `---
tracker:
  kind: github
  repo: acme/widgets
polling:
  interval: 1s
hooks:
  after_create: echo "$CLAUDE_DISPATCHER_KIND $CLAUDE_DISPATCHER_NUMBER $CLAUDE_DISPATCHER_CLONE" > "` + marks + `/after_create-$CLAUDE_DISPATCHER_NUMBER"
  before_run: pwd > "` + marks + `/before_run-$CLAUDE_DISPATCHER_NUMBER"
  after_run: ': > "` + marks + `/after_run-$CLAUDE_DISPATCHER_NUMBER"'
  before_remove: ': > "` + marks + `/before_remove-$CLAUDE_DISPATCHER_NUMBER"'
` + extra + `triggers:
  - name: implement
    on: issue
    when: {labels: {all: [ready-for-agent]}}
    action: "/implement issue #{{ .issue.number }} ({{ .trigger.name }}, attempt {{ .attempt }})"
---
共通 prompt for {{ .issue.title }} in {{ .workspace }}
`
}

func (s *sandbox) marksDir() string { return filepath.Join(s.root, "marks") }

func (s *sandbox) mark(name string) string { return filepath.Join(s.marksDir(), name) }

func (s *sandbox) workspace(number int) string {
	return filepath.Join(s.clone, ".claude-dispatcher", "workspaces", "issue-"+strconv.Itoa(number))
}

// onClaude は claude の stub の応答を r にする。stdout は stream-json の 1 行を既定で出す。release を待つ stub は、
// テストの後片付けで release して、テストが先に終わっても居残らせない。
func (s *sandbox) onClaude(r stubwire.Rule) {
	s.t.Helper()
	if r.ReleaseFile != "" {
		s.t.Cleanup(func() { _ = os.WriteFile(r.ReleaseFile, nil, 0o644) })
	}
	if r.Stdout == "" {
		r.Stdout = `{"type":"result","subtype":"success"}` + "\n"
	}
	s.respond("claude", r)
}

func (s *sandbox) releaseFile() string { return filepath.Join(s.root, "release") }

func (s *sandbox) release() { mustWrite(s.t, s.releaseFile(), "") }

// logLines は log.jsonl の完全な行を返す。
func (s *sandbox) logLines() []map[string]any {
	s.t.Helper()
	f, err := os.Open(filepath.Join(s.defaultStateDir(), "log.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []map[string]any
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var line map[string]any
		if json.Unmarshal(scanner.Bytes(), &line) == nil {
			lines = append(lines, line)
		}
	}
	return lines
}

// events は log.jsonl のうち event が name の行を返す。
func (s *sandbox) events(name string) []map[string]any {
	var found []map[string]any
	for _, line := range s.logLines() {
		if line["event"] == name {
			found = append(found, line)
		}
	}
	return found
}

// waitEvents は event が name の行が n 行になるまで待つ。
func (s *sandbox) waitEvents(name string, n int) []map[string]any {
	s.t.Helper()
	waitFor(s.t, func() bool { return len(s.events(name)) >= n }, "log.jsonl に "+name+" の行が "+strconv.Itoa(n)+" 行にならない")
	return s.events(name)
}

func TestIssueMatchingATriggerGetsAWorkerInItsWorkspaceAndIsCompletedWhenItLeavesTheTrigger(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	done := readyIssue(42)
	done.labels = nil
	s.onClaude(stubwire.Rule{Writes: []stubwire.FileWrite{s.ghResponses(done)}})

	s.startLoop()

	end := s.waitEvents("end", 1)[0]
	if end["target"] != "issue#42" || end["outcome"] != "completed" || end["trigger"] != "implement" {
		t.Fatalf("end の行 = %v", end)
	}
	calls := s.calls("claude")
	if len(calls) != 1 || calls[0].Cwd != s.workspace(42) {
		t.Fatalf("claude の呼び出し = %d 回 (cwd %v), want workspace %s で 1 回", len(calls), calls, s.workspace(42))
	}
}

func TestCompletedIssueIsNotLaunchedAgain(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	done := readyIssue(42)
	done.labels = nil
	s.onClaude(stubwire.Rule{Writes: []stubwire.FileWrite{s.ghResponses(done)}})
	s.startLoop()
	s.waitEvents("end", 1)

	ticks := len(s.events("tick"))
	s.waitEvents("tick", ticks+2)

	if calls := s.calls("claude"); len(calls) != 1 {
		t.Fatalf("claude の呼び出し = %d 回, want 1 (trigger から外れた issue を起動し直した)", len(calls))
	}
}

func TestWorkspaceIsCreatedWithHooksBeforeTheWorkerRuns(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})

	s.startLoop()

	s.waitEvents("start", 1)
	if got := strings.TrimSpace(string(mustRead(t, s.mark("after_create-42")))); got != "issue 42 "+s.clone {
		t.Fatalf("after_create の環境 = %q", got)
	}
	if got := strings.TrimSpace(string(mustRead(t, s.mark("before_run-42")))); got != s.workspace(42) {
		t.Fatalf("before_run の cwd = %q, want %s", got, s.workspace(42))
	}
}

func TestWorkerGetsTheCommonPromptAsSystemPromptAndTheActionAsTheUserPrompt(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow("claude:\n  args: [--permission-mode, auto]\n"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})

	s.startLoop()

	start := s.waitEvents("start", 1)[0]
	argv := s.calls("claude")[0].Argv[1:]
	file := argValueAfter(argv, "--append-system-prompt-file")
	want := []string{"--permission-mode", "auto", "-p", "--output-format", "stream-json", "--verbose",
		"--session-id", asString(start["session_id"]), "--append-system-prompt-file", file,
		"--", "/implement issue #42 (implement, attempt 1)"}
	if !slices.Equal(argv, want) {
		t.Fatalf("argv = %q\nwant %q", argv, want)
	}
	if got := string(mustRead(t, file)); got != "共通 prompt for issue 42 in "+s.workspace(42)+"\n" {
		t.Fatalf("共通 prompt = %q", got)
	}
}

func TestSessionIDIsAUUIDAndIsLoggedWithTheStart(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})

	s.startLoop()

	start := s.waitEvents("start", 1)[0]
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(asString(start["session_id"])) {
		t.Fatalf("session_id = %v", start["session_id"])
	}
}

func TestWorkerThatLeavesTheTriggerMidwayIsNotStopped(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	s.startLoop()
	s.waitEvents("start", 1)
	offTrigger := readyIssue(42)
	offTrigger.labels = nil

	s.setIssues(offTrigger)

	ticks := len(s.events("tick"))
	s.waitEvents("tick", ticks+2)
	if ends := s.events("end"); len(ends) != 0 {
		t.Fatalf("trigger から外れただけの worker を止めた: %v", ends)
	}
}

func TestWorkerOfAnIssueThatIsClosedIsStoppedAndItsWorkspaceRemoved(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	s.startLoop()
	s.waitEvents("start", 1)
	closed := readyIssue(42)
	closed.closed = true

	s.setIssues(closed)

	end := s.waitEvents("end", 1)[0]
	if end["outcome"] != "stopped" {
		t.Fatalf("end の行 = %v, want stopped", end)
	}
	for _, name := range []string{"after_run-42", "before_remove-42"} {
		if _, err := os.Stat(s.mark(name)); err != nil {
			t.Fatalf("%s を撃っていない: %v", name, err)
		}
	}
	if _, err := os.Stat(s.workspace(42)); !os.IsNotExist(err) {
		t.Fatalf("終端の issue の workspace が残っている: %v", err)
	}
}

func TestWorkerThatEndsStillMatchingTheTriggerFails(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})

	s.startLoop()

	end := s.waitEvents("end", 1)[0]
	if end["outcome"] != "failed" {
		t.Fatalf("end の行 = %v, want failed", end)
	}
}

func TestLoopWithAnActionThatCannotBeRenderedFailsToStart(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(strings.Replace(s.workerWorkflow(""), "{{ .trigger.name }}", "{{ .issue.body }}", 1))
	s.setIssues(readyIssue(42))

	r := s.run("loop")

	assertExit(t, r, 2)
	if !strings.Contains(r.stderr, "triggers[0].action") || len(s.calls("claude")) != 0 {
		t.Fatalf("stderr:\n%s", r.stderr)
	}
}

func TestActionThatFailsToRenderForTheIssueFailsWithoutLaunching(t *testing.T) {
	// 検査の見本の label では描画でき、実際の issue の label では未知の変数に当たる action
	s := newSandbox(t)
	s.writeWorkflow(strings.Replace(s.workerWorkflow(""), "{{ .trigger.name }}", "{{ if eq (index .issue.labels 0) `label` }}x{{ else }}{{ .issue.body }}{{ end }}", 1))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})

	s.startLoop()

	end := s.waitEvents("end", 1)[0]
	if end["outcome"] != "failed" || !strings.Contains(asString(end["reason"]), "描画") {
		t.Fatalf("end の行 = %v, want 描画の失敗", end)
	}
	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("描画に失敗したのに claude を起動した: %v", calls[0].Argv)
	}
}

func TestFailingBeforeRunHookFailsWithoutLaunching(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(regexp.MustCompile(`(?m)^  before_run: .*$`).ReplaceAllString(s.workerWorkflow(""), "  before_run: exit 3"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})

	s.startLoop()

	end := s.waitEvents("end", 1)[0]
	if end["outcome"] != "failed" || !strings.Contains(asString(end["reason"]), "before_run") {
		t.Fatalf("end の行 = %v, want before_run の失敗", end)
	}
	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatal("before_run が失敗したのに claude を起動した")
	}
}

func TestWorkersAreLaunchedOnlyUpToTheConcurrencyLimit(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow("limits:\n  max_concurrent: 1\n"))
	s.setIssues(readyIssue(42), readyIssue(43))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	s.startLoop()
	s.waitEvents("start", 1)

	ticks := len(s.events("tick"))
	s.waitEvents("tick", ticks+2)

	if starts := s.events("start"); len(starts) != 1 || starts[0]["target"] != "issue#42" {
		t.Fatalf("start の行 = %v, want 古い issue#42 の 1 本だけ", starts)
	}
}

func TestFirstStopRequestWaitsForTheRunningWorker(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	loop := s.startLoop()
	s.waitEvents("start", 1)

	loop.signal(syscall.SIGINT)

	loop.waitForOutput(regexp.MustCompile(` 停止待ち: `))
	// 停止待ちの間も周期ごとに作業対象を読み直す。読み直しが届いたら、少なくとも 1 周期は止まらずに待った
	rereads := len(s.calls("gh"))
	waitFor(t, func() bool { return len(s.calls("gh")) > rereads }, "停止待ちの間に作業対象を読み直さない")
	if !loop.running() {
		t.Fatal("1 回目の停止要求で、worker の終了を待たずに止まった")
	}
	s.release()
	assertExit(t, loop.wait(), 0)
	if ends := s.events("end"); len(ends) != 1 || ends[0]["outcome"] == "stopped" {
		t.Fatalf("end の行 = %v, want worker が自分で終わった行", ends)
	}
}

func TestSecondStopRequestStopsTheRunningWorker(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	loop := s.startLoop()
	s.waitEvents("start", 1)
	loop.signal(syscall.SIGINT)
	loop.waitForOutput(regexp.MustCompile(` 停止待ち: `))

	loop.signal(syscall.SIGINT)

	assertExit(t, loop.wait(), 0)
	ends := s.events("end")
	if len(ends) != 1 || ends[0]["outcome"] != "stopped" {
		t.Fatalf("end の行 = %v, want stopped", ends)
	}
	if code, ok := ends[0]["exit_code"]; ok {
		t.Fatalf("signal で止めた worker に exit_code %v が載った", code)
	}
	if _, err := os.Stat(s.mark("after_run-42")); err != nil {
		t.Fatalf("止めた worker の after_run を撃っていない: %v", err)
	}
}

func TestIssueThatComesBackToTheTriggerAfterCompletingIsLaunchedAgain(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	done := readyIssue(42)
	done.labels = nil
	s.onClaude(stubwire.Rule{Writes: []stubwire.FileWrite{s.ghResponses(done)}})
	s.startLoop()
	s.waitEvents("end", 1)

	s.setIssues(readyIssue(42))

	s.waitEvents("start", 2)
}

func TestWorkerThatFailsButLeavesTheTriggerIsCompleted(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	done := readyIssue(42)
	done.labels = nil
	s.onClaude(stubwire.Rule{Exit: 1, Writes: []stubwire.FileWrite{s.ghResponses(done)}})

	s.startLoop()

	end := s.waitEvents("end", 1)[0]
	if end["outcome"] != "completed" || !strings.Contains(asString(end["reason"]), "worker は") {
		t.Fatalf("end の行 = %v, want 失敗を添えた completed", end)
	}
}

func TestFailedIssueIsNotLaunchedAgainWhileItStillMatchesTheTrigger(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})
	s.startLoop()
	s.waitEvents("end", 1)

	ticks := len(s.events("tick"))
	s.waitEvents("tick", ticks+2)

	if calls := s.calls("claude"); len(calls) != 1 {
		t.Fatalf("claude の呼び出し = %d 回, want 1 (失敗した issue を起動し直した)", len(calls))
	}
}

func TestFailedIssueIsLaunchedAgainAfterLeavingTheTriggerOnce(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})
	s.startLoop()
	s.waitEvents("end", 1)
	offTrigger := readyIssue(42)
	offTrigger.labels = nil
	s.setIssues(offTrigger)
	ticks := len(s.events("tick"))
	s.waitEvents("tick", ticks+2)

	s.setIssues(readyIssue(42))

	s.waitEvents("start", 2)
}

func TestFailingAfterCreateHookRemovesTheWorkspaceAndFails(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(regexp.MustCompile(`(?m)^  after_create: .*$`).ReplaceAllString(s.workerWorkflow(""), "  after_create: exit 4"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})

	s.startLoop()

	end := s.waitEvents("end", 1)[0]
	if end["outcome"] != "failed" || !strings.Contains(asString(end["reason"]), "after_create") {
		t.Fatalf("end の行 = %v, want after_create の失敗", end)
	}
	if _, err := os.Stat(s.workspace(42)); !os.IsNotExist(err) {
		t.Fatalf("作りかけの workspace が残っている: %v", err)
	}
}

func TestFailingAfterRunHookIsLoggedAndTheWorkerStillEnds(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(regexp.MustCompile(`(?m)^  after_run: .*$`).ReplaceAllString(s.workerWorkflow(""), "  after_run: exit 5"))
	s.setIssues(readyIssue(42))
	done := readyIssue(42)
	done.labels = nil
	s.onClaude(stubwire.Rule{Writes: []stubwire.FileWrite{s.ghResponses(done)}})

	s.startLoop()

	end := s.waitEvents("end", 1)[0]
	if end["outcome"] != "completed" {
		t.Fatalf("end の行 = %v, want after_run の失敗では completed のまま", end)
	}
	errs := s.waitEvents("error", 1)
	if !strings.Contains(asString(errs[0]["error"]), "after_run") {
		t.Fatalf("error の行 = %v", errs)
	}
}

func TestHookThatRunsPastItsTimeoutIsStoppedAndFails(t *testing.T) {
	s := newSandbox(t)
	workflow := regexp.MustCompile(`(?m)^  before_run: .*$`).ReplaceAllString(s.workerWorkflow(""), "  before_run: 'while :; do :; done'\n  timeout: 1s")
	s.writeWorkflow(workflow)
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})

	s.startLoop()

	end := s.waitEvents("end", 1)[0]
	if end["outcome"] != "failed" || !strings.Contains(asString(end["reason"]), "before_run") {
		t.Fatalf("end の行 = %v, want before_run の timeout", end)
	}
}

func TestWorkerOfAnIssueClosedWhileWaitingToStopIsStopped(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	loop := s.startLoop()
	s.waitEvents("start", 1)
	loop.signal(syscall.SIGINT)
	loop.waitForOutput(regexp.MustCompile(` 停止待ち: `))
	closed := readyIssue(42)
	closed.closed = true

	s.setIssues(closed)

	assertExit(t, loop.wait(), 0)
	if ends := s.events("end"); len(ends) != 1 || ends[0]["outcome"] != "stopped" {
		t.Fatalf("end の行 = %v, want 終端で stopped", ends)
	}
}

func TestWorkspaceOfAnIssueClosedAfterItsWorkerCompletedIsRemoved(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	done := readyIssue(42)
	done.labels = nil
	s.onClaude(stubwire.Rule{Writes: []stubwire.FileWrite{s.ghResponses(done)}})
	s.startLoop()
	s.waitEvents("end", 1)
	closed := readyIssue(42)
	closed.closed = true

	s.setIssues(closed)

	waitFor(t, func() bool { _, err := os.Stat(s.workspace(42)); return os.IsNotExist(err) }, "claim を解いた後に終端になった issue の workspace が消えない")
}

func TestFailingBeforeRemoveHookIsLoggedAndTheWorkspaceIsStillRemoved(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(regexp.MustCompile(`(?m)^  before_remove: .*$`).ReplaceAllString(s.workerWorkflow(""), "  before_remove: exit 6"))
	mustMkdir(t, s.workspace(7))
	closed := readyIssue(7)
	closed.closed = true
	s.setIssues(closed)

	s.startLoop()

	errs := s.waitEvents("error", 1)
	if !strings.Contains(asString(errs[0]["error"]), "before_remove") {
		t.Fatalf("error の行 = %v", errs)
	}
	waitFor(t, func() bool { _, err := os.Stat(s.workspace(7)); return os.IsNotExist(err) }, "before_remove が失敗した workspace が消えない")
}

func TestWorkspaceIsRemovedUnderTheRootItWasCreatedInAfterTheRootChanges(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	s.startLoop()
	s.waitEvents("start", 1)
	s.writeWorkflow(s.workerWorkflow("workspace:\n  root: elsewhere\n"))
	ticks := len(s.events("tick"))
	s.waitEvents("tick", ticks+1)
	closed := readyIssue(42)
	closed.closed = true

	s.setIssues(closed)

	s.waitEvents("end", 1)
	waitFor(t, func() bool { _, err := os.Stat(s.workspace(42)); return os.IsNotExist(err) }, "起動したときの root の workspace が消えない")
}

func TestLoopRemovesTheWorkspacesOfClosedIssuesWhenItStarts(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	mustMkdir(t, s.workspace(7))
	mustMkdir(t, s.workspace(8))
	closed := readyIssue(7)
	closed.closed = true
	open := readyIssue(8)
	open.labels = nil
	s.setIssues(closed, open)

	s.startLoop()

	waitFor(t, func() bool { _, err := os.Stat(s.workspace(7)); return os.IsNotExist(err) }, "終端の issue の workspace が消えない")
	if _, err := os.Stat(s.mark("before_remove-7")); err != nil {
		t.Fatalf("before_remove を撃っていない: %v", err)
	}
	if _, err := os.Stat(s.workspace(8)); err != nil {
		t.Fatalf("open な issue の workspace を消した: %v", err)
	}
}

// argValueAfter は argv の flag の次の値を返す。無ければ ""。
func argValueAfter(argv []string, flag string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	return ""
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func mustRead(t *testing.T, file string) []byte {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
