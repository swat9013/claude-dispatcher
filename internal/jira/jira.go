// Package jira は acli (Atlassian CLI) を撃って Jira Cloud の issue 置き場を読む adapter (system.md §13、ADR 0010)。
// tracker には書かない。Jira に CL 置き場は無い。
package jira

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
)

// Runner は acli を 1 回撃ち、stdout を返す。失敗は *proc.Error。
type Runner interface {
	Run(args ...string) ([]byte, error)
}

// Exec は acli の実物を撃つ Runner。acli は撃つたびに Env の PATH から探す (github.Exec と同じく、loop が走っている間の
// 入れ替えに追従させる)。
type Exec struct {
	Env     []string
	Timeout time.Duration
}

func (a Exec) Run(args ...string) ([]byte, error) {
	path, err := deps.Lookup("acli", a.Env)
	if err != nil {
		return nil, err
	}
	out, err := proc.Command{Path: path, Env: a.Env, Timeout: a.Timeout}.Output(args...)
	return []byte(out), err
}

// Project は Jira Cloud の project (site と project key)。Key は大文字に揃えてある。
type Project struct {
	Site string
	Key  string
}

// String は issue 置き場の表示名 (`<site>/<project key>`)。
func (p Project) String() string { return p.Site + "/" + p.Key }

// IssueKey は番号の issue の key (`WIDGETS-123`)。
func (p Project) IssueKey(number int) string { return p.Key + "-" + strconv.Itoa(number) }

var (
	sitePattern = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	keyPattern  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
)

// ParseSite は Jira Cloud の site (`acme.atlassian.net`) の綴りを確かめる。
func ParseSite(text string) (string, error) {
	if !sitePattern.MatchString(text) {
		return "", errors.New("site の綴り (acme.atlassian.net) で書く")
	}
	return text, nil
}

// ParseKey は project key の綴りを確かめ、大文字に揃える。
func ParseKey(text string) (string, error) {
	if !keyPattern.MatchString(text) {
		return "", errors.New("project key の綴り (WIDGETS。[A-Za-z][A-Za-z0-9_]*) で書く")
	}
	return strings.ToUpper(text), nil
}

// ScopeKey は project の issue 置き場を指す scope key。site と project key は大文字と小文字を区別しないので小文字にする。
func ScopeKey(p Project) string { return strings.ToLower(p.String()) }

// Store は project の issue 置き場を読む部品。
type Store struct {
	acli    Runner
	project Project
	// blockers は open な一覧で依存先 (blocked) を読むか。どれかの trigger が blocked を書いているときだけ読む
	// (依存を持つ issue 1 件ごとに読み直しが要るので、要るときだけ撃つ)
	blockers bool
}

// NewStore は triggers を評価するための issue 置き場の部品を組み立てる。
func NewStore(acli Runner, project Project, triggers []trigger.Trigger) Store {
	blockers := slices.ContainsFunc(triggers, func(t trigger.Trigger) bool { return t.Issue.Blocked != nil })
	return Store{acli: acli, project: project, blockers: blockers}
}

func (s Store) ScopeKey() string { return ScopeKey(s.project) }

// Open は種類の open な作業対象を全件読む。Jira に CL は無い。失敗は *target.Failure。
func (s Store) Open(kind target.Kind) ([]target.Item, error) {
	if kind != target.KindIssue {
		return nil, fmt.Errorf("作業対象の種類 %q は Jira の issue 置き場に無い", kind)
	}
	issues, err := s.openIssues()
	if err != nil {
		return nil, s.fail(s.classify(err, target.Unavailable), err)
	}
	items := make([]target.Item, len(issues))
	for i, issue := range issues {
		items[i] = issue
	}
	return items, nil
}

// Read は作業対象 1 件を読み直す。消えた・別の project へ移った・見えなくなった issue は Closed として返す
// (formats.md §6)。失敗は *target.Failure。
func (s Store) Read(ref target.Ref) (target.Item, error) {
	if ref.Kind != target.KindIssue {
		return nil, fmt.Errorf("作業対象の種類 %q は Jira の issue 置き場に無い", ref.Kind)
	}
	n, err := s.view(s.project.IssueKey(ref.Number))
	if err == nil {
		number, ok := s.number(n.Key)
		if !ok {
			// 別の project へ移した (旧 key の転送で、移った先の issue が返った)
			return target.Issue{Number: ref.Number, Closed: true}, nil
		}
		return s.normalize(n, number, countBlockers(n.Fields.IssueLinks)), nil
	}
	if kind := s.classify(err, target.Unavailable); kind != target.Unavailable {
		return nil, s.fail(kind, err)
	}
	// 認証は通っている。消えた issue と見えなくなった issue は view では見分けられないので、open な一覧に無ければ終端とする。
	// 依存先は要らないので、一覧の検索だけを撃つ
	open, listErr := s.search(s.openJQL())
	if listErr != nil {
		return nil, s.fail(s.classify(listErr, target.Unavailable), listErr)
	}
	key := s.project.IssueKey(ref.Number)
	if slices.ContainsFunc(open, func(n issueJSON) bool { return strings.EqualFold(n.Key, key) }) {
		return nil, s.fail(target.Unavailable, err)
	}
	return target.Issue{Number: ref.Number, Closed: true}, nil
}

// fields は acli の一覧が受け付ける field のうち読むもの。key は field でなく常に返る (key だけを渡すと null の列が返る)
const fields = "summary,issuetype,assignee,status,labels"

// projectJQL は project を指す JQL の条件。key は JQL の予約語 (SET など) と重なりうるので、文字列として囲む
func (s Store) projectJQL() string { return "project = " + quote(s.project.Key) }

// openJQL は project の open な issue の JQL (formats.md §2.2)。
func (s Store) openJQL() string {
	return s.projectJQL() + " AND statusCategory != Done"
}

// openIssues は project の open な issue を全件読み、正規化して返す。失敗は分類前の error。
func (s Store) openIssues() ([]target.Issue, error) {
	nodes, err := s.search(s.openJQL())
	if err != nil {
		return nil, err
	}
	blocked := map[string]int{}
	if s.blockers {
		if blocked, err = s.openBlockers(); err != nil {
			return nil, err
		}
	}
	issues := make([]target.Issue, 0, len(nodes))
	for _, n := range nodes {
		number, ok := s.number(n.Key)
		if !ok {
			return nil, fmt.Errorf("open な一覧に project %s でない key %q がある (project key を改名したなら tracker.repo を書き換えて loop を起動し直す)", s.project.Key, n.Key)
		}
		issues = append(issues, s.normalize(n, number, blocked[n.Key]))
	}
	return issues, nil
}

// openBlockers は、依存を持つ open な issue を検索で絞り、1 件ずつ読み直して未解決の依存先の数を返す (key ごと)。
func (s Store) openBlockers() (map[string]int, error) {
	nodes, err := s.search(s.openJQL() + ` AND issueLinkType = "is blocked by"`)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, n := range nodes {
		viewed, err := s.view(n.Key)
		if err != nil {
			return nil, err
		}
		counts[n.Key] = countBlockers(viewed.Fields.IssueLinks)
	}
	return counts, nil
}

// search は JQL の検索を全件読む (`--paginate`。`--limit` を省くと 30 件で切れる)。
func (s Store) search(jql string) ([]issueJSON, error) {
	out, err := s.acli.Run("jira", "workitem", "search", "--jql", jql, "--fields", fields, "--json", "--paginate")
	if err != nil {
		return nil, err
	}
	return pages(out)
}

// view は issue 1 件を、依存先の link を含めて読む。
func (s Store) view(key string) (issueJSON, error) {
	out, err := s.acli.Run("jira", "workitem", "view", key, "--fields", fields+",issuelinks", "--json")
	if err != nil {
		return issueJSON{}, err
	}
	var n issueJSON
	if err := json.Unmarshal(out, &n); err != nil {
		return issueJSON{}, fmt.Errorf("acli の出力を読めない: %w", err)
	}
	return n, nil
}

// pages は `--paginate` の出力 (全 page を 1 つにした配列) を読む。
func pages(out []byte) ([]issueJSON, error) {
	var list []*issueJSON
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("acli の出力を読めない: %w", err)
	}
	all := make([]issueJSON, 0, len(list))
	for _, n := range list {
		// field に key だけを渡すと null の列が返る。読めない要素を黙って捨てない
		if n == nil || n.Key == "" {
			return nil, errors.New("acli の出力に key の無い issue がある")
		}
		all = append(all, *n)
	}
	return all, nil
}

// number は key の番号。key が project のものでなければ false。
func (s Store) number(key string) (int, bool) {
	prefix, digits, ok := strings.Cut(key, "-")
	if !ok || !strings.EqualFold(prefix, s.project.Key) {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || strconv.Itoa(n) != digits {
		return 0, false
	}
	return n, true
}

// statusJSON は status (と、その区分) の応答。
type statusJSON struct {
	Name     string `json:"name"`
	Category struct {
		Key string `json:"key"`
	} `json:"statusCategory"`
}

// done は status の区分が done (終端) か。
func (s statusJSON) done() bool { return s.Category.Key == "done" }

// issueJSON は acli の issue の応答のうち読むもの。
type issueJSON struct {
	Key    string `json:"key"`
	Fields struct {
		Summary   string `json:"summary"`
		IssueType struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		Assignee *struct {
			AccountID string `json:"accountId"`
		} `json:"assignee"`
		Status     statusJSON  `json:"status"`
		Labels     []string    `json:"labels"`
		IssueLinks []issueLink `json:"issuelinks"`
	} `json:"fields"`
}

// issueLink は issue の link 1 本。inwardIssue があれば、この issue が相手に対して type の inward の関係にある
// (`Blocks` なら「この issue は相手に blocked されている」)。
type issueLink struct {
	Type struct {
		Name string `json:"name"`
	} `json:"type"`
	InwardIssue *struct {
		Fields struct {
			Status statusJSON `json:"status"`
		} `json:"fields"`
	} `json:"inwardIssue"`
}

// blocksLinkType は依存 (blocks / is blocked by) の link type の名前
const blocksLinkType = "Blocks"

// countBlockers は、is blocked by の link のうち相手の status の区分が done でないものの数。
func countBlockers(links []issueLink) int {
	n := 0
	for _, l := range links {
		if l.Type.Name == blocksLinkType && l.InwardIssue != nil && !l.InwardIssue.Fields.Status.done() {
			n++
		}
	}
	return n
}

// normalize は応答の issue 1 件を作業対象の形に写す。Jira の一覧は作成日時を返さないので CreatedAt はゼロ値のまま
// (候補は番号の小さい順に並ぶ。formats.md §2.4)。
func (s Store) normalize(n issueJSON, number, openBlockers int) target.Issue {
	i := target.Issue{
		Number:       number,
		Key:          n.Key,
		Title:        n.Fields.Summary,
		URL:          "https://" + s.project.Site + "/browse/" + n.Key,
		Closed:       n.Fields.Status.done(),
		Labels:       n.Fields.Labels,
		Status:       n.Fields.Status.Name,
		Type:         n.Fields.IssueType.Name,
		OpenBlockers: openBlockers,
	}
	if n.Fields.Assignee != nil {
		i.Assignees = []string{n.Fields.Assignee.AccountID}
	}
	return i
}

func (s Store) fail(kind target.FailureKind, err error) *target.Failure {
	return &target.Failure{Kind: kind, Place: s.project.String(), Err: err}
}

// classify は acli の失敗を分類する。acli はどの失敗も exit 1 で返し、文言は訳されうるので、文言では分けない。
// 認証の状態を問い合わせ、落ちれば認証の失敗、通れば otherwise。acli を起動できなかった失敗は問い合わせない。
func (s Store) classify(err error, otherwise target.FailureKind) target.FailureKind {
	var failed *proc.Error
	if !errors.As(err, &failed) {
		return target.Unavailable
	}
	if _, authErr := s.authStatus(); authErr != nil {
		return target.Auth
	}
	return otherwise
}

// authStatus は acli の認証の状態を問い合わせる (server に問い合わせる)。
func (s Store) authStatus() ([]byte, error) {
	return s.acli.Run("jira", "auth", "status")
}

// siteLine は `acli jira auth status` の出力の site の行
var siteLine = regexp.MustCompile(`(?m)^\s*Site:\s*(\S+)\s*$`)

// Name は trigger が書いた status 名か issue type 名 1 つ。
type Name struct {
	Trigger string
	Value   string
}

// Names は起動時の確認で実在を確かめる名前。
type Names struct {
	Statuses []Name
	Types    []Name
}

// NamesOf は triggers の status と type の述語に書かれた名前を、trigger ごとに重ねずに集める (大文字と小文字を区別しない)。
func NamesOf(triggers []trigger.Trigger) Names {
	var names Names
	for _, t := range triggers {
		names.Statuses = appendNames(names.Statuses, t.Name, t.Issue.StatusAny, t.Issue.StatusNone)
		names.Types = appendNames(names.Types, t.Name, t.Issue.TypeAny, t.Issue.TypeNone)
	}
	return names
}

func appendNames(names []Name, triggerName string, lists ...[]string) []Name {
	for _, list := range lists {
		for _, value := range list {
			if !slices.ContainsFunc(names, func(n Name) bool { return n.Trigger == triggerName && strings.EqualFold(n.Value, value) }) {
				names = append(names, Name{Trigger: triggerName, Value: value})
			}
		}
	}
	return names
}

// Unknown は、trigger が書いた status 名か issue type 名が実在しないこと。1 件 1 行の Problems で名指しする。
type Unknown struct {
	Problems []string
}

func (u *Unknown) Error() string { return strings.Join(u.Problems, "\n") }

// Confirm は起動時の issue 置き場の確認 (formats.md §6): acli の認証の site・open な一覧・status 名・issue type 名の順に
// 確かめ、最初に落ちたもので返す。失敗は *target.Failure (認証・見えない・その他) か *Unknown。
func (s Store) Confirm(names Names) error {
	out, err := s.authStatus()
	if err != nil {
		var failed *proc.Error
		if errors.As(err, &failed) {
			return s.fail(target.Auth, err)
		}
		return s.fail(target.Unavailable, err)
	}
	m := siteLine.FindSubmatch(out)
	if m == nil {
		return s.fail(target.NotVisible, errors.New("acli jira auth status の出力から認証の site を読めない"))
	}
	if site := string(m[1]); !strings.EqualFold(site, s.project.Site) {
		return s.fail(target.NotVisible, fmt.Errorf("acli の認証の site %s が tracker.host %s と違う (acli jira auth switch --site %s で切り替える)", site, s.project.Site, s.project.Site))
	}
	if _, err := s.search(s.openJQL()); err != nil {
		return s.fail(s.classify(err, target.NotVisible), err)
	}
	var problems []string
	// 同じ名前 (大文字と小文字を区別しない) は 1 度だけ問い合わせ、書いた trigger ごとに名指しする
	statusErrors := map[string]error{}
	for _, n := range names.Statuses {
		name := strings.ToLower(n.Value)
		err, asked := statusErrors[name]
		if !asked {
			_, err = s.acli.Run("jira", "workitem", "search", "--jql", s.projectJQL()+" AND status = "+quote(n.Value), "--count")
			if err != nil && s.classify(err, target.Unavailable) == target.Auth {
				return s.fail(target.Auth, err)
			}
			statusErrors[name] = err
		}
		// issue 置き場の確認が先に通り、認証も通っているので、ここでの失敗は status 名の誤りと読む。acli の理由も添える
		if err != nil {
			problems = append(problems, fmt.Sprintf("trigger %s: status %q が site %s に無い (%v)", n.Trigger, n.Value, s.project.Site, err))
		}
	}
	if len(names.Types) > 0 {
		types, err := s.issueTypes()
		if err != nil {
			return s.fail(s.classify(err, target.NotVisible), err)
		}
		for _, n := range names.Types {
			if !slices.ContainsFunc(types, func(t string) bool { return strings.EqualFold(t, n.Value) }) {
				problems = append(problems, fmt.Sprintf("trigger %s: issue type %q が project %s に無い (%s)", n.Trigger, n.Value, s.project.Key, strings.Join(types, " / ")))
			}
		}
	}
	if len(problems) > 0 {
		return &Unknown{Problems: problems}
	}
	return nil
}

// issueTypes は project の issue type の名前を読む。
func (s Store) issueTypes() ([]string, error) {
	out, err := s.acli.Run("jira", "project", "view", "--key", s.project.Key, "--json")
	if err != nil {
		return nil, err
	}
	var project struct {
		IssueTypes []struct {
			Name string `json:"name"`
		} `json:"issueTypes"`
	}
	if err := json.Unmarshal(out, &project); err != nil {
		return nil, fmt.Errorf("acli の出力を読めない: %w", err)
	}
	names := make([]string, len(project.IssueTypes))
	for i, t := range project.IssueTypes {
		names[i] = t.Name
	}
	return names, nil
}

// quote は JQL の文字列の値を `"` で囲む。`"` と `\` は `\` で escape する。
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
