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

	_, err := store.Open(target.KindIssue)

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
	items, err := store.Open(target.KindIssue)
	if err != nil {
		t.Fatal(err)
	}
	issues := make([]target.Issue, len(items))
	for i, item := range items {
		issues[i] = item.(target.Issue)
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
	item, err := github.NewStore(r, github.Repo{Owner: "acme", Name: "widgets"}).Read(target.Ref{Kind: target.KindIssue, Number: 42})
	if err != nil {
		return target.Issue{}, err
	}
	return item.(target.Issue), nil
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

// ghFailed は gh api graphql が stderr を出して exit 1 で落ちた失敗
func ghFailed(stderr string) response {
	return response{err: &proc.Error{Name: "gh", Args: []string{"api", "graphql"}, Exit: 1, Stderr: stderr}}
}

func TestRereadIssueIsNotClosedWhenTheMissingIssueIsOnlyMentionedInsideAMessage(t *testing.T) {
	for _, stderr := range []string{
		"gh: upstream said Could not resolve to an Issue (HTTP 502)\n",
		// message の無い 404 (手前の proxy の HTML などでありうる)
		"gh: HTTP 404\n",
	} {
		t.Run(strings.TrimSpace(stderr), func(t *testing.T) {
			_, err := reread(t, ghFailed(stderr))

			var failure *target.Failure
			if !errors.As(err, &failure) || failure.Kind != target.Unavailable {
				t.Fatalf("err = %v, want 読めない (消えた issue と読まない)", err)
			}
		})
	}
}

func TestGhFailuresAreClassifiedByTheStatusPositionAndTheMessage(t *testing.T) {
	for stderr, kind := range map[string]target.FailureKind{
		"gh: Bad credentials (HTTP 401)\n":                                      target.Auth,
		"HTTP 401: Bad credentials (https://api.github.com/graphql)\n":          target.Auth,
		"gh: Not Found (HTTP 404)\n":                                            target.NotVisible,
		"gh: Could not resolve to a Repository with the name 'acme/widgets'.\n": target.NotVisible,
		"gh: Too Many Requests (HTTP 429)\n":                                    target.RateLimit,
		"gh: API rate limit exceeded for user ID 1.\n":                          target.RateLimit,
		"gh: Bad Gateway (HTTP 502)\n":                                          target.Unavailable,
		// 本文・message の中の status は status の位置にないので読まない
		"gh: upstream returned HTTP 404 (HTTP 502)\n":      target.Unavailable,
		"gh: upstream said (HTTP 401) (HTTP 502)\n":        target.Unavailable,
		"gh: Bad Gateway (HTTP 502)\nupstream: HTTP 401\n": target.Unavailable,
		// message の無い応答 (手前の proxy の HTML などでありうる) は status に依らず分類しない
		"gh: HTTP 401\n": target.Unavailable,
		"gh: HTTP 429\n": target.Unavailable,
	} {
		t.Run(strings.TrimSpace(stderr), func(t *testing.T) {
			_, err := github.NewStore(ghFailed(stderr), github.Repo{Owner: "acme", Name: "widgets"}).Open(target.KindIssue)

			var failure *target.Failure
			if !errors.As(err, &failure) || failure.Kind != kind {
				t.Fatalf("err = %v, want %v", err, kind)
			}
		})
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

func clNode(number int, extra string) string {
	return fmt.Sprintf(`{"number":%d,"title":"t","state":"OPEN","createdAt":"2026-01-01T00:00:00Z","authorAssociation":"OWNER","isDraft":false,
		"isCrossRepository":false,"headRefName":"b","headRepository":{"nameWithOwner":"acme/widgets"},"mergeable":"MERGEABLE",
		"reviewDecision":null,"labels":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":0,"nodes":[]},
		"latestOpinionatedReviews":{"totalCount":0,"nodes":[]},"commits":{"nodes":[]}%s}`, number, extra)
}

func rereadCL(t *testing.T, r response) (target.CL, error) {
	t.Helper()
	item, err := github.NewStore(r, github.Repo{Owner: "acme", Name: "widgets"}).Read(target.Ref{Kind: target.KindCL, Number: 5})
	if err != nil {
		return target.CL{}, err
	}
	return item.(target.CL), nil
}

func TestRereadCLThatIsMergedIsClosed(t *testing.T) {
	node := strings.Replace(clNode(5, ""), `"state":"OPEN"`, `"state":"MERGED"`, 1)

	cl, err := rereadCL(t, response{out: []byte(`{"data":{"repository":{"pullRequest":` + node + `}}}`)})

	if err != nil || !cl.Closed || cl.Number != 5 {
		t.Fatalf("CL = %+v (%v), want 終端", cl, err)
	}
}

func TestRereadCLThatGhCannotResolveIsClosed(t *testing.T) {
	gone := &proc.Error{Name: "gh", Args: []string{"api", "graphql"}, Exit: 1, Stderr: "GraphQL: Could not resolve to a PullRequest with the number of 5. (repository.pullRequest)"}

	cl, err := rereadCL(t, response{err: gone})

	if err != nil || !cl.Closed {
		t.Fatalf("CL = %+v (%v), want 消えた CL は終端", cl, err)
	}
}

func TestRereadCLMissingFromTheResponseIsClosed(t *testing.T) {
	cl, err := rereadCL(t, response{out: []byte(`{"data":{"repository":{"pullRequest":null}}}`)})

	if err != nil || !cl.Closed {
		t.Fatalf("CL = %+v (%v), want 終端", cl, err)
	}
}

func TestRereadCLFailureOtherThanAMissingCLIsNotClosed(t *testing.T) {
	_, err := rereadCL(t, response{err: &proc.Error{Name: "gh", Args: []string{"api", "graphql"}, Exit: 1, Stderr: "HTTP 502"}})

	if err == nil {
		t.Fatal("gh の失敗を終端として読んだ")
	}
}

func TestCLWithoutRequiredReviewsIsApprovedByAWritersApprovalWithoutChangeRequests(t *testing.T) {
	cases := []struct {
		name    string
		reviews string
		want    bool
	}{
		{"承認だけなら承認済み", `[{"state":"APPROVED"}]`, true},
		{"変更要求が残っていれば承認済みでない", `[{"state":"APPROVED"},{"state":"CHANGES_REQUESTED"}]`, false},
		{"review が無ければ承認済みでない", `[]`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			node := strings.Replace(clNode(5, ""), `"latestOpinionatedReviews":{"totalCount":0,"nodes":[]}`, `"latestOpinionatedReviews":{"totalCount":0,"nodes":`+c.reviews+`}`, 1)

			cl, err := rereadCL(t, response{out: []byte(`{"data":{"repository":{"pullRequest":` + node + `}}}`)})

			if err != nil || cl.Approved != c.want {
				t.Fatalf("approved = %v (%v), want %v", cl.Approved, err, c.want)
			}
		})
	}
}

func TestCLWithRequiredReviewsFollowsTheReviewDecision(t *testing.T) {
	node := strings.Replace(clNode(5, ""), `"reviewDecision":null`, `"reviewDecision":"REVIEW_REQUIRED"`, 1)
	node = strings.Replace(node, `"latestOpinionatedReviews":{"totalCount":0,"nodes":[]}`, `"latestOpinionatedReviews":{"totalCount":1,"nodes":[{"state":"APPROVED"}]}`, 1)

	cl, err := rereadCL(t, response{out: []byte(`{"data":{"repository":{"pullRequest":` + node + `}}}`)})

	if err != nil || cl.Approved {
		t.Fatalf("approved = %v (%v), want reviewDecision に従って承認済みでない", cl.Approved, err)
	}
}

func TestCLWithMoreWriterReviewsThanOneRoundTripIsTruncated(t *testing.T) {
	node := strings.Replace(clNode(5, ""), `"latestOpinionatedReviews":{"totalCount":0,"nodes":[]}`, `"latestOpinionatedReviews":{"totalCount":101,"nodes":[]}`, 1)

	_, err := rereadCL(t, response{out: []byte(`{"data":{"repository":{"pullRequest":` + node + `}}}`)})

	var failure *target.Failure
	if !errors.As(err, &failure) || failure.Kind != target.Truncated {
		t.Fatalf("err = %v", err)
	}
}
