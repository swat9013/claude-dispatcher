package blackbox_test

import (
	"os"
	"strings"
	"testing"
)

// workflow 定義の読み込みと検査 (formats.md §2)。誤りは項目の位置を名指しして失敗させ、gh を撃たない。

// workflowWithTriggers は既定の tracker に triggers の列 (YAML の列の要素を並べた文字列) を付けた workflow 定義。
func workflowWithTriggers(triggers string) string {
	return "---\ntracker:\n  kind: github\n  repo: acme/widgets\ntriggers:" + triggers + "\n---\n共通 prompt\n"
}

// assertRejected は workflow 定義の誤りとして exit 2 で失敗し、stderr が names をすべて含み、gh を撃っていないことを確かめる。
func (s *sandbox) assertRejected(r runResult, names ...string) {
	s.t.Helper()
	assertExit(s.t, r, 2)
	if r.stdout != "" {
		s.t.Fatalf("失敗したのに stdout に出た: %q", r.stdout)
	}
	for _, name := range names {
		if !strings.Contains(r.stderr, name) {
			s.t.Fatalf("stderr が %q を名指ししていない:\n%s", name, r.stderr)
		}
	}
	s.assertNoGh("workflow 定義が誤っているのに")
}

func TestWorkflowDefinitionErrorsAreRejectedNamingTheItem(t *testing.T) {
	// 誤りの種類ごとの名指しは internal/workflow の単体テストが持つ。ここでは代表で、名指しが stderr に出て gh を撃たないことを見る
	cases := []struct {
		name     string
		workflow string
		names    []string
	}{
		{"入れ子の未知の key", strings.Replace(defaultWorkflow, "all: [ready-for-agent]", "al: [ready-for-agent]", 1), []string{"triggers[0].when.labels.al"}},
		{"文法の誤り", strings.Replace(defaultWorkflow, "on: issue", "on: pr", 1), []string{"triggers[0].on", "pr"}},
		{"YAML として読めない", strings.Replace(defaultWorkflow, "tracker:", "tracker: [", 1), []string{"WORKFLOW.md"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeWorkflowWithCommands(c.workflow)

			r := s.dryRun()

			s.assertRejected(r, c.names...)
		})
	}
}

func TestEveryWorkflowDefinitionErrorIsReportedOnItsOwnLine(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(workflowWithTriggers("\n  - {name: a, on: pr, action: /a}\n  - {name: b, on: issue, action: /b, colour: red}"))

	r := s.dryRun()

	s.assertRejected(r, "triggers[0].on", "triggers[1].colour")
	if lines := strings.Split(strings.TrimSpace(r.stderr), "\n"); len(lines) != 2 {
		t.Fatalf("誤り 2 件が %d 行で出た:\n%s", len(lines), r.stderr)
	}
}

func TestUnsetVariableIsRejectedNamingTheItemAndTheVariable(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(strings.Replace(defaultWorkflow, "repo: acme/widgets", "repo: $ISSUE_REPO", 1))

	r := s.dryRun()

	s.assertRejected(r, "tracker.repo", "ISSUE_REPO")
}

func TestEmptyVariableIsRejectedLikeAnUnsetOne(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(strings.Replace(defaultWorkflow, "  repo: acme/widgets\n", "  repo: acme/widgets\n  token: $WIDGETS_TOKEN\n", 1))

	r := s.runWithEnv(map[string]string{"WIDGETS_TOKEN": ""}, "loop", "--dry-run")

	s.assertRejected(r, "tracker.token", "WIDGETS_TOKEN")
}

func TestMissingWorkflowDefinitionIsRejectedNamingThePath(t *testing.T) {
	s := newSandbox(t)
	if err := os.Remove(s.workflowFile()); err != nil {
		t.Fatal(err)
	}

	r := s.dryRun()

	s.assertRejected(r, s.workflowFile())
}

func TestLoopRejectsASecondWorkflowPath(t *testing.T) {
	s := newSandbox(t)

	r := s.run("loop", "--dry-run", "a.md", "b.md")

	assertExit(t, r, 2)
	s.assertNoGh("引数が誤っているのに")
}

func TestLoopRejectsAnUnknownFlag(t *testing.T) {
	s := newSandbox(t)

	r := s.run("loop", "--now")

	assertExit(t, r, 2)
	s.assertNoGh("flag が誤っているのに")
}
