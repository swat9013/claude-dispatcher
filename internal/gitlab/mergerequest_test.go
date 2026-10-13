package gitlab_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/gitlab"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

const (
	// openMergeRequests は open な merge request の一覧の GraphQL (page を問わない)
	openMergeRequests = "graphql:OpenMergeRequests?"
	// fullPath は GraphQL の project の変数
	fullPath = "fullPath=acme/sub/widgets"
	// projectEndpoint は project を REST で読む endpoint
	projectEndpoint = "projects/acme%2Fsub%2Fwidgets"
	// recheckEndpoint は merge request に conflict の計算し直しを求める REST の一覧の endpoint (iid を問わない)
	recheckEndpoint = projectEndpoint + "/merge_requests?"
	// membersEndpoint は project の member を user の id でまとめて読む endpoint (id を問わない)
	membersEndpoint = projectEndpoint + "/members/all?"
)

// mr は GraphQL が返す merge request 1 本の fixture。既定は、同じ project の branch から開いた、draft でない、conflict も
// discussion も pipeline も承認も無い、作者が user 1 の merge request。
type mr struct {
	iid         int
	state       string
	draft       bool
	mergeStatus string
	sourceID    *int
	labels      int
	authorID    int
	discussions []discussion
	// moreDiscussions は discussion の続きの page があるか (endCursor は "d<iid>")
	moreDiscussions bool
}

// discussion は解決できる discussion 1 本。author は最初の note の作者、access はその access level (null なら "")。
type discussion struct {
	authorID int
	access   string
	resolved bool
	system   bool
}

func openMR(iid int) mr {
	same := 7
	return mr{iid: iid, state: "opened", mergeStatus: "CAN_BE_MERGED", sourceID: &same, authorID: 1}
}

func (d discussion) node() map[string]any {
	var access any
	if d.access != "" {
		access = d.access
	}
	note := map[string]any{"system": d.system, "author": map[string]any{"id": "gid://gitlab/User/" + strconv.Itoa(d.authorID)}, "maxAccessLevelOfAuthor": access}
	return map[string]any{"resolvable": true, "resolved": d.resolved, "notes": map[string]any{"nodes": []any{note}}}
}

func discussionNodes(ds []discussion) []any {
	nodes := []any{}
	for _, d := range ds {
		nodes = append(nodes, d.node())
	}
	return nodes
}

func (m mr) node() map[string]any {
	labels := []any{}
	for i := range min(m.labels, 100) {
		labels = append(labels, map[string]any{"title": "l" + strconv.Itoa(i)})
	}
	var source any
	if m.sourceID != nil {
		source = *m.sourceID
	}
	return map[string]any{
		"iid": strconv.Itoa(m.iid), "title": "t", "webUrl": "https://gitlab.example.com/acme/sub/widgets/-/merge_requests/" + strconv.Itoa(m.iid),
		"state": m.state, "createdAt": "2026-01-01T00:00:00Z", "draft": m.draft, "sourceBranch": "b" + strconv.Itoa(m.iid),
		"sourceProjectId": source, "targetProjectId": 7, "mergeStatusEnum": m.mergeStatus, "approved": false,
		"labels":       map[string]any{"pageInfo": map[string]any{"hasNextPage": m.labels > 100}, "nodes": labels},
		"headPipeline": nil,
		"author":       map[string]any{"id": "gid://gitlab/User/" + strconv.Itoa(m.authorID)},
		"discussions": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": m.moreDiscussions, "endCursor": "d" + strconv.Itoa(m.iid)},
			"nodes":    discussionNodes(m.discussions),
		},
	}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// listPage は一覧の 1 page の応答。next が空でなければ、続きの page の endCursor。
func listPage(t *testing.T, next string, mrs ...mr) response {
	nodes := []any{}
	for _, m := range mrs {
		nodes = append(nodes, m.node())
	}
	page := map[string]any{"pageInfo": map[string]any{"hasNextPage": next != "", "endCursor": next}, "nodes": nodes}
	return response{stdout: marshal(t, map[string]any{"data": map[string]any{"project": map[string]any{"mergeRequests": page}}})}
}

// single は merge request 1 本を読み直した応答。m が nil なら merge request が無い。
func single(t *testing.T, m *mr) response {
	var node any
	if m != nil {
		node = m.node()
	}
	return response{stdout: marshal(t, map[string]any{"data": map[string]any{"project": map[string]any{"mergeRequest": node}}})}
}

// members は project の member の応答 (user の id → access level)。
func members(t *testing.T, levels map[int]int) response {
	list := []any{}
	for id, level := range levels {
		list = append(list, map[string]any{"id": id, "access_level": level})
	}
	return response{stdout: marshal(t, list)}
}

// readMR は glab が m を返すときに merge request 7 を読み直す。作者は Developer。
func readMR(t *testing.T, m mr) (target.Item, error) {
	t.Helper()
	g := glab{
		"graphql:MergeRequest?" + fullPath + "&iid=" + strconv.Itoa(m.iid): single(t, &m),
		membersEndpoint: members(t, map[int]int{1: 30}),
		recheckEndpoint: {stdout: "[]"},
	}
	return gitlab.NewStore(g, project).ReadCL(m.iid)
}

func readCL(t *testing.T, m mr) target.CL {
	t.Helper()
	item, err := readMR(t, m)
	if err != nil {
		t.Fatal(err)
	}
	return item.(target.CL)
}

func TestConflictIsDecidedByTheMergeStatus(t *testing.T) {
	for status, want := range map[string]target.Mergeability{
		"CAN_BE_MERGED": target.MergeClean, "CANNOT_BE_MERGED": target.MergeConflict,
		"UNCHECKED": target.MergeUnknown, "CHECKING": target.MergeUnknown, "CANNOT_BE_MERGED_RECHECK": target.MergeUnknown,
	} {
		t.Run(status, func(t *testing.T) {
			m := openMR(7)
			m.mergeStatus = status

			cl := readCL(t, m)

			if cl.Mergeable != want {
				t.Fatalf("Mergeable = %v, want %v", cl.Mergeable, want)
			}
		})
	}
}

func TestDraftMergeRequestWaitingForARecheckIsNotReadAsConflictFree(t *testing.T) {
	// draft では detailedMergeStatus が DRAFT_STATUS になって計算中かが隠れ、conflicts は再計算待ちで false を返す
	m := openMR(7)
	m.draft, m.mergeStatus = true, "CANNOT_BE_MERGED_RECHECK"

	cl := readCL(t, m)

	if cl.Mergeable != target.MergeUnknown {
		t.Fatalf("Mergeable = %v, want 決められない", cl.Mergeable)
	}
}

func TestUnknownMergeStatusFailsAsUnavailable(t *testing.T) {
	m := openMR(7)
	m.mergeStatus = "SOMETHING_NEW"

	_, err := readMR(t, m)

	var failure *target.Failure
	if !errors.As(err, &failure) || failure.Kind != target.Unavailable || !strings.Contains(err.Error(), "SOMETHING_NEW") {
		t.Fatalf("err = %v", err)
	}
}

func TestMergeRequestsWhoseConflictIsNotYetDecidedAreRecheckedInOneCall(t *testing.T) {
	unchecked, clean, recheck := openMR(1), openMR(2), openMR(3)
	unchecked.mergeStatus, recheck.mergeStatus = "UNCHECKED", "CANNOT_BE_MERGED_RECHECK"
	var endpoints []string
	g := recording{glab{
		openMergeRequests: listPage(t, "", unchecked, clean, recheck),
		membersEndpoint:   members(t, map[int]int{1: 30}),
		recheckEndpoint:   {stdout: "[]"},
	}, &endpoints}

	_, err := gitlab.NewStore(g, project).OpenCLs()

	want := recheckEndpoint[:len(recheckEndpoint)-1] + "?iids[]=1&iids[]=3&with_merge_status_recheck=true&per_page=100"
	if err != nil || !slices.Contains(endpoints, want) {
		t.Fatalf("endpoints = %q, err = %v, want %q を撃つ", endpoints, err, want)
	}
}

func TestNoRecheckIsAskedWhenEveryConflictIsDecided(t *testing.T) {
	var endpoints []string
	g := recording{glab{
		openMergeRequests: listPage(t, "", openMR(1)),
		membersEndpoint:   members(t, map[int]int{1: 30}),
	}, &endpoints}

	_, err := gitlab.NewStore(g, project).OpenCLs()

	if err != nil || slices.ContainsFunc(endpoints, func(e string) bool { return strings.Contains(e, "with_merge_status_recheck") }) {
		t.Fatalf("endpoints = %q, err = %v", endpoints, err)
	}
}

func TestOpenMergeRequestsCostOneCallPerPagePlusOneMemberReadAndOneRecheck(t *testing.T) {
	first, second, third := openMR(1), openMR(2), openMR(3)
	second.authorID, third.authorID = 2, 3
	first.mergeStatus, third.mergeStatus = "UNCHECKED", "CHECKING"
	var endpoints []string
	g := recording{glab{
		openMergeRequests + fullPath:               listPage(t, "c1", first, second),
		openMergeRequests + fullPath + "&after=c1": listPage(t, "", third),
		membersEndpoint:                            members(t, map[int]int{1: 30, 2: 20}),
		recheckEndpoint:                            {stdout: "[]"},
	}, &endpoints}

	items, err := gitlab.NewStore(g, project).OpenCLs()

	if err != nil || len(items) != 3 || len(endpoints) != 4 {
		t.Fatalf("items = %d, endpoints = %q, err = %v (want GraphQL 2・member 1・計算し直し 1)", len(items), endpoints, err)
	}
}

func TestAuthorsAreCollaboratorsFromDeveloperUpAndAreReadInOneCall(t *testing.T) {
	developer, reporter, outsider := openMR(1), openMR(2), openMR(3)
	reporter.authorID, outsider.authorID = 2, 3
	var endpoints []string
	g := recording{glab{
		openMergeRequests: listPage(t, "", developer, reporter, outsider),
		membersEndpoint:   members(t, map[int]int{1: 30, 2: 20}),
	}, &endpoints}

	items, err := gitlab.NewStore(g, project).OpenCLs()

	if err != nil {
		t.Fatal(err)
	}
	var collaborators []bool
	for _, item := range items {
		collaborators = append(collaborators, item.(target.CL).AuthorIsCollaborator)
	}
	want := membersEndpoint[:len(membersEndpoint)-1] + "?user_ids[]=1&user_ids[]=2&user_ids[]=3&per_page=100"
	if !slices.Equal(collaborators, []bool{true, false, false}) || !slices.Contains(endpoints, want) {
		t.Fatalf("collaborators = %v, endpoints = %q", collaborators, endpoints)
	}
}

func TestReviewUnresolvedCountsOnlyDiscussionsStartedByDeveloperOrAbove(t *testing.T) {
	for access, want := range map[string]bool{"Developer": true, "Maintainer": true, "Owner": true, "Reporter": false, "Guest": false, "": false} {
		t.Run(access, func(t *testing.T) {
			m := openMR(7)
			m.discussions = []discussion{{authorID: 5, access: access}}

			cl := readCL(t, m)

			if cl.ReviewUnresolved != want {
				t.Fatalf("ReviewUnresolved = %v, want %v", cl.ReviewUnresolved, want)
			}
		})
	}
}

func TestResolvedAndSystemDiscussionsAreNotUnresolvedReviews(t *testing.T) {
	m := openMR(7)
	m.discussions = []discussion{{authorID: 5, access: "Developer", resolved: true}, {authorID: 5, access: "Developer", system: true}}

	cl := readCL(t, m)

	if cl.ReviewUnresolved {
		t.Fatal("ReviewUnresolved = true")
	}
}

func TestDiscussionsBeyondTheFirstPageAreReadToo(t *testing.T) {
	m := openMR(7)
	m.moreDiscussions = true
	rest := map[string]any{"pageInfo": map[string]any{"hasNextPage": false, "endCursor": "e"}, "nodes": []any{discussion{authorID: 5, access: "Developer"}.node()}}
	g := glab{
		"graphql:MergeRequest?" + fullPath + "&iid=7": single(t, &m),
		"graphql:MergeRequestDiscussions?" + fullPath + "&iid=7&after=d7": {stdout: marshal(t, map[string]any{"data": map[string]any{
			"project": map[string]any{"mergeRequest": map[string]any{"discussions": rest}}}})},
		membersEndpoint: members(t, map[int]int{1: 30}),
	}

	item, err := gitlab.NewStore(g, project).ReadCL(7)

	if err != nil || !item.(target.CL).ReviewUnresolved {
		t.Fatalf("item = %v, err = %v", item, err)
	}
}

func TestMergeRequestWithMoreThan100LabelsFailsAsTruncated(t *testing.T) {
	m := openMR(7)
	m.labels = 101

	_, err := readMR(t, m)

	var failure *target.Failure
	if !errors.As(err, &failure) || failure.Kind != target.Truncated {
		t.Fatalf("err = %v", err)
	}
}

func TestMergeRequestIsTerminalOnlyWhenMergedOrClosed(t *testing.T) {
	for state, terminal := range map[string]bool{"merged": true, "closed": true, "opened": false, "locked": false} {
		t.Run(state, func(t *testing.T) {
			m := openMR(7)
			m.state = state

			item, err := readMR(t, m)

			if err != nil || item.Terminal() != terminal {
				t.Fatalf("item = %v, err = %v, want Terminal() = %v", item, err, terminal)
			}
		})
	}
}

func TestMergeRequestThatIsNotOpenedIsLeftOutOfTheOpenOnes(t *testing.T) {
	// locked (merge の処理中) は読み直しでは終端にしないが、merge 中の branch へ手直しの worker を送らないよう一覧からは外す
	locked := openMR(1)
	locked.state = "locked"
	g := glab{openMergeRequests: listPage(t, "", locked, openMR(2)), membersEndpoint: members(t, map[int]int{1: 30})}

	items, err := gitlab.NewStore(g, project).OpenCLs()

	if err != nil || len(items) != 1 || items[0].(target.CL).Number != 2 {
		t.Fatalf("items = %v, err = %v", items, err)
	}
}

func TestMergeRequestThatIsGoneIsReadAsClosed(t *testing.T) {
	g := glab{"graphql:MergeRequest?" + fullPath + "&iid=7": single(t, nil)}

	item, err := gitlab.NewStore(g, project).ReadCL(7)

	if err != nil || !item.Terminal() {
		t.Fatalf("item = %v, err = %v", item, err)
	}
}

func TestMergeRequestFromADeletedForkHasNoHeadRepo(t *testing.T) {
	m := openMR(7)
	m.sourceID = nil

	cl := readCL(t, m)

	if cl.HeadRepo != "" || cl.SameRepo {
		t.Fatalf("HeadRepo = %q, SameRepo = %v", cl.HeadRepo, cl.SameRepo)
	}
}

func TestMergeRequestsOfAProjectThatIsNotVisibleFailAsNotVisible(t *testing.T) {
	g := glab{openMergeRequests: {stdout: `{"data":{"project":null}}`}}

	_, err := gitlab.NewStore(g, project).OpenCLs()

	var failure *target.Failure
	if !errors.As(err, &failure) || failure.Kind != target.NotVisible {
		t.Fatalf("err = %v", err)
	}
}

func TestGraphQLFailureWithoutAStatusIsClassifiedByReadingTheProjectOverREST(t *testing.T) {
	// GitLab が GraphQL の errors で答えると、glab は認証の失敗にも status を出さない
	for name, tc := range map[string]struct {
		project response
		want    target.FailureKind
	}{
		"REST も認証で落ちる":         {response{stderr: "glab: 401 Unauthorized (HTTP 401)"}, target.Auth},
		"REST で project が見えない": {response{stderr: "glab: 404 Project Not Found (HTTP 404)"}, target.NotVisible},
		"REST は通る":             {response{stdout: `{"id":7}`}, target.Unavailable},
	} {
		t.Run(name, func(t *testing.T) {
			g := glab{openMergeRequests: {stderr: "glab: Invalid token\n"}, projectEndpoint: tc.project}

			_, err := gitlab.NewStore(g, project).OpenCLs()

			var failure *target.Failure
			if !errors.As(err, &failure) || failure.Kind != tc.want {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestReasonOfAGraphQLFailureKeepsTheGraphQLMessageWhenTheRESTProbeAlsoFails(t *testing.T) {
	g := glab{openMergeRequests: {stderr: "glab: Invalid token\n"}, projectEndpoint: {stderr: "glab: 401 Unauthorized (HTTP 401)"}}

	_, err := gitlab.NewStore(g, project).OpenCLs()

	if err == nil || !strings.Contains(err.Error(), "Invalid token") || !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("err = %v, want GraphQL と REST の両方の失敗を載せる", err)
	}
}

func TestGraphQLFailureWithAStatusIsClassifiedWithoutTheRESTProbe(t *testing.T) {
	// 登録の無い REST の project を撃てば読めないの失敗になるので、認証と分類されたなら probe を撃っていない
	g := glab{openMergeRequests: {stderr: "glab: 401 Unauthorized (HTTP 401)"}}

	_, err := gitlab.NewStore(g, project).OpenCLs()

	var failure *target.Failure
	if !errors.As(err, &failure) || failure.Kind != target.Auth {
		t.Fatalf("err = %v", err)
	}
}

func TestFailedRecheckRequestLeavesTheConflictUndecidedWithoutFailingTheObservation(t *testing.T) {
	unchecked := openMR(1)
	unchecked.mergeStatus = "UNCHECKED"
	g := glab{
		openMergeRequests: listPage(t, "", unchecked, openMR(2)),
		membersEndpoint:   members(t, map[int]int{1: 30}),
		recheckEndpoint:   {stderr: "glab: HTTP 502\n"},
	}

	items, err := gitlab.NewStore(g, project).OpenCLs()

	if err != nil || len(items) != 2 || items[0].(target.CL).Mergeable != target.MergeUnknown {
		t.Fatalf("items = %v, err = %v", items, err)
	}
}

func TestRecheckIsAskedOncePerHundredUndecidedMergeRequests(t *testing.T) {
	var undecided []mr
	for iid := 1; iid <= 101; iid++ {
		m := openMR(iid)
		m.mergeStatus = "UNCHECKED"
		undecided = append(undecided, m)
	}
	var endpoints []string
	g := recording{glab{
		openMergeRequests: listPage(t, "", undecided...),
		membersEndpoint:   members(t, map[int]int{1: 30}),
		recheckEndpoint:   {stdout: "[]"},
	}, &endpoints}

	_, err := gitlab.NewStore(g, project).OpenCLs()

	rechecks := slices.DeleteFunc(endpoints, func(e string) bool { return !strings.Contains(e, "with_merge_status_recheck") })
	if err != nil || len(rechecks) != 2 {
		t.Fatalf("計算し直しの求め = %d 回, err = %v, want 2 回 (100 本と 1 本)", len(rechecks), err)
	}
}
