package gitlab_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/gitlab"
	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

// glab は endpoint (argv の最後) ごとに応答を返す Runner (glab の代役)。key が `?` で終われば query string を問わず当てる。
// どれにも当たらなければ、読んだものが無い 404。
type glab map[string]response

type response struct {
	stdout string
	stderr string
}

func (g glab) Run(args ...string) ([]byte, error) {
	endpoint := args[len(args)-1]
	for key, r := range g {
		if endpoint == key || strings.HasSuffix(key, "?") && strings.HasPrefix(endpoint, key) {
			if r.stderr != "" {
				return nil, &proc.Error{Name: "glab", Args: args, Exit: 1, Stderr: r.stderr}
			}
			return []byte(r.stdout), nil
		}
	}
	// 登録の無い endpoint は、どの分類にも当たらない失敗にする (綴りを誤った endpoint を 404 の消えた扱いで通さない)
	return nil, &proc.Error{Name: "glab", Args: args, Exit: 1, Stderr: "fake glab: no response for " + endpoint}
}

var project = gitlab.Project{Host: "gitlab.example.com", Path: "acme/sub/widgets"}

const issuesEndpoint = "projects/acme%2Fsub%2Fwidgets/issues"

// nonMember は作者 (user 1) が project の member でないことを返す応答
const nonMember = "projects/acme%2Fsub%2Fwidgets/members/all/1"

var notFound = response{stderr: "glab: 404 Not found (HTTP 404)"}

func issueJSON(iid int) string {
	return `{"iid":` + strconv.Itoa(iid) + `,"title":"t","state":"opened","labels":["a"],"author":{"id":1}}`
}

func TestOpenIssuesReadsBothSpellingsOfThePaginatedOutput(t *testing.T) {
	for name, stdout := range map[string]string{
		"page ごとの配列を連ねる": "[" + issueJSON(1) + "," + issueJSON(2) + "]\n[" + issueJSON(3) + "]",
		"1 つの配列に結合する":    "[" + issueJSON(1) + "," + issueJSON(2) + "," + issueJSON(3) + "]",
	} {
		t.Run(name, func(t *testing.T) {
			store := gitlab.NewStore(glab{issuesEndpoint + "?": {stdout: stdout}, nonMember: notFound}, project)

			items, err := store.Open(target.KindIssue)

			if err != nil || len(items) != 3 {
				t.Fatalf("items = %v, err = %v", items, err)
			}
		})
	}
}

func TestIssueThatIsGoneIsReadAsClosed(t *testing.T) {
	store := gitlab.NewStore(glab{issuesEndpoint + "/7": notFound}, project)

	item, err := store.Read(target.Ref{Kind: target.KindIssue, Number: 7})

	if err != nil || !item.Terminal() {
		t.Fatalf("item = %v, err = %v", item, err)
	}
}

func TestIssueOfAProjectThatIsNotVisibleFailsAsNotVisible(t *testing.T) {
	store := gitlab.NewStore(glab{issuesEndpoint + "/7": {stderr: "glab: 404 Project Not Found (HTTP 404)"}}, project)

	_, err := store.Read(target.Ref{Kind: target.KindIssue, Number: 7})

	var failure *target.Failure
	if !errors.As(err, &failure) || failure.Kind != target.NotVisible {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthorWhoIsNotAMemberIsNotACollaborator(t *testing.T) {
	store := gitlab.NewStore(glab{issuesEndpoint + "/7": {stdout: issueJSON(7)}, nonMember: notFound}, project)

	item, err := store.Read(target.Ref{Kind: target.KindIssue, Number: 7})

	if err != nil || item.(target.Issue).AuthorIsCollaborator {
		t.Fatalf("item = %v, err = %v", item, err)
	}
}

const mergeRequestsEndpoint = "projects/acme%2Fsub%2Fwidgets/merge_requests"

// mergeRequest は merge request 7 の本体を fields で返し、承認と discussion を空で返す glab。
func mergeRequest(fields string) glab {
	base := mergeRequestsEndpoint + "/7"
	return glab{
		base:                               {stdout: `{"iid":7,"author":{"id":1},` + fields + `}`},
		base + "/approvals":                {stdout: `{"approved":false}`},
		base + "/discussions?per_page=100": {stdout: `[]`},
		nonMember:                          notFound,
	}
}

func readMergeRequest(t *testing.T, g glab) target.CL {
	t.Helper()
	item, err := gitlab.NewStore(g, project).Read(target.Ref{Kind: target.KindCL, Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	return item.(target.CL)
}

func TestMergeRequestIsTerminalOnlyWhenMergedOrClosed(t *testing.T) {
	for state, terminal := range map[string]bool{"merged": true, "closed": true, "opened": false, "locked": false} {
		t.Run(state, func(t *testing.T) {
			cl := readMergeRequest(t, mergeRequest(`"state":"`+state+`","source_project_id":7,"target_project_id":7,"detailed_merge_status":"mergeable"`))

			if cl.Terminal() != terminal {
				t.Fatalf("Terminal() = %v, want %v", cl.Terminal(), terminal)
			}
		})
	}
}

func TestMergeRequestThatLeftOpenedAfterTheListIsLeftOutOfTheOpenOnes(t *testing.T) {
	// locked (merge の処理中) は Read では終端にしないが、merge 中の branch へ手直しの worker を送らないよう一覧からは外す
	for _, state := range []string{"merged", "closed", "locked"} {
		t.Run(state, func(t *testing.T) {
			g := mergeRequest(`"state":"` + state + `","source_project_id":7,"target_project_id":7,"detailed_merge_status":"mergeable"`)
			g[mergeRequestsEndpoint+"?"] = response{stdout: `[{"iid":7}]`}

			items, err := gitlab.NewStore(g, project).Open(target.KindCL)

			if err != nil || len(items) != 0 {
				t.Fatalf("items = %v, err = %v", items, err)
			}
		})
	}
}

func TestMergeRequestFromADeletedForkHasNoHeadRepo(t *testing.T) {
	cl := readMergeRequest(t, mergeRequest(`"state":"opened","source_project_id":null,"target_project_id":7,"detailed_merge_status":"mergeable"`))

	if cl.HeadRepo != "" || cl.SameRepo {
		t.Fatalf("HeadRepo = %q, SameRepo = %v", cl.HeadRepo, cl.SameRepo)
	}
}

func TestDetailedMergeStatusDecidesWhetherTheConflictIsStillBeingChecked(t *testing.T) {
	for status, want := range map[string]target.Mergeability{
		"checking": target.MergeUnknown, "unchecked": target.MergeUnknown, "preparing": target.MergeUnknown,
		"mergeable": target.MergeClean, "ci_must_pass": target.MergeClean,
	} {
		t.Run(status, func(t *testing.T) {
			cl := readMergeRequest(t, mergeRequest(`"state":"opened","source_project_id":7,"target_project_id":7,"detailed_merge_status":"`+status+`"`))

			if cl.Mergeable != want {
				t.Fatalf("Mergeable = %v, want %v", cl.Mergeable, want)
			}
		})
	}
}

func TestMergeRequestWithoutDetailedMergeStatusFailsAsAGitLabOlderThanSupported(t *testing.T) {
	// detailed_merge_status の無い GitLab は対象外。conflict を計算中の merge request を conflict なしと読まない
	g := mergeRequest(`"state":"opened","source_project_id":7,"target_project_id":7,"merge_status":"unchecked"`)

	_, err := gitlab.NewStore(g, project).Read(target.Ref{Kind: target.KindCL, Number: 7})

	var failure *target.Failure
	if !errors.As(err, &failure) || failure.Kind != target.Unavailable || !strings.Contains(err.Error(), "detailed_merge_status") {
		t.Fatalf("err = %v", err)
	}
}

func TestMergeRequestThatIsGoneIsReadAsClosed(t *testing.T) {
	store := gitlab.NewStore(glab{mergeRequestsEndpoint + "/7": notFound}, project)

	item, err := store.Read(target.Ref{Kind: target.KindCL, Number: 7})

	if err != nil || !item.Terminal() {
		t.Fatalf("item = %v, err = %v", item, err)
	}
}

func TestPathNeedsAGroupAndAName(t *testing.T) {
	for _, path := range []string{"widgets", "acme/", "acme//widgets", "acme/wid gets"} {
		if _, err := gitlab.ParsePath(path); err == nil {
			t.Errorf("ParsePath(%q) を通した", path)
		}
	}
	for _, path := range []string{"acme/widgets", "acme/sub/deeper/widgets"} {
		if _, err := gitlab.ParsePath(path); err != nil {
			t.Errorf("ParsePath(%q) = %v", path, err)
		}
	}
}
