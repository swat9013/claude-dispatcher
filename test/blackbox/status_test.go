package blackbox_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// 状態 file・`status`・`paths --json` (formats.md §7)。status は別の端末から撃つ想定で、走っている loop と並べて撃つ。

// waitStatus は `status` の stdout が want を含むまで撃ち直し、その stdout を返す。
func (s *sandbox) waitStatus(want string) string {
	s.t.Helper()
	var out string
	waitFor(s.t, func() bool {
		r := s.run("status")
		out = r.stdout
		return r.exit == 0 && strings.Contains(out, want)
	}, "status に "+want+" が出ない")
	return out
}

// waitStatusRow は `status` の表に cells を並べた行が出るまで撃ち直し、その stdout を返す。列は空白で揃えるので、間の空白の
// 数は問わない。
func (s *sandbox) waitStatusRow(cells ...string) string {
	s.t.Helper()
	quoted := make([]string, len(cells))
	for i, c := range cells {
		quoted[i] = regexp.QuoteMeta(c)
	}
	row := regexp.MustCompile(`(?m)^` + strings.Join(quoted, ` +`) + `( |$)`)
	var out string
	waitFor(s.t, func() bool {
		r := s.run("status")
		out = r.stdout
		return r.exit == 0 && row.MatchString(out)
	}, "status に行 "+strings.Join(cells, " ")+" が出ない")
	return out
}

func TestStatusShowsTheRunningWorker(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	s.startLoop()
	s.waitEvents("start", 1)

	out := s.waitStatusRow("issue#42", "implement", "1", "running")

	if !strings.HasPrefix(out, "● loop 稼働中  github.com/acme/widgets\n") {
		t.Fatalf("status の見出し:\n%s", out)
	}
}

func TestLoopShowsTheActivityOfARunningWorker(t *testing.T) {
	// stream の最新の完結した行を要約して、活動の行と status に出す (formats.md §6)
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"テストを\t読む\n次の行"}]}}`
	s.onClaude(stubwire.Rule{Stdout: line + "\n" + `{"type":"assist`, ReleaseFile: s.releaseFile()}) // 書きかけの行は数えない
	loop := s.startLoop()

	loop.waitForOutput(regexp.MustCompile(`活動 issue#42: テストを 読む$`))
	s.waitStatus("  テストを 読む\n")
}

func TestActivityShowsTheFileAToolUsesRelativeToTheWorkspace(t *testing.T) {
	// tool の入力の要点は、workspace の中の file を workspace からの相対 path で出す (formats.md §6)
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	file := filepath.Join(s.clone, ".claude-dispatcher", "workspaces", "issue-42", "internal", "a.go")
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"` + file + `"}}]}}`
	s.onClaude(stubwire.Rule{Stdout: line + "\n", ReleaseFile: s.releaseFile()})
	loop := s.startLoop()

	loop.waitForOutput(regexp.MustCompile(`活動 issue#42: Read internal/a\.go$`))
}

func TestStatusShowsARetryWaitingForItsBackoff(t *testing.T) {
	// 既定の backoff (10s) の間に status を撃つ
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})
	s.startLoop()
	s.waitEvents("retry", 1)

	s.waitStatusRow("issue#42", "implement", "1", "waiting_retry")
}

func TestStatusShowsAnAbandonedIssueWithHowToClearIt(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow("limits:\n  max_attempts: 1\n"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})
	s.startLoop()
	s.waitEvents("abandon", 1)

	s.waitStatus("打ち切り issue#42 (implement): label を外して trigger から外し、1 周期待ってから付け直すと解ける")
}

func TestStatusShowsTheErrorOfTheLastTick(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues()
	s.startLoop()
	s.waitEvents("tick", 1)

	s.respond("gh", stubwire.Rule{Stderr: "gh の障害", Exit: 1}) // status は workflow 定義を読むので、壊すのは gh の側

	out := s.waitStatus("gh の障害")

	if !regexp.MustCompile(`直近の tick \d\d:\d\d:\d\d error: .*gh の障害`).MatchString(out) {
		t.Fatalf("見出しに直近の tick の error が無い:\n%s", out)
	}

}

func TestStatusShowsAmbiguousCLs(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.clWorkflow("{}"))
	s.setStore(nil, numbered(cl{head: "shared"}, cl{head: "shared"}))
	s.startLoop()
	s.waitEvents("tick", 1)

	s.waitStatus("曖昧な CL shared: cl#1, cl#2")
}

func TestStatusWithoutALoopSaysSoWithoutWritingAnything(t *testing.T) {
	s := newSandbox(t)

	r := s.run("status")

	assertExit(t, r, 0)
	if r.stdout != "○ loop なし  github.com/acme/widgets\n記録なし\n" {
		t.Fatalf("status:\n%s", r.stdout)
	}
	s.assertNoGh("status は")
	if _, err := os.Stat(s.stateRoot); !os.IsNotExist(err) {
		t.Fatalf("status が state root を作った: %v", err)
	}
}

func TestStatusAfterTheLoopStoppedShowsNoWorkers(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	loop := s.startLoop()
	s.waitEvents("start", 1)
	loop.signal(syscall.SIGINT)
	loop.waitForOutput(regexp.MustCompile(`停止待ち`)) // 続けて送ると 1 回にまとめられるので、1 回目を受けたのを待つ
	if out := s.waitStatus("● loop 停止待ち"); !strings.Contains(out, "次の tick なし") {
		t.Fatalf("停止待ちの status に次の tick の予定が出る:\n%s", out)
	}
	loop.signal(syscall.SIGINT)
	loop.wait()

	r := s.run("status")

	assertExit(t, r, 0)
	if !strings.HasPrefix(r.stdout, "○ loop なし  github.com/acme/widgets\n") || strings.Contains(r.stdout, "issue#42") {
		t.Fatalf("止まった loop の status:\n%s", r.stdout)
	}
}

func TestPathsJSONShowsThePlacesOfTheScope(t *testing.T) {
	s := newSandbox(t)

	r := s.run("paths", "--json")

	assertExit(t, r, 0)
	var got map[string]string
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("paths --json の出力を読めない: %v\n%s", err, r.stdout)
	}
	dir := s.defaultStateDir()
	want := map[string]string{
		"scope_key":      "github.com/acme/widgets",
		"state_dir":      dir,
		"log":            filepath.Join(dir, "log.jsonl"),
		"status_file":    filepath.Join(dir, "status.json"),
		"workspace_root": filepath.Join(s.clone, ".claude-dispatcher", "workspaces"),
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
	s.assertNoGh("paths は")
}
