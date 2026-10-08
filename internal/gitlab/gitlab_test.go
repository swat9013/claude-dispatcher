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

// glab の HTTP 403 の stderr。glab 1.120.0 を、403 だけを返すローカルの HTTP server に向けて撃ち、出たものを逐語で写した。
// proxy は HTML の本文で、GitLab は JSON の本文で拒否する
const (
	apiForbiddenByProxy  = "glab: HTTP 403\n"
	apiForbiddenByGitLab = "glab: 403 Forbidden (HTTP 403)\n"
	viewForbiddenByProxy = "          \n   ERROR  \n          \n" +
		"  Get http://127.0.0.1:8403/api/v4/projects/acme%2Fw: 403 failed to parse unknown error format: <html>                \n" +
		"  <head><title>403 Forbidden</title></head>                                                                           \n" +
		"  <body>                                                                                                              \n" +
		"  <center><h1>403 Forbidden</h1></center>                                                                             \n" +
		"  </body>                                                                                                             \n" +
		"  </html>                                                                                                             \n" +
		"  .                                                                                                                   \n\n"
	viewForbiddenByGitLab = "          \n   ERROR  \n          \n" +
		"  Get http://127.0.0.1:8403/api/v4/projects/json%2Fw: 403 {message: 403 Forbidden}.                                   \n\n"
)

const forbiddenReason = "HTTP 403 で拒否された (接続元のネットワークか、token の権限)"

// assertContains は text が wants をどれも含むことを確かめる。
func assertContains(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("%q を含まない: %s", want, text)
		}
	}
}

// assertLacks は text が bodies のどれも含まないことを確かめる。
func assertLacks(t *testing.T, text string, bodies ...string) {
	t.Helper()
	for _, body := range bodies {
		if strings.Contains(text, body) {
			t.Errorf("%q が載った: %s", body, text)
		}
	}
}

func TestForbiddenObservationIsUnavailableWithAShortReasonInsteadOfTheStderr(t *testing.T) {
	for name, stderr := range map[string]string{"proxy の HTML": apiForbiddenByProxy, "GitLab の JSON": apiForbiddenByGitLab} {
		t.Run(name, func(t *testing.T) {
			store := gitlab.NewStore(glab{issuesEndpoint + "/7": {stderr: stderr}}, project)

			_, err := store.Read(target.Ref{Kind: target.KindIssue, Number: 7})

			var failure *target.Failure
			if !errors.As(err, &failure) || failure.Kind != target.Unavailable {
				t.Fatalf("err = %v, want 読めない", err)
			}
			assertContains(t, err.Error(), forbiddenReason, "glab api --hostname", "exit 1")
			assertLacks(t, err.Error(), strings.TrimSpace(stderr))
		})
	}
}

func TestForbiddenRepoViewGivesTheShortReasonWithTheRequestInsteadOfTheBody(t *testing.T) {
	for name, stderr := range map[string]string{
		"proxy の HTML":  viewForbiddenByProxy,
		"GitLab の JSON": viewForbiddenByGitLab,
		// 本文の無い 403 (実物から写したものではない)。403 の直後が改行になる
		"本文なし": "          \n   ERROR  \n          \n  Get http://127.0.0.1:8403/api/v4/projects/acme%2Fw: 403\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := gitlab.CurrentProject(glab{"json": {stderr: stderr}})

			if err == nil {
				t.Fatal("err = nil")
			}
			request := "Get http://127.0.0.1:8403/api/v4/projects/"
			assertContains(t, err.Error(), forbiddenReason, request, "glab repo view", "exit 1")
			assertLacks(t, err.Error(), "<html>", "{message:", "failed to parse", "ERROR")
		})
	}
}

func TestRepoViewFailureWithAnHTMLBodyKeepsTheRequestAndTheStatusButNotTheBody(t *testing.T) {
	stderr := "          \n   ERROR  \n          \n" +
		"  Get https://gitlab.example.com/api/v4/projects/acme%2Fw: 502 failed to parse unknown error format: <html>\n" +
		"  <head><title>502 Bad Gateway</title></head>\n  <body><h1>502 Bad Gateway</h1></body>\n  </html>\n  .\n\n"

	_, err := gitlab.CurrentProject(glab{"json": {stderr: stderr}})

	if err == nil {
		t.Fatal("err = nil")
	}
	assertContains(t, err.Error(), "Get https://gitlab.example.com/api/v4/projects/acme%2Fw: 502", "glab repo view", "exit 1")
	assertLacks(t, err.Error(), "<html>", "<h1>", "Bad Gateway")
}

func TestOtherFailuresKeepTheirKindAndTheStderr(t *testing.T) {
	for stderr, kind := range map[string]target.FailureKind{
		"glab: 401 Unauthorized (HTTP 401)\n":          target.Auth,
		"glab: 404 Project Not Found (HTTP 404)\n":     target.NotVisible,
		"glab: 429 Too Many Requests (HTTP 429)\n":     target.RateLimit,
		"glab: 500 Internal Server Error (HTTP 500)\n": target.Unavailable,
		// 本文の中の 403 は、status の位置にないので 403 と読まない
		"glab: 502 Bad Gateway (HTTP 502)\nupstream: HTTP 403\n": target.Unavailable,
		"glab: upstream said (HTTP 403) (HTTP 502)\n":            target.Unavailable,
		// 401・404・429 も status の位置でだけ読む
		"glab: 502 Bad Gateway (HTTP 502)\nupstream said (HTTP 401)\n": target.Unavailable,
		"glab: upstream said (HTTP 429) (HTTP 502)\n":                  target.Unavailable,
	} {
		t.Run(strings.TrimSpace(stderr), func(t *testing.T) {
			store := gitlab.NewStore(glab{issuesEndpoint + "/7": {stderr: stderr}}, project)

			_, err := store.Read(target.Ref{Kind: target.KindIssue, Number: 7})

			var failure *target.Failure
			if !errors.As(err, &failure) || failure.Kind != kind || !strings.Contains(err.Error(), strings.TrimSpace(stderr)) {
				t.Fatalf("err = %v, want %v で stderr を含む", err, kind)
			}
		})
	}
}

func TestRepoViewFailureOtherThanForbiddenKeepsTheStderrEvenIfTheBodyMentions403(t *testing.T) {
	stderr := "  Get https://gitlab.example.com/api/v4/projects/acme%2Fw: 502 see https://status.example.com: 403 errors\n"

	_, err := gitlab.CurrentProject(glab{"json": {stderr: stderr}})

	if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(stderr)) {
		t.Fatalf("err = %v, want stderr %q を含む", err, stderr)
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
