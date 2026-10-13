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
cl:
  kind: github
  repo: acme/widgets
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
	return "---\ntracker:\n  kind: jira\n  host: acme.atlassian.net\n  repo: WIDGETS\ncl:\n  kind: github\n  repo: acme/widgets\ntriggers:" + triggers + "\n---\n"
}

func TestJiraDefinitionIsReadWithTheProjectKeyInUpperCase(t *testing.T) {
	def, err := load(t, validJira, nil)

	if err != nil {
		t.Fatal(err)
	}
	p := def.Triggers[0].Issue
	if def.Tracker.Place.String() != "acme.atlassian.net/WIDGETS" || strings.Join(p.StatusAny, ",") != "Ready for Agent" ||
		strings.Join(p.TypeNone, ",") != "Epic" || p.Blocked == nil {
		t.Fatalf("読んだ定義 = %+v", def)
	}
}

func TestJiraReferenceIsTheIssueKey(t *testing.T) {
	def, err := load(t, validJira, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := def.Reference(target.Ref{Kind: target.KindIssue, Number: 123})

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

// cl: の CL 置き場 (案内の「項目」の cl・「置き場のサービスごとの違い」、system.md §13)。

// jiraWithGitHubCL は triggers を tracker と cl より前に書く (検査は key の順に依らない)。
const jiraWithGitHubCL = `---
triggers:
  - name: fix-ci
    on: cl
    when:
      ci_failed: true
      author: collaborator
    action: /fix {{ .cl.number }}
tracker:
  kind: jira
  host: acme.atlassian.net
  repo: WIDGETS
cl:
  kind: github
  repo: acme/widgets
---
`

// withCLKind は jiraWithGitHubCL の cl.kind を kind に替える。
func withCLKind(kind string) string {
	return strings.Replace(jiraWithGitHubCL, "  kind: github\n", "  kind: "+kind+"\n", 1)
}

func TestJiraWithAGitHubCLHostReadsCLTriggersRegardlessOfKeyOrder(t *testing.T) {
	def, err := load(t, jiraWithGitHubCL, nil)

	if err != nil {
		t.Fatal(err)
	}
	p, place := def.CLHost()
	if p.Kind != "github" || place.String() != "acme/widgets" || def.Triggers[0].CL.Author == "" {
		t.Fatalf("CL 置き場 = %v %v, 定義 = %+v", p.Kind, place, def)
	}
}

func TestAGitLabCLHostDefaultsTheHostToGitLabCom(t *testing.T) {
	def, err := load(t, withCLKind("gitlab"), nil)

	if err != nil {
		t.Fatal(err)
	}
	if _, place := def.CLHost(); place.String() != "gitlab.com/acme/widgets" {
		t.Fatalf("CL 置き場 = %v", place)
	}
}

func TestAWorkItemIsReferredToByThePlaceItLivesIn(t *testing.T) {
	def, err := load(t, withCLKind("gitlab"), nil)
	if err != nil {
		t.Fatal(err)
	}

	issue, cl := def.Reference(target.Ref{Kind: target.KindIssue, Number: 123}), def.Reference(target.Ref{Kind: target.KindCL, Number: 7})

	if issue != "WIDGETS-123" || cl != "!7" {
		t.Fatalf("参照 = %q, %q, want WIDGETS-123, !7", issue, cl)
	}
}

func TestATrackerThatHostsCLsIsItsOwnCLHost(t *testing.T) {
	def, err := load(t, withTriggers("\n  - {name: f, on: cl, action: /f}"), nil)
	if err != nil {
		t.Fatal(err)
	}

	p, place := def.CLHost()

	if p.Kind != "github" || place.String() != "acme/widgets" || def.CL != nil {
		t.Fatalf("CL 置き場 = %v %v, cl: = %+v", p.Kind, place, def.CL)
	}
}

func TestAMissingCLDeclarationIsNamedOnlyWhenNothingElseExplainsIt(t *testing.T) {
	const githubCL = "cl:\n  kind: github\n  repo: acme/widgets\n"
	const missing = "cl: 必須の項目が無い"
	cases := []struct {
		name     string
		workflow string
		// named は誤りに現れる綴り、unnamed は現れない綴り
		named, unnamed []string
	}{
		{"jira で cl: を書かない", strings.Replace(validJira, githubCL, "", 1), []string{missing, "tracker.kind: jira"}, nil},
		// 値の無い cl: は空の対応表として読み、欠落は中の項目で名指しする
		{"jira で cl: を値なしで書く", strings.Replace(validJira, githubCL, "cl:\n", 1), []string{"cl.kind", "cl.repo"}, []string{missing}},
		{"jira で cl.kind を誤る", strings.Replace(validJira, "  kind: github\n", "  kind: bitbucket\n", 1), []string{"cl.kind"}, []string{missing}},
		{"tracker.kind が未知", strings.Replace(strings.Replace(validJira, githubCL, "", 1), "kind: jira", "kind: linear", 1), []string{"tracker.kind"}, []string{missing}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(problems(t, c.workflow), "\n")

			for _, name := range c.named {
				if !strings.Contains(got, name) {
					t.Errorf("誤りが %q を名指ししていない:\n%s", name, got)
				}
			}
			for _, name := range c.unnamed {
				if strings.Contains(got, name) {
					t.Errorf("誤りが %q を名指しした:\n%s", name, got)
				}
			}
		})
	}
}

func TestCLDeclarationsThatCannotBeWrittenAreNamed(t *testing.T) {
	githubWithCL := strings.Replace(withTriggers("\n  - {name: f, on: issue, action: /f}"), "triggers:", "cl:\n  kind: gitlab\n  repo: acme/widgets\ntriggers:", 1)
	cases := []struct {
		name     string
		workflow string
		names    []string
	}{
		{"github の cl:", githubWithCL, []string{"cl", "tracker.kind: github"}},
		{"cl.kind に jira", withCLKind("jira"), []string{"cl.kind", "jira"}},
		{"cl.kind の未知の値", withCLKind("bitbucket"), []string{"cl.kind", "bitbucket"}},
		{"cl.kind の欠落", strings.Replace(jiraWithGitHubCL, "cl:\n  kind: github\n", "cl:\n", 1), []string{"cl.kind", "必須"}},
		{"cl.repo の欠落", strings.Replace(jiraWithGitHubCL, "  repo: acme/widgets\n", "", 1), []string{"cl.repo", "必須"}},
		{"cl.repo の綴り", strings.Replace(jiraWithGitHubCL, "repo: acme/widgets", "repo: widgets", 1), []string{"cl.repo"}},
		{"github の cl.host", strings.Replace(jiraWithGitHubCL, "  kind: github\n", "  kind: github\n  host: github.com\n", 1), []string{"cl.host", "未知の key"}},
		{"cl.token", strings.Replace(jiraWithGitHubCL, "  kind: github\n", "  kind: github\n  token: $T\n", 1), []string{"cl.token", "未知の key"}},
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
