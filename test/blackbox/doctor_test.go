package blackbox_test

import (
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// doctor (formats.md §7.4): 導入を確かめ、利用者の約束に頼る宣言を警告する。

func TestDoctorWarnsAboutCLTriggersThatRelyOnTheUsersPromise(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(workflowWithTriggers(`
  - {name: merge, on: cl, when: {approved: true, head: "claude/*"}, action: /t}
  - {name: fix, on: cl, when: {ci_failed: true, draft: false, author: collaborator}, action: /fix}
  - {name: labeled, on: cl, when: {ci_failed: true, labels: {all: [agent]}}, action: /fix}
  - {name: own, on: cl, when: {conflict: true, same_repo: true}, action: /fix}`))
	s.setStore(nil, nil)

	r := s.run("doctor")

	assertExit(t, r, 0)
	for _, want := range []string{
		"警告 trigger merge: approved: true の CL に action を当てている (merge を worker に任せうる)\n",
		"警告 trigger fix: head・labels・same_repo: true のどれでも絞っていない (人の CL や fork の CL に worker を送りうる)\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout に %q が無い:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "trigger labeled") || strings.Contains(r.stdout, "trigger own") {
		t.Errorf("絞り込みのある trigger を警告した:\n%s", r.stdout)
	}
}

func TestDoctorShowsTheChecksThatPassedAndTheSettingsEntries(t *testing.T) {
	s := newSandbox(t)
	s.setStore(nil, nil)

	r := s.run("doctor")

	assertExit(t, r, 0)
	for _, want := range []string{
		"ok   workflow 定義 " + s.workflowFile() + "\n",
		"ok   issue 置き場 acme/widgets\n",
		"ok   事前検査\n",
		"settings の permissions.allow に要りそうな entry (CLI は書かない):\n  Bash(gh issue:*)\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout に %q が無い:\n%s", want, r.stdout)
		}
	}
}

func TestDoctorFailsWhenTheLeadingSkillOfATriggerIsMissing(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(workflowWithTriggers("\n  - {name: implement, on: issue, action: /missing}"))

	r := s.run("doctor")

	assertExit(t, r, 1)
	if !strings.Contains(r.stdout, "NG   "+missingSkill+"\n") {
		t.Fatalf("stdout:\n%s", r.stdout)
	}
}

func TestDoctorFailsWhenTheIssueStoreIsNotVisible(t *testing.T) {
	s := newSandbox(t)
	s.respond("gh", stubwire.Rule{Stderr: "GraphQL: Could not resolve to a Repository with the name 'acme/widgets'.", Exit: 1})

	r := s.run("doctor")

	assertExit(t, r, 1)
	if !strings.Contains(r.stdout, "NG   issue 置き場 acme/widgets: ") {
		t.Fatalf("stdout:\n%s", r.stdout)
	}
}

func TestDoctorStopsAtAnInvalidWorkflowWithoutCallingGh(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(workflowWithTriggers("\n  - {name: implement, on: pr, action: /implement}"))

	r := s.run("doctor")

	assertExit(t, r, 1)
	if !strings.Contains(r.stdout, "NG   ") || !strings.Contains(r.stdout, "triggers[0].on") {
		t.Fatalf("stdout:\n%s", r.stdout)
	}
	s.assertNoGh("workflow 定義が誤りの doctor は")
}

func TestDoctorFailsWhenTheClaudeCommandCannotBeResolved(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(strings.Replace(defaultWorkflow, "triggers:", "claude:\n  command: no-such-claude\ntriggers:", 1))

	r := s.run("doctor")

	assertExit(t, r, 1)
	if !strings.Contains(r.stdout, "NG   ") || !strings.Contains(r.stdout, "no-such-claude") {
		t.Fatalf("stdout:\n%s", r.stdout)
	}
}
