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
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/swat9013/claude-dispatcher/internal/provider"
	"github.com/swat9013/claude-dispatcher/internal/render"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
)

// Definition は検査に通った workflow 定義。
type Definition struct {
	// Dir は workflow 定義の file がある dir の絶対 path
	Dir     string
	Tracker Tracker
	// CL は issue 置き場と別の CL 置き場 (cl:)。書かれていなければ nil。書けるのは CL 置き場の役割を持たない種類 (jira) だけ
	CL *CL
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
	// StallTimeout は worker の活動 (活動として数える stream の行) が途絶えてから止めるまで、RunTimeout は worker 1 回分の上限時間。0 なら見ない
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

// Tracker は issue 置き場の設定。
type Tracker struct {
	Kind provider.Kind
	// Place は provider が tracker.repo と tracker.host から読んだ置き場
	Place provider.Place
	// Token は tracker.token の値。書かれていなければ ""。書けるのは provider が支えると宣言した種類だけ
	Token string
}

// Provider は tracker.kind の provider。
func (t Tracker) Provider() provider.Provider { return mustProvider(t.Kind) }

// CL は cl: の CL 置き場の設定。token は持たず、CL 置き場の CLI 自身の認証を使う。
type CL struct {
	Kind provider.Kind
	// Place は cl.kind の provider が cl.repo と cl.host から読んだ置き場
	Place provider.Place
}

// mustProvider は検査に通った workflow 定義の種類の provider。検査に通った種類は必ず provider を持つので、無ければ panic する。
func mustProvider(kind provider.Kind) provider.Provider {
	p, ok := provider.Of(kind)
	if !ok {
		panic(fmt.Sprintf("種類 %q の provider が無い (検査に通っていない workflow 定義)", kind))
	}
	return p
}

// CLHost は CL 置き場の役割を担う provider と CL 置き場 (system.md §13)。cl: があればその種類と置き場、無ければ issue 置き場と
// 同じ場所。検査に通った workflow 定義は CL 置き場をちょうど 1 つ持つ (ADR 0015) ので、無ければ panic する。
func (d Definition) CLHost() (provider.Provider, provider.Place) {
	if p, place, ok := d.SeparateCLHost(); ok {
		return p, place
	}
	p := d.Tracker.Provider()
	if p.CLHost == nil {
		panic(fmt.Sprintf("tracker.kind %q の workflow 定義に CL 置き場が無い (検査に通っていない workflow 定義)", d.Tracker.Kind))
	}
	return p, d.Tracker.Place
}

// SeparateCLHost は issue 置き場と別の CL 置き場 (cl:) の provider と置き場。cl: が無ければ false。
func (d Definition) SeparateCLHost() (provider.Provider, provider.Place, bool) {
	if d.CL == nil {
		return provider.Provider{}, provider.Place{}, false
	}
	return mustProvider(d.CL.Kind), d.CL.Place, true
}

// CLPlace は CL 置き場を指す綴り (`<種類> <置き場>`)。書いたとおりの綴りを返す (tick error の行にもこの綴りが出る)。読み直した定義の CL 置き場が起動時と同じかを比べるのに使う。
func (d Definition) CLPlace() string {
	p, place := d.CLHost()
	return string(p.Kind) + " " + place.String()
}

// Settings は worker が外部 store を操作するのに要りそうな Claude Code の settings の entry。役割ごとに、その役割を担う
// provider から引く (system.md §11)。
func (d Definition) Settings() []string {
	clHost, _ := d.CLHost()
	return provider.Settings(d.Tracker.Provider().Tracker, *clHost.CLHost)
}

// Reference は作業対象を人が読む行に出す参照 (formats.md §5)。呼び方は、作業対象の置き場の役割を担う provider が宣言する。
func (d Definition) Reference(ref target.Ref) string {
	if ref.Kind == target.KindCL {
		p, place := d.CLHost()
		return p.Reference(place, ref)
	}
	return d.Tracker.Provider().Reference(d.Tracker.Place, ref)
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
	// provider は tracker.kind の provider。種類に依る検査が key の順 (triggers を tracker より前に書くなど) に依らないよう、
	// front matter を辿る前に引く。引けなければ nil で、種類に依る検査は撃たない (誤りは tracker.kind で名指しする)
	provider *provider.Provider
	// clHost は CL 置き場の役割を担う provider (tracker.kind の provider が役割を持てばそれ、持たなければ cl.kind の
	// provider)。provider と同じく front matter を辿る前に引く。引けなければ nil で、CL 側の宣言の検査は撃たない (誤りは
	// cl か cl.kind で名指しする)。clHostKey はその種類を書いた key
	clHost    *provider.Provider
	clHostKey string
	// clWritten は cl: を書いたか
	clWritten bool
}

// peek は対応表 n の key の値を、alias を辿って返す。n が対応表でないか key が無ければ nil。
func peek(n *yaml.Node, key string) *yaml.Node {
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

// peekKind は対応表 n の kind の値の provider。provider を持つ種類でなければ nil。
func peekKind(n *yaml.Node) *provider.Provider {
	kind := peek(n, "kind")
	if kind == nil || kind.Kind != yaml.ScalarNode {
		return nil
	}
	if p, ok := provider.Of(provider.Kind(kind.Value)); ok {
		return &p
	}
	return nil
}

// peekProviders は tracker.kind の provider と CL 置き場の役割を担う provider を、他の項目を読む前に引く。
func (c *checker) peekProviders(root *yaml.Node) {
	c.provider = peekKind(peek(root, "tracker"))
	cl := peek(root, "cl")
	c.clWritten = cl != nil
	switch {
	case c.provider == nil:
	case c.provider.CLHost != nil:
		c.clHost, c.clHostKey = c.provider, "tracker.kind"
	default:
		if p := peekKind(cl); p != nil && p.CLHost != nil {
			c.clHost, c.clHostKey = p, "cl.kind"
		}
	}
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
	c.peekProviders(root)
	// top level は key を持たないので、欠落を行なしで名指しする
	c.mapping(&yaml.Node{}, root, "", c.topFields(def))
	// issue 置き場の provider が CL 置き場の役割を持たなければ、CL 置き場は cl: でしか決まらない。project の CL 置き場は
	// ちょうど 1 つなので必須にする (ADR 0015)。mapping の必須は種類に依らないので、ここで欠落を名指しする
	if c.provider != nil && c.provider.CLHost == nil && !c.clWritten && root.Kind == yaml.MappingNode {
		c.fail(&yaml.Node{}, "cl", "必須の項目が無い (tracker.kind: %s では、CL を出す %s の repo を宣言する)", c.provider.Kind, provider.Join(provider.CLHostKinds()))
	}
}

// 以下の *Fields は、対応表ごとに受ける key とその読み方の表。読み込みが受ける key を案内と突き合わせるテストが列挙する
// (ADR 0012)。

// topFields は front matter の top level の表。
func (c *checker) topFields(def *Definition) map[string]field {
	return map[string]field{
		"tracker": {required: true, read: func(key, n *yaml.Node, path string) { c.tracker(key, n, path, &def.Tracker) }},
		"cl": {read: func(key, n *yaml.Node, path string) {
			if c.supports(key, path, provider.CLPlace) {
				def.CL = c.cl(key, n, path)
			}
		}},
		"polling": {read: func(key, n *yaml.Node, path string) { c.polling(key, n, path, &def.Interval) }},
		"workspace": {read: func(key, n *yaml.Node, path string) {
			c.mapping(key, n, path, c.workspaceFields(def))
		}},
		"hooks": {read: func(key, n *yaml.Node, path string) { c.hooks(key, n, path, &def.Hooks) }},
		"limits": {read: func(key, n *yaml.Node, path string) {
			c.mapping(key, n, path, c.limitsFields(def))
		}},
		"claude": {read: func(key, n *yaml.Node, path string) {
			c.mapping(key, n, path, c.claudeFields(def))
		}},
		"triggers": {required: true, read: func(_, n *yaml.Node, path string) { def.Triggers = c.triggers(n, path) }},
	}
}

// workspaceFields は workspace の表。
func (c *checker) workspaceFields(def *Definition) map[string]field {
	return map[string]field{
		"root": {read: func(_, n *yaml.Node, path string) {
			if s, ok := c.variableOrLiteral(n, path); ok {
				def.WorkspaceRoot = c.absolute(n, path, def.Dir, s)
			}
		}},
	}
}

// limitsFields は limits の表。
func (c *checker) limitsFields(def *Definition) map[string]field {
	return map[string]field{
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
	}
}

// claudeFields は claude の表。
func (c *checker) claudeFields(def *Definition) map[string]field {
	return map[string]field{
		"command": {read: func(_, n *yaml.Node, path string) {
			if s, ok := c.str(n, path); ok {
				def.Claude.Command = s
			}
		}},
		"args": {read: func(_, n *yaml.Node, path string) { def.Claude.Args, _ = c.strList(n, path) }},
	}
}

// supports は、issue 置き場の役割を担う provider (tracker.kind) が宣言 f を支えるかを返す。支えなければ n を理由を添えて
// 名指しで失敗させる (formats.md §2.8)。支える宣言の無いものは支えない (fail closed)。種類が読めていなければ名指しせず (誤りは
// tracker.kind で名指しする)、支えるとも答えない。
func (c *checker) supports(n *yaml.Node, path string, f provider.Feature) bool {
	if c.provider == nil {
		return false
	}
	return c.declared(n, path, "tracker.kind", c.provider.Kind, c.provider.Tracker.Declarations(), f)
}

// clSupports は supports の CL 置き場の役割の版。CL 置き場の役割を担う provider が引けていなければ名指しせず (誤りは on: cl
// か cl.kind で名指しする)、支えるとも答えない。
func (c *checker) clSupports(n *yaml.Node, path string, f provider.Feature) bool {
	if c.clHost == nil {
		return false
	}
	return c.declared(n, path, c.clHostKey, c.clHost.Kind, c.clHost.CLHost.Declarations, f)
}

// declared は役割の宣言 d が f を支えるかを返し、支えなければ、種類を書いた key (key) と種類 (kind) と理由を添えて n を
// 名指しする。
func (c *checker) declared(n *yaml.Node, path, key string, kind provider.Kind, d provider.Declarations, f provider.Feature) bool {
	if !d.Supported(f) {
		c.fail(n, path, "%s: %s では書けない (%s)", key, kind, d.DeclineReason(f))
		return false
	}
	return true
}

// sample は kind の作業対象の、描画を確かめる見本。issue を key で指す種類の issue には key を足す (`.issue.key` はその種類
// でだけ使える)。
func (c *checker) sample(kind target.Kind) target.Item {
	item := render.Sample(kind)
	if issue, ok := item.(target.Issue); ok && c.provider != nil && c.provider.Tracker.KeyedIssues {
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
	c.mapping(owner, n, path, c.hooksFields(h))
}

// hooksFields は hooks の表。
func (c *checker) hooksFields(h *Hooks) map[string]field {
	script := func(target *string) field {
		return field{read: func(_, n *yaml.Node, path string) { *target, _ = c.str(n, path) }}
	}
	return map[string]field{
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
	}
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

// tracker は tracker の項目を、先に引いた provider (c.provider) の読み方で読む。provider が無ければ repo・host・token は
// 読まない (誤りは tracker.kind で名指しする)。repo と host がどちらも読めたら、provider が置き場にする。
func (c *checker) tracker(owner, n *yaml.Node, path string, t *Tracker) {
	var place placeInput
	c.mapping(owner, n, path, c.trackerFields(t, &place))
	if c.provider == nil {
		return
	}
	t.Kind = c.provider.Kind
	t.Place, _ = c.place(owner, n, path, "tracker.kind", c.provider, &place)
}

// cl は cl の項目を、先に引いた CL 置き場の provider (c.clHost) の読み方で読む。provider が無ければ置き場を読まない (誤りは
// cl.kind で名指しする)。
func (c *checker) cl(owner, n *yaml.Node, path string) *CL {
	var in placeInput
	c.mapping(owner, n, path, c.clFields(&in))
	if c.clHost == nil {
		return nil
	}
	place, ok := c.place(owner, n, path, "cl.kind", c.clHost, &in)
	if !ok {
		return nil
	}
	return &CL{Kind: c.clHost.Kind, Place: place}
}

// place は対応表 (tracker か cl) が読んだ repo と host を、種類 p の置き場にする。host を書かなければ既定を使い、既定を
// 持たない host の欠落を名指しする (key は種類を書いた key)。repo と host がどちらも読めたときだけ true。
func (c *checker) place(owner, n *yaml.Node, path, key string, p *provider.Provider, in *placeInput) (provider.Place, bool) {
	if !in.hostWritten {
		// 既定を持たない種類は ""。host を書けない種類は "" で置き場を作る
		in.host, _ = p.Host.Default()
		in.hostRead = true
		// 既定を持たない host は必須。mapping の必須は種類に依らないので、ここで欠落を名指しする
		if p.Host.Required() {
			in.hostRead = false
			if n.Kind == yaml.MappingNode {
				c.fail(owner, join(path, "host"), "必須の項目が無い (%s: %s では %s)", key, p.Kind, p.Host.RequiredHint())
			}
		}
	}
	if !in.repoRead || !in.hostRead {
		return provider.Place{}, false
	}
	return p.NewPlace(in.host, in.repo), true
}

// placeInput は tracker の表が読んだ repo と host。読めたものだけ repoRead と hostRead を立てる。
type placeInput struct {
	repo, host         string
	repoRead, hostRead bool
	hostWritten        bool
}

// trackerFields は tracker の表。repo と host は place に預ける。
func (c *checker) trackerFields(t *Tracker, place *placeInput) map[string]field {
	fields := c.placeFields(c.provider, place)
	// 種類の値は peekProviders が読んだものを使う。ここでは読めなかった誤りを名指しする
	fields["kind"] = field{required: true, read: func(_, n *yaml.Node, path string) {
		if s, ok := c.str(n, path); ok && c.provider == nil {
			c.fail(n, path, "未知の値 %q (%s)", s, provider.Join(provider.Kinds()))
		}
	}}
	fields["token"] = field{read: func(key, n *yaml.Node, path string) {
		if c.supports(key, path, provider.Token) {
			t.Token, _ = c.variableOnly(n, path)
		}
	}}
	return fields
}

// clFields は cl の表。repo と host は place に預ける。token は持たない (CL 置き場の CLI 自身の認証を使う)。
func (c *checker) clFields(place *placeInput) map[string]field {
	fields := c.placeFields(c.clHost, place)
	// 種類の値は peekProviders が読んだものを使う。ここでは CL 置き場にならない種類の誤りを名指しする
	fields["kind"] = field{required: true, read: func(_, n *yaml.Node, path string) {
		s, ok := c.str(n, path)
		kinds := provider.CLHostKinds()
		switch _, known := provider.Of(provider.Kind(s)); {
		case !ok || slices.Contains(kinds, provider.Kind(s)):
		case known:
			c.fail(n, path, "%q は CL 置き場にならない (%s)", s, provider.Join(kinds))
		default:
			c.fail(n, path, "未知の値 %q (%s)", s, provider.Join(kinds))
		}
	}}
	return fields
}

// placeFields は置き場を指す表 (tracker と cl) の repo と host を、種類 p の読み方で読んで place に預ける。p が無ければ
// 読まない (誤りは種類の key で名指しする)。
func (c *checker) placeFields(p *provider.Provider, place *placeInput) map[string]field {
	return map[string]field{
		"repo": {required: true, read: func(_, n *yaml.Node, path string) {
			if p != nil {
				place.repo, place.repoRead = parsed(c, n, path, p.ParseRepo)
			}
		}},
		"host": {read: func(key, n *yaml.Node, path string) {
			place.hostWritten = true
			switch {
			case p == nil:
			case !p.Host.Writable():
				c.fail(key, path, "未知の key")
			default:
				place.host, place.hostRead = parsed(c, n, path, p.Host.Parse)
			}
		}},
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
	c.mapping(owner, n, path, c.pollingFields(interval))
}

// pollingFields は polling の表。
func (c *checker) pollingFields(interval *time.Duration) map[string]field {
	return map[string]field{
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
	}
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
		var later deferredItems
		c.mapping(item, item, at, c.triggerFields(t, names, &later))
		switch {
		case later.when == nil:
		case t.On == target.KindIssue:
			c.issuePredicate(later.whenKey, later.when, later.whenPath, &t.Issue)
		case t.On == target.KindCL:
			c.clPredicate(later.whenKey, later.when, later.whenPath, &t.CL)
		}
		if later.action != nil {
			t.Action = c.action(later.action, later.actionPath, t.On)
		}
	}
	return triggers
}

// deferredItems は trigger の項目のうち、on を読んでから読む when と action の node と位置。
type deferredItems struct {
	when, whenKey, action *yaml.Node
	whenPath, actionPath  string
}

// triggerFields は trigger 1 つの表。name は names (前の trigger の名前) と突き合わせ、when と action は later に預ける。
func (c *checker) triggerFields(t *trigger.Trigger, names map[string]bool, later *deferredItems) map[string]field {
	return map[string]field{
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
					t.On = kind
				default:
					c.fail(n, path, "未知の値 %q (%s / %s)", s, target.KindIssue, target.KindCL)
				}
			}
		}},
		"when": {read: func(key, n *yaml.Node, path string) {
			later.whenKey, later.when, later.whenPath = key, n, path
		}},
		"action": {required: true, read: func(_, n *yaml.Node, path string) { later.action, later.actionPath = n, path }},
	}
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
	c.mapping(owner, n, path, c.issuePredicateFields(p, &assignee, &unassigned))
	if assignee != nil && unassigned != nil {
		c.fail(assignee, path, "assignee と unassigned は一緒に書けない")
	}
}

// issuePredicateFields は issue 側の述語の表。assignee と unassigned を読んだら、その node を預ける (併記の検査に使う)。
func (c *checker) issuePredicateFields(p *trigger.IssuePredicate, assignee, unassigned **yaml.Node) map[string]field {
	return map[string]field{
		"labels": {read: func(key, n *yaml.Node, path string) {
			c.labels(key, n, path, &p.LabelsAll, &p.LabelsAny, &p.LabelsNone)
		}},
		"assignee": {read: func(_, n *yaml.Node, path string) {
			*assignee = n
			p.Assignee, _ = c.str(n, path)
		}},
		"unassigned": {read: func(_, n *yaml.Node, path string) {
			*unassigned = n
			if b, ok := c.boolean(n, path); ok {
				p.Unassigned = &b
			}
		}},
		"author": {read: func(key, n *yaml.Node, path string) {
			p.Author = c.author(n, path)
			c.supports(key, path, provider.Author)
		}},
		"milestone": {read: func(key, n *yaml.Node, path string) {
			p.Milestone, _ = c.str(n, path)
			c.supports(key, path, provider.Milestone)
		}},
		"blocked": {read: func(key, n *yaml.Node, path string) {
			p.Blocked = c.condition(n, path)
			c.supports(key, path, provider.Blocked)
		}},
		"status": {read: func(key, n *yaml.Node, path string) {
			c.names(key, n, path, &p.StatusAny, &p.StatusNone)
			c.supports(key, path, provider.Status)
		}},
		"type": {read: func(key, n *yaml.Node, path string) {
			c.names(key, n, path, &p.TypeAny, &p.TypeNone)
			c.supports(key, path, provider.Type)
		}},
	}
}

func (c *checker) clPredicate(owner, n *yaml.Node, path string, p *trigger.CLPredicate) {
	var head *yaml.Node
	c.mapping(owner, n, path, c.clPredicateFields(p, &head))
	if head != nil && p.SameRepo != nil && !*p.SameRepo {
		c.fail(head, path, "head と same_repo: false は一緒に書けない (head は同じ repo の branch にだけ当たる)")
	}
}

// clPredicateFields は CL 側の述語の表。head を読んだら、その node を預ける (same_repo との併記の検査に使う)。
func (c *checker) clPredicateFields(p *trigger.CLPredicate, head **yaml.Node) map[string]field {
	return map[string]field{
		"conflict":          {read: func(_, n *yaml.Node, path string) { p.Conflict = c.condition(n, path) }},
		"review_unresolved": {read: func(_, n *yaml.Node, path string) { p.ReviewUnresolved = c.condition(n, path) }},
		"ci_failed":         {read: func(_, n *yaml.Node, path string) { p.CIFailed = c.condition(n, path) }},
		"approved":          {read: func(_, n *yaml.Node, path string) { p.Approved = c.condition(n, path) }},
		"labels": {read: func(key, n *yaml.Node, path string) {
			c.labels(key, n, path, &p.LabelsAll, &p.LabelsAny, &p.LabelsNone)
		}},
		"head": {read: func(_, n *yaml.Node, path string) {
			*head = n
			if s, ok := c.str(n, path); ok {
				if _, err := pathpkg.Match(s, ""); err != nil {
					c.fail(n, path, "%q: pattern の綴りの誤り (Go の path.Match の綴りで書く)", s)
				}
				p.Head = s
			}
		}},
		"same_repo": {read: func(_, n *yaml.Node, path string) { p.SameRepo = c.condition(n, path) }},
		"author": {read: func(key, n *yaml.Node, path string) {
			p.Author = c.author(n, path)
			c.clSupports(key, path, provider.Author)
		}},
		"draft": {read: func(_, n *yaml.Node, path string) { p.Draft = c.condition(n, path) }},
	}
}

// labels は述語の labels (all / any / none) を読む。
func (c *checker) labels(owner, n *yaml.Node, path string, all, anyOf, none *[]string) {
	c.mapping(owner, n, path, c.labelsFields(all, anyOf, none))
}

// labelsFields は述語の labels の表。
func (c *checker) labelsFields(all, anyOf, none *[]string) map[string]field {
	return map[string]field{
		"all":  {read: func(_, n *yaml.Node, path string) { *all, _ = c.strList(n, path) }},
		"none": {read: func(_, n *yaml.Node, path string) { *none, _ = c.strList(n, path) }},
		"any":  c.anyList(anyOf),
	}
}

// names は述語の名前の条件 (status と type の any / none) を読む。
func (c *checker) names(owner, n *yaml.Node, path string, anyOf, none *[]string) {
	c.mapping(owner, n, path, c.namesFields(anyOf, none))
}

// namesFields は述語の名前の条件 (status と type) の表。
func (c *checker) namesFields(anyOf, none *[]string) map[string]field {
	return map[string]field{
		"none": {read: func(_, n *yaml.Node, path string) { *none, _ = c.strList(n, path) }},
		"any":  c.anyList(anyOf),
	}
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
