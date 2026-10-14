package blackbox_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// 事前検査 (formats.md §2.9): action の先頭の skill か command が呼べるか。

const missingSkill = "trigger implement: action の先頭の /missing が見つからない (plugin・repo の .claude・~/.claude の skill と command)"

func TestLoopDoesNotStartWhenTheLeadingSkillOfATriggerIsMissing(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithoutCommands(workflowWithTriggers("\n  - {name: implement, on: issue, action: /missing}"))

	r := s.run("loop")

	assertExit(t, r, 2)
	if !strings.Contains(r.stderr, missingSkill) {
		t.Fatalf("stderr:\n%s", r.stderr)
	}
}

func TestDryRunFailsWhenTheLeadingSkillOfATriggerIsMissing(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithoutCommands(workflowWithTriggers("\n  - {name: implement, on: issue, action: /missing}"))

	r := s.dryRun()

	s.assertRejected(r, missingSkill)
}

func TestActionThatStartsWithATemplateVariableFailsByName(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(workflowWithTriggers("\n  - {name: implement, on: issue, action: \"{{ .trigger.name }} を進める\"}"))

	r := s.dryRun()

	s.assertRejected(r, "trigger implement: action が template 変数で始まるので、先頭の skill を確かめられない")
}

func TestLeadingSkillOfTheRepoPassesTheCheck(t *testing.T) {
	s := newSandbox(t)
	mustWrite(t, s.clone+"/.claude/skills/playbook/SKILL.md", "---\nname: playbook\n---\n")
	s.writeWorkflowWithoutCommands(workflowWithTriggers("\n  - {name: implement, on: issue, action: /playbook}"))
	s.setIssues(readyIssue(1))

	r := s.dryRun()

	assertExit(t, r, 0)
}

func TestTriggerThatLosesItsLeadingSkillIsLeftOutOfTheEvaluation(t *testing.T) {
	// 事前検査に落ちた urgent は評価から外れるので、urgent と implement の両方に当たる issue#43 は implement の候補になる
	// (usecases.md UC-5)
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.abandonedWorkflow())
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	loop := s.startLoop()
	s.waitEvents("tick", 1)

	if err := os.Remove(s.command("urgent")); err != nil {
		t.Fatal(err)
	}
	urgent := readyIssue(43)
	urgent.labels = append(urgent.labels, "urgent")
	s.setIssues(readyIssue(42), urgent)

	loop.waitForOutput(regexp.MustCompile(`起動 issue#42 \(implement`))
	loop.waitForOutput(regexp.MustCompile(`tick ok · 候補 2: implement issue #42, implement issue #43 · 起動しない trigger: urgent \(action の先頭の /urgent が見つからない`))
	s.waitStatus("起動しない trigger urgent: action の先頭の /urgent が見つからない")
}

// 「skill を失った trigger の retry」のテストは、skill を attempt 1 が走っている間に消し、tick が事前検査に落ちたのを
// 出してから attempt 1 を終わらせる。事前検査は tick の中でだけ撃ち、tick の外の再起動は直近の tick の結果で決める
// (formats.md §6 の再起動) ので、skill を消したのを tick が見る前に backoff が明けると attempt 2 が起動する。

func TestRetryOfATriggerThatLostItsSkillDoesNotHoldTheSlot(t *testing.T) {
	// 並列上限 1。urgent で失敗して再起動を待つ issue#43 の skill が消えていれば、implement の issue#42 は起動する
	s := newSandbox(t)
	s.writeWorkflowWithCommands(strings.Replace(s.workerWorkflow(fastRetry), "triggers:\n", `triggers:
  - name: urgent
    on: issue
    when: {labels: {all: [urgent]}}
    action: /urgent
`, 1))
	urgent := readyIssue(43)
	urgent.labels = append(urgent.labels, "urgent")
	s.setIssues(urgent)
	// implement の worker は s.releaseFile() で待たせるので、urgent の worker は別の file で終わらせる
	urgentRelease := filepath.Join(s.root, "release-urgent")
	t.Cleanup(func() { _ = os.WriteFile(urgentRelease, nil, 0o644) })
	s.respondAll("claude", []stubwire.Rule{
		{ArgsContain: []string{"/urgent"}, Stdout: `{"type":"result"}` + "\n", Exit: 1, ReleaseFile: urgentRelease},
		{Stdout: `{"type":"result"}` + "\n", ReleaseFile: s.releaseFile()},
	})
	loop := s.startLoop()
	s.waitEvents("start", 1)
	if err := os.Remove(s.command("urgent")); err != nil {
		t.Fatal(err)
	}
	s.setIssues(readyIssue(42), urgent)
	loop.waitForOutput(regexp.MustCompile(`起動しない trigger: urgent`))

	mustWrite(t, urgentRelease, "")

	loop.waitForOutput(regexp.MustCompile(`起動 issue#42 \(implement`))
	s.waitStatusRow("issue#43", "urgent", "1", "waiting_retry")
}

func TestRetryOfATriggerThatLostItsSkillIsReleasedWhenTheIssueCloses(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.workerWorkflow(fastRetry))
	s.setIssues(readyIssue(42))
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	loop := s.startLoop()
	s.waitEvents("start", 1)
	if err := os.Remove(s.command("implement")); err != nil {
		t.Fatal(err)
	}
	loop.waitForOutput(regexp.MustCompile(`起動しない trigger: implement`))
	s.release()
	s.waitEvents("retry", 1)
	closed := readyIssue(42)
	closed.closed = true

	s.setIssues(closed)

	if release := s.waitEvents("release", 1)[0]; release["reason"] != "terminal" {
		t.Fatalf("release の行 = %v", release)
	}
}
