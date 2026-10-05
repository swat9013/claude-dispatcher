// Package gitlab は glab CLI を撃って GitLab の issue 置き場を読む adapter (system.md §13)。tracker には書かない。
// CL 置き場 (merge request) はまだ読まない。
package gitlab

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

// Runner は glab を 1 回撃ち、stdout を返す。失敗は *proc.Error。
type Runner interface {
	Run(args ...string) ([]byte, error)
}

// Exec は glab の実物を撃つ Runner。glab は撃つたびに Env の PATH から探す (github.Exec と同じく、loop が走っている間の
// 入れ替えに追従させる)。
type Exec struct {
	Env     []string
	Timeout time.Duration
}

func (g Exec) Run(args ...string) ([]byte, error) {
	path, err := deps.Lookup("glab", g.Env)
	if err != nil {
		return nil, err
	}
	out, err := proc.Command{Path: path, Env: g.Env, Timeout: g.Timeout}.Output(args...)
	return []byte(out), err
}

// DefaultHost は tracker.host を省いたときの host
const DefaultHost = "gitlab.com"

// Project は GitLab の project (host と、group と subgroup を含む path)。
type Project struct {
	Host string
	Path string
}

// String は issue 置き場の表示名 (`<host>/<path>`)。
func (p Project) String() string { return p.Host + "/" + p.Path }

var (
	hostPattern = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)
	pathPart    = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// ParseHost は host 名 (`gitlab.example.com`。port を付けてもよい) の綴りを確かめる。
func ParseHost(text string) (string, error) {
	if !hostPattern.MatchString(text) {
		return "", errors.New("host 名 (gitlab.example.com) の綴りで書く")
	}
	return text, nil
}

// ParsePath は project の path (`group/name`。subgroup を含む段数を問わない) の綴りを確かめる。
func ParsePath(text string) (string, error) {
	parts := strings.Split(text, "/")
	if len(parts) < 2 {
		return "", errors.New("group/name の綴り (subgroup を含めてよい) で書く")
	}
	for _, part := range parts {
		if !pathPart.MatchString(part) {
			return "", errors.New("group/name の綴り (subgroup を含めてよい) で書く")
		}
	}
	return text, nil
}

// ScopeKey は project の issue 置き場を指す scope key。GitLab の path は大文字と小文字を区別しないので小文字にする。
func ScopeKey(p Project) string { return strings.ToLower(p.String()) }

// Store は project の issue 置き場を読む部品。
type Store struct {
	glab    Runner
	project Project
}

func NewStore(glab Runner, project Project) Store { return Store{glab: glab, project: project} }

func (s Store) ScopeKey() string { return ScopeKey(s.project) }

// errMergeRequests は CL 置き場を読もうとしたときの誤り。workflow 定義の検査が gitlab の on: cl を落とすので、届けば実装の誤り
var errMergeRequests = errors.New("GitLab の merge request は CL 置き場として読めない (未対応)")

// Open は種類の open な作業対象を全件読む。失敗は *target.Failure。
func (s Store) Open(kind target.Kind) ([]target.Item, error) {
	switch kind {
	case target.KindIssue:
		issues, err := s.openIssues()
		if err != nil {
			return nil, err
		}
		items := make([]target.Item, len(issues))
		for i, issue := range issues {
			items[i] = issue
		}
		return items, nil
	case target.KindCL:
		return nil, errMergeRequests
	}
	return nil, fmt.Errorf("未知の作業対象の種類 %q", kind)
}

// Read は作業対象 1 件を読み直す。失敗は *target.Failure。
func (s Store) Read(ref target.Ref) (target.Item, error) {
	switch ref.Kind {
	case target.KindIssue:
		return s.issue(ref.Number)
	case target.KindCL:
		return nil, errMergeRequests
	}
	return nil, fmt.Errorf("未知の作業対象の種類 %q", ref.Kind)
}

// get は REST の endpoint を GET で撃つ。glab は field (-f / -F) を渡すと POST を撃つので、条件は query string に書く。
// host は --hostname で明示する (git の作業ツリーの外では gitlab.com に向くため)。
func (s Store) get(endpoint string, flags ...string) ([]byte, error) {
	args := append([]string{"api", "--hostname", s.project.Host}, flags...)
	return s.glab.Run(append(args, endpoint)...)
}

// endpoint は project の下の REST の endpoint。path は `/` を含めて URL エンコードする。
func (s Store) endpoint(rest string) string {
	return "projects/" + url.PathEscape(s.project.Path) + "/" + rest
}

// issueJSON は REST の issue の応答のうち読むもの。
type issueJSON struct {
	IID       int       `json:"iid"`
	Title     string    `json:"title"`
	WebURL    string    `json:"web_url"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	Labels    []string  `json:"labels"`
	Assignees []struct {
		Username string `json:"username"`
	} `json:"assignees"`
	Milestone *struct {
		Title string `json:"title"`
	} `json:"milestone"`
	Author struct {
		ID int `json:"id"`
	} `json:"author"`
}

// openIssues は project の open な issue を全件読み、正規化して返す。失敗は *target.Failure。
func (s Store) openIssues() ([]target.Issue, error) {
	out, err := s.get(s.endpoint("issues?state=opened&order_by=created_at&sort=asc&per_page=100"), "--paginate")
	if err != nil {
		return nil, s.fail(classify(err), err)
	}
	// --paginate は page ごとの配列を連ねて出すか 1 つの配列に結合して出す (glab の版による)。どちらも配列の列として読む
	var nodes []issueJSON
	decoder := json.NewDecoder(bytes.NewReader(out))
	for {
		var page []issueJSON
		err := decoder.Decode(&page)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, s.fail(target.Unavailable, fmt.Errorf("glab api の出力を読めない: %w", err))
		}
		nodes = append(nodes, page...)
	}
	// 作者の立場は作者ごとに 1 回だけ読む
	collaborators := map[int]bool{}
	issues := []target.Issue{}
	for _, n := range nodes {
		i, err := s.normalize(n, collaborators)
		if err != nil {
			return nil, err
		}
		issues = append(issues, i)
	}
	return issues, nil
}

// issue は issue 1 件を読み直す。close されていれば Closed。消えた issue も Closed として返す。失敗は *target.Failure。
func (s Store) issue(iid int) (target.Issue, error) {
	out, err := s.get(s.endpoint("issues/" + strconv.Itoa(iid)))
	if gone(err) {
		return target.Issue{Number: iid, Closed: true}, nil
	}
	if err != nil {
		return target.Issue{}, s.fail(classify(err), err)
	}
	var n issueJSON
	if err := json.Unmarshal(out, &n); err != nil {
		return target.Issue{}, s.fail(target.Unavailable, fmt.Errorf("glab api の出力を読めない: %w", err))
	}
	return s.normalize(n, map[int]bool{})
}

// normalize は応答の issue 1 件を作業対象の形に写す。collaborators は作者ごとの立場の読み出し済みの分。
func (s Store) normalize(n issueJSON, collaborators map[int]bool) (target.Issue, error) {
	collaborator, known := collaborators[n.Author.ID]
	if !known {
		var err error
		if collaborator, err = s.collaborator(n.Author.ID); err != nil {
			return target.Issue{}, err
		}
		collaborators[n.Author.ID] = collaborator
	}
	i := target.Issue{
		Number:               n.IID,
		Title:                n.Title,
		URL:                  n.WebURL,
		Closed:               n.State == "closed",
		CreatedAt:            n.CreatedAt,
		Labels:               n.Labels,
		AuthorIsCollaborator: collaborator,
	}
	for _, a := range n.Assignees {
		i.Assignees = append(i.Assignees, a.Username)
	}
	if n.Milestone != nil {
		i.Milestone = n.Milestone.Title
	}
	return i, nil
}

// developerAccess は collaborator と数える access level の下限 (Developer)。push できる層に揃える (formats.md §2.2)
const developerAccess = 30

// collaborator は user が project で Developer 以上の access level (group から継承したものを含む) を持つか。member で
// なければ false。
func (s Store) collaborator(userID int) (bool, error) {
	out, err := s.get(s.endpoint("members/all/" + strconv.Itoa(userID)))
	if gone(err) {
		return false, nil
	}
	if err != nil {
		return false, s.fail(classify(err), err)
	}
	var member struct {
		AccessLevel int `json:"access_level"`
	}
	if err := json.Unmarshal(out, &member); err != nil {
		return false, s.fail(target.Unavailable, fmt.Errorf("glab api の出力を読めない: %w", err))
	}
	return member.AccessLevel >= developerAccess, nil
}

func (s Store) fail(kind target.FailureKind, err error) *target.Failure {
	return &target.Failure{Kind: kind, Place: s.project.String(), Err: err}
}

// glab は HTTP の失敗をどれも exit 1 で返し、stderr の `(HTTP <status>)` で見分ける
const (
	authMarker      = "(HTTP 401)"
	notFoundMarker  = "(HTTP 404)"
	rateLimitMarker = "(HTTP 429)"
	// projectGone は project が見えないときの 404 の文言。issue や member が無いときの 404 (`404 Not found`) と分ける
	projectGone = "Project Not Found"
)

// gone は、project は見えていて、読んだもの (issue・member) が無いときの 404 か。
func gone(err error) bool {
	var failed *proc.Error
	return errors.As(err, &failed) && strings.Contains(failed.Stderr, notFoundMarker) && !strings.Contains(failed.Stderr, projectGone)
}

// classify は glab の失敗を分類する。
func classify(err error) target.FailureKind {
	var failed *proc.Error
	if !errors.As(err, &failed) {
		return target.Unavailable
	}
	switch {
	case strings.Contains(failed.Stderr, authMarker):
		return target.Auth
	case strings.Contains(failed.Stderr, notFoundMarker):
		return target.NotVisible
	case strings.Contains(failed.Stderr, rateLimitMarker):
		return target.RateLimit
	}
	return target.Unavailable
}
