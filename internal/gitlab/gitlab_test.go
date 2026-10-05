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

// glab は endpoint (argv の最後) の接頭辞ごとに応答を返す Runner (glab の代役)。どれにも当たらなければ member でない 404。
type glab map[string]response

type response struct {
	stdout string
	stderr string
}

func (g glab) Run(args ...string) ([]byte, error) {
	endpoint := args[len(args)-1]
	for prefix, r := range g {
		if strings.HasPrefix(endpoint, prefix) {
			if r.stderr != "" {
				return nil, &proc.Error{Name: "glab", Args: args, Exit: 1, Stderr: r.stderr}
			}
			return []byte(r.stdout), nil
		}
	}
	return nil, &proc.Error{Name: "glab", Args: args, Exit: 1, Stderr: "glab: 404 Not found (HTTP 404)"}
}

var project = gitlab.Project{Host: "gitlab.example.com", Path: "acme/sub/widgets"}

const issuesEndpoint = "projects/acme%2Fsub%2Fwidgets/issues"

func issueJSON(iid int) string {
	return `{"iid":` + strconv.Itoa(iid) + `,"title":"t","state":"opened","labels":["a"],"author":{"id":1}}`
}

func TestOpenIssuesReadsBothSpellingsOfThePaginatedOutput(t *testing.T) {
	for name, stdout := range map[string]string{
		"page ごとの配列を連ねる": "[" + issueJSON(1) + "," + issueJSON(2) + "]\n[" + issueJSON(3) + "]",
		"1 つの配列に結合する":    "[" + issueJSON(1) + "," + issueJSON(2) + "," + issueJSON(3) + "]",
	} {
		t.Run(name, func(t *testing.T) {
			store := gitlab.NewStore(glab{issuesEndpoint + "?": {stdout: stdout}}, project)

			items, err := store.Open(target.KindIssue)

			if err != nil || len(items) != 3 {
				t.Fatalf("items = %v, err = %v", items, err)
			}
		})
	}
}

func TestIssueThatIsGoneIsReadAsClosed(t *testing.T) {
	store := gitlab.NewStore(glab{issuesEndpoint + "/7": {stderr: "glab: 404 Not found (HTTP 404)"}}, project)

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
	store := gitlab.NewStore(glab{issuesEndpoint + "/7": {stdout: issueJSON(7)}}, project)

	item, err := store.Read(target.Ref{Kind: target.KindIssue, Number: 7})

	if err != nil || item.(target.Issue).AuthorIsCollaborator {
		t.Fatalf("item = %v, err = %v", item, err)
	}
}

func TestMergeRequestsAreNotRead(t *testing.T) {
	store := gitlab.NewStore(glab{}, project)

	_, err := store.Open(target.KindCL)

	if err == nil {
		t.Fatal("merge request を読めたことにした")
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
