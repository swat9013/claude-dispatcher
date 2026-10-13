package blackbox_test

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// GitLab の merge request を CL 側の trigger の作業対象にする (formats.md §2.3 の gitlab の写像)。glab は stub で、GraphQL と
// REST の応答の形を返す。語彙と絞り込みの 1 つずつの意味は internal/trigger の単体テストが持ち、ここでは merge request の field から
// 候補までの写像を見る。

const (
	// gitlabProjectID は issue 置き場 (= merge request の target) の project の id。gitlabForkID は fork の project の id
	gitlabProjectID = 7
	gitlabForkID    = 99
	// gitlabOpenMRsQuery は open な merge request の一覧の GraphQL の問い合わせの書き出し
	gitlabOpenMRsQuery = "query OpenMergeRequests("
	// gitlabReadMRQuery は merge request 1 本を読み直す GraphQL の問い合わせの書き出し
	gitlabReadMRQuery = "query MergeRequest("
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
	// authorID は作者の user id。0 なら developerID
	authorID int
	// mergeStatus は mergeStatusEnum (空なら CAN_BE_MERGED)
	mergeStatus string
	// pipeline は headPipeline の status。空なら headPipeline が無い
	pipeline    string
	approved    bool
	discussions []glDiscussion
}

// glDiscussion は discussion 1 本。notes の先頭が最初の note。
type glDiscussion struct {
	notes []glNote
}

// glNote は discussion の note 1 つ。どの note も解決できる (resolvable)。
type glNote struct {
	authorID int
	resolved bool
	system   bool
}

// readyMR は source branch が worktree-issue-<番号> の merge request。
func readyMR(iid int) glMR { return glMR{iid: iid, branch: "worktree-issue-" + strconv.Itoa(iid)} }

func (m glMR) json() map[string]any {
	source, author := gitlabProjectID, m.authorID
	if m.fork {
		source = gitlabForkID
	}
	if author == 0 {
		author = developerID
	}
	status := m.mergeStatus
	if status == "" {
		status = "CAN_BE_MERGED"
	}
	var pipeline any
	if m.pipeline != "" {
		pipeline = map[string]any{"status": m.pipeline}
	}
	return map[string]any{
		"iid":             strconv.Itoa(m.iid),
		"title":           "cl " + strconv.Itoa(m.iid),
		"webUrl":          "https://" + gitlabHost + "/" + gitlabRepo + "/-/merge_requests/" + strconv.Itoa(m.iid),
		"state":           "opened",
		"createdAt":       createdAt(m.iid),
		"labels":          map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": []any{}},
		"draft":           m.draft,
		"sourceBranch":    m.branch,
		"sourceProjectId": source,
		"targetProjectId": gitlabProjectID,
		"mergeStatusEnum": status,
		"approved":        m.approved,
		"headPipeline":    pipeline,
		"author":          map[string]any{"id": userGID(author)},
		"discussions":     map[string]any{"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}, "nodes": m.discussionsJSON()},
	}
}

// userGID は user の GraphQL の global ID。
func userGID(id int) string { return "gid://gitlab/User/" + strconv.Itoa(id) }

// accessLevels は note の作者の project での access level (GraphQL の maxAccessLevelOfAuthor)。載っていなければ member でない
var accessLevels = map[int]string{developerID: "Developer", reporterID: "Reporter"}

// discussionsJSON は discussion の列。解決済みかは discussion の単位で、どの note も解決済みのときに解決済み。
func (m glMR) discussionsJSON() []map[string]any {
	out := []map[string]any{}
	for _, d := range m.discussions {
		resolved := true
		for _, n := range d.notes {
			resolved = resolved && n.resolved
		}
		first := d.notes[0]
		var access any
		if level, ok := accessLevels[first.authorID]; ok {
			access = level
		}
		note := map[string]any{"system": first.system, "author": map[string]any{"id": userGID(first.authorID)}, "maxAccessLevelOfAuthor": access}
		out = append(out, map[string]any{"resolvable": true, "resolved": resolved, "notes": map[string]any{"nodes": []any{note}}})
	}
	return out
}

// glabStoreRules は glab が issues と mrs を返す応答 rule の列。merge request の作者と discussion の書き手の access level は、
// developerID が Developer、reporterID が Reporter。conflict の計算し直しの求めには空の一覧で答える。
func glabStoreRules(issues []glIssue, mrs []glMR) []stubwire.Rule {
	marshal := func(v any) string {
		raw, _ := json.Marshal(v)
		return string(raw)
	}
	nodes := []map[string]any{}
	for _, m := range mrs {
		nodes = append(nodes, m.json())
	}
	page := map[string]any{"data": map[string]any{"project": map[string]any{"mergeRequests": map[string]any{
		"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}, "nodes": nodes}}}}
	rules := []stubwire.Rule{
		{ArgsPrefix: glabAPI("graphql"), ArgsContain: []string{gitlabOpenMRsQuery}, Stdout: marshal(page)},
	}
	// 起動の直前の読み直し。変数 `iid=1` は `iid=12` の部分文字列なので、iid の大きい順に並べて先に当てる
	byIIDDesc := slices.SortedFunc(slices.Values(mrs), func(a, b glMR) int { return b.iid - a.iid })
	for _, m := range byIIDDesc {
		single := map[string]any{"data": map[string]any{"project": map[string]any{"mergeRequest": m.json()}}}
		rules = append(rules, stubwire.Rule{ArgsPrefix: glabAPI("graphql"), ArgsContain: []string{gitlabReadMRQuery, "iid=" + strconv.Itoa(m.iid)}, Stdout: marshal(single)})
	}
	rules = append(rules, []stubwire.Rule{
		{ArgsPrefix: glabAPI(), ArgsContain: []string{gitlabProject + "/members/all?"},
			Stdout: marshal([]map[string]any{{"id": developerID, "access_level": 30}, {"id": reporterID, "access_level": 20}})},
		{ArgsPrefix: glabAPI(), ArgsContain: []string{"with_merge_status_recheck=true"}, Stdout: "[]"},
	}...)
	return append(rules, glabRules(issues...)...)
}

// mrCandidate は試運転が merge request の候補に出す 1 行。番号は GitLab の merge request の綴り (`!<番号>`) で出る。
func mrCandidate(trigger string, iid int) string {
	return trigger + "\tcl\t!" + strconv.Itoa(iid) + "\tcl " + strconv.Itoa(iid)
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
	conflicting.mergeStatus = "CANNOT_BE_MERGED"
	byDeveloper, resolved, byReporter, systemNote := readyMR(3), readyMR(4), readyMR(5), readyMR(6)
	byDeveloper.discussions = []glDiscussion{{notes: []glNote{{authorID: developerID}}}}
	resolved.discussions = []glDiscussion{{notes: []glNote{{authorID: developerID, resolved: true}}}}
	byReporter.discussions = []glDiscussion{{notes: []glNote{{authorID: reporterID}}}}
	systemNote.discussions = []glDiscussion{{notes: []glNote{{authorID: developerID, system: true}}}}
	// 書き手は最初の note の作者: Reporter が始めて Developer が返信した discussion は数えない
	reporterThenDeveloper := readyMR(11)
	reporterThenDeveloper.discussions = []glDiscussion{{notes: []glNote{{authorID: reporterID}, {authorID: developerID}}}}
	// 後ろの note が未解決なら未解決: 最初の note が解決済みでも、返信が未解決のまま残る discussion は数える
	laterNoteUnresolved := readyMR(12)
	laterNoteUnresolved.discussions = []glDiscussion{{notes: []glNote{{authorID: developerID, resolved: true}, {authorID: reporterID}}}}
	failed, passed := readyMR(7), readyMR(8)
	failed.pipeline, passed.pipeline = "FAILED", "SUCCESS"
	approved, unapproved := readyMR(9), readyMR(10)
	approved.approved = true
	for _, tc := range []struct {
		name, vocabulary string
		mrs              []glMR
		want             []int
	}{
		{"conflict", "conflict", []glMR{conflicting, clean}, []int{1}},
		{"review_unresolved", "review_unresolved", []glMR{byDeveloper, resolved, byReporter, systemNote}, []int{3}},
		{"review_unresolved の書き手は最初の note の作者", "review_unresolved", []glMR{byDeveloper, reporterThenDeveloper}, []int{3}},
		{"review_unresolved は後ろの note の未解決も数える", "review_unresolved", []glMR{resolved, laterNoteUnresolved}, []int{12}},
		{"ci_failed", "ci_failed", []glMR{failed, passed}, []int{7}},
		{"approved", "approved", []glMR{approved, unapproved}, []int{9}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)

			r := s.dryRunGitLabCL(`
  - {name: t, on: cl, when: {`+tc.vocabulary+`: true}, action: /t}`, tc.mrs...)

			var want []string
			for _, n := range tc.want {
				want = append(want, mrCandidate("t", n))
			}
			assertCandidates(t, r, want...)
			s.assertGlabOnlyReads()
		})
	}
}

func TestGitLabConflictThatIsStillBeingCheckedMatchesNeitherWay(t *testing.T) {
	for _, status := range []string{"UNCHECKED", "CHECKING", "CANNOT_BE_MERGED_RECHECK"} {
		t.Run(status, func(t *testing.T) {
			s := newSandbox(t)
			pending := readyMR(1)
			pending.mergeStatus = status

			r := s.dryRunGitLabCL(`
  - {name: conflicted, on: cl, when: {conflict: true}, action: /t}
  - {name: clean, on: cl, when: {conflict: false}, action: /t}`, pending)

			assertCandidates(t, r)
		})
	}
}

func TestGitLabCanceledPipelineIsNotAFailure(t *testing.T) {
	s := newSandbox(t)
	canceled, noPipeline := readyMR(1), readyMR(2)
	canceled.pipeline = "CANCELED"

	r := s.dryRunGitLabCL(`
  - {name: fix, on: cl, when: {ci_failed: true}, action: /t}
  - {name: green, on: cl, when: {ci_failed: false}, action: /t}`, canceled, noPipeline)

	assertCandidates(t, r, mrCandidate("green", 1), mrCandidate("green", 2))
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
	byReporter := readyMR(5)
	byReporter.branch, byReporter.authorID = "claude-dispatcher/issue-5", reporterID

	r := s.dryRunGitLabCL(`
  - {name: t, on: cl, when: {head: "claude-dispatcher/*", draft: false, author: collaborator}, action: /t}`, own, fromFork, draft, other, byReporter)

	assertCandidates(t, r, mrCandidate("t", 1))
}

func TestGitLabMergeRequestsFromTheSameBranchAreAmbiguousButAForkIsAnotherBranch(t *testing.T) {
	s := newSandbox(t)
	first, second, fromFork := readyMR(1), readyMR(2), readyMR(3)
	second.branch = first.branch
	fromFork.branch, fromFork.fork = first.branch, true

	r := s.dryRunGitLabCL(`
  - {name: t, on: cl, when: {same_repo: false}, action: /t}
  - {name: u, on: cl, action: /t}`, first, second, fromFork)

	assertCandidates(t, r, mrCandidate("t", 3))
}

func TestGitLabFailedRecheckRequestStillListsTheCandidatesAndNamesTheFailure(t *testing.T) {
	s := newSandbox(t)
	unchecked, clean := readyMR(1), readyMR(2)
	unchecked.mergeStatus = "UNCHECKED"
	s.writeWorkflowWithCommands(gitlabWorkflowWithTriggers(`
  - {name: t, on: cl, action: /t}`))
	recheckFails := stubwire.Rule{ArgsPrefix: glabAPI(), ArgsContain: []string{"with_merge_status_recheck=true"}, Stderr: "glab: HTTP 502\n", Exit: 1}
	s.respondAll("glab", append([]stubwire.Rule{recheckFails}, glabStoreRules(nil, []glMR{unchecked, clean})...))

	r := s.dryRun()

	assertCandidates(t, r, mrCandidate("t", 1), mrCandidate("t", 2))
	if !strings.Contains(r.stderr, "補助の要求が落ちた") {
		t.Fatalf("stderr に計算し直しの求めの失敗が無い:\n%s", r.stderr)
	}
}

func TestGitLabDoctorKeepsThePlaceOKAndNamesAFailedRecheckRequest(t *testing.T) {
	s := newSandbox(t)
	unchecked := readyMR(1)
	unchecked.mergeStatus = "UNCHECKED"
	s.writeWorkflowWithCommands(gitlabWorkflowWithTriggers(`
  - {name: t, on: cl, when: {head: "worktree-issue-*"}, action: /t}`))
	recheckFails := stubwire.Rule{ArgsPrefix: glabAPI(), ArgsContain: []string{"with_merge_status_recheck=true"}, Stderr: "glab: HTTP 502\n", Exit: 1}
	s.respondAll("glab", append([]stubwire.Rule{recheckFails}, glabStoreRules(nil, []glMR{unchecked})...))

	r := s.run("doctor")

	if !strings.Contains(r.stdout, "ok   issue 置き場 "+gitlabScopeKey+" (補助の要求が落ちた: ") {
		t.Fatalf("issue 置き場の ok の行に計算し直しの求めの失敗が無い:\n%s", r.stdout)
	}
}
