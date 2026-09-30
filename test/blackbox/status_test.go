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

func TestStatusShowsTheRunningWorker(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	s.startLoop()
	s.waitEvents("start", 1)

	out := s.waitStatus("issue#42\timplement\tattempt 1\t走っている")

	if !strings.HasPrefix(out, "loop 稼働中 · scope github.com/acme/widgets") {
		t.Fatalf("status の見出し:\n%s", out)
	}
}

func TestLoopShowsTheActivityOfARunningWorker(t *testing.T) {
	// stream の最新の完結した行を要約して、活動の行と status に出す (formats.md §6)
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"テストを\t読む\n次の行"}]}}`
	s.onClaude(stubwire.Rule{Stdout: line + "\n" + `{"type":"assist`, ReleaseFile: s.releaseFile()}) // 書きかけの行は数えない
	loop := s.startLoop()

	loop.waitForOutput(regexp.MustCompile(`活動 issue#42: テストを 読む$`))
	s.waitStatus("\tテストを 読む\n")
}

func TestStatusShowsARetryWaitingForItsBackoff(t *testing.T) {
	// 既定の backoff (10s) の間に status を撃つ
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})
	s.startLoop()
	s.waitEvents("retry", 1)

	s.waitStatus("issue#42\timplement\tattempt 1\t再起動待ち")
}

func TestStatusShowsAnAbandonedIssueWithHowToClearIt(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow("limits:\n  max_attempts: 1\n"))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{})
	s.startLoop()
	s.waitEvents("abandon", 1)

	s.waitStatus("打ち切り issue#42 (implement): label を外して trigger から外し、1 周期待ってから付け直すと解ける")
}

func TestStatusShowsAmbiguousCLs(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.clWorkflow("{}"))
	s.setStore(nil, numbered(cl{head: "shared"}, cl{head: "shared"}))
	s.startLoop()
	s.waitEvents("tick", 1)

	s.waitStatus("曖昧な CL shared: cl#1, cl#2")
}

func TestStatusWithoutALoopSaysSoWithoutWritingAnything(t *testing.T) {
	s := newSandbox(t)

	r := s.run("status")

	assertExit(t, r, 0)
	if r.stdout != "loop なし · scope github.com/acme/widgets · 記録なし\n" {
		t.Fatalf("status:\n%s", r.stdout)
	}
	s.assertNoGh("status は")
	if _, err := os.Stat(s.stateRoot); !os.IsNotExist(err) {
		t.Fatalf("status が state root を作った: %v", err)
	}
}

func TestStatusAfterTheLoopStoppedShowsNoWorkers(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.workerWorkflow(""))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	loop := s.startLoop()
	s.waitEvents("start", 1)
	loop.signal(syscall.SIGINT)
	loop.waitForOutput(regexp.MustCompile(`停止待ち`)) // 続けて送ると 1 回にまとめられるので、1 回目を受けたのを待つ
	loop.signal(syscall.SIGINT)
	loop.wait()

	r := s.run("status")

	assertExit(t, r, 0)
	if !strings.HasPrefix(r.stdout, "loop なし · scope github.com/acme/widgets") || strings.Contains(r.stdout, "issue#42") {
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
