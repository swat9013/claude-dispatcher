package blackbox_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// Jira の issue 置き場と、cl: で宣言した別の CL 置き場 (案内の「項目」の cl、formats.md §6 の CL 置き場の確認、ADR 0015)。
// issue は acli、CL は gh の stub が返す。

// jiraFixCI は issue の trigger と、CI の落ちた CL に当たる trigger を置いた jira の workflow 定義。
var jiraFixCI = jiraWorkflowWithTriggers(`
  - {name: implement, on: issue, when: {status: {any: [Ready for Agent]}}, action: "/implement {{ .issue.key }}"}
  - {name: fix-ci, on: cl, when: {ci_failed: true, author: collaborator}, action: "/fix-ci {{ .cl.number }}"}`)

// failingCL は CI の落ちた CL。
func failingCL(number int) cl {
	c := readyCL(number)
	c.checks = "FAILURE"
	return c
}

func TestJiraWithAGitHubCLHostRunsCLTriggersOnTheGitHubCLs(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(jiraFixCI)
	s.setJiraPlaces(readyJiraIssue(42))
	s.setStore(nil, []cl{failingCL(7), readyCL(8)})

	r := s.dryRun()

	assertCandidates(t, r, jiraCandidate("implement", 42), clCandidate("fix-ci", 7))
	s.assertAcliOnlyReads()
}

func TestJiraDryRunReadsEachOpenListOnlyOnceForTheConfirmationAndTheSnapshot(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(jiraFixCI)
	s.setJiraPlaces(readyJiraIssue(42))
	s.setStore(nil, []cl{failingCL(7)})

	r := s.dryRun()

	assertCandidates(t, r, jiraCandidate("implement", 42), clCandidate("fix-ci", 7))
	// 候補の読み直しは workitem view と pullRequest(number:) を撃つので、一覧の数には入らない。stub は一覧を 1 page で返す
	if n := s.countCalls("acli", func(arg string) bool { return arg == jiraOpenJQL }); n != 1 {
		t.Fatalf("issue 置き場の open な一覧を %d 回読んだ (1 回のはず)", n)
	}
	if n := s.countCalls("gh", func(arg string) bool { return strings.Contains(arg, clListQuery) }); n != 1 {
		t.Fatalf("CL 置き場の open な一覧を %d 回読んだ (1 回のはず)", n)
	}
}

func TestJiraDryRunWithAGitLabCLHostStillNamesTheFailedRecheckRequestOfTheConfirmedList(t *testing.T) {
	s := newSandbox(t)
	gitlabCL := "cl:\n  kind: gitlab\n  host: " + gitlabHost + "\n  repo: " + gitlabRepo + "\n"
	s.writeWorkflowWithCommands("---\n" + jiraTracker + gitlabCL + "triggers:\n  - {name: t, on: cl, action: /t}\n---\n共通 prompt\n")
	s.setJiraPlaces()
	unchecked := readyMR(1)
	unchecked.mergeStatus = "UNCHECKED"
	recheckFails := stubwire.Rule{ArgsPrefix: glabAPI(), ArgsContain: []string{"with_merge_status_recheck=true"}, Stderr: "glab: HTTP 502\n", Exit: 1}
	s.respondAll("glab", append([]stubwire.Rule{recheckFails}, glabStoreRules(nil, []glMR{unchecked})...))

	r := s.dryRun()

	assertCandidates(t, r, mrCandidate("t", 1))
	if !strings.Contains(r.stderr, "補助の要求が落ちた") {
		t.Fatalf("stderr に計算し直しの求めの失敗が無い:\n%s", r.stderr)
	}
	if n := s.countCalls("glab", func(arg string) bool { return strings.Contains(arg, gitlabOpenMRsQuery) }); n != 1 {
		t.Fatalf("CL 置き場の open な一覧を %d 回読んだ (1 回のはず)", n)
	}
}

// countCalls は name の CLI の呼び出しのうち、match に当たる argument を持つものの数。
func (s *sandbox) countCalls(name string, match func(arg string) bool) int {
	s.t.Helper()
	n := 0
	for _, c := range s.calls(name) {
		if slices.ContainsFunc(c.Argv, match) {
			n++
		}
	}
	return n
}

func TestJiraConfirmsTheCLHostEvenWithoutCLTriggers(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		exit   int
	}{
		{"認証が通らない", "HTTP 401: Bad credentials", 4},
		{"見えない", "GraphQL: Could not resolve to a Repository with the name 'acme/widgets'. (repository)", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeWorkflowWithCommands(jiraWorkflow)
			s.setJiraPlaces(readyJiraIssue(42))
			s.failGh(c.stderr, 1)

			for _, args := range [][]string{{"loop", "--dry-run"}, {"loop"}} {
				r := s.run(args...)

				assertExit(t, r, c.exit)
				if r.stdout != "" {
					t.Fatalf("%v: CL 置き場を確かめられないのに stdout に出た: %q", args, r.stdout)
				}
			}
			if _, err := os.Stat(s.jiraStateDir()); !os.IsNotExist(err) {
				t.Fatalf("起動時に落ちたのに state dir を作った: %v", err)
			}
		})
	}
}

func TestJiraDoctorNamesTheCLHostItCannotRead(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(jiraFixCI)
	s.setJiraPlaces()
	s.failGh("HTTP 401: Bad credentials", 1)

	r := s.run("doctor")

	assertExit(t, r, 1)
	if !strings.Contains(r.stdout, "NG   CL 置き場 acme/widgets: ") {
		t.Fatalf("stdout:\n%s", r.stdout)
	}
}

func TestJiraRejectsAWorkflowWithoutTheCLDeclaration(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflowWithCommands(strings.Replace(jiraWorkflow, githubCL, "", 1))

	r := s.dryRun()

	s.assertRejected(r, "cl: 必須の項目が無い")
	s.assertNoAcli("workflow 定義が誤っているのに")
}

// jiraSetupInGitHubClone は GitHub の origin を持つ clone で `setup --kind jira` を撃ち、置いた雛形を返す。
func (s *sandbox) jiraSetupInGitHubClone() string {
	s.t.Helper()
	if err := os.Remove(s.workflowFile()); err != nil {
		s.t.Fatal(err)
	}
	s.respond("git", githubOrigin)
	s.respond("gh", repoView)
	assertExit(s.t, s.run("setup", "--kind", "jira", "--host="+jiraSite, "--repo", "widgets"), 0)
	written, err := os.ReadFile(s.workflowFile())
	if err != nil {
		s.t.Fatal(err)
	}
	return string(written)
}

func TestJiraSetupFillsTheCLHostFromTheOriginOfTheClone(t *testing.T) {
	s := newSandbox(t)

	written := s.jiraSetupInGitHubClone()

	for _, want := range []string{
		"tracker:\n  kind: jira\n  host: " + jiraSite + "\n  repo: WIDGETS\n",
		"cl:\n  kind: github\n  repo: acme/widgets\n",
		"origin",
		"claude-dispatcher/{{ .issue.key }}",
		"gh pr create",
		"\n  - name: resolve-conflict\n", "\n  - name: address-review\n", "\n  - name: fix-ci\n",
	} {
		if !strings.Contains(written, want) {
			t.Errorf("雛形に %q が無い:\n%s", want, written)
		}
	}
	for _, unwanted := range []string{"glab", "題名に {{ .issue.key }}", "\n      approved: true"} {
		if strings.Contains(written, unwanted) {
			t.Errorf("雛形に %q がある:\n%s", unwanted, written)
		}
	}
	s.assertNoAcli("setup なのに")
}

func TestJiraSetupWritesTheSameCLTriggersAsTheTemplateOfTheCLHost(t *testing.T) {
	s := newSandbox(t)
	native := s.setupTemplate()

	written := s.jiraSetupInGitHubClone()

	start := strings.Index(native, "\n  - name: resolve-conflict\n")
	end := strings.LastIndex(native, "\n---\n")
	if start < 0 || end < start {
		t.Fatalf("github の雛形に CL 側の trigger が無い:\n%s", native)
	}
	if section := native[start:end]; !strings.Contains(written, section) {
		t.Fatalf("jira の雛形の CL 側の trigger が github の雛形と違う:\n%s\nwant を含む:\n%s", written, section)
	}
}

func TestJiraSetupFailsWhenTheCLHostCannotBeDetermined(t *testing.T) {
	s := newSandbox(t)
	if err := os.Remove(s.workflowFile()); err != nil {
		t.Fatal(err)
	}
	s.respond("git", stubwire.Rule{ArgsPrefix: []string{"remote", "get-url", "origin"}, Stderr: "error: No such remote 'origin'\n", Exit: 2})

	r := s.run("setup", "--kind", "jira", "--host", jiraSite, "--repo", jiraKey)

	assertExit(t, r, 1)
	if _, err := os.Stat(s.workflowFile()); !os.IsNotExist(err) {
		t.Fatalf("CL 置き場を決められないのに書いた: %v", err)
	}
}
