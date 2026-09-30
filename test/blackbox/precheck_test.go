package blackbox_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// 事前検査 (formats.md §2.9): action の先頭の skill か command が呼べるか。

const missingSkill = "trigger implement: action の先頭の /missing が見つからない (plugin・repo の .claude・~/.claude の skill と command)"

func TestLoopDoesNotStartWhenTheLeadingSkillOfATriggerIsMissing(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(workflowWithTriggers("\n  - {name: implement, on: issue, action: /missing}"))

	r := s.run("loop")

	assertExit(t, r, 2)
	if !strings.Contains(r.stderr, missingSkill) {
		t.Fatalf("stderr:\n%s", r.stderr)
	}
}

func TestDryRunFailsWhenTheLeadingSkillOfATriggerIsMissing(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(workflowWithTriggers("\n  - {name: implement, on: issue, action: /missing}"))

	r := s.dryRun()

	s.assertRejected(r, missingSkill)
}

func TestActionThatStartsWithATemplateVariableFailsByName(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(workflowWithTriggers("\n  - {name: implement, on: issue, action: \"{{ .trigger.name }} を進める\"}"))

	r := s.dryRun()

	s.assertRejected(r, "trigger implement: action が template 変数で始まるので、先頭の skill を確かめられない")
}

func TestLeadingSkillOfTheRepoPassesTheCheck(t *testing.T) {
	s := newSandbox(t)
	mustWrite(t, s.clone+"/.claude/skills/playbook/SKILL.md", "---\nname: playbook\n---\n")
	s.writeWorkflow(workflowWithTriggers("\n  - {name: implement, on: issue, action: /playbook}"))
	s.setIssues(readyIssue(1))

	r := s.dryRun()

	assertExit(t, r, 0)
}

func TestTickLaunchesTheOtherTriggersWhenOneLosesItsLeadingSkill(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.abandonedWorkflow())
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
	loop.waitForOutput(regexp.MustCompile(`tick ok · .* · 起動しない trigger: urgent \(action の先頭の /urgent が見つからない`))
	s.waitStatus("起動しない trigger urgent: action の先頭の /urgent が見つからない")
	if starts := s.events("start"); len(starts) != 1 {
		t.Fatalf("start の行 = %d 行, want issue#42 の 1 行だけ (issue#43 は urgent に当たる)", len(starts))
	}
}
