package blackbox_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// Jira の issue 置き場 (formats.md §2.1 の tracker.kind: jira、ADR 0010)。acli は stub で、acli の --json の応答の形を返す。
// 述語の 1 つずつの意味は internal/trigger の単体テストが持ち、ここでは acli の応答から候補・起動・読み直しまでを通した経路と、
// 起動時の issue 置き場の確認と、jira で書けない宣言の名指しを見る。

const (
	jiraSite     = "acme.atlassian.net"
	jiraKey      = "WIDGETS"
	jiraScopeKey = "acme.atlassian.net/widgets"
	// jiraOpenJQL は open な一覧の JQL
	jiraOpenJQL = `project = "WIDGETS" AND statusCategory != Done`
	// jiraBlockedJQL は依存を持つ open な issue の JQL
	jiraBlockedJQL = jiraOpenJQL + ` AND issueLinkType = "is blocked by"`
	readyStatus    = "Ready for Agent"
)

// jiraTracker は jira の issue 置き場を指す tracker の宣言。repo は小文字で書き、大文字に揃うことも通す。
const jiraTracker = "tracker:\n  kind: jira\n  host: " + jiraSite + "\n  repo: widgets\n"

// jiraWorkflowWithTriggers は trigger の宣言だけを差し替えた jira の workflow 定義。
func jiraWorkflowWithTriggers(triggers string) string {
	return "---\n" + jiraTracker + "triggers:" + triggers + "\n---\n共通 prompt\n"
}

// jiraWorkflow は status が Ready for Agent の issue に当たる trigger 1 つだけの、jira の workflow 定義。
var jiraWorkflow = jiraWorkflowWithTriggers(`
  - {name: implement, on: issue, when: {status: {any: [Ready for Agent]}}, action: "/implement {{ .issue.key }}"}`)

// jiraStateDir は jira の issue 置き場の state dir (formats.md §1)。
func (s *sandbox) jiraStateDir() string {
	sum := sha256.Sum256([]byte(jiraScopeKey))
	return filepath.Join(s.stateRoot, "acme.atlassian.net_widgets-"+hex.EncodeToString(sum[:])[:8])
}

// jiraIssue は acli が返す issue 1 件の fixture。
type jiraIssue struct {
	number    int
	title     string
	status    string
	done      bool
	issueType string
	labels    []string
	// assignee は担当者の accountId (空なら担当者なし)
	assignee string
	// blockers は is blocked by の依存先の status の区分が done か (1 つ 1 要素)
	blockers []bool
	// gone は消えたか見えなくなった issue (view が失敗し、一覧にも出ない)
	gone bool
	// viewFails は view だけが失敗する issue (一覧には出る。一時的な失敗の代役)
	viewFails bool
}

// readyJiraIssue は Ready for Agent の task。
func readyJiraIssue(number int) jiraIssue {
	return jiraIssue{number: number, title: "issue " + strconv.Itoa(number), status: readyStatus, issueType: "Task"}
}

func (i jiraIssue) key() string { return jiraKey + "-" + strconv.Itoa(i.number) }

func jiraStatus(name string, done bool) map[string]any {
	category := map[bool]string{false: "new", true: "done"}[done]
	return map[string]any{"name": name, "statusCategory": map[string]any{"key": category}}
}

// json は acli の issue の応答の形。withLinks なら issuelinks を載せる (view の応答)。
func (i jiraIssue) json(withLinks bool) map[string]any {
	var assignee any
	if i.assignee != "" {
		assignee = map[string]any{"accountId": i.assignee, "displayName": "someone"}
	}
	labels := i.labels
	if labels == nil {
		labels = []string{}
	}
	fields := map[string]any{
		"summary":   i.title,
		"issuetype": map[string]any{"name": i.issueType},
		"assignee":  assignee,
		"status":    jiraStatus(i.status, i.done),
		"labels":    labels,
	}
	if withLinks {
		links := []map[string]any{}
		for n, done := range i.blockers {
			links = append(links, map[string]any{
				"type":        map[string]any{"name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
				"inwardIssue": map[string]any{"key": jiraKey + "-" + strconv.Itoa(900+n), "fields": map[string]any{"status": jiraStatus("blocker", done)}},
			})
		}
		// この issue が他を block する link (outward) は依存先に数えない
		links = append(links, map[string]any{
			"type":         map[string]any{"name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
			"outwardIssue": map[string]any{"key": jiraKey + "-999", "fields": map[string]any{"status": jiraStatus("other", false)}},
		})
		fields["issuelinks"] = links
	}
	return map[string]any{"key": i.key(), "id": strconv.Itoa(70000 + i.number), "fields": fields}
}

// jiraAuthStatus は acli jira auth status の応答 rule。site は認証の site
func jiraAuthStatus(site string) stubwire.Rule {
	return stubwire.Rule{
		ArgsPrefix: []string{"jira", "auth", "status"},
		Stdout:     "✓ Authenticated\n  Site: " + site + "\n  Email: someone@example.com\n  Authentication Type: oauth\n",
	}
}

func jiraSearch(jql string) []string { return []string{"jira", "workitem", "search", "--jql", jql} }

// jiraKnownStatuses は site にある status 名 (件数の検索に応える)
var jiraKnownStatuses = []string{readyStatus, "In Review", "Needs Triage"}

// jiraRules は acli が issues を返す応答 rule の列。認証は acme の site で通り、status 名は jiraKnownStatuses だけが実在し、
// project の issue type は Task / Subtask / Epic。open な一覧は渡した順に返す (並べるのは dispatcher)。
func jiraRules(issues ...jiraIssue) []stubwire.Rule {
	rules := []stubwire.Rule{jiraAuthStatus(jiraSite)}
	for _, name := range jiraKnownStatuses {
		rules = append(rules, stubwire.Rule{ArgsPrefix: jiraSearch(`project = "WIDGETS" AND status = "` + name + `"`), Stdout: "✓ Number of work items in the search: 1\n"})
	}
	rules = append(rules, stubwire.Rule{
		ArgsPrefix: []string{"jira", "project", "view", "--key", jiraKey, "--json"},
		Stdout:     `{"key":"WIDGETS","issueTypes":[{"name":"Task"},{"name":"Subtask","subtask":true},{"name":"Epic"}]}`,
	})
	open, blocked := []map[string]any{}, []map[string]any{}
	for _, i := range issues {
		view := stubwire.Rule{ArgsPrefix: []string{"jira", "workitem", "view", i.key()}}
		if i.gone || i.viewFails {
			view.Stderr, view.Exit = "✗ Error: 課題は存在しないか、表示できる権限がありません。\n", 1
		} else {
			raw, _ := json.Marshal(i.json(true))
			view.Stdout = string(raw)
		}
		rules = append(rules, view)
		if i.gone || i.done {
			continue
		}
		open = append(open, i.json(false))
		if len(i.blockers) > 0 {
			blocked = append(blocked, i.json(false))
		}
	}
	rawBlocked, _ := json.Marshal(blocked)
	rawOpen, _ := json.Marshal(open)
	// 依存の検索は open な一覧の JQL を前に含むので、先に置く
	return append(rules,
		stubwire.Rule{ArgsPrefix: jiraSearch(jiraBlockedJQL), Stdout: string(rawBlocked)},
		stubwire.Rule{ArgsPrefix: jiraSearch(jiraOpenJQL), Stdout: string(rawOpen)},
	)
}

// setJiraIssues は acli が issues を返すようにする。
func (s *sandbox) setJiraIssues(issues ...jiraIssue) {
	s.t.Helper()
	s.respondAll("acli", jiraRules(issues...))
}

// jiraResponses は jiraRules を acli の応答 file の中身にしたもの。worker の代役が issue を書き換えるときに使う。
func (s *sandbox) jiraResponses(issues ...jiraIssue) stubwire.FileWrite {
	raw, err := json.Marshal(jiraRules(issues...))
	if err != nil {
		s.t.Fatal(err)
	}
	return stubwire.FileWrite{Path: stubwire.ResponsesFile(s.stubRoot, "acli"), Content: string(raw)}
}

// jiraCandidate は試運転の候補の行。参照は Jira の key
func jiraCandidate(trigger string, number int) string {
	return trigger + "\tissue\t" + jiraKey + "-" + strconv.Itoa(number) + "\tissue " + strconv.Itoa(number)
}

// assertAcliOnlyReads は acli の呼び出しがどれも読み出し (workitem search / workitem view / auth status / project view) で
// あることを確かめる。
func (s *sandbox) assertAcliOnlyReads() {
	s.t.Helper()
	calls := s.calls("acli")
	if len(calls) == 0 {
		s.t.Fatal("acli を撃っていない")
	}
	reads := [][]string{{"jira", "workitem", "search"}, {"jira", "workitem", "view"}, {"jira", "auth", "status"}, {"jira", "project", "view"}}
	for _, c := range calls {
		args := c.Argv[1:]
		if !slices.ContainsFunc(reads, func(read []string) bool { return len(args) >= len(read) && slices.Equal(args[:len(read)], read) }) {
			s.t.Fatalf("acli の呼び出し %q が読み出しでない", args)
		}
	}
}

// assertNoOtherTrackerCLI は gh も glab も撃っていないことを確かめる。
func (s *sandbox) assertNoOtherTrackerCLI(why string) {
	s.t.Helper()
	s.assertNoGh(why)
	s.assertNoGlab(why)
}

// assertNoAcli は acli が 1 度も撃たれていないことを確かめる。
func (s *sandbox) assertNoAcli(why string) {
	s.t.Helper()
	if calls := s.calls("acli"); len(calls) != 0 {
		s.t.Fatalf("%s acli を撃った: %v", why, calls[0].Argv)
	}
}

func TestJiraDryRunListsTheIssuesInTheStatusByNumberWithTheirKeys(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(jiraWorkflow)
	triage := readyJiraIssue(44)
	triage.status = "Needs Triage"
	// acli の一覧は作成日時を返さないので、一覧の順に依らず番号の小さい順に並ぶ
	s.setJiraIssues(readyJiraIssue(43), triage, readyJiraIssue(9))

	r := s.dryRun()

	assertCandidates(t, r, jiraCandidate("implement", 9), jiraCandidate("implement", 43))
	s.assertAcliOnlyReads()
	s.assertNoOtherTrackerCLI("jira の issue 置き場なのに")
}

func TestJiraPredicatesAreEvaluatedOnTheAcliResponses(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(jiraWorkflowWithTriggers(`
  - {name: unblock, on: issue, when: {blocked: true}, action: /unblock}
  - {name: subtask, on: issue, when: {type: {any: [subtask]}, status: {none: [In Review]}}, action: /subtask}
  - {name: mine, on: issue, when: {assignee: "557058:abc", labels: {all: [Backend]}}, action: /mine}`))
	blockedByOpen := readyJiraIssue(10)
	blockedByOpen.blockers = []bool{true, false}
	blockedByDone := readyJiraIssue(11)
	blockedByDone.blockers = []bool{true}
	blockedByDone.issueType = "Subtask"
	inReview := readyJiraIssue(12)
	inReview.issueType, inReview.status = "Subtask", "In Review"
	mine := readyJiraIssue(13)
	mine.assignee, mine.labels = "557058:abc", []string{"backend"}
	s.setJiraIssues(blockedByOpen, blockedByDone, inReview, mine)

	r := s.dryRun()

	assertCandidates(t, r, jiraCandidate("unblock", 10), jiraCandidate("subtask", 11), jiraCandidate("mine", 13))
}

func TestJiraBlockersAreNotReadWithoutABlockedPredicate(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(jiraWorkflow)
	blocked := readyJiraIssue(10)
	blocked.blockers = []bool{false}
	s.setJiraIssues(blocked)

	assertCandidates(t, s.dryRun(), jiraCandidate("implement", 10))

	for _, c := range s.calls("acli") {
		if slices.Contains(c.Argv, jiraBlockedJQL) || slices.Equal(c.Argv[1:min(4, len(c.Argv))], []string{"jira", "workitem", "view"}) {
			t.Fatalf("blocked を書いていないのに依存先を読んだ: %q", c.Argv[1:])
		}
	}
}

func TestJiraUnknownStatusAndTypeNamesAreNamedBeforeObserving(t *testing.T) {
	for _, args := range [][]string{{"loop", "--dry-run"}, {"loop"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			s := newSandbox(t)
			s.writeWorkflowWithCommands(jiraWorkflowWithTriggers(`
  - {name: implement, on: issue, when: {status: {any: [Ready for Agnet]}}, action: /implement}
  - {name: epics, on: issue, when: {type: {none: [Epik]}}, action: /epics}`))
			s.setJiraIssues(readyJiraIssue(42))

			r := s.run(args...)

			assertExit(t, r, 2)
			for _, want := range []string{`trigger implement: status "Ready for Agnet"`, `trigger epics: issue type "Epik"`} {
				if !strings.Contains(r.stderr, want) {
					t.Errorf("stderr が %q を名指ししていない:\n%s", want, r.stderr)
				}
			}
			if r.stdout != "" {
				t.Fatalf("失敗したのに stdout に出た: %q", r.stdout)
			}
			if _, err := os.Stat(s.jiraStateDir()); err == nil {
				t.Fatal("起動時の確認に落ちたのに state dir を作った")
			}
		})
	}
}

func TestJiraFailsWhenAcliIsSignedInToAnotherSite(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(jiraWorkflow)
	s.respondAll("acli", append([]stubwire.Rule{jiraAuthStatus("other.atlassian.net")}, jiraRules(readyJiraIssue(42))...))

	r := s.dryRun()

	assertExit(t, r, 2)
	if !strings.Contains(r.stderr, "other.atlassian.net") || !strings.Contains(r.stderr, jiraSite) {
		t.Fatalf("stderr が 2 つの site を名指ししていない:\n%s", r.stderr)
	}
	for _, c := range s.calls("acli") {
		if slices.Contains(c.Argv, "search") {
			t.Fatalf("別の site に認証しているのに検索した: %q", c.Argv[1:])
		}
	}
}

func TestJiraObservationFailuresAreClassifiedByTheAuthStatus(t *testing.T) {
	failingSearch := stubwire.Rule{ArgsPrefix: []string{"jira", "workitem", "search"}, Stderr: "✗ Error: failed to parse JQL query\n", Exit: 1}
	for _, tc := range []struct {
		name  string
		rules []stubwire.Rule
		exit  int
	}{
		{"認証", []stubwire.Rule{{ArgsPrefix: []string{"jira", "auth", "status"}, Stderr: "✗ Error: failed to retrieve authenticated status\n", Exit: 1}}, 4},
		{"見えない", []stubwire.Rule{jiraAuthStatus(jiraSite), failingSearch}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeWorkflowWithCommands(jiraWorkflow)
			s.respondAll("acli", tc.rules)

			r := s.dryRun()

			assertExit(t, r, tc.exit)
			if r.stdout != "" {
				t.Fatalf("失敗したのに stdout に出た: %q", r.stdout)
			}
		})
	}
}

// jiraWorkerWorkflow は workerWorkflow の tracker を jira に、trigger を Ready for Agent の status に差し替えたもの。
func (s *sandbox) jiraWorkerWorkflow() string {
	w := s.workerWorkflow("")
	for _, r := range [][2]string{
		{"tracker:\n  kind: github\n  repo: acme/widgets\n", jiraTracker},
		{"when: {labels: {all: [ready-for-agent]}}", "when: {status: {any: [Ready for Agent]}}"},
		{"/implement issue #{{ .issue.number }}", "/implement {{ .issue.key }}"},
	} {
		if !strings.Contains(w, r[0]) {
			s.t.Fatalf("workerWorkflow に %q が無い", r[0])
		}
		w = strings.Replace(w, r[0], r[1], 1)
	}
	return w
}

func TestJiraLoopLaunchesAWorkerAndCompletesWhenTheStatusMoves(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.jiraWorkerWorkflow())
	s.setJiraIssues(readyJiraIssue(42))
	moved := readyJiraIssue(42)
	moved.status = "In Review"
	s.onClaude(stubwire.Rule{Writes: []stubwire.FileWrite{s.jiraResponses(moved)}})

	loop := s.startLoop()

	loop.waitForOutput(regexp.MustCompile(` 終了 issue#42 \(implement\): completed — trigger から外れた`))
	calls := s.calls("claude")
	if len(calls) != 1 || calls[0].Cwd != s.workspace(42) {
		t.Fatalf("claude の呼び出し = %d 回 (%v), want workspace %s で 1 回", len(calls), calls, s.workspace(42))
	}
	if action := calls[0].Argv[len(calls[0].Argv)-1]; action != "/implement WIDGETS-42 (implement, attempt 1)" {
		t.Fatalf("action = %q", action)
	}
	if mark, err := os.ReadFile(s.mark("after_create-42")); err != nil || !strings.HasPrefix(string(mark), "issue 42 ") {
		t.Fatalf("hooks の環境変数 = %q (%v), want 種類 issue と番号 42", mark, err)
	}
	if _, err := os.Stat(filepath.Join(s.jiraStateDir(), "log.jsonl")); err != nil {
		t.Fatalf("jira の scope key の state dir に log.jsonl が無い: %v", err)
	}
	if !strings.Contains(loop.stdout.String(), "implement issue WIDGETS-42") {
		t.Fatalf("tick の行の候補が key で出ていない:\n%s", loop.stdout.String())
	}
	s.assertAcliOnlyReads()
	s.assertNoOtherTrackerCLI("jira の issue 置き場なのに")
}

func TestJiraIssueThatVanishesIsTreatedAsTerminal(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.jiraWorkerWorkflow())
	s.setJiraIssues(readyJiraIssue(42))
	vanished := readyJiraIssue(42)
	vanished.gone = true
	s.onClaude(stubwire.Rule{Writes: []stubwire.FileWrite{s.jiraResponses(vanished)}})

	loop := s.startLoop()

	loop.waitForOutput(regexp.MustCompile(` 終了 issue#42 \(implement\): completed — 終端`))
	if _, err := os.Stat(s.mark("before_remove-42")); err != nil {
		t.Fatalf("終端の issue の workspace を消していない: %v", err)
	}
}

func TestJiraIssueThatFailsToBeViewedButIsStillOpenIsNotTreatedAsTerminal(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.jiraWorkerWorkflow())
	s.setJiraIssues(readyJiraIssue(42))
	unreadable := readyJiraIssue(42)
	unreadable.viewFails = true
	s.onClaude(stubwire.Rule{Writes: []stubwire.FileWrite{s.jiraResponses(unreadable)}})

	loop := s.startLoop()

	loop.waitForOutput(regexp.MustCompile(`issue#42.*終わった worker の作業対象を読み直せない`))
	if strings.Contains(loop.stdout.String(), "終了 issue#42") {
		t.Fatalf("読み直せないのに終わり方を決めた:\n%s", loop.stdout.String())
	}
	if _, err := os.Stat(s.mark("before_remove-42")); err == nil {
		t.Fatal("open な issue の workspace を消した")
	}
}

func TestJiraIssueMovedToAnotherProjectIsTreatedAsTerminal(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(s.jiraWorkerWorkflow())
	s.setJiraIssues(readyJiraIssue(42))
	// 移した先の issue (旧 key の転送で返る)。一覧には出ない
	moved, _ := json.Marshal(map[string]any{"key": "GADGETS-7", "fields": map[string]any{"summary": "moved", "status": jiraStatus(readyStatus, false), "issuetype": map[string]any{"name": "Task"}}})
	rules := append([]stubwire.Rule{{ArgsPrefix: []string{"jira", "workitem", "view", "WIDGETS-42"}, Stdout: string(moved)}}, jiraRules()...)
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	s.onClaude(stubwire.Rule{Writes: []stubwire.FileWrite{{Path: stubwire.ResponsesFile(s.stubRoot, "acli"), Content: string(raw)}}})

	loop := s.startLoop()

	loop.waitForOutput(regexp.MustCompile(` 終了 issue#42 \(implement\): completed — 終端`))
}

func TestJiraRejectsTheDeclarationsItCannotSupportWhereverTheyAreWritten(t *testing.T) {
	s := newSandbox(t)
	// triggers を tracker より前に書いても、kind に依る誤りを名指しする
	s.writeWorkflowWithCommands(`---
triggers:
  - {name: implement, on: issue, when: {author: collaborator, milestone: v1}, action: /implement}
  - {name: fix, on: cl, action: /fix}
` + strings.Replace(jiraTracker, "  repo: widgets\n", "  repo: widgets\n  token: $JIRA_TOKEN\n", 1) + `---
共通 prompt
`)

	r := s.runWithEnv(map[string]string{"JIRA_TOKEN": "secret"}, "loop", "--dry-run")

	s.assertRejected(r, "triggers[0].when.author", "triggers[0].when.milestone", "triggers[1].on", "tracker.token")
	s.assertNoAcli("workflow 定義が誤っているのに")
}

func TestJiraScopeKeyIsTheLowercasedSiteAndProjectKey(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(strings.Replace(jiraWorkflow, "host: "+jiraSite, "host: ACME.atlassian.net", 1))

	r := s.run("paths", "--json")

	assertExit(t, r, 0)
	if !strings.Contains(r.stdout, `"scope_key":"`+jiraScopeKey+`"`) {
		t.Fatalf("stdout:\n%s", r.stdout)
	}
	s.assertNoAcli("paths なのに")
}

func TestJiraDoctorNamesThePlaceAndTheAcliEntries(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(jiraWorkflow)
	s.setJiraIssues()

	r := s.run("doctor")

	assertExit(t, r, 0)
	for _, want := range []string{
		"ok   issue 置き場 " + jiraSite + "/" + jiraKey + "\n",
		"ok   acli " + filepath.Join(s.binDir, "acli") + "\n",
		"  Bash(acli jira workitem:*)\n",
		"  Bash(git push:*)\n",
		"\nCL を開く CLI (gh pr / glab mr) の entry も足す\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout に %q が無い:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "Bash(gh ") || strings.Contains(r.stdout, "Bash(glab ") {
		t.Errorf("jira なのに gh か glab の entry を出した:\n%s", r.stdout)
	}
}

func TestJiraDoctorNamesEachUnknownStatus(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(jiraWorkflowWithTriggers(`
  - {name: implement, on: issue, when: {status: {any: [Redy, Ready for Agent, Todo]}}, action: /implement}`))
	s.setJiraIssues()

	r := s.run("doctor")

	assertExit(t, r, 1)
	for _, want := range []string{`NG   trigger implement: status "Redy"`, `NG   trigger implement: status "Todo"`} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout に %q が無い:\n%s", want, r.stdout)
		}
	}
}

func TestJiraSetupWritesTheSiteAndProjectKeyFromTheFlags(t *testing.T) {
	s := newSandbox(t)
	if err := os.Remove(s.workflowFile()); err != nil {
		t.Fatal(err)
	}

	r := s.run("setup", "--kind", "jira", "--host="+jiraSite, "--repo", "widgets")

	assertExit(t, r, 0)
	written, err := os.ReadFile(s.workflowFile())
	if err != nil {
		t.Fatal(err)
	}
	if want := "tracker:\n  kind: jira\n  host: " + jiraSite + "\n  repo: WIDGETS\n"; !strings.Contains(string(written), want) {
		t.Fatalf("雛形の tracker が %q でない:\n%s", want, written)
	}
	for _, wanted := range []string{"any: [Ready for Agent]", "claude-dispatcher/{{ .issue.key }}", "acli"} {
		if !strings.Contains(string(written), wanted) {
			t.Errorf("jira の雛形に %q が無い:\n%s", wanted, written)
		}
	}
	for _, unwanted := range []string{"\n    on: cl", "\n      type:"} {
		if strings.Contains(string(written), unwanted) {
			t.Errorf("jira の雛形に %q がある:\n%s", unwanted, written)
		}
	}
	for _, name := range []string{"git", "gh", "glab", "acli"} {
		if calls := s.calls(name); len(calls) != 0 {
			t.Errorf("flag で決めたのに %s を撃った: %v", name, calls[0].Argv)
		}
	}
}

func TestJiraSetupTemplateAloneLetsTheDryRunPassWithoutPlugins(t *testing.T) {
	s := newSandbox(t)
	if err := os.RemoveAll(filepath.Join(s.home, ".claude")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.workflowFile()); err != nil {
		t.Fatal(err)
	}
	assertExit(t, s.run("setup", "--kind", "jira", "--host", jiraSite, "--repo", jiraKey), 0)
	s.setJiraIssues(readyJiraIssue(42))

	r := s.dryRun()

	assertCandidates(t, r, jiraCandidate("implement", 42))
}

func TestSetupRejectsMalformedJiraFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--kind", "jira", "--host", jiraSite},
		{"--kind", "linear", "--host", jiraSite, "--repo", jiraKey},
		{"--host", jiraSite, "--repo", jiraKey},
		{"--kind", "jira", "--host", "https://" + jiraSite, "--repo", jiraKey},
		{"--kind", "jira", "--host", jiraSite, "--repo", "acme/widgets"},
		{"--kind"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			s := newSandbox(t)
			if err := os.Remove(s.workflowFile()); err != nil {
				t.Fatal(err)
			}

			r := s.run(append([]string{"setup"}, args...)...)

			assertExit(t, r, 2)
			if _, err := os.Stat(s.workflowFile()); err == nil {
				t.Fatal("引数が誤っているのに雛形を書いた")
			}
		})
	}
}
