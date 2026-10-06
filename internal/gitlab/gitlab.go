// Package gitlab は glab CLI を撃って GitLab の issue 置き場と CL 置き場 (merge request) を読む adapter (system.md §13)。
// tracker には書かない。
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

// CurrentProject は cwd の clone の project を glab repo view で引く。host は応答の web_url の host (port を含む) で決める。
// remote の URL は渡さない: URL の host は ssh の host (alias・altssh・ssh 専用の host) でありえ、渡すと glab がその host の
// API を撃つ。URL は認証情報を含みうるので、argv に載せないためでもある。
func CurrentProject(glab Runner) (Project, error) {
	out, err := glab.Run("repo", "view", "--output", "json")
	if err != nil {
		return Project{}, fmt.Errorf("glab repo view が失敗した: %w", replaceForbiddenStderr(err))
	}
	var view struct {
		PathWithNamespace string `json:"path_with_namespace"`
		WebURL            string `json:"web_url"`
	}
	if err := json.Unmarshal(out, &view); err != nil {
		return Project{}, fmt.Errorf("glab repo view の出力を読めない: %w", err)
	}
	web, err := url.Parse(view.WebURL)
	if err != nil {
		return Project{}, fmt.Errorf("glab repo view の web_url %q を読めない: %w", view.WebURL, err)
	}
	host, err := ParseHost(strings.ToLower(web.Host))
	if err != nil {
		return Project{}, fmt.Errorf("glab repo view の web_url %q: %w", view.WebURL, err)
	}
	path, err := ParsePath(view.PathWithNamespace)
	if err != nil {
		return Project{}, fmt.Errorf("glab repo view の path_with_namespace %q: %w", view.PathWithNamespace, err)
	}
	return Project{Host: host, Path: path}, nil
}

// ScopeKey は project の issue 置き場を指す scope key。GitLab の path は大文字と小文字を区別しないので小文字にする。
func ScopeKey(p Project) string { return strings.ToLower(p.String()) }

// Store は project の issue 置き場と CL 置き場 (merge request) を読む部品。
type Store struct {
	glab    Runner
	project Project
}

func NewStore(glab Runner, project Project) Store { return Store{glab: glab, project: project} }

func (s Store) ScopeKey() string { return ScopeKey(s.project) }

// Open は種類の open な作業対象を全件読む。失敗は *target.Failure。
func (s Store) Open(kind target.Kind) ([]target.Item, error) {
	switch kind {
	case target.KindIssue:
		return items(s.openIssues())
	case target.KindCL:
		return items(s.openMergeRequests())
	}
	return nil, fmt.Errorf("未知の作業対象の種類 %q", kind)
}

// Read は作業対象 1 件を読み直す。失敗は *target.Failure。
func (s Store) Read(ref target.Ref) (target.Item, error) {
	switch ref.Kind {
	case target.KindIssue:
		return s.issue(ref.Number)
	case target.KindCL:
		cl, _, err := s.mergeRequest(ref.Number, collaborators{})
		return cl, err
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

// pages は `glab api --paginate` の出力を要素の列として読む。glab は page ごとの配列を連ねて出すか、1 つの配列に結合して
// 出す (版による) ので、どちらも配列の列として読む。
func pages[T any](out []byte) ([]T, error) {
	var all []T
	decoder := json.NewDecoder(bytes.NewReader(out))
	for {
		var page []T
		err := decoder.Decode(&page)
		if errors.Is(err, io.EOF) {
			return all, nil
		}
		if err != nil {
			return nil, fmt.Errorf("glab api の出力を読めない: %w", err)
		}
		all = append(all, page...)
	}
}

// object は `glab api` の出力を JSON の値 1 つとして読む。
func object[T any](out []byte) (T, error) {
	var v T
	if err := json.Unmarshal(out, &v); err != nil {
		return v, fmt.Errorf("glab api の出力を読めない: %w", err)
	}
	return v, nil
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
	// issue_type で issue だけに絞る (task・incident・test case も同じ一覧に出るが、作業対象の issue ではない)
	out, err := s.get(s.endpoint("issues?state=opened&issue_type=issue&order_by=created_at&sort=asc&per_page=100"), "--paginate")
	if err != nil {
		return nil, s.fail(classify(err), err)
	}
	nodes, err := pages[issueJSON](out)
	if err != nil {
		return nil, s.fail(target.Unavailable, err)
	}
	known := collaborators{}
	issues := []target.Issue{}
	for _, n := range nodes {
		i, err := s.normalize(n, known)
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
	n, err := object[issueJSON](out)
	if err != nil {
		return target.Issue{}, s.fail(target.Unavailable, err)
	}
	return s.normalize(n, collaborators{})
}

// normalize は応答の issue 1 件を作業対象の形に写す。
func (s Store) normalize(n issueJSON, known collaborators) (target.Issue, error) {
	collaborator, err := s.isCollaborator(n.Author.ID, known)
	if err != nil {
		return target.Issue{}, err
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

// collaborators は user ごとの collaborator かの読み出し済みの分。1 回の観測 (一覧か 1 件の読み直し) の中で、同じ user を
// 読み直さないために使い回す
type collaborators map[int]bool

// isCollaborator は user が collaborator か。known に無ければ読んで known に足す。
func (s Store) isCollaborator(userID int, known collaborators) (bool, error) {
	if collaborator, ok := known[userID]; ok {
		return collaborator, nil
	}
	collaborator, err := s.collaborator(userID)
	if err != nil {
		return false, err
	}
	known[userID] = collaborator
	return collaborator, nil
}

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
	member, err := object[struct {
		AccessLevel int `json:"access_level"`
	}](out)
	if err != nil {
		return false, s.fail(target.Unavailable, err)
	}
	return member.AccessLevel >= developerAccess, nil
}

func (s Store) fail(kind target.FailureKind, err error) *target.Failure {
	return &target.Failure{Kind: kind, Place: s.project.String(), Err: replaceForbiddenStderr(err)}
}

// glab は HTTP の失敗をどれも exit 1 で返し、stderr の `(HTTP <status>)` で見分ける
const (
	authMarker      = "(HTTP 401)"
	notFoundMarker  = "(HTTP 404)"
	rateLimitMarker = "(HTTP 429)"
	// projectGone は project が見えないときの 404 の文言。issue・merge request・member が無いときの 404 (`404 Not found`)
	// と分ける
	projectGone = "Project Not Found"
)

// gone は、project は見えていて、読んだもの (issue・merge request・member) が無いときの 404 か。
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

// forbiddenPattern は glab が stderr の status の位置に出す HTTP 403。glab api は本文が JSON なら `glab: <message> (HTTP 403)`
// の行末に、HTML なら `glab: HTTP 403` の行頭に出す。glab repo view は `<METHOD> <URL>: 403 <本文>` の行頭に出す (本文の無い
// 403 なら 403 の後は改行)。本文の中の 403 には当てない
var forbiddenPattern = regexp.MustCompile(`(?m)\(HTTP 403\)\s*$|^glab: HTTP 403\b|^\s*[A-Za-z]+ https?://\S+: 403\b`)

// forbiddenReason は HTTP 403 の失敗の理由。403 は手前の proxy の拒否 (接続元のネットワーク) でも GitLab の拒否 (token の
// scope の不足など) でも起き、stderr の本文は proxy の HTML でありうるので、本文の代わりにこれを出す
const forbiddenReason = "HTTP 403 で拒否された (接続元のネットワークか、token の権限)"

// replaceForbiddenStderr は glab の失敗が HTTP 403 なら、stderr を forbiddenReason に差し替えた *proc.Error を返す
// (撃ったコマンドと exit code は残す)。それ以外は err のまま返す。差し替えた後の stderr も 401・404・429 の marker を
// 含まないので、classify と gone は差し替えの前後で同じ答えを返す。
func replaceForbiddenStderr(err error) error {
	var failed *proc.Error
	if !errors.As(err, &failed) || !forbiddenPattern.MatchString(failed.Stderr) {
		return err
	}
	shortened := *failed
	shortened.Stderr = forbiddenReason
	return &shortened
}
