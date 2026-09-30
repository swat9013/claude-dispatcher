package blackbox_test

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// 試運転 `loop --dry-run` (formats.md §5) と、issue 側の trigger の評価 (formats.md §2.2 / §2.3)。

// candidateLines は試運転の stdout を 1 候補 1 行の列で返す。
func candidateLines(t *testing.T, r runResult) []string {
	t.Helper()
	assertExit(t, r, 0)
	if r.stdout == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
}

func candidate(trigger string, number int) string {
	return trigger + "\tissue\t#" + strconv.Itoa(number) + "\tissue " + strconv.Itoa(number)
}

func assertCandidates(t *testing.T, r runResult, want ...string) {
	t.Helper()
	got := candidateLines(t, r)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("候補:\n%s\nwant:\n%s\nstderr:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"), r.stderr)
	}
}

func TestDryRunListsTheIssuesThatMatchATriggerOldestFirst(t *testing.T) {
	s := newSandbox(t)
	unlabeled := readyIssue(44)
	unlabeled.labels = nil
	s.setIssues(readyIssue(43), unlabeled, readyIssue(42))

	r := s.dryRun()

	assertCandidates(t, r, candidate("implement", 42), candidate("implement", 43))
}

func TestDryRunListsCandidatesInTriggerDeclarationOrderBeforeCreationOrder(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(workflowWithTriggers(`
  - name: review
    on: issue
    when: {labels: {all: [review]}}
    action: /review
  - name: implement
    on: issue
    when: {labels: {all: [ready-for-agent]}}
    action: /implement`))
	older := readyIssue(10)
	newer := readyIssue(20)
	newer.labels = []string{"review"}
	s.setIssues(older, newer)

	r := s.dryRun()

	assertCandidates(t, r, candidate("review", 20), candidate("implement", 10))
}

func TestIssueMatchingTwoTriggersIsListedOnlyUnderTheFirstDeclared(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(workflowWithTriggers(`
  - name: first
    on: issue
    when: {labels: {all: [ready-for-agent]}}
    action: /first
  - name: second
    on: issue
    when: {labels: {any: [ready-for-agent]}}
    action: /second`))
	s.setIssues(readyIssue(7))

	r := s.dryRun()

	assertCandidates(t, r, candidate("first", 7))
}

func TestTriggerWithoutWhenMatchesEveryOpenIssue(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(workflowWithTriggers(`
  - name: all
    on: issue
    action: /all`))
	bare := readyIssue(3)
	bare.labels = nil
	s.setIssues(bare)

	r := s.dryRun()

	assertCandidates(t, r, candidate("all", 3))
}

func TestEachIssuePredicateNarrowsTheCandidates(t *testing.T) {
	with := func(number int, change func(*issue)) issue {
		i := readyIssue(number)
		change(&i)
		return i
	}
	cases := []struct {
		name   string
		when   string
		issues []issue
		want   []int
	}{
		{"labels.any は列のどれかを持つ issue に当たる", `{labels: {any: [bug, docs]}}`,
			[]issue{with(1, func(i *issue) { i.labels = []string{"docs"} }), with(2, func(i *issue) { i.labels = []string{"feature"} })}, []int{1}},
		{"labels.none は列の label を持たない issue に当たる", `{labels: {none: [needs-info]}}`,
			[]issue{with(1, func(i *issue) { i.labels = []string{"needs-info"} }), with(2, func(*issue) {})}, []int{2}},
		{"label は大文字と小文字を区別せずに比べる", `{labels: {all: [Ready-For-Agent]}}`,
			[]issue{with(1, func(*issue) {})}, []int{1}},
		{"assignee はその人が assignee に居る issue に当たる", `{assignee: alice}`,
			[]issue{with(1, func(i *issue) { i.assignees = []string{"bob", "alice"} }), with(2, func(i *issue) { i.assignees = []string{"bob"} })}, []int{1}},
		{"unassigned: true は assignee の居ない issue に当たる", `{unassigned: true}`,
			[]issue{with(1, func(i *issue) { i.assignees = []string{"bob"} }), with(2, func(*issue) {})}, []int{2}},
		{"unassigned: false は assignee の居る issue に当たる", `{unassigned: false}`,
			[]issue{with(1, func(i *issue) { i.assignees = []string{"bob"} }), with(2, func(*issue) {})}, []int{1}},
		{"author: collaborator は owner・member・collaborator の issue に当たる", `{author: collaborator}`,
			[]issue{
				with(1, func(i *issue) { i.association = "OWNER" }),
				with(2, func(i *issue) { i.association = "MEMBER" }),
				with(3, func(i *issue) { i.association = "COLLABORATOR" }),
				with(4, func(i *issue) { i.association = "CONTRIBUTOR" }),
				with(5, func(i *issue) { i.association = "NONE" }),
			}, []int{1, 2, 3}},
		{"author: non_collaborator はそれ以外の issue に当たる", `{author: non_collaborator}`,
			[]issue{with(1, func(i *issue) { i.association = "OWNER" }), with(2, func(i *issue) { i.association = "FIRST_TIME_CONTRIBUTOR" })}, []int{2}},
		{"milestone はその題名の milestone の issue に当たる", `{milestone: v1}`,
			[]issue{with(1, func(i *issue) { i.milestone = "v1" }), with(2, func(i *issue) { i.milestone = "v2" }), with(3, func(*issue) {})}, []int{1}},
		{"blocked: false は未解決の依存先が無い issue に当たる", `{blocked: false}`,
			[]issue{with(1, func(i *issue) { i.blockers = []string{"OPEN"} }), with(2, func(i *issue) { i.blockers = []string{"CLOSED"} }), with(3, func(*issue) {})}, []int{2, 3}},
		{"blocked: true は未解決の依存先がある issue に当たる", `{blocked: true}`,
			[]issue{with(1, func(i *issue) { i.blockers = []string{"CLOSED", "OPEN"} }), with(2, func(i *issue) { i.blockers = []string{"CLOSED"} })}, []int{1}},
		{"書いた条件はすべて AND で評価する", `{labels: {all: [ready-for-agent]}, unassigned: true}`,
			[]issue{with(1, func(i *issue) { i.assignees = []string{"bob"} }), with(2, func(i *issue) { i.labels = nil }), with(3, func(*issue) {})}, []int{3}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeWorkflow(workflowWithTriggers("\n  - name: t\n    on: issue\n    when: " + c.when + "\n    action: /t"))
			s.setIssues(c.issues...)

			r := s.dryRun()

			var want []string
			for _, n := range c.want {
				want = append(want, candidate("t", n))
			}
			assertCandidates(t, r, want...)
		})
	}
}

func TestDryRunPrintsNothingWhenNoIssueMatches(t *testing.T) {
	s := newSandbox(t)
	s.setIssues()

	r := s.dryRun()

	assertExit(t, r, 0)
	if r.stdout != "" || r.stderr != "" {
		t.Fatalf("stdout = %q, stderr = %q, want 両方とも空", r.stdout, r.stderr)
	}
}

func TestDryRunReplacesControlCharactersInTheTitleWithSpaces(t *testing.T) {
	s := newSandbox(t)
	i := readyIssue(5)
	i.title = "a\tb\nc\x1b[31md"
	s.setIssues(i)

	r := s.dryRun()

	assertCandidates(t, r, "implement\tissue\t#5\ta b c [31md")
}

func TestDryRunWritesNothing(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(1))

	assertExit(t, s.dryRun(), 0)

	if _, err := os.Stat(s.stateRoot); !os.IsNotExist(err) {
		t.Fatalf("試運転が state root (%s) を作った: %v", s.stateRoot, err)
	}
}

func TestDryRunReadsTheIssueRepoOfTheWorkflowDefinition(t *testing.T) {
	s := newSandbox(t)

	assertExit(t, s.dryRun(), 0)

	calls := s.calls("gh")
	if len(calls) != 1 {
		t.Fatalf("gh の呼び出し = %d 回, want 1", len(calls))
	}
	owner, _ := argValue(calls[0].Argv, "owner")
	name, _ := argValue(calls[0].Argv, "name")
	if owner != "acme" || name != "widgets" {
		t.Fatalf("gh が読んだ置き場 = %s/%s, want acme/widgets (argv %q)", owner, name, calls[0].Argv)
	}
}

func TestDryRunReadsTheWorkflowDefinitionAtTheGivenPath(t *testing.T) {
	s := newSandbox(t)
	other := s.root + "/elsewhere/flow.md"
	mustWrite(t, other, strings.Replace(defaultWorkflow, "name: implement", "name: elsewhere", 1))
	s.setIssues(readyIssue(1))

	r := s.dryRun(other)

	assertCandidates(t, r, candidate("elsewhere", 1))
}

func TestGhFailuresAreClassifiedIntoExitCodes(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		exit   int
		want   int
	}{
		{"認証が通らなければ 4", "HTTP 401: Bad credentials", 1, 4},
		{"gh auth login を促されたら 4", "To get started with GitHub CLI, please run:  gh auth login", 4, 4},
		{"置き場が見えなければ 2", "GraphQL: Could not resolve to a Repository with the name 'acme/widgets'. (repository)", 1, 2},
		{"rate limit なら 1", "GraphQL: API rate limit exceeded for user ID 1.", 1, 1},
		{"その他の失敗は 1", "connection reset", 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			s.failGh(c.stderr, c.exit)

			r := s.dryRun()

			assertExit(t, r, c.want)
			if r.stdout != "" || r.stderr == "" {
				t.Fatalf("stdout = %q, stderr = %q, want stdout は空で stderr に理由", r.stdout, r.stderr)
			}
		})
	}
}

func TestIssueWithMoreLabelsThanOneReadCoversIsNotEvaluated(t *testing.T) {
	s := newSandbox(t)
	i := readyIssue(9)
	i.labelTotal = 101
	s.setIssues(i)

	r := s.dryRun()

	assertExit(t, r, 1)
	if r.stdout != "" || !strings.Contains(r.stderr, "#9") {
		t.Fatalf("stdout = %q, stderr = %q, want stdout は空で stderr に #9", r.stdout, r.stderr)
	}
}

func TestTokenFromTheWorkflowDefinitionReachesGhAsGhToken(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(strings.Replace(defaultWorkflow, "  repo: acme/widgets\n", "  repo: acme/widgets\n  token: $WIDGETS_TOKEN\n", 1))

	r := s.runWithEnv(map[string]string{"WIDGETS_TOKEN": "secret-1"}, "loop", "--dry-run")

	assertExit(t, r, 0)
	if got := s.calls("gh")[0].Env["GH_TOKEN"]; got != "secret-1" {
		t.Fatalf("gh の GH_TOKEN = %q, want secret-1", got)
	}
}

func TestRepoWrittenAsAVariableIsReadFromTheEnvironment(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(strings.Replace(defaultWorkflow, "repo: acme/widgets", "repo: $ISSUE_REPO", 1))

	r := s.runWithEnv(map[string]string{"ISSUE_REPO": "other/gadgets"}, "loop", "--dry-run")

	assertExit(t, r, 0)
	owner, _ := argValue(s.calls("gh")[0].Argv, "owner")
	name, _ := argValue(s.calls("gh")[0].Argv, "name")
	if owner != "other" || name != "gadgets" {
		t.Fatalf("gh が読んだ置き場 = %s/%s, want other/gadgets", owner, name)
	}
}
