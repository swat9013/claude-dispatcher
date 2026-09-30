// Package github は gh CLI を撃って GitHub の issue 置き場を読む adapter (system.md §13)。tracker には書かない。
package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

// Runner は gh を 1 回撃ち、stdout を返す。失敗は *proc.Error。
type Runner interface {
	Run(args ...string) ([]byte, error)
}

// Exec は gh の実物を撃つ Runner。Path は解決済みの絶対 path、Env は子プロセスの env。
type Exec struct {
	Path    string
	Env     []string
	Timeout time.Duration
}

func (g Exec) Run(args ...string) ([]byte, error) {
	out, err := proc.Command{Path: g.Path, Env: g.Env, Timeout: g.Timeout}.Output(args...)
	return []byte(out), err
}

// Repo は GitHub の repo (`owner/name`)。
type Repo struct {
	Owner string
	Name  string
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

var repoPart = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ParseRepo は `owner/name` の綴りを読む。
func ParseRepo(text string) (Repo, error) {
	owner, name, ok := strings.Cut(text, "/")
	if !ok || !repoPart.MatchString(owner) || !repoPart.MatchString(name) {
		return Repo{}, errors.New("owner/name の綴りで書く")
	}
	return Repo{Owner: owner, Name: name}, nil
}

// ScopeKey は repo の issue 置き場を指す scope key。GitHub の owner と repo の名前は大文字と小文字を区別しないので小文字にする。
func ScopeKey(repo Repo) string { return strings.ToLower("github.com/" + repo.String()) }

// IssueStore は repo の issue 置き場を読む部品。
type IssueStore struct {
	gh   Runner
	repo Repo
}

func NewIssueStore(gh Runner, repo Repo) IssueStore { return IssueStore{gh: gh, repo: repo} }

func (s IssueStore) ScopeKey() string { return ScopeKey(s.repo) }

// connectionSize は issue ごとに 1 往復で読む label・assignee・依存先の上限。超えたら読み切れないとして失敗させる
const connectionSize = 100

var issuesQuery = fmt.Sprintf(`
query($owner: String!, $name: String!, $endCursor: String) {
  repository(owner: $owner, name: $name) {
    issues(states: OPEN, first: 100, after: $endCursor, orderBy: {field: CREATED_AT, direction: ASC}) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number
        title
        url
        createdAt
        authorAssociation
        labels(first: %[1]d) { totalCount nodes { name } }
        assignees(first: %[1]d) { totalCount nodes { login } }
        milestone { title }
        blockedBy(first: %[1]d) { totalCount nodes { state } }
      }
    }
  }
}`, connectionSize)

type connection[T any] struct {
	TotalCount int
	Nodes      []T
}

type issuePage struct {
	Data struct {
		Repository *struct {
			Issues struct {
				Nodes []struct {
					Number            int
					Title             string
					URL               string
					CreatedAt         time.Time
					AuthorAssociation string
					Labels            connection[struct{ Name string }]
					Assignees         connection[struct{ Login string }]
					Milestone         *struct{ Title string }
					BlockedBy         connection[struct{ State string }]
				}
			}
		}
	}
}

// OpenIssues は repo の open な issue を全件読み、正規化して返す。失敗は *target.Failure。
func (s IssueStore) OpenIssues() ([]target.Issue, error) {
	// -f は生文字列。-F だと数字だけの owner / name が Int に型付けされ String! 変数に入らない
	out, err := s.gh.Run("api", "graphql", "--paginate", "--slurp",
		"-f", "query="+issuesQuery, "-f", "owner="+s.repo.Owner, "-f", "name="+s.repo.Name)
	if err != nil {
		return nil, s.fail(classify(err), err)
	}
	var pages []issuePage
	if err := json.Unmarshal(out, &pages); err != nil {
		return nil, s.fail(target.Unavailable, fmt.Errorf("gh api graphql の出力を読めない: %w", err))
	}
	issues := []target.Issue{}
	for _, page := range pages {
		if page.Data.Repository == nil {
			return nil, s.fail(target.NotVisible, errors.New("gh api graphql の出力に repository が無い"))
		}
		for _, n := range page.Data.Repository.Issues.Nodes {
			for _, c := range []struct {
				what        string
				total, read int
			}{
				{"label", n.Labels.TotalCount, len(n.Labels.Nodes)},
				{"assignee", n.Assignees.TotalCount, len(n.Assignees.Nodes)},
				{"依存先", n.BlockedBy.TotalCount, len(n.BlockedBy.Nodes)},
			} {
				if c.total > c.read {
					return nil, s.fail(target.Truncated, fmt.Errorf("issue #%d の %s が %d 件あり、1 往復で読める %d 件を超えた", n.Number, c.what, c.total, connectionSize))
				}
			}
			i := target.Issue{
				Number:               n.Number,
				Title:                n.Title,
				URL:                  n.URL,
				CreatedAt:            n.CreatedAt,
				AuthorIsCollaborator: collaboratorAssociations[n.AuthorAssociation],
			}
			for _, l := range n.Labels.Nodes {
				i.Labels = append(i.Labels, l.Name)
			}
			for _, a := range n.Assignees.Nodes {
				i.Assignees = append(i.Assignees, a.Login)
			}
			if n.Milestone != nil {
				i.Milestone = n.Milestone.Title
			}
			for _, b := range n.BlockedBy.Nodes {
				if b.State == "OPEN" {
					i.OpenBlockers++
				}
			}
			issues = append(issues, i)
		}
	}
	return issues, nil
}

// collaboratorAssociations は collaborator と数える作者の立場 (GitHub の CommentAuthorAssociation)
var collaboratorAssociations = map[string]bool{"OWNER": true, "MEMBER": true, "COLLABORATOR": true}

func (s IssueStore) fail(kind target.FailureKind, err error) *target.Failure {
	return &target.Failure{Kind: kind, Place: s.repo.String(), Err: err}
}

// 認証が要るときの gh の exit code と、未認証 / token 失効のときに stderr へ出る文言
const authExit = 4

var (
	authMarkers       = []string{"HTTP 401", "gh auth login", "Bad credentials"}
	notVisibleMarkers = []string{"Could not resolve to a Repository", "HTTP 404"}
	rateLimitMarkers  = []string{"rate limit", "http 429"}
)

// classify は gh の失敗を分類する。
func classify(err error) target.FailureKind {
	var failed *proc.Error
	if !errors.As(err, &failed) {
		return target.Unavailable
	}
	switch {
	case failed.Exit == authExit || containsAny(failed.Stderr, authMarkers):
		return target.Auth
	case containsAny(failed.Stderr, notVisibleMarkers):
		return target.NotVisible
	case containsAny(strings.ToLower(failed.Stderr), rateLimitMarkers):
		return target.RateLimit
	}
	return target.Unavailable
}

func containsAny(text string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}
