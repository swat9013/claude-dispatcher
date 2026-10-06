package workflow_test

import (
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/target"
)

// tracker.kind: jira の workflow 定義 (formats.md §2.1・§2.2・§2.8)。

const validJira = `---
tracker:
  kind: jira
  host: acme.atlassian.net
  repo: widgets
triggers:
  - name: implement
    on: issue
    when:
      status:
        any: [Ready for Agent]
      type:
        none: [Epic]
      blocked: false
    action: |
      /implement {{ .issue.key }}
---
共通 prompt {{ .issue.key }}
`

func jiraWithTriggers(triggers string) string {
	return "---\ntracker:\n  kind: jira\n  host: acme.atlassian.net\n  repo: WIDGETS\ntriggers:" + triggers + "\n---\n"
}

func TestJiraDefinitionIsReadWithTheProjectKeyInUpperCase(t *testing.T) {
	def, err := load(t, validJira, nil)

	if err != nil {
		t.Fatal(err)
	}
	p := def.Triggers[0].Issue
	if def.Tracker.Place() != "acme.atlassian.net/WIDGETS" || strings.Join(p.StatusAny, ",") != "Ready for Agent" ||
		strings.Join(p.TypeNone, ",") != "Epic" || p.Blocked == nil {
		t.Fatalf("読んだ定義 = %+v", def)
	}
}

func TestJiraReferenceIsTheIssueKey(t *testing.T) {
	def, err := load(t, validJira, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := def.Tracker.Reference(target.Ref{Kind: target.KindIssue, Number: 123})

	if got != "WIDGETS-123" {
		t.Fatalf("参照 = %q, want WIDGETS-123", got)
	}
}

func TestDeclarationsATrackerKindCannotSupportAreNamed(t *testing.T) {
	cases := []struct {
		name     string
		workflow string
		names    []string
	}{
		{"jira の site の欠落", strings.Replace(validJira, "  host: acme.atlassian.net\n", "", 1), []string{"tracker.host", "必須"}},
		{"jira の site の綴り", strings.Replace(validJira, "host: acme.atlassian.net", "host: https://acme.atlassian.net", 1), []string{"tracker.host"}},
		{"jira の project key の綴り", strings.Replace(validJira, "repo: widgets", "repo: acme/widgets", 1), []string{"tracker.repo"}},
		{"jira の token", strings.Replace(validJira, "  repo: widgets\n", "  repo: widgets\n  token: $T\n", 1), []string{"tracker.token", "jira"}},
		{"jira の author", jiraWithTriggers("\n  - {name: f, on: issue, when: {author: collaborator}, action: /f}"), []string{"triggers[0].when.author", "jira"}},
		{"jira の milestone", jiraWithTriggers("\n  - {name: f, on: issue, when: {milestone: v1}, action: /f}"), []string{"triggers[0].when.milestone", "jira"}},
		{"jira の on: cl", jiraWithTriggers("\n  - {name: f, on: cl, action: /f}"), []string{"triggers[0].on", "jira"}},
		{"jira の空の status.any", jiraWithTriggers("\n  - {name: f, on: issue, when: {status: {any: []}}, action: /f}"), []string{"triggers[0].when.status.any"}},
		{"status の未知の key", jiraWithTriggers("\n  - {name: f, on: issue, when: {status: {all: [a]}}, action: /f}"), []string{"triggers[0].when.status.all"}},
		{"github の status", withTriggers("\n  - {name: f, on: issue, when: {status: {any: [Todo]}}, action: /f}"), []string{"triggers[0].when.status", "github"}},
		{"github の type", withTriggers("\n  - {name: f, on: issue, when: {type: {any: [Bug]}}, action: /f}"), []string{"triggers[0].when.type", "github"}},
		{"github の .issue.key", withTriggers("\n  - {name: f, on: issue, action: \"/f {{ .issue.key }}\"}"), []string{"triggers[0].action", "key"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(problems(t, c.workflow), "\n")

			for _, name := range c.names {
				if !strings.Contains(got, name) {
					t.Fatalf("誤りが %q を名指ししていない:\n%s", name, got)
				}
			}
		})
	}
}
