package blackbox_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// GitLab の merge request を CL 側の trigger の作業対象にする (formats.md §2.3 の gitlab の写像)。glab は stub で、REST の
// 応答の形を返す。語彙と絞り込みの 1 つずつの意味は internal/trigger の単体テストが持ち、ここでは merge request の field から
// 候補までの写像を見る。

const (
	// gitlabProjectID は issue 置き場 (= merge request の target) の project の id。gitlabForkID は fork の project の id
	gitlabProjectID = 7
	gitlabForkID    = 99
	// gitlabOpenMRs は open な merge request の一覧の endpoint
	gitlabOpenMRs = gitlabProject + "/merge_requests?state=opened&order_by=created_at&sort=asc&per_page=100"
	// developerID・reporterID は discussion の書き手の user id (access level は Developer・Reporter)
	developerID = 501
	reporterID  = 502
)

// glMR は glab が返す merge request 1 件の fixture。既定は、同じ project の branch から開いた、draft でない、conflict も
// 未解決の discussion も pipeline も承認も無い merge request。
type glMR struct {
	iid    int
	branch string
	fork   bool
	draft  bool
	labels []string
	// hasConflicts は has_conflicts。mergeStatus は detailed_merge_status (空なら mergeable)
	hasConflicts bool
	mergeStatus  string
	// pipeline は head_pipeline の status。空なら head_pipeline が無い
	pipeline    string
	approved    bool
	discussions []glDiscussion
	// state が空なら opened。merged / closed なら終端
	state string
}

// glDiscussion は discussion 1 本。最初の note の書き手と、解決済みか・system note か。
type glDiscussion struct {
	authorID int
	resolved bool
	system   bool
}

// readyMR は source branch が worktree-issue-<番号> の merge request。
func readyMR(iid int) glMR { return glMR{iid: iid, branch: "worktree-issue-" + strconv.Itoa(iid)} }

func (m glMR) json() map[string]any {
	source := gitlabProjectID
	if m.fork {
		source = gitlabForkID
	}
	status, state := m.mergeStatus, m.state
	if status == "" {
		status = "mergeable"
	}
	if state == "" {
		state = "opened"
	}
	labels := m.labels
	if labels == nil {
		labels = []string{}
	}
	var pipeline any
	if m.pipeline != "" {
		pipeline = map[string]any{"id": 1, "status": m.pipeline}
	}
	return map[string]any{
		"id":                    800000 + m.iid,
		"iid":                   m.iid,
		"title":                 "cl " + strconv.Itoa(m.iid),
		"web_url":               "https://" + gitlabHost + "/" + gitlabRepo + "/-/merge_requests/" + strconv.Itoa(m.iid),
		"state":                 state,
		"created_at":            createdAt(m.iid),
		"labels":                labels,
		"draft":                 m.draft,
		"source_branch":         m.branch,
		"source_project_id":     source,
		"target_project_id":     gitlabProjectID,
		"has_conflicts":         m.hasConflicts,
		"detailed_merge_status": status,
		"head_pipeline":         pipeline,
		"author":                map[string]any{"id": developerID, "username": "dev"},
	}
}

func (m glMR) discussionsJSON() []map[string]any {
	out := []map[string]any{}
	for i, d := range m.discussions {
		out = append(out, map[string]any{
			"id":              "d" + strconv.Itoa(i),
			"individual_note": false,
			"notes": []map[string]any{{
				"id": i + 1, "system": d.system, "resolvable": !d.system, "resolved": d.resolved,
				"author": map[string]any{"id": d.authorID, "username": "user" + strconv.Itoa(d.authorID)},
			}},
		})
	}
	return out
}

// glabStoreRules は glab が issues と mrs を返す応答 rule の列。merge request の作者と discussion の書き手の access level は、
// developerID が Developer、reporterID が Reporter。
func glabStoreRules(issues []glIssue, mrs []glMR) []stubwire.Rule {
	marshal := func(v any) string {
		raw, _ := json.Marshal(v)
		return string(raw)
	}
	rules := []stubwire.Rule{
		{ArgsPrefix: glabAPI(gitlabProject + "/members/all/" + strconv.Itoa(developerID)), Stdout: `{"access_level":30}`},
		{ArgsPrefix: glabAPI(gitlabProject + "/members/all/" + strconv.Itoa(reporterID)), Stdout: `{"access_level":20}`},
	}
	open := []map[string]any{}
	for _, m := range mrs {
		base := gitlabProject + "/merge_requests/" + strconv.Itoa(m.iid)
		rules = append(rules,
			stubwire.Rule{ArgsPrefix: glabAPI(base), Stdout: marshal(m.json())},
			stubwire.Rule{ArgsPrefix: glabAPI(base + "/approvals"), Stdout: marshal(map[string]any{"approved": m.approved})},
			stubwire.Rule{ArgsPrefix: glabAPI("--paginate", base+"/discussions?per_page=100"), Stdout: marshal(m.discussionsJSON())},
		)
		if m.state == "" {
			open = append(open, m.json())
		}
	}
	rules = append(rules, stubwire.Rule{ArgsPrefix: glabAPI("--paginate", gitlabOpenMRs), Stdout: marshal(open)})
	return append(rules, glabRules(issues...)...)
}

// dryRunGitLabCL は triggers の gitlab の workflow 定義で、glab が mrs を返すときの試運転を撃つ。
func (s *sandbox) dryRunGitLabCL(triggers string, mrs ...glMR) runResult {
	s.t.Helper()
	s.writeWorkflowWithCommands(gitlabWorkflowWithTriggers(triggers))
	s.respondAll("glab", glabStoreRules(nil, mrs))
	return s.dryRun()
}

func TestGitLabCLVocabularyIsReadFromTheMergeRequest(t *testing.T) {
	conflicting, clean := readyMR(1), readyMR(2)
	conflicting.hasConflicts = true
	byDeveloper, resolved, byReporter, systemNote := readyMR(3), readyMR(4), readyMR(5), readyMR(6)
	byDeveloper.discussions = []glDiscussion{{authorID: developerID}}
	resolved.discussions = []glDiscussion{{authorID: developerID, resolved: true}}
	byReporter.discussions = []glDiscussion{{authorID: reporterID}}
	systemNote.discussions = []glDiscussion{{authorID: developerID, system: true}}
	failed, passed := readyMR(7), readyMR(8)
	failed.pipeline, passed.pipeline = "failed", "success"
	approved, unapproved := readyMR(9), readyMR(10)
	approved.approved = true
	for _, tc := range []struct {
		vocabulary string
		mrs        []glMR
		want       []int
	}{
		{"conflict", []glMR{conflicting, clean}, []int{1}},
		{"review_unresolved", []glMR{byDeveloper, resolved, byReporter, systemNote}, []int{3}},
		{"ci_failed", []glMR{failed, passed}, []int{7}},
		{"approved", []glMR{approved, unapproved}, []int{9}},
	} {
		t.Run(tc.vocabulary, func(t *testing.T) {
			s := newSandbox(t)

			r := s.dryRunGitLabCL(`
  - {name: t, on: cl, when: {`+tc.vocabulary+`: true}, action: /t}`, tc.mrs...)

			var want []string
			for _, n := range tc.want {
				want = append(want, clCandidate("t", n))
			}
			assertCandidates(t, r, want...)
			s.assertGlabOnlyReads()
		})
	}
}

func TestGitLabConflictThatIsStillBeingCheckedMatchesNeitherWay(t *testing.T) {
	s := newSandbox(t)
	checking := readyMR(1)
	checking.mergeStatus = "checking"

	r := s.dryRunGitLabCL(`
  - {name: conflicted, on: cl, when: {conflict: true}, action: /t}
  - {name: clean, on: cl, when: {conflict: false}, action: /t}`, checking)

	assertCandidates(t, r)
}

func TestGitLabCanceledPipelineIsNotAFailure(t *testing.T) {
	s := newSandbox(t)
	canceled, noPipeline := readyMR(1), readyMR(2)
	canceled.pipeline = "canceled"

	r := s.dryRunGitLabCL(`
  - {name: fix, on: cl, when: {ci_failed: true}, action: /t}
  - {name: green, on: cl, when: {ci_failed: false}, action: /t}`, canceled, noPipeline)

	assertCandidates(t, r, clCandidate("green", 1), clCandidate("green", 2))
}

func TestGitLabCLFiltersReadTheSourceBranchProjectAndDraft(t *testing.T) {
	s := newSandbox(t)
	own := readyMR(1)
	own.branch = "claude-dispatcher/issue-1"
	fromFork := readyMR(2)
	fromFork.branch, fromFork.fork = "claude-dispatcher/issue-2", true
	draft := readyMR(3)
	draft.branch, draft.draft = "claude-dispatcher/issue-3", true
	other := readyMR(4)
	other.branch = "feature/x"

	r := s.dryRunGitLabCL(`
  - {name: t, on: cl, when: {head: "claude-dispatcher/*", draft: false, author: collaborator}, action: /t}`, own, fromFork, draft, other)

	assertCandidates(t, r, clCandidate("t", 1))
}

func TestGitLabMergeRequestsFromTheSameBranchAreAmbiguousButAForkIsAnotherBranch(t *testing.T) {
	s := newSandbox(t)
	first, second, fromFork := readyMR(1), readyMR(2), readyMR(3)
	second.branch = first.branch
	fromFork.branch, fromFork.fork = first.branch, true

	r := s.dryRunGitLabCL(`
  - {name: t, on: cl, when: {same_repo: false}, action: /t}
  - {name: u, on: cl, action: /t}`, first, second, fromFork)

	assertCandidates(t, r, clCandidate("t", 3))
	if strings.Contains(r.stdout, "\t#1\t") || strings.Contains(r.stdout, "\t#2\t") {
		t.Fatalf("曖昧な merge request を候補にした:\n%s", r.stdout)
	}
}
