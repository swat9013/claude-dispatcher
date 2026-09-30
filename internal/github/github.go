// Package github は gh CLI を撃って GitHub の issue 置き場と CL 置き場を読む adapter (system.md §13)。tracker には書かない。
package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
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

// Store は repo の issue 置き場と CL 置き場を読む部品。
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
		return items(s.openIssues())
	case target.KindCL:
		return items(s.openCLs())
	}
	return nil, fmt.Errorf("未知の作業対象の種類 %q", kind)
}

// Read は作業対象 1 件を読み直す。失敗は *target.Failure。
func (s Store) Read(ref target.Ref) (target.Item, error) {
	switch ref.Kind {
	case target.KindIssue:
		return s.issue(ref.Number)
	case target.KindCL:
		return s.cl(ref.Number)
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

// connectionSize は作業対象ごとに 1 往復で読む label・assignee・依存先・review thread の上限。超えたら読み切れないとして失敗させる
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

// openIssues は repo の open な issue を全件読み、正規化して返す。失敗は *target.Failure。
func (s Store) openIssues() ([]target.Issue, error) {
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

// issue は issue 1 件を読み直す。close されていれば Closed。消えた issue も Closed として返す。失敗は *target.Failure。
func (s Store) issue(number int) (target.Issue, error) {
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
	if err := s.readAll(fmt.Sprintf("issue #%d", n.Number), []connectionCount{
		{"label", n.Labels.TotalCount, len(n.Labels.Nodes)},
		{"assignee", n.Assignees.TotalCount, len(n.Assignees.Nodes)},
		{"依存先", n.BlockedBy.TotalCount, len(n.BlockedBy.Nodes)},
	}); err != nil {
		return target.Issue{}, err
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

// clFields は CL 1 件について読む field。一覧と 1 件の読み直しで同じものを読む。review thread の書き手は、thread の
// 最初の comment の作者
var clFields = fmt.Sprintf(`
        number
        title
        url
        state
        createdAt
        authorAssociation
        isDraft
        isCrossRepository
        headRefName
        headRepository { nameWithOwner }
        mergeable
        reviewDecision
        labels(first: %[1]d) { totalCount nodes { name } }
        reviewThreads(first: %[1]d) { totalCount nodes { isResolved comments(first: 1) { nodes { authorAssociation } } } }
        latestOpinionatedReviews(first: %[1]d, writersOnly: true) { totalCount nodes { state } }
        commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }`, connectionSize)

var clsQuery = `
query($owner: String!, $name: String!, $endCursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequests(states: OPEN, first: 50, after: $endCursor, orderBy: {field: CREATED_AT, direction: ASC}) {
      pageInfo { hasNextPage endCursor }
      nodes {` + clFields + `
      }
    }
  }
}`

var clQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {` + clFields + `
    }
  }
}`

// clNode は clFields の応答。
type clNode struct {
	Number            int
	Title             string
	URL               string
	State             string
	CreatedAt         time.Time
	AuthorAssociation string
	IsDraft           bool
	IsCrossRepository bool
	HeadRefName       string
	HeadRepository    *struct{ NameWithOwner string }
	Mergeable         string
	ReviewDecision    *string
	Labels            connection[struct{ Name string }]
	// LatestOpinionatedReviews は、書き込み権限を持つ reviewer ごとの最後の承認か変更要求
	LatestOpinionatedReviews connection[struct{ State string }]
	ReviewThreads            connection[struct {
		IsResolved bool
		Comments   struct {
			Nodes []struct{ AuthorAssociation string }
		}
	}]
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct{ State string }
			}
		}
	}
}

type clPage struct {
	Data struct {
		Repository *struct {
			PullRequests struct {
				Nodes []clNode
			}
		}
	}
}

// openCLs は repo の open な CL を全件読み、正規化して返す。失敗は *target.Failure。
func (s Store) openCLs() ([]target.CL, error) {
	out, err := s.gh.Run("api", "graphql", "--paginate", "--slurp",
		"-f", "query="+clsQuery, "-f", "owner="+s.repo.Owner, "-f", "name="+s.repo.Name)
	if err != nil {
		return nil, s.fail(classify(err), err)
	}
	var pages []clPage
	if err := json.Unmarshal(out, &pages); err != nil {
		return nil, s.fail(target.Unavailable, fmt.Errorf("gh api graphql の出力を読めない: %w", err))
	}
	cls := []target.CL{}
	for _, page := range pages {
		if page.Data.Repository == nil {
			return nil, s.fail(target.NotVisible, errors.New("gh api graphql の出力に repository が無い"))
		}
		for _, n := range page.Data.Repository.PullRequests.Nodes {
			cl, err := s.normalizeCL(n)
			if err != nil {
				return nil, err
			}
			cls = append(cls, cl)
		}
	}
	return cls, nil
}

// clGone は、読み直した CL が消えているときの gh の文言。消えた CL は終端と同じに扱う
const clGone = "Could not resolve to a PullRequest"

// cl は CL 1 件を読み直す。merge か close されていれば Closed。消えた CL も Closed として返す。失敗は *target.Failure。
func (s Store) cl(number int) (target.CL, error) {
	out, err := s.gh.Run("api", "graphql",
		"-f", "query="+clQuery, "-f", "owner="+s.repo.Owner, "-f", "name="+s.repo.Name, "-F", fmt.Sprintf("number=%d", number))
	var failed *proc.Error
	if errors.As(err, &failed) && strings.Contains(failed.Stderr, clGone) {
		return target.CL{Number: number, Closed: true}, nil
	}
	if err != nil {
		return target.CL{}, s.fail(classify(err), err)
	}
	var payload struct {
		Data struct {
			Repository *struct{ PullRequest *clNode }
		}
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return target.CL{}, s.fail(target.Unavailable, fmt.Errorf("gh api graphql の出力を読めない: %w", err))
	}
	switch {
	case payload.Data.Repository == nil:
		return target.CL{}, s.fail(target.NotVisible, errors.New("gh api graphql の出力に repository が無い"))
	case payload.Data.Repository.PullRequest == nil:
		return target.CL{Number: number, Closed: true}, nil
	}
	return s.normalizeCL(*payload.Data.Repository.PullRequest)
}

// mergeability は GitHub の mergeable の値を CL の merge conflict の有無に写す。
var mergeability = map[string]target.Mergeability{"MERGEABLE": target.MergeClean, "CONFLICTING": target.MergeConflict}

// approved は CL が承認済みか。review を必須にした repo では reviewDecision で決める。review が必須でない repo では
// reviewDecision が null なので、書き込み権限を持つ reviewer の最後の review に承認があり、変更要求が無いことで決める。
func approved(n clNode) bool {
	if n.ReviewDecision != nil {
		return *n.ReviewDecision == "APPROVED"
	}
	states := make([]string, len(n.LatestOpinionatedReviews.Nodes))
	for i, r := range n.LatestOpinionatedReviews.Nodes {
		states[i] = r.State
	}
	return slices.Contains(states, "APPROVED") && !slices.Contains(states, "CHANGES_REQUESTED")
}

// normalizeCL は応答の CL 1 件を作業対象の形に写す (CL の状態の語彙へ写す。system.md §6)。
func (s Store) normalizeCL(n clNode) (target.CL, error) {
	if err := s.readAll(fmt.Sprintf("CL #%d", n.Number), []connectionCount{
		{"label", n.Labels.TotalCount, len(n.Labels.Nodes)},
		{"review thread", n.ReviewThreads.TotalCount, len(n.ReviewThreads.Nodes)},
		{"review", n.LatestOpinionatedReviews.TotalCount, len(n.LatestOpinionatedReviews.Nodes)},
	}); err != nil {
		return target.CL{}, err
	}
	cl := target.CL{
		Number:               n.Number,
		Title:                n.Title,
		URL:                  n.URL,
		Closed:               n.State != "OPEN",
		CreatedAt:            n.CreatedAt,
		AuthorIsCollaborator: collaboratorAssociations[n.AuthorAssociation],
		Head:                 n.HeadRefName,
		SameRepo:             !n.IsCrossRepository,
		Draft:                n.IsDraft,
		Mergeable:            mergeability[n.Mergeable],
		Approved:             approved(n),
	}
	if n.HeadRepository != nil {
		cl.HeadRepo = n.HeadRepository.NameWithOwner
	}
	for _, l := range n.Labels.Nodes {
		cl.Labels = append(cl.Labels, l.Name)
	}
	for _, t := range n.ReviewThreads.Nodes {
		if !t.IsResolved && len(t.Comments.Nodes) > 0 && collaboratorAssociations[t.Comments.Nodes[0].AuthorAssociation] {
			cl.ReviewUnresolved = true
		}
	}
	if commits := n.Commits.Nodes; len(commits) > 0 && commits[0].Commit.StatusCheckRollup != nil {
		state := commits[0].Commit.StatusCheckRollup.State
		cl.CIFailed = state == "FAILURE" || state == "ERROR"
	}
	return cl, nil
}

// connectionCount は connection (name) の件数 (total) と、1 往復で読めた件数 (read)。
type connectionCount struct {
	name        string
	total, read int
}

// readAll は、作業対象 (what) の connection を 1 往復で読み切れたかを確かめる。読み切れなければ Truncated の失敗を返す。
// 切り詰めた像から候補を出さない。
func (s Store) readAll(what string, counts []connectionCount) error {
	for _, c := range counts {
		if c.total > c.read {
			return s.fail(target.Truncated, fmt.Errorf("%s の %s が %d 件あり、1 往復で読める %d 件を超えた", what, c.name, c.total, connectionSize))
		}
	}
	return nil
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
