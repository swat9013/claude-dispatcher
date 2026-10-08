package blackbox_test

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// 試運転 `loop --dry-run` (formats.md §5)。述語の 1 つずつの意味は internal/trigger の単体テストが持ち、ここでは
// gh の応答から候補の行までを通した経路を見る。

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
	// 番号の大きい #50 のほうが古い
	oldest := readyIssue(50)
	oldest.createdAt = createdAt(1)
	s.setIssues(readyIssue(43), unlabeled, oldest, readyIssue(42))

	r := s.dryRun()

	assertCandidates(t, r, candidate("implement", 50), candidate("implement", 42), candidate("implement", 43))
}

func TestDryRunListsCandidatesInTriggerDeclarationOrderBeforeCreationOrder(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(workflowWithTriggers(`
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
	s.writeWorkflowWithCommands(workflowWithTriggers(`
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

func TestPredicatesAreEvaluatedOnWhatGhReturns(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(workflowWithTriggers(`
  - name: t
    on: issue
    when: {author: collaborator, unassigned: true, milestone: v1, blocked: false}
    action: /t`))
	matching := readyIssue(1)
	matching.milestone = "v1"
	matching.blockers = []string{"CLOSED"}
	outsider := readyIssue(2)
	outsider.milestone, outsider.association = "v1", "CONTRIBUTOR"
	assigned := readyIssue(3)
	assigned.milestone, assigned.assignees = "v1", []string{"bob"}
	blocked := readyIssue(4)
	blocked.milestone, blocked.blockers = "v1", []string{"OPEN"}
	s.setIssues(matching, outsider, assigned, blocked)

	r := s.dryRun()

	assertCandidates(t, r, candidate("t", 1))
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

// setStaleList は gh の open な一覧に listed を載せ、1 件の読み直しには read を返すようにする (一覧の検索が古い)。
func (s *sandbox) setStaleList(listed []issue, read ...stubwire.Rule) {
	s.t.Helper()
	rules := append(slices.Clone(read), stubwire.Rule{ArgsPrefix: []string{"api", "graphql"}, ArgsContain: []string{issueListQuery}, Stdout: issuePages(listed...)})
	s.respondAll("gh", rules)
}

func TestDryRunDoesNotListACandidateThatIsClosedWhenReadAgain(t *testing.T) {
	s := newSandbox(t)
	closed := readyIssue(1)
	closed.closed = true
	s.setStaleList([]issue{readyIssue(1), readyIssue(2)}, readRule(closed), readRule(readyIssue(2)))

	r := s.dryRun()

	assertCandidates(t, r, candidate("implement", 2))
}

func TestDryRunDoesNotListACandidateThatLeftTheTriggerWhenReadAgain(t *testing.T) {
	s := newSandbox(t)
	unlabeled := readyIssue(1)
	unlabeled.labels = nil
	s.setStaleList([]issue{readyIssue(1)}, readRule(unlabeled))

	r := s.dryRun()

	assertCandidates(t, r)
}

func TestDryRunDoesNotListACandidateThatMatchesAnEarlierTriggerWhenReadAgain(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(workflowWithTriggers(`
  - name: review
    on: issue
    when: {labels: {all: [review]}}
    action: /review
  - name: implement
    on: issue
    when: {labels: {all: [ready-for-agent]}}
    action: /implement`))
	both := readyIssue(1)
	both.labels = append(both.labels, "review")
	s.setStaleList([]issue{readyIssue(1)}, readRule(both))

	r := s.dryRun()

	assertCandidates(t, r)
}

func TestDryRunFailsWithoutPrintingCandidatesWhenACandidateCannotBeReadAgain(t *testing.T) {
	s := newSandbox(t)
	unreadable := stubwire.Rule{ArgsPrefix: []string{"api", "graphql"}, ArgsContain: []string{issueReadQuery, "number=2"}, Stderr: "HTTP 401: Bad credentials", Exit: 1}
	s.setStaleList([]issue{readyIssue(1), readyIssue(2)}, readRule(readyIssue(1)), unreadable)

	r := s.dryRun()

	assertExit(t, r, 4)
	if r.stdout != "" || !strings.Contains(r.stderr, "#2") {
		t.Fatalf("stdout = %q, stderr = %q, want stdout は空で stderr に #2", r.stdout, r.stderr)
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

func TestDryRunFailsWhenGhCannotBeResolved(t *testing.T) {
	skipIfSelfResolutionReachesARealOne(t, "gh")
	s := newSandbox(t)
	if err := os.Remove(s.binDir + "/gh"); err != nil {
		t.Fatal(err)
	}

	r := s.dryRun()

	assertExit(t, r, 1)
	if r.stdout != "" || !strings.Contains(r.stderr, "gh") {
		t.Fatalf("stdout = %q, stderr = %q", r.stdout, r.stderr)
	}
}

func TestIssueWithMoreThanOneReadCoversIsNotEvaluated(t *testing.T) {
	for _, connection := range []string{"labels", "assignees", "blockedBy"} {
		t.Run(connection, func(t *testing.T) {
			s := newSandbox(t)
			i := readyIssue(9)
			i.overflow = connection
			s.setIssues(i)

			r := s.dryRun()

			assertExit(t, r, 1)
			if r.stdout != "" || !strings.Contains(r.stderr, "#9") {
				t.Fatalf("stdout = %q, stderr = %q, want stdout は空で stderr に #9", r.stdout, r.stderr)
			}
		})
	}
}

func TestTokenFromTheWorkflowDefinitionReachesGhAsGhToken(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(strings.Replace(defaultWorkflow, "  repo: acme/widgets\n", "  repo: acme/widgets\n  token: $WIDGETS_TOKEN\n", 1))

	r := s.runWithEnv(map[string]string{"WIDGETS_TOKEN": "secret-1"}, "loop", "--dry-run")

	assertExit(t, r, 0)
	if got := s.calls("gh")[0].Env["GH_TOKEN"]; got != "secret-1" {
		t.Fatalf("gh の GH_TOKEN = %q, want secret-1", got)
	}
}

func TestRepoWrittenAsAVariableIsReadFromTheEnvironment(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(strings.Replace(defaultWorkflow, "repo: acme/widgets", "repo: $ISSUE_REPO", 1))

	r := s.runWithEnv(map[string]string{"ISSUE_REPO": "other/gadgets"}, "loop", "--dry-run")

	assertExit(t, r, 0)
	owner, _ := argValue(s.calls("gh")[0].Argv, "owner")
	name, _ := argValue(s.calls("gh")[0].Argv, "name")
	if owner != "other" || name != "gadgets" {
		t.Fatalf("gh が読んだ置き場 = %s/%s, want other/gadgets", owner, name)
	}
}
