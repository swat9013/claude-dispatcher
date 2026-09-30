package github_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

// TestTimedOutGhIsStoppedWithTheChildrenItStarted は、stdout を握ったまま眠る孫を残す gh が timeout で
// process group ごと止まり、Run が上限の近くで戻ることを見る。
func TestTimedOutGhIsStoppedWithTheChildrenItStarted(t *testing.T) {
	gh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nsleep 30 &\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := time.Now()

	_, err := github.Exec{Env: []string{"PATH=" + filepath.Dir(gh) + ":/bin:/usr/bin"}, Timeout: 200 * time.Millisecond}.Run("repo", "view")

	if err == nil || !strings.Contains(err.Error(), "を超えても終わらない") {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout 後も孫が pipe を握って戻らなかった (%s)", elapsed)
	}
}

func TestRepoThatGhCannotResolveIsNotFound(t *testing.T) {
	gh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\necho \"GraphQL: Could not resolve to a Repository with the name 'acme/x'. (repository)\" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	store := github.NewStore(github.Exec{Env: []string{"PATH=" + filepath.Dir(gh)}, Timeout: 5 * time.Second}, github.Repo{Owner: "acme", Name: "x"})

	_, err := store.OpenIssues()

	var failure *target.Failure
	if !errors.As(err, &failure) || failure.Kind != target.NotVisible {
		t.Fatalf("err = %v", err)
	}
}

// pages は gh api graphql --paginate --slurp の応答を返す Runner (gh の代役)。
type pages string

func (p pages) Run(...string) ([]byte, error) { return []byte(p), nil }

func issueNode(number int, association string, blockers ...string) string {
	nodes := make([]string, len(blockers))
	for i, b := range blockers {
		nodes[i] = `{"state":"` + b + `"}`
	}
	return fmt.Sprintf(`{"number":%d,"title":"t","createdAt":"2026-01-01T00:00:00Z","authorAssociation":%q,
		"labels":{"totalCount":0,"nodes":[]},"assignees":{"totalCount":0,"nodes":[]},"milestone":null,
		"blockedBy":{"totalCount":%d,"nodes":[%s]}}`, number, association, len(blockers), strings.Join(nodes, ","))
}

func openIssues(t *testing.T, nodes ...string) []target.Issue {
	t.Helper()
	store := github.NewStore(pages(`[{"data":{"repository":{"issues":{"nodes":[`+strings.Join(nodes, ",")+`]}}}}]`), github.Repo{Owner: "acme", Name: "widgets"})
	issues, err := store.OpenIssues()
	if err != nil {
		t.Fatal(err)
	}
	return issues
}

func TestAuthorAssociationIsCollaboratorOnlyForOwnerMemberAndCollaborator(t *testing.T) {
	for association, want := range map[string]bool{
		"OWNER": true, "MEMBER": true, "COLLABORATOR": true,
		"CONTRIBUTOR": false, "FIRST_TIME_CONTRIBUTOR": false, "FIRST_TIMER": false, "NONE": false, "MANNEQUIN": false,
	} {
		issues := openIssues(t, issueNode(1, association))

		if issues[0].AuthorIsCollaborator != want {
			t.Errorf("%s: collaborator = %v, want %v", association, issues[0].AuthorIsCollaborator, want)
		}
	}
}

func TestOnlyOpenBlockersAreCountedAsUnresolved(t *testing.T) {
	issues := openIssues(t, issueNode(1, "OWNER", "CLOSED", "OPEN", "CLOSED", "OPEN"))

	if issues[0].OpenBlockers != 2 {
		t.Fatalf("未解決の依存先 = %d, want 2", issues[0].OpenBlockers)
	}
}

// response は gh の 1 回分の応答を返す Runner (gh の代役)。
type response struct {
	out []byte
	err error
}

func (r response) Run(...string) ([]byte, error) { return r.out, r.err }

func reread(t *testing.T, r response) (target.Issue, error) {
	t.Helper()
	return github.NewStore(r, github.Repo{Owner: "acme", Name: "widgets"}).Issue(42)
}

func TestRereadIssueThatIsClosedIsClosed(t *testing.T) {
	node := strings.Replace(issueNode(42, "OWNER"), `"number":42,`, `"number":42,"state":"CLOSED",`, 1)

	issue, err := reread(t, response{out: []byte(`{"data":{"repository":{"issue":` + node + `}}}`)})

	if err != nil || !issue.Closed || issue.Number != 42 {
		t.Fatalf("issue = %+v (%v), want 終端", issue, err)
	}
}

func TestRereadIssueThatIsOpenIsNotClosed(t *testing.T) {
	node := strings.Replace(issueNode(42, "OWNER"), `"number":42,`, `"number":42,"state":"OPEN",`, 1)

	issue, err := reread(t, response{out: []byte(`{"data":{"repository":{"issue":` + node + `}}}`)})

	if err != nil || issue.Closed {
		t.Fatalf("issue = %+v (%v), want open", issue, err)
	}
}

func TestRereadIssueThatGhCannotResolveIsClosed(t *testing.T) {
	gone := &proc.Error{Name: "gh", Args: []string{"api", "graphql"}, Exit: 1, Stderr: "GraphQL: Could not resolve to an Issue with the number of 42. (repository.issue)"}

	issue, err := reread(t, response{err: gone})

	if err != nil || !issue.Closed {
		t.Fatalf("issue = %+v (%v), want 消えた issue は終端", issue, err)
	}
}

func TestRereadIssueMissingFromTheResponseIsClosed(t *testing.T) {
	issue, err := reread(t, response{out: []byte(`{"data":{"repository":{"issue":null}}}`)})

	if err != nil || !issue.Closed {
		t.Fatalf("issue = %+v (%v), want 終端", issue, err)
	}
}

func TestRereadIssueOfARepositoryMissingFromTheResponseIsNotVisible(t *testing.T) {
	_, err := reread(t, response{out: []byte(`{"data":{"repository":null}}`)})

	var failure *target.Failure
	if !errors.As(err, &failure) || failure.Kind != target.NotVisible {
		t.Fatalf("err = %v", err)
	}
}

func TestRereadFailureOtherThanAMissingIssueIsNotClosed(t *testing.T) {
	_, err := reread(t, response{err: &proc.Error{Name: "gh", Args: []string{"api", "graphql"}, Exit: 1, Stderr: "HTTP 502"}})

	if err == nil {
		t.Fatal("gh の失敗を終端として読んだ")
	}
}

func TestCLWithMoreReviewThreadsThanOneRoundTripIsTruncated(t *testing.T) {
	node := `{"number":5,"title":"t","state":"OPEN","createdAt":"2026-01-01T00:00:00Z","authorAssociation":"OWNER","isDraft":false,
		"isCrossRepository":false,"headRefName":"b","headRepository":{"nameWithOwner":"acme/widgets"},"mergeable":"MERGEABLE",
		"reviewDecision":null,"labels":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":101,"nodes":[]},"commits":{"nodes":[]}}`
	store := github.NewStore(pages(`[{"data":{"repository":{"pullRequests":{"nodes":[`+node+`]}}}}]`), github.Repo{Owner: "acme", Name: "widgets"})

	_, err := store.Open(target.KindCL)

	var failure *target.Failure
	if !errors.As(err, &failure) || failure.Kind != target.Truncated {
		t.Fatalf("err = %v", err)
	}
}
