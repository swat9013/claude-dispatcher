// Package workflow は workflow 定義 (`WORKFLOW.md`) を読んで検査する (system.md §8、formats.md §2)。
// 誤りは項目の位置を名指しし、すべてを集めて返す。
package workflow

import (
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/gitlab"
	"github.com/swat9013/claude-dispatcher/internal/jira"
	"github.com/swat9013/claude-dispatcher/internal/render"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
)

// Definition は検査に通った workflow 定義。
type Definition struct {
	// Dir は workflow 定義の file がある dir の絶対 path
	Dir     string
	Tracker Tracker
	// Interval は tick の周期
	Interval time.Duration
	// WorkspaceRoot は workspace を置く dir の絶対 path
	WorkspaceRoot string
	Hooks         Hooks
	// MaxConcurrent は同時に走らせる worker の上限
	MaxConcurrent int
	// MaxAttempts は作業対象 1 件の attempt の上限、MaxRetryBackoff は backoff の上限
	MaxAttempts     int
	MaxRetryBackoff time.Duration
	// StallTimeout は worker の出力が途絶えてから止めるまで、RunTimeout は worker 1 回分の上限時間。0 なら見ない
	StallTimeout time.Duration
	RunTimeout   time.Duration
	Claude       Claude
	Triggers     []trigger.Trigger
	// Prompt は本文 (共通 prompt) の template
	Prompt string
}

// Hooks は workspace の hooks の shell script (空なら撃たない) と、1 つの hook の上限時間。
type Hooks struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	Timeout      time.Duration
}

// Claude は worker の起動の仕方。
type Claude struct {
	Command string
	Args    []string
}

// Kinds は trigger に現れる作業対象の種類を、trigger の宣言順に重ねずに返す。
func (d Definition) Kinds() []target.Kind {
	var kinds []target.Kind
	for _, t := range d.Triggers {
		if t.On != "" && !slices.Contains(kinds, t.On) {
			kinds = append(kinds, t.On)
		}
	}
	return kinds
}

// TrackerKind は tracker の種類 (formats.md §2.1 の tracker.kind)。
type TrackerKind string

const (
	GitHub TrackerKind = "github"
	GitLab TrackerKind = "gitlab"
	Jira   TrackerKind = "jira"
)

// Tracker は issue 置き場の設定。置き場は種類の field (github は Repo、gitlab は Project、jira は JiraProject) にだけ入る。
type Tracker struct {
	Kind TrackerKind
	Repo github.Repo
	// Project は gitlab の issue 置き場 (host と path)
	Project gitlab.Project
	// JiraProject は jira の issue 置き場 (site と project key)
	JiraProject jira.Project
	// Token は gh に GH_TOKEN として渡す token。書かれていなければ ""。gitlab では書けない
	Token string
}

// Reference は作業対象を人が読む行に出す参照 (formats.md §5)。GitLab の merge request は `!<番号>`、Jira の issue は key、
// それ以外は `#<番号>`。GitLab は issue と merge request に別々に番号を振るので、同じ番号の issue と取り違えないよう綴りを分ける。
func (t Tracker) Reference(ref target.Ref) string {
	switch {
	case t.Kind == GitLab && ref.Kind == target.KindCL:
		return "!" + strconv.Itoa(ref.Number)
	case t.Kind == Jira && ref.Kind == target.KindIssue:
		return t.JiraProject.IssueKey(ref.Number)
	}
	return "#" + strconv.Itoa(ref.Number)
}

// Place は issue 置き場の表示名 (github は `owner/name`、gitlab は `<host>/<path>`、jira は `<site>/<project key>`)。
func (t Tracker) Place() string {
	switch t.Kind {
	case GitLab:
		return t.Project.String()
	case Jira:
		return t.JiraProject.String()
	}
	return t.Repo.String()
}

// 周期の既定と範囲 (formats.md §2.1)
const (
	DefaultInterval      = 5 * time.Minute
	MinInterval          = time.Second
	MaxInterval          = 24 * time.Hour
	DefaultWorkspaceRoot = ".claude-dispatcher/workspaces"
	DefaultHookTimeout   = 60 * time.Second
	DefaultClaudeCommand = "claude"
	DefaultMaxAttempts   = 3
	DefaultRetryBackoff  = 5 * time.Minute
	DefaultStallTimeout  = 15 * time.Minute
	DefaultRunTimeout    = time.Hour
)

// Errors は workflow 定義の誤りの列。1 件 1 行で出す。
type Errors struct {
	Path     string
	Problems []string
}

func (e *Errors) Error() string {
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		lines[i] = e.Path + ": " + p
	}
	return strings.Join(lines, "\n")
}

// Load は path の workflow 定義を読んで検査する。`$VAR` は getenv で引く。誤りは *Errors。
func Load(path string, getenv func(string) string) (Definition, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, &Errors{Path: path, Problems: []string{fmt.Sprintf("workflow 定義を読めない: %v", err)}}
	}
	front, body, err := split(raw)
	if err != nil {
		return Definition{}, &Errors{Path: path, Problems: []string{err.Error()}}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(front, &doc); err != nil {
		return Definition{}, &Errors{Path: path, Problems: []string{fmt.Sprintf("front matter を YAML として読めない: %v", err)}}
	}
	c := &checker{getenv: getenv}
	dir := filepath.Dir(path)
	def := Definition{
		Dir: dir, Interval: DefaultInterval, WorkspaceRoot: filepath.Join(dir, DefaultWorkspaceRoot),
		Hooks: Hooks{Timeout: DefaultHookTimeout}, MaxConcurrent: 1, MaxAttempts: DefaultMaxAttempts,
		MaxRetryBackoff: DefaultRetryBackoff, StallTimeout: DefaultStallTimeout, RunTimeout: DefaultRunTimeout, Claude: Claude{Command: DefaultClaudeCommand}, Prompt: body,
	}
	root := &yaml.Node{Kind: yaml.MappingNode}
	if len(doc.Content) == 1 {
		root = resolve(doc.Content[0])
	}
	c.decode(root, &def)
	// 本文はどの worker にも渡るので、trigger に現れる種類すべての見本で描画してみる。種類が 1 つも読めなければ issue の見本で
	// 描画し、本文の綴りの誤りだけは確かめる
	kinds := def.Kinds()
	if len(kinds) == 0 {
		kinds = []target.Kind{target.KindIssue}
	}
	for _, kind := range kinds {
		if err := render.Check("本文", body, c.sample(kind)); err != nil {
			c.problems = append(c.problems, problem{text: fmt.Sprintf("本文 (共通 prompt): %v", err)})
			break
		}
	}
	if len(c.problems) > 0 {
		return Definition{}, &Errors{Path: path, Problems: c.lines()}
	}
	return def, nil
}

// split は file を front matter と本文に分ける。1 行目が `---` の行で、次の `---` の行までが front matter。その後ろが本文。
// front matter は 1 行目の `---` を空行に置き換えた形で返す。YAML の行番号が、そのまま file の行番号になる。
func split(raw []byte) (front []byte, body string, err error) {
	const fence = "---"
	lines := strings.SplitAfter(string(raw), "\n")
	if strings.TrimRight(lines[0], "\r\n") != fence {
		return nil, "", errors.New("front matter が無い (1 行目を --- にする)")
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r\n") == fence {
			return []byte("\n" + strings.Join(lines[1:i], "")), strings.Join(lines[i+1:], ""), nil
		}
	}
	return nil, "", errors.New("front matter が閉じていない (--- の行で閉じる)")
}

// checker は front matter の node を辿って Definition に写し、誤りを集める。
type checker struct {
	getenv   func(string) string
	problems []problem
	// trackerKind は tracker の種類。種類に依る検査が key の順 (triggers を tracker より前に書くなど) に依らないよう、
	// front matter を辿る前に読む。読めなければ "" で、種類に依る検査は撃たない (誤りは tracker.kind で名指しする)
	trackerKind TrackerKind
}

// peekTrackerKind は front matter の tracker.kind を、他の項目を読む前に読む。既知の種類でなければ ""。
func peekTrackerKind(root *yaml.Node) TrackerKind {
	value := func(n *yaml.Node, key string) *yaml.Node {
		if n == nil || n.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				return resolve(n.Content[i+1])
			}
		}
		return nil
	}
	kind := value(value(root, "tracker"), "kind")
	if kind == nil || kind.Kind != yaml.ScalarNode {
		return ""
	}
	switch k := TrackerKind(kind.Value); k {
	case GitHub, GitLab, Jira:
		return k
	}
	return ""
}

// problem は誤り 1 件。line は file の行番号 (分からなければ 0)
type problem struct {
	line int
	text string
}

func (c *checker) fail(n *yaml.Node, path, format string, a ...any) {
	p := problem{line: n.Line, text: path + ": " + fmt.Sprintf(format, a...)}
	if n.Line > 0 {
		p.text = fmt.Sprintf("%s (%d 行目): %s", path, n.Line, fmt.Sprintf(format, a...))
	}
	c.problems = append(c.problems, p)
}

// lines は誤りを file の中の順に並べて返す (必須の欠落は対応表を読み終えてから足すので、そのままでは順が崩れる)。
func (c *checker) lines() []string {
	sort.SliceStable(c.problems, func(i, j int) bool { return c.problems[i].line < c.problems[j].line })
	lines := make([]string, len(c.problems))
	for i, p := range c.problems {
		lines[i] = p.text
	}
	return lines
}

// field は対応表の 1 項目の読み方。required なら欠落を誤りにする。read は項目の key と、alias を辿った値を受ける。
type field struct {
	required bool
	read     func(key, value *yaml.Node, path string)
}

// resolve は alias を辿って、anchor を付けた node を返す。
func resolve(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == "!!null" }

// mapping は n を対応表として読み、fields にある key を読む。未知の key・同じ key の 2 回目・必須の欠落を誤りにする。
// 値を書いていない key (null) は、空の対応表として読む。必須の欠落は、この対応表を持つ key (owner) の行で名指しする。
func (c *checker) mapping(owner, n *yaml.Node, path string, fields map[string]field) {
	if isNull(n) {
		n = &yaml.Node{Kind: yaml.MappingNode}
	}
	if n.Kind != yaml.MappingNode {
		c.fail(n, path, "対応表 (key: value) で書く")
		return
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		child := join(path, key.Value)
		f, known := fields[key.Value]
		switch {
		case !known:
			c.fail(key, child, "未知の key")
		case seen[key.Value]:
			c.fail(key, child, "同じ key を 2 回書いている")
		default:
			seen[key.Value] = true
			f.read(key, resolve(value), child)
		}
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if fields[name].required && !seen[name] {
			c.fail(owner, join(path, name), "必須の項目が無い")
		}
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func (c *checker) decode(root *yaml.Node, def *Definition) {
	c.trackerKind = peekTrackerKind(root)
	// top level は key を持たないので、欠落を行なしで名指しする
	c.mapping(&yaml.Node{}, root, "", map[string]field{
		"tracker": {required: true, read: func(key, n *yaml.Node, path string) { c.tracker(key, n, path, &def.Tracker) }},
		"polling": {read: func(key, n *yaml.Node, path string) { c.polling(key, n, path, &def.Interval) }},
		"workspace": {read: func(key, n *yaml.Node, path string) {
			c.mapping(key, n, path, map[string]field{
				"root": {read: func(_, n *yaml.Node, path string) {
					if s, ok := c.variableOrLiteral(n, path); ok {
						def.WorkspaceRoot = c.absolute(n, path, def.Dir, s)
					}
				}},
			})
		}},
		"hooks": {read: func(key, n *yaml.Node, path string) { c.hooks(key, n, path, &def.Hooks) }},
		"limits": {read: func(key, n *yaml.Node, path string) {
			c.mapping(key, n, path, map[string]field{
				"max_concurrent": {read: func(_, n *yaml.Node, path string) {
					if v, ok := c.positive(n, path); ok {
						def.MaxConcurrent = v
					}
				}},
				"max_attempts": {read: func(_, n *yaml.Node, path string) {
					if v, ok := c.positive(n, path); ok {
						def.MaxAttempts = v
					}
				}},
				"max_retry_backoff": {read: func(_, n *yaml.Node, path string) {
					if d, ok := c.duration(n, path); ok {
						if d <= 0 {
							c.fail(n, path, "0 より長くする")
							return
						}
						def.MaxRetryBackoff = d
					}
				}},
				"stall_timeout": {read: func(_, n *yaml.Node, path string) { c.timeout(n, path, &def.StallTimeout) }},
				"run_timeout":   {read: func(_, n *yaml.Node, path string) { c.timeout(n, path, &def.RunTimeout) }},
			})
		}},
		"claude": {read: func(key, n *yaml.Node, path string) {
			c.mapping(key, n, path, map[string]field{
				"command": {read: func(_, n *yaml.Node, path string) {
					if s, ok := c.str(n, path); ok {
						def.Claude.Command = s
					}
				}},
				"args": {read: func(_, n *yaml.Node, path string) { def.Claude.Args, _ = c.strList(n, path) }},
			})
		}},
		"triggers": {required: true, read: func(_, n *yaml.Node, path string) { def.Triggers = c.triggers(n, path) }},
	})
}

// unsupported は、宣言 n を支えない tracker の種類とその理由の表 why に今の種類があれば、n を理由を添えて名指しで失敗させる。
// 種類が読めていなければ何もしない (誤りは tracker.kind で名指しする)。
func (c *checker) unsupported(n *yaml.Node, path string, why map[TrackerKind]string) {
	if reason, ok := why[c.trackerKind]; ok {
		c.fail(n, path, "tracker.kind: %s では書けない (%s)", c.trackerKind, reason)
	}
}

// 宣言ごとの、支えない tracker の種類と理由の表 (formats.md §2.8)
var (
	unsupportedToken = map[TrackerKind]string{
		GitLab: "glab は glab auth login の認証を使う",
		Jira:   "acli は acli jira auth login の認証を使う",
	}
	unsupportedCL        = map[TrackerKind]string{Jira: "Jira に CL は無い。issue 置き場と別の CL 置き場の宣言は #114"}
	unsupportedAuthor    = map[TrackerKind]string{Jira: "worker を誰の書き込みで起動してよいかの線を、Jira の権限に引けない"}
	unsupportedMilestone = map[TrackerKind]string{Jira: "sprint は無いことがあり、fixVersions は acli の一覧で読めない"}
	unsupportedBlocked   = map[TrackerKind]string{GitLab: "GitLab の CE は issue の依存を API で返さないので、blocked: false が全件に当たる"}
	// status と issue type は Jira の issue にだけある
	unsupportedStatusAndType = map[TrackerKind]string{
		GitHub: "status と issue type は Jira の issue にだけある",
		GitLab: "status と issue type は Jira の issue にだけある",
	}
)

// sample は kind の作業対象の、描画を確かめる見本。jira の issue には key を足す (`.issue.key` は jira でだけ使える)。
func (c *checker) sample(kind target.Kind) target.Item {
	item := render.Sample(kind)
	if issue, ok := item.(target.Issue); ok && c.trackerKind == Jira {
		issue.Key = "KEY-1"
		return issue
	}
	return item
}

// absolute は s を絶対 path にする。`~/` は HOME から、相対 path は dir から。HOME が空なら `~/` を読めない誤りにする。
func (c *checker) absolute(n *yaml.Node, path, dir, s string) string {
	switch {
	case strings.HasPrefix(s, "~/"):
		home := c.getenv("HOME")
		if !filepath.IsAbs(home) {
			c.fail(n, path, "HOME が絶対 path でないので ~/ を読めない")
			return ""
		}
		return filepath.Join(home, s[2:])
	case filepath.IsAbs(s):
		return filepath.Clean(s)
	}
	return filepath.Join(dir, s)
}

func (c *checker) hooks(owner, n *yaml.Node, path string, h *Hooks) {
	script := func(target *string) field {
		return field{read: func(_, n *yaml.Node, path string) { *target, _ = c.str(n, path) }}
	}
	c.mapping(owner, n, path, map[string]field{
		"after_create":  script(&h.AfterCreate),
		"before_run":    script(&h.BeforeRun),
		"after_run":     script(&h.AfterRun),
		"before_remove": script(&h.BeforeRemove),
		"timeout": {read: func(_, n *yaml.Node, path string) {
			if d, ok := c.duration(n, path); ok {
				if d <= 0 {
					c.fail(n, path, "0 より長くする")
					return
				}
				h.Timeout = d
			}
		}},
	})
}

// duration は Go の duration の綴りの文字列を読む。
func (c *checker) duration(n *yaml.Node, path string) (time.Duration, bool) {
	s, ok := c.str(n, path)
	if !ok {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		c.fail(n, path, "%q は Go の duration の綴り (90s / 5m / 1h30m) で書く", s)
		return 0, false
	}
	return d, true
}

// timeout は 0 (見ない) 以上の duration を読む。
func (c *checker) timeout(n *yaml.Node, path string, target *time.Duration) {
	if d, ok := c.duration(n, path); ok {
		if d < 0 {
			c.fail(n, path, "負にできない (0s で無効)")
			return
		}
		*target = d
	}
}

// positive は 1 以上の整数を読む。
func (c *checker) positive(n *yaml.Node, path string) (int, bool) {
	var v int
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" || n.Decode(&v) != nil || v < 1 {
		c.fail(n, path, "1 以上の整数で書く")
		return 0, false
	}
	return v, true
}

// tracker は tracker の項目を、先に読んだ種類 (c.trackerKind) の読み方で読む。種類が読めなければ repo と token は読まない
// (誤りは tracker.kind で名指しする)。
func (c *checker) tracker(owner, n *yaml.Node, path string, t *Tracker) {
	t.Kind = c.trackerKind
	if c.trackerKind == GitLab {
		t.Project.Host = gitlab.DefaultHost
	}
	hostWritten := false
	c.mapping(owner, n, path, map[string]field{
		// 種類の値は peekTrackerKind が読んだものを使う。ここでは読めなかった誤りを名指しする
		"kind": {required: true, read: func(_, n *yaml.Node, path string) {
			if s, ok := c.str(n, path); ok && c.trackerKind == "" {
				c.fail(n, path, "未知の値 %q (%s / %s / %s)", s, GitHub, GitLab, Jira)
			}
		}},
		"repo": {required: true, read: func(_, n *yaml.Node, path string) {
			switch c.trackerKind {
			case GitHub:
				t.Repo, _ = parsed(c, n, path, github.ParseRepo)
			case GitLab:
				t.Project.Path, _ = parsed(c, n, path, gitlab.ParsePath)
			case Jira:
				t.JiraProject.Key, _ = parsed(c, n, path, jira.ParseKey)
			}
		}},
		"host": {read: func(key, n *yaml.Node, path string) {
			hostWritten = true
			switch c.trackerKind {
			case GitHub:
				c.fail(key, path, "未知の key")
			case GitLab:
				if host, ok := parsed(c, n, path, gitlab.ParseHost); ok {
					t.Project.Host = host
				}
			case Jira:
				t.JiraProject.Site, _ = parsed(c, n, path, jira.ParseSite)
			}
		}},
		"token": {read: func(key, n *yaml.Node, path string) {
			c.unsupported(key, path, unsupportedToken)
			if c.trackerKind == GitHub {
				t.Token, _ = c.variableOnly(n, path)
			}
		}},
	})
	// jira の site は既定を持たない。mapping の必須は種類に依らないので、ここで欠落を名指しする
	if c.trackerKind == Jira && !hostWritten && n.Kind == yaml.MappingNode {
		c.fail(owner, join(path, "host"), "必須の項目が無い (tracker.kind: %s では Jira Cloud の site を書く)", Jira)
	}
}

// parsed は `$VAR` で書ける項目を読み、parse で解釈した値を返す。
func parsed[T any](c *checker, n *yaml.Node, path string, parse func(string) (T, error)) (T, bool) {
	var zero T
	s, ok := c.variableOrLiteral(n, path)
	if !ok {
		return zero, false
	}
	v, err := parse(s)
	if err != nil {
		if n.Value != s {
			// 環境変数の値は名指しに載せない (秘密を置く運用がありうる)
			c.fail(n, path, "%s の値が %v", n.Value, err)
		} else {
			c.fail(n, path, "%q: %v", s, err)
		}
		return zero, false
	}
	return v, true
}

func (c *checker) polling(owner, n *yaml.Node, path string, interval *time.Duration) {
	c.mapping(owner, n, path, map[string]field{
		"interval": {read: func(_, n *yaml.Node, path string) {
			d, ok := c.duration(n, path)
			switch {
			case !ok:
			case d < MinInterval || d > MaxInterval:
				c.fail(n, path, "%q は 1s 以上 24h 以下にする", n.Value)
			default:
				*interval = d
			}
		}},
	})
}

var triggerName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func (c *checker) triggers(n *yaml.Node, path string) []trigger.Trigger {
	if n.Kind != yaml.SequenceNode && !isNull(n) {
		c.fail(n, path, "列で書く")
		return nil
	}
	if len(n.Content) == 0 {
		c.fail(n, path, "trigger を 1 つ以上書く")
		return nil
	}
	triggers := make([]trigger.Trigger, len(n.Content))
	names := map[string]bool{}
	for i, item := range n.Content {
		at := fmt.Sprintf("%s[%d]", path, i)
		t := &triggers[i]
		item = resolve(item)
		// when と action は on の種類で読み方が変わるので、on を読んでから (key の順に関わらず) 読む
		var when, whenKey, action *yaml.Node
		var actionPath, whenPath string
		c.mapping(item, item, at, map[string]field{
			"name": {required: true, read: func(_, n *yaml.Node, path string) {
				s, ok := c.str(n, path)
				switch {
				case !ok:
				case !triggerName.MatchString(s):
					c.fail(n, path, "%q: 名前は [A-Za-z0-9._-]+ で書く", s)
				case names[s]:
					c.fail(n, path, "%q: 名前が前の trigger と重なる", s)
				default:
					names[s] = true
					t.Name = s
				}
			}},
			"on": {required: true, read: func(_, n *yaml.Node, path string) {
				if s, ok := c.str(n, path); ok {
					switch kind := target.Kind(s); kind {
					case target.KindIssue:
						t.On = kind
					case target.KindCL:
						c.unsupported(n, path, unsupportedCL)
						t.On = kind
					default:
						c.fail(n, path, "未知の値 %q (%s / %s)", s, target.KindIssue, target.KindCL)
					}
				}
			}},
			"when":   {read: func(key, n *yaml.Node, path string) { whenKey, when, whenPath = key, n, path }},
			"action": {required: true, read: func(_, n *yaml.Node, path string) { action, actionPath = n, path }},
		})
		switch {
		case when == nil:
		case t.On == target.KindIssue:
			c.issuePredicate(whenKey, when, whenPath, &t.Issue)
		case t.On == target.KindCL:
			c.clPredicate(whenKey, when, whenPath, &t.CL)
		}
		if action != nil {
			t.Action = c.action(action, actionPath, t.On)
		}
	}
	return triggers
}

// action は trigger の action を読み、on の種類の見本で描画してみる。種類が読めていなければ描画は確かめない。
func (c *checker) action(n *yaml.Node, path string, kind target.Kind) string {
	s, ok := c.str(n, path)
	switch {
	case !ok:
	case strings.TrimSpace(s) == "":
		c.fail(n, path, "空白だけにできない")
	case kind != "":
		if err := render.Check("action", s, c.sample(kind)); err != nil {
			c.fail(n, path, "%v", err)
		}
	}
	return s
}

func (c *checker) issuePredicate(owner, n *yaml.Node, path string, p *trigger.IssuePredicate) {
	var assignee, unassigned *yaml.Node
	c.mapping(owner, n, path, map[string]field{
		"labels": {read: func(key, n *yaml.Node, path string) {
			c.labels(key, n, path, &p.LabelsAll, &p.LabelsAny, &p.LabelsNone)
		}},
		"assignee": {read: func(_, n *yaml.Node, path string) {
			assignee = n
			p.Assignee, _ = c.str(n, path)
		}},
		"unassigned": {read: func(_, n *yaml.Node, path string) {
			unassigned = n
			if b, ok := c.boolean(n, path); ok {
				p.Unassigned = &b
			}
		}},
		"author": {read: func(key, n *yaml.Node, path string) {
			p.Author = c.author(n, path)
			c.unsupported(key, path, unsupportedAuthor)
		}},
		"milestone": {read: func(key, n *yaml.Node, path string) {
			p.Milestone, _ = c.str(n, path)
			c.unsupported(key, path, unsupportedMilestone)
		}},
		"blocked": {read: func(key, n *yaml.Node, path string) {
			p.Blocked = c.condition(n, path)
			c.unsupported(key, path, unsupportedBlocked)
		}},
		"status": {read: func(key, n *yaml.Node, path string) {
			c.names(key, n, path, &p.StatusAny, &p.StatusNone)
			c.unsupported(key, path, unsupportedStatusAndType)
		}},
		"type": {read: func(key, n *yaml.Node, path string) {
			c.names(key, n, path, &p.TypeAny, &p.TypeNone)
			c.unsupported(key, path, unsupportedStatusAndType)
		}},
	})
	if assignee != nil && unassigned != nil {
		c.fail(assignee, path, "assignee と unassigned は一緒に書けない")
	}
}

func (c *checker) clPredicate(owner, n *yaml.Node, path string, p *trigger.CLPredicate) {
	var head *yaml.Node
	c.mapping(owner, n, path, map[string]field{
		"conflict":          {read: func(_, n *yaml.Node, path string) { p.Conflict = c.condition(n, path) }},
		"review_unresolved": {read: func(_, n *yaml.Node, path string) { p.ReviewUnresolved = c.condition(n, path) }},
		"ci_failed":         {read: func(_, n *yaml.Node, path string) { p.CIFailed = c.condition(n, path) }},
		"approved":          {read: func(_, n *yaml.Node, path string) { p.Approved = c.condition(n, path) }},
		"labels": {read: func(key, n *yaml.Node, path string) {
			c.labels(key, n, path, &p.LabelsAll, &p.LabelsAny, &p.LabelsNone)
		}},
		"head": {read: func(_, n *yaml.Node, path string) {
			head = n
			if s, ok := c.str(n, path); ok {
				if _, err := pathpkg.Match(s, ""); err != nil {
					c.fail(n, path, "%q: pattern の綴りの誤り (Go の path.Match の綴りで書く)", s)
				}
				p.Head = s
			}
		}},
		"same_repo": {read: func(_, n *yaml.Node, path string) { p.SameRepo = c.condition(n, path) }},
		"author":    {read: func(_, n *yaml.Node, path string) { p.Author = c.author(n, path) }},
		"draft":     {read: func(_, n *yaml.Node, path string) { p.Draft = c.condition(n, path) }},
	})
	if head != nil && p.SameRepo != nil && !*p.SameRepo {
		c.fail(head, path, "head と same_repo: false は一緒に書けない (head は同じ repo の branch にだけ当たる)")
	}
}

// labels は述語の labels (all / any / none) を読む。
func (c *checker) labels(owner, n *yaml.Node, path string, all, anyOf, none *[]string) {
	c.mapping(owner, n, path, map[string]field{
		"all":  {read: func(_, n *yaml.Node, path string) { *all, _ = c.strList(n, path) }},
		"none": {read: func(_, n *yaml.Node, path string) { *none, _ = c.strList(n, path) }},
		"any":  c.anyList(anyOf),
	})
}

// names は述語の名前の条件 (status と type の any / none) を読む。
func (c *checker) names(owner, n *yaml.Node, path string, anyOf, none *[]string) {
	c.mapping(owner, n, path, map[string]field{
		"none": {read: func(_, n *yaml.Node, path string) { *none, _ = c.strList(n, path) }},
		"any":  c.anyList(anyOf),
	})
}

// anyList は any の列の読み方。空の列はどの作業対象にも当たらないので拒む。
func (c *checker) anyList(anyOf *[]string) field {
	return field{read: func(_, n *yaml.Node, path string) {
		if l, ok := c.strList(n, path); ok {
			if len(l) == 0 {
				c.fail(n, path, "空の列は書けない (どの作業対象にも当たらない)")
			}
			*anyOf = l
		}
	}}
}

func (c *checker) author(n *yaml.Node, path string) trigger.Author {
	s, ok := c.str(n, path)
	if !ok {
		return ""
	}
	switch a := trigger.Author(s); a {
	case trigger.Collaborator, trigger.NonCollaborator:
		return a
	}
	c.fail(n, path, "未知の値 %q (%s / %s)", s, trigger.Collaborator, trigger.NonCollaborator)
	return ""
}

// condition は真偽の条件を読む。読めなければ nil (条件なし) を返し、誤りは c に積む。
func (c *checker) condition(n *yaml.Node, path string) *bool {
	if b, ok := c.boolean(n, path); ok {
		return &b
	}
	return nil
}

// str は n を空でない文字列として読む。空の文字列は、条件を書かなかったのと取り違えるので拒む。
func (c *checker) str(n *yaml.Node, path string) (string, bool) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		c.fail(n, path, "文字列で書く")
		return "", false
	}
	if n.Value == "" {
		c.fail(n, path, "空にできない")
		return "", false
	}
	return n.Value, true
}

func (c *checker) boolean(n *yaml.Node, path string) (bool, bool) {
	var b bool
	if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" || n.Decode(&b) != nil {
		c.fail(n, path, "true か false で書く")
		return false, false
	}
	return b, true
}

func (c *checker) strList(n *yaml.Node, path string) ([]string, bool) {
	if n.Kind != yaml.SequenceNode {
		c.fail(n, path, "文字列の列 ([a, b]) で書く")
		return nil, false
	}
	list := []string{}
	ok := true
	for i, item := range n.Content {
		s, itemOK := c.str(resolve(item), fmt.Sprintf("%s[%d]", path, i))
		ok = ok && itemOK
		list = append(list, s)
	}
	return list, ok
}

var variable = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)$`)

// variableOrLiteral は `$VAR` で書ける項目を読む。値の全体が `$NAME` なら環境変数 NAME の値に置き換える。
func (c *checker) variableOrLiteral(n *yaml.Node, path string) (string, bool) {
	s, ok := c.str(n, path)
	if !ok {
		return "", false
	}
	if m := variable.FindStringSubmatch(s); m != nil {
		return c.lookup(n, path, m[1])
	}
	return s, true
}

// variableOnly は `$VAR` でだけ書ける項目 (秘密) を読む。値そのものは書かせない。
func (c *checker) variableOnly(n *yaml.Node, path string) (string, bool) {
	s, ok := c.str(n, path)
	if !ok {
		return "", false
	}
	m := variable.FindStringSubmatch(s)
	if m == nil {
		c.fail(n, path, "$VAR で書く (値そのものを workflow 定義に書かない)")
		return "", false
	}
	return c.lookup(n, path, m[1])
}

// lookup は環境変数 name の値を引く。未設定か空なら誤り。
func (c *checker) lookup(n *yaml.Node, path, name string) (string, bool) {
	value := c.getenv(name)
	if value == "" {
		c.fail(n, path, "環境変数 %s が未設定か空", name)
		return "", false
	}
	return value, true
}
