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
	cases := []struct {
		name     string
		workflow string
		names    []string
	}{
		{"top level の未知の key", strings.Replace(defaultWorkflow, "tracker:", "trackr:\n  kind: github\ntracker:", 1), []string{"trackr"}},
		{"入れ子の未知の key", strings.Replace(defaultWorkflow, "all: [ready-for-agent]", "al: [ready-for-agent]", 1), []string{"triggers[0].when.labels.al"}},
		{"trigger の未知の key", strings.Replace(defaultWorkflow, "    action: |", "    acton: x\n    action: |", 1), []string{"triggers[0].acton"}},
		{"必須の項目の欠落", strings.Replace(defaultWorkflow, "  repo: acme/widgets\n", "", 1), []string{"tracker.repo"}},
		{"未知の tracker", strings.Replace(defaultWorkflow, "kind: github", "kind: jira", 1), []string{"tracker.kind", "jira"}},
		{"repo の綴りの誤り", strings.Replace(defaultWorkflow, "repo: acme/widgets", "repo: widgets", 1), []string{"tracker.repo", "widgets"}},
		{"未知の作業対象の種類", strings.Replace(defaultWorkflow, "on: issue", "on: pr", 1), []string{"triggers[0].on", "pr"}},
		{"空の action", strings.Replace(defaultWorkflow, "    action: |\n      /implement\n", "    action: \"\"\n", 1), []string{"triggers[0].action"}},
		{"action の欠落", strings.Replace(defaultWorkflow, "    action: |\n      /implement\n", "", 1), []string{"triggers[0].action"}},
		{"型の誤り", strings.Replace(defaultWorkflow, "all: [ready-for-agent]", "all: ready-for-agent", 1), []string{"triggers[0].when.labels.all"}},
		{"未知の作者の立場", strings.Replace(defaultWorkflow, "        all: [ready-for-agent]", "        all: [ready-for-agent]\n      author: owner", 1), []string{"triggers[0].when.author", "owner"}},
		{"assignee と unassigned の併記", strings.Replace(defaultWorkflow, "        all: [ready-for-agent]", "        all: [ready-for-agent]\n      assignee: alice\n      unassigned: true", 1), []string{"triggers[0].when"}},
		{"空の labels.any", strings.Replace(defaultWorkflow, "all: [ready-for-agent]", "any: []", 1), []string{"triggers[0].when.labels.any"}},
		{"trigger の名前の重複", workflowWithTriggers("\n  - {name: t, on: issue, action: /a}\n  - {name: t, on: issue, action: /b}"), []string{"triggers[1].name", "t"}},
		{"trigger の名前の綴り", workflowWithTriggers("\n  - {name: \"a b\", on: issue, action: /a}"), []string{"triggers[0].name"}},
		{"trigger が 1 つも無い", "---\ntracker:\n  kind: github\n  repo: acme/widgets\ntriggers: []\n---\n", []string{"triggers"}},
		{"周期の範囲外", strings.Replace(defaultWorkflow, "triggers:", "polling:\n  interval: 30s\ntriggers:", 1), []string{"polling.interval"}},
		{"周期の綴り", strings.Replace(defaultWorkflow, "triggers:", "polling:\n  interval: soon\ntriggers:", 1), []string{"polling.interval"}},
		{"同じ key の 2 回目", strings.Replace(defaultWorkflow, "  kind: github\n", "  kind: github\n  kind: github\n", 1), []string{"tracker.kind"}},
		{"token に値そのもの", strings.Replace(defaultWorkflow, "  repo: acme/widgets\n", "  repo: acme/widgets\n  token: ghp_abc\n", 1), []string{"tracker.token"}},
		{"YAML として読めない", strings.Replace(defaultWorkflow, "tracker:", "tracker: [", 1), []string{"WORKFLOW.md"}},
		{"front matter が無い", "共通 prompt だけ\n", []string{"front matter"}},
		{"front matter が閉じていない", "---\ntracker:\n  kind: github\n", []string{"front matter"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeWorkflow(c.workflow)

			r := s.dryRun()

			s.assertRejected(r, c.names...)
		})
	}
}

func TestEveryWorkflowDefinitionErrorIsReportedOnItsOwnLine(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(workflowWithTriggers("\n  - {name: a, on: pr, action: /a}\n  - {name: b, on: issue, action: /b, colour: red}"))

	r := s.dryRun()

	s.assertRejected(r, "triggers[0].on", "triggers[1].colour")
	if lines := strings.Split(strings.TrimSpace(r.stderr), "\n"); len(lines) != 2 {
		t.Fatalf("誤り 2 件が %d 行で出た:\n%s", len(lines), r.stderr)
	}
}

func TestUnsetVariableIsRejectedNamingTheItemAndTheVariable(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(strings.Replace(defaultWorkflow, "repo: acme/widgets", "repo: $ISSUE_REPO", 1))

	r := s.dryRun()

	s.assertRejected(r, "tracker.repo", "ISSUE_REPO")
}

func TestEmptyVariableIsRejectedLikeAnUnsetOne(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(strings.Replace(defaultWorkflow, "  repo: acme/widgets\n", "  repo: acme/widgets\n  token: $WIDGETS_TOKEN\n", 1))

	r := s.runWithEnv(map[string]string{"WIDGETS_TOKEN": ""}, "loop", "--dry-run")

	s.assertRejected(r, "tracker.token", "WIDGETS_TOKEN")
}

func TestRepoReadFromAVariableIsCheckedLikeAWrittenOne(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(strings.Replace(defaultWorkflow, "repo: acme/widgets", "repo: $ISSUE_REPO", 1))

	r := s.runWithEnv(map[string]string{"ISSUE_REPO": "not-a-repo"}, "loop", "--dry-run")

	s.assertRejected(r, "tracker.repo")
}

func TestMissingWorkflowDefinitionIsRejectedNamingThePath(t *testing.T) {
	s := newSandbox(t)
	if err := os.Remove(s.workflowFile()); err != nil {
		t.Fatal(err)
	}

	r := s.dryRun()

	s.assertRejected(r, s.workflowFile())
}

func TestDryRunRejectsExtraArguments(t *testing.T) {
	s := newSandbox(t)

	assertExit(t, s.run("loop", "--dry-run", "a.md", "b.md"), 2)
	assertExit(t, s.run("loop", "--now"), 2)
	s.assertNoGh("引数が誤っているのに")
}
