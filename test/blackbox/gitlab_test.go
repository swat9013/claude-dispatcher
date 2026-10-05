package blackbox_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// GitLab の issue 置き場 (formats.md §2.1 の tracker.kind: gitlab)。glab は stub で、REST の応答の形を返す。
// 述語の 1 つずつの意味は internal/trigger の単体テストが持ち、ここでは glab の応答から候補・起動までを通した経路と、
// gitlab で書けない宣言の名指しを見る。

const (
	gitlabHost     = "gitlab.example.com"
	gitlabRepo     = "acme/sub/widgets"
	gitlabScopeKey = "gitlab.example.com/acme/sub/widgets"
	// gitlabProject は REST の endpoint で project を指す綴り (path を URL エンコードしたもの)
	gitlabProject = "projects/acme%2Fsub%2Fwidgets"
	// gitlabOpenIssues は open な issue の一覧の endpoint
	gitlabOpenIssues = gitlabProject + "/issues?state=opened&order_by=created_at&sort=asc&per_page=100"
)

// gitlabTracker は gitlab の issue 置き場を指す tracker の宣言。
const gitlabTracker = "tracker:\n  kind: gitlab\n  host: " + gitlabHost + "\n  repo: " + gitlabRepo + "\n"

// gitlabWorkflow は ready-for-agent の issue に当たる trigger 1 つだけの、gitlab の workflow 定義。
const gitlabWorkflow = "---\n" + gitlabTracker + `triggers:
  - name: implement
    on: issue
    when:
      labels:
        all: [ready-for-agent]
    action: |
      /implement
---
共通 prompt
`

// gitlabWorkflowWithTriggers は trigger の宣言だけを差し替えた gitlab の workflow 定義。
func gitlabWorkflowWithTriggers(triggers string) string {
	return "---\n" + gitlabTracker + "triggers:" + triggers + "\n---\n共通 prompt\n"
}

// gitlabStateDir は gitlab の issue 置き場の state dir (formats.md §1)。
func (s *sandbox) gitlabStateDir() string {
	sum := sha256.Sum256([]byte(gitlabScopeKey))
	return filepath.Join(s.stateRoot, "gitlab.example.com_acme_sub_widgets-"+hex.EncodeToString(sum[:])[:8])
}

// glIssue は glab が返す issue 1 件の fixture。GitLab の REST の応答の形に写して返す。
type glIssue struct {
	iid       int
	title     string
	labels    []string
	assignees []string
	milestone string
	// authorID は作者の user id。accessLevel は作者の project での access level (0 なら member でない)
	authorID    int
	accessLevel int
	closed      bool
}

// readyGitLabIssue は ready-for-agent の付いた、Developer の作者の issue。作成日時は番号の順に並ぶ。
func readyGitLabIssue(iid int) glIssue {
	return glIssue{iid: iid, title: "issue " + strconv.Itoa(iid), labels: []string{readyLabel}, authorID: 1000 + iid, accessLevel: 30}
}

func (i glIssue) json() map[string]any {
	assignees := []map[string]any{}
	for _, a := range i.assignees {
		assignees = append(assignees, map[string]any{"username": a})
	}
	var milestone any
	if i.milestone != "" {
		milestone = map[string]any{"title": i.milestone}
	}
	labels := i.labels
	if labels == nil {
		labels = []string{}
	}
	return map[string]any{
		"id":         900000 + i.iid,
		"iid":        i.iid,
		"title":      i.title,
		"web_url":    "https://" + gitlabHost + "/" + gitlabRepo + "/-/issues/" + strconv.Itoa(i.iid),
		"state":      map[bool]string{false: "opened", true: "closed"}[i.closed],
		"created_at": createdAt(i.iid),
		"labels":     labels,
		"assignees":  assignees,
		"milestone":  milestone,
		"author":     map[string]any{"id": i.authorID, "username": "user" + strconv.Itoa(i.authorID)},
	}
}

// glabAPI は glab の REST の GET の argv の先頭 (`api --hostname <host>`) に args を足したもの。
func glabAPI(args ...string) []string {
	return append([]string{"api", "--hostname", gitlabHost}, args...)
}

// glabRules は glab が issues を返す応答 rule の列。open な一覧には closed でないものを載せ、1 件の読み直しと作者の
// access level の読み出しには、どの issue にも応える。一覧は 1 page 2 件の配列を連ねて返す (--paginate の出力)。
func glabRules(issues ...glIssue) []stubwire.Rule {
	var rules []stubwire.Rule
	var pages []string
	var page []map[string]any
	for _, i := range issues {
		single, _ := json.Marshal(i.json())
		rules = append(rules, stubwire.Rule{ArgsPrefix: glabAPI(gitlabProject + "/issues/" + strconv.Itoa(i.iid)), Stdout: string(single)})
		member := stubwire.Rule{ArgsPrefix: glabAPI(gitlabProject + "/members/all/" + strconv.Itoa(i.authorID))}
		if i.accessLevel == 0 {
			member.Stdout, member.Stderr, member.Exit = `{"message":"404 Not found"}`, "glab: 404 Not found (HTTP 404)\n", 1
		} else {
			member.Stdout = `{"id":` + strconv.Itoa(i.authorID) + `,"access_level":` + strconv.Itoa(i.accessLevel) + `}`
		}
		rules = append(rules, member)
		if i.closed {
			continue
		}
		page = append(page, i.json())
		if len(page) == 2 {
			raw, _ := json.Marshal(page)
			pages, page = append(pages, string(raw)), nil
		}
	}
	if len(page) > 0 || len(pages) == 0 {
		if page == nil {
			page = []map[string]any{}
		}
		raw, _ := json.Marshal(page)
		pages = append(pages, string(raw))
	}
	return append(rules, stubwire.Rule{ArgsPrefix: glabAPI("--paginate", gitlabOpenIssues), Stdout: strings.Join(pages, "\n")})
}

// setGitLabIssues は gitlab の workflow 定義を置き、glab が issues を返すようにする。
func (s *sandbox) setGitLabIssues(issues ...glIssue) {
	s.t.Helper()
	s.respondAll("glab", glabRules(issues...))
}

// glabResponses は glabRules を glab の応答 file の中身にしたもの。worker の代役が issue を書き換えるときに使う。
func (s *sandbox) glabResponses(issues ...glIssue) stubwire.FileWrite {
	raw, err := json.Marshal(glabRules(issues...))
	if err != nil {
		s.t.Fatal(err)
	}
	return stubwire.FileWrite{Path: stubwire.ResponsesFile(s.stubRoot, "glab"), Content: string(raw)}
}

// assertGlabOnlyReads は glab の呼び出しがどれも REST の GET (`api --hostname <host> …`) で、method を変える flag
// (`-f` / `-F` / `--field` / `--raw-field` / `-X` / `--method`) を持たないことを確かめる。field を渡すと glab は POST を撃つ。
func (s *sandbox) assertGlabOnlyReads() {
	s.t.Helper()
	calls := s.calls("glab")
	if len(calls) == 0 {
		s.t.Fatal("glab を撃っていない")
	}
	for _, c := range calls {
		args := c.Argv[1:]
		if len(args) < 4 || args[0] != "api" || args[1] != "--hostname" || args[2] != gitlabHost {
			s.t.Fatalf("glab の呼び出し %q が api --hostname %s で始まらない", args, gitlabHost)
		}
		for _, arg := range args {
			switch arg {
			case "-f", "-F", "--field", "--raw-field", "-X", "--method", "--input":
				s.t.Fatalf("glab の呼び出し %q が GET 以外になりうる flag %s を持つ", args, arg)
			}
		}
	}
}

func TestGitLabDryRunListsTheIssuesThatMatchATrigger(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(gitlabWorkflow)
	unlabeled := readyGitLabIssue(44)
	unlabeled.labels = nil
	s.setGitLabIssues(readyGitLabIssue(42), readyGitLabIssue(43), unlabeled)

	r := s.dryRun()

	assertCandidates(t, r, candidate("implement", 42), candidate("implement", 43))
	s.assertGlabOnlyReads()
	s.assertNoGh("gitlab の issue 置き場なのに")
}

func TestGitLabLabelsAndAssigneesAreComparedIgnoringCase(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(gitlabWorkflowWithTriggers(`
  - {name: implement, on: issue, when: {labels: {all: [Ready-For-Agent]}, assignee: Alice}, action: /implement}`))
	assigned := readyGitLabIssue(42)
	assigned.assignees = []string{"alice"}
	s.setGitLabIssues(assigned, readyGitLabIssue(43))

	r := s.dryRun()

	assertCandidates(t, r, candidate("implement", 42))
}

func TestGitLabAuthorIsACollaboratorFromDeveloperUp(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(gitlabWorkflowWithTriggers(`
  - {name: implement, on: issue, when: {author: collaborator}, action: /implement}
  - {name: outsider, on: issue, when: {author: non_collaborator}, action: /outsider}`))
	developer := readyGitLabIssue(42)
	reporter := readyGitLabIssue(43)
	reporter.accessLevel = 20
	stranger := readyGitLabIssue(44)
	stranger.accessLevel = 0
	s.setGitLabIssues(developer, reporter, stranger)

	r := s.dryRun()

	assertCandidates(t, r, candidate("implement", 42), candidate("outsider", 43), candidate("outsider", 44))
}

func TestGitLabHostDefaultsToGitLabCom(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(strings.Replace(gitlabWorkflow, "  host: "+gitlabHost+"\n", "", 1))

	r := s.run("paths", "--json")

	assertExit(t, r, 0)
	if !strings.Contains(r.stdout, `"scope_key":"gitlab.com/acme/sub/widgets"`) {
		t.Fatalf("stdout:\n%s", r.stdout)
	}
}

func TestGitLabScopeKeyIsTheLowercasedHostAndPath(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(strings.Replace(gitlabWorkflow, "repo: "+gitlabRepo, "repo: Acme/Sub/Widgets", 1))

	r := s.run("paths", "--json")

	assertExit(t, r, 0)
	if !strings.Contains(r.stdout, `"scope_key":"`+gitlabScopeKey+`"`) {
		t.Fatalf("stdout:\n%s", r.stdout)
	}
	s.assertNoGlab("paths なのに")
}

// assertNoGlab は glab が 1 度も撃たれていないことを確かめる。
func (s *sandbox) assertNoGlab(why string) {
	s.t.Helper()
	if calls := s.calls("glab"); len(calls) != 0 {
		s.t.Fatalf("%s glab を撃った: %v", why, calls[0].Argv)
	}
}

func TestGitLabObservationFailuresAreClassified(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stderr string
		exit   int
	}{
		{"認証", "glab: 401 Unauthorized (HTTP 401)\n", 4},
		{"見えない", "glab: 404 Project Not Found (HTTP 404)\n", 2},
		{"rate limit", "glab: 429 Too Many Requests (HTTP 429)\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeWorkflowWithCommands(gitlabWorkflow)
			s.respond("glab", stubwire.Rule{ArgsPrefix: []string{"api"}, Stderr: tc.stderr, Exit: 1})

			r := s.dryRun()

			assertExit(t, r, tc.exit)
			if r.stdout != "" {
				t.Fatalf("失敗したのに stdout に出た: %q", r.stdout)
			}
		})
	}
}

func TestGitLabLoopLaunchesAWorkerAndCompletesWhenTheIssueLeavesTheTrigger(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(strings.Replace(s.workerWorkflow(""), "tracker:\n  kind: github\n  repo: acme/widgets\n", gitlabTracker, 1))
	s.setGitLabIssues(readyGitLabIssue(42))
	done := readyGitLabIssue(42)
	done.labels = nil
	s.onClaude(stubwire.Rule{Writes: []stubwire.FileWrite{s.glabResponses(done)}})

	loop := s.startLoop()

	loop.waitForOutput(regexp.MustCompile(` 終了 issue#42 \(implement\): completed`))
	calls := s.calls("claude")
	if len(calls) != 1 || calls[0].Cwd != s.workspace(42) {
		t.Fatalf("claude の呼び出し = %d 回 (%v), want workspace %s で 1 回", len(calls), calls, s.workspace(42))
	}
	action := calls[0].Argv[len(calls[0].Argv)-1]
	if action != "/implement issue #42 (implement, attempt 1)" {
		t.Fatalf("action = %q", action)
	}
	if _, err := os.Stat(filepath.Join(s.gitlabStateDir(), "log.jsonl")); err != nil {
		t.Fatalf("gitlab の scope key の state dir に log.jsonl が無い: %v", err)
	}
	s.assertGlabOnlyReads()
	s.assertNoGh("gitlab の issue 置き場なのに")
}

func TestGitLabRejectsTheDeclarationsItCannotSupportWhereverTheyAreWritten(t *testing.T) {
	s := newSandbox(t)
	// triggers を tracker より前に書いても、kind に依る誤りを名指しする
	s.writeWorkflowWithCommands(`---
triggers:
  - {name: implement, on: issue, when: {blocked: false}, action: /implement}
  - {name: fix, on: cl, when: {ci_failed: true, head: "x/*"}, action: /fix}
tracker:
  kind: gitlab
  repo: acme/sub/widgets
  token: $GL_TOKEN
---
共通 prompt
`)

	r := s.runWithEnv(map[string]string{"GL_TOKEN": "secret"}, "loop", "--dry-run")

	s.assertRejected(r, "triggers[0].when.blocked", "triggers[1].on", "tracker.token")
	s.assertNoGlab("workflow 定義が誤っているのに")
}

func TestGitHubRejectsTheHostOfTheTracker(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(strings.Replace(defaultWorkflow, "  repo: acme/widgets\n", "  repo: acme/widgets\n  host: github.com\n", 1))

	r := s.dryRun()

	s.assertRejected(r, "tracker.host", "未知の key")
}

func TestGitLabRepoNeedsAGroupAndAName(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(strings.Replace(gitlabWorkflow, "repo: "+gitlabRepo, "repo: widgets", 1))

	r := s.dryRun()

	s.assertRejected(r, "tracker.repo")
}

func TestGitLabDoctorNamesThePlaceAndTheGlabEntries(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(gitlabWorkflow)
	s.setGitLabIssues()

	r := s.run("doctor")

	assertExit(t, r, 0)
	for _, want := range []string{
		"ok   issue 置き場 " + gitlabScopeKey + "\n",
		"ok   glab " + filepath.Join(s.binDir, "glab") + "\n",
		"  Bash(glab issue:*)\n",
		"  Bash(glab mr:*)\n",
		"  Bash(git push:*)\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout に %q が無い:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "Bash(gh ") || strings.Contains(r.stdout, "ok   gh ") {
		t.Errorf("gitlab なのに gh を出した:\n%s", r.stdout)
	}
}

// gitlabOrigin は clone の origin の URL (scp の綴り) を返す git の応答 rule。
var gitlabOrigin = stubwire.Rule{ArgsPrefix: []string{"remote", "get-url", "origin"}, Stdout: "git@" + gitlabHost + ":" + gitlabRepo + ".git\n"}

// gitlabRepoView は origin の URL を渡した glab repo view が project を返す応答 rule。
var gitlabRepoView = stubwire.Rule{
	ArgsPrefix: []string{"repo", "view", "git@" + gitlabHost + ":" + gitlabRepo + ".git", "--output", "json"},
	Stdout:     `{"id":7,"path_with_namespace":"` + gitlabRepo + `"}`,
}

func TestGitLabSetupWritesTheHostAndPathOfTheOrigin(t *testing.T) {
	s := newSandbox(t)
	if err := os.Remove(s.workflowFile()); err != nil {
		t.Fatal(err)
	}
	s.respond("git", gitlabOrigin)
	s.respond("glab", gitlabRepoView)

	r := s.run("setup")

	assertExit(t, r, 0)
	written, err := os.ReadFile(s.workflowFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), gitlabTracker) {
		t.Fatalf("雛形の tracker が %q でない:\n%s", gitlabTracker, written)
	}
	for _, unwanted := range []string{"blocked:", "on: cl", "pull request", " gh "} {
		if strings.Contains(string(written), unwanted) {
			t.Errorf("gitlab の雛形に %q がある:\n%s", unwanted, written)
		}
	}
	for _, wanted := range []string{"merge request", "glab", "Closes #{{ .issue.number }}"} {
		if !strings.Contains(string(written), wanted) {
			t.Errorf("gitlab の雛形に %q が無い:\n%s", wanted, written)
		}
	}
	s.assertNoGh("gitlab の clone なのに")
}

func TestGitLabSetupTemplateAloneLetsTheDryRunPassWithoutPlugins(t *testing.T) {
	s := newSandbox(t)
	if err := os.RemoveAll(filepath.Join(s.home, ".claude")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.workflowFile()); err != nil {
		t.Fatal(err)
	}
	s.respond("git", gitlabOrigin)
	s.respondAll("glab", append([]stubwire.Rule{gitlabRepoView}, glabRules(readyGitLabIssue(42))...))
	assertExit(t, s.run("setup"), 0)

	r := s.dryRun()

	assertExit(t, r, 0)
	if !strings.HasPrefix(r.stdout, "implement\tissue\t#42\t") {
		t.Fatalf("試運転の stdout:\n%s\nstderr:\n%s", r.stdout, r.stderr)
	}
}

func TestPathIsLeftAsIsWhenOnlyTheUnusedTrackerCLIIsMissing(t *testing.T) {
	s := newSandbox(t)
	if err := os.Remove(filepath.Join(s.binDir, "glab")); err != nil {
		t.Fatal(err)
	}
	s.setIssues(readyIssue(42))

	r := s.dryRun()

	assertExit(t, r, 0)
	calls := s.calls("gh")
	if len(calls) == 0 {
		t.Fatal("gh を撃っていない")
	}
	if got := calls[0].Env["PATH"]; got != s.binDir {
		t.Fatalf("gh に渡った PATH = %q, want %q (使わない glab が無いだけで PATH を書き換えた)", got, s.binDir)
	}
}
