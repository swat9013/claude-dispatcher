// Package github は gh CLI を撃って GitHub の issue 置き場を読む adapter (system.md §13)。tracker には書かない。
package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

// Runner は gh を 1 回撃ち、stdout を返す。失敗は *proc.Error。
type Runner interface {
	Run(args ...string) ([]byte, error)
}

// Exec は gh の実物を撃つ Runner。gh は撃つたびに Env の PATH から探す。loop は何日も走るので、途中で gh を入れ替えても
// (Homebrew の更新で置き場が変わるなど) 次の tick から追従させる。
type Exec struct {
	Env     []string
	Timeout time.Duration
}

// Ready は gh を解決できるかを確かめる。loop の起動時に、tick で落ち続ける前に止めるために使う。
func (g Exec) Ready() error {
	_, err := deps.Lookup("gh", g.Env)
	return err
}

func (g Exec) Run(args ...string) ([]byte, error) {
	path, err := deps.Lookup("gh", g.Env)
	if err != nil {
		return nil, err
	}
	out, err := proc.Command{Path: path, Env: g.Env, Timeout: g.Timeout}.Output(args...)
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

// Store は repo の issue 置き場を読む部品。
type Store struct {
	gh   Runner
	repo Repo
}

func NewStore(gh Runner, repo Repo) Store { return Store{gh: gh, repo: repo} }

func (s Store) ScopeKey() string { return ScopeKey(s.repo) }

// Open は種類の open な作業対象を全件読む。失敗は *target.Failure。
func (s Store) Open(kind target.Kind) ([]target.Item, error) {
	switch kind {
	case target.KindIssue:
		return items(s.OpenIssues())
	}
	return nil, fmt.Errorf("未知の作業対象の種類 %q", kind)
}

// Read は作業対象 1 件を読み直す。失敗は *target.Failure。
func (s Store) Read(ref target.Ref) (target.Item, error) {
	switch ref.Kind {
	case target.KindIssue:
		return s.Issue(ref.Number)
	}
	return nil, fmt.Errorf("未知の作業対象の種類 %q", ref.Kind)
}

func items[T target.Item](list []T, err error) ([]target.Item, error) {
	if err != nil {
		return nil, err
	}
	out := make([]target.Item, len(list))
	for i, item := range list {
		out[i] = item
	}
	return out, nil
}

// connectionSize は issue ごとに 1 往復で読む label・assignee・依存先の上限。超えたら読み切れないとして失敗させる
const connectionSize = 100

// issueFields は issue 1 件について読む field。一覧と 1 件の読み直しで同じものを読む
var issueFields = fmt.Sprintf(`
        number
        title
        url
        state
        createdAt
        authorAssociation
        labels(first: %[1]d) { totalCount nodes { name } }
        assignees(first: %[1]d) { totalCount nodes { login } }
        milestone { title }
        blockedBy(first: %[1]d) { totalCount nodes { state } }`, connectionSize)

var issuesQuery = `
query($owner: String!, $name: String!, $endCursor: String) {
  repository(owner: $owner, name: $name) {
    issues(states: OPEN, first: 100, after: $endCursor, orderBy: {field: CREATED_AT, direction: ASC}) {
      pageInfo { hasNextPage endCursor }
      nodes {` + issueFields + `
      }
    }
  }
}`

var issueQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    issue(number: $number) {` + issueFields + `
    }
  }
}`

type connection[T any] struct {
	TotalCount int
	Nodes      []T
}

// issueNode は issueFields の応答。
type issueNode struct {
	Number            int
	Title             string
	URL               string
	State             string
	CreatedAt         time.Time
	AuthorAssociation string
	Labels            connection[struct{ Name string }]
	Assignees         connection[struct{ Login string }]
	Milestone         *struct{ Title string }
	BlockedBy         connection[struct{ State string }]
}

type issuePage struct {
	Data struct {
		Repository *struct {
			Issues struct {
				Nodes []issueNode
			}
		}
	}
}

// OpenIssues は repo の open な issue を全件読み、正規化して返す。失敗は *target.Failure。
func (s Store) OpenIssues() ([]target.Issue, error) {
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
			i, err := s.normalize(n)
			if err != nil {
				return nil, err
			}
			issues = append(issues, i)
		}
	}
	return issues, nil
}

// issueGone は、読み直した issue が消えている (削除・移管) ときの gh の文言。消えた issue は終端と同じに扱う
const issueGone = "Could not resolve to an Issue"

// Issue は issue 1 件を読み直す。close されていれば Closed。消えた issue も Closed として返す。失敗は *target.Failure。
func (s Store) Issue(number int) (target.Issue, error) {
	out, err := s.gh.Run("api", "graphql",
		"-f", "query="+issueQuery, "-f", "owner="+s.repo.Owner, "-f", "name="+s.repo.Name, "-F", fmt.Sprintf("number=%d", number))
	var failed *proc.Error
	if errors.As(err, &failed) && strings.Contains(failed.Stderr, issueGone) {
		return target.Issue{Number: number, Closed: true}, nil
	}
	if err != nil {
		return target.Issue{}, s.fail(classify(err), err)
	}
	var payload struct {
		Data struct {
			Repository *struct{ Issue *issueNode }
		}
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return target.Issue{}, s.fail(target.Unavailable, fmt.Errorf("gh api graphql の出力を読めない: %w", err))
	}
	switch {
	case payload.Data.Repository == nil:
		return target.Issue{}, s.fail(target.NotVisible, errors.New("gh api graphql の出力に repository が無い"))
	case payload.Data.Repository.Issue == nil:
		return target.Issue{Number: number, Closed: true}, nil
	}
	return s.normalize(*payload.Data.Repository.Issue)
}

// normalize は応答の issue 1 件を作業対象の形に写す。
func (s Store) normalize(n issueNode) (target.Issue, error) {
	for _, c := range []struct {
		what        string
		total, read int
	}{
		{"label", n.Labels.TotalCount, len(n.Labels.Nodes)},
		{"assignee", n.Assignees.TotalCount, len(n.Assignees.Nodes)},
		{"依存先", n.BlockedBy.TotalCount, len(n.BlockedBy.Nodes)},
	} {
		if c.total > c.read {
			return target.Issue{}, s.fail(target.Truncated, fmt.Errorf("issue #%d の %s が %d 件あり、1 往復で読める %d 件を超えた", n.Number, c.what, c.total, connectionSize))
		}
	}
	i := target.Issue{
		Number:               n.Number,
		Title:                n.Title,
		URL:                  n.URL,
		Closed:               n.State == "CLOSED",
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
	return i, nil
}

// collaboratorAssociations は collaborator と数える作者の立場 (GitHub の CommentAuthorAssociation)
var collaboratorAssociations = map[string]bool{"OWNER": true, "MEMBER": true, "COLLABORATOR": true}

func (s Store) fail(kind target.FailureKind, err error) *target.Failure {
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
