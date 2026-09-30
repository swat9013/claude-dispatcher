// Package workflow は workflow 定義 (`WORKFLOW.md`) を読んで検査する (system.md §8、formats.md §2)。
// 誤りは項目の位置を名指しし、すべてを集めて返す。
package workflow

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
)

// Definition は検査に通った workflow 定義。
type Definition struct {
	Tracker Tracker
	// Interval は tick の周期
	Interval time.Duration
	Triggers []trigger.Trigger
	// Prompt は本文 (共通 prompt)。worker に渡す (#78)
	Prompt string
}

// Tracker は issue 置き場の設定。
type Tracker struct {
	Repo github.Repo
	// Token は gh に GH_TOKEN として渡す token。書かれていなければ ""
	Token string
}

// 周期の既定と範囲 (formats.md §2.1)
const (
	DefaultInterval = 5 * time.Minute
	MinInterval     = time.Second
	MaxInterval     = 24 * time.Hour
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
	def := Definition{Interval: DefaultInterval, Prompt: body}
	root := &yaml.Node{Kind: yaml.MappingNode}
	if len(doc.Content) == 1 {
		root = resolve(doc.Content[0])
	}
	c.decode(root, &def)
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
	// top level は key を持たないので、欠落を行なしで名指しする
	c.mapping(&yaml.Node{}, root, "", map[string]field{
		"tracker":  {required: true, read: func(key, n *yaml.Node, path string) { c.tracker(key, n, path, &def.Tracker) }},
		"polling":  {read: func(key, n *yaml.Node, path string) { c.polling(key, n, path, &def.Interval) }},
		"triggers": {required: true, read: func(_, n *yaml.Node, path string) { def.Triggers = c.triggers(n, path) }},
	})
}

func (c *checker) tracker(owner, n *yaml.Node, path string, t *Tracker) {
	c.mapping(owner, n, path, map[string]field{
		"kind": {required: true, read: func(_, n *yaml.Node, path string) {
			if s, ok := c.str(n, path); ok {
				if s != "github" {
					c.fail(n, path, "未知の値 %q (github)", s)
				}
			}
		}},
		"repo": {required: true, read: func(_, n *yaml.Node, path string) {
			s, ok := c.variableOrLiteral(n, path)
			if !ok {
				return
			}
			parsed, err := github.ParseRepo(s)
			if err != nil {
				if n.Value != s {
					// 環境変数の値は名指しに載せない (秘密を置く運用がありうる)
					c.fail(n, path, "%s の値が %v", n.Value, err)
				} else {
					c.fail(n, path, "%q: %v", s, err)
				}
				return
			}
			t.Repo = parsed
		}},
		"token": {read: func(_, n *yaml.Node, path string) {
			if s, ok := c.variableOnly(n, path); ok {
				t.Token = s
			}
		}},
	})
}

func (c *checker) polling(owner, n *yaml.Node, path string, interval *time.Duration) {
	c.mapping(owner, n, path, map[string]field{
		"interval": {read: func(_, n *yaml.Node, path string) {
			s, ok := c.str(n, path)
			if !ok {
				return
			}
			d, err := time.ParseDuration(s)
			switch {
			case err != nil:
				c.fail(n, path, "%q は Go の duration の綴り (90s / 5m / 1h30m) で書く", s)
			case d < MinInterval || d > MaxInterval:
				c.fail(n, path, "%q は 1s 以上 24h 以下にする", s)
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
					if trigger.Kind(s) != trigger.Issue {
						c.fail(n, path, "未知の値 %q (%s)", s, trigger.Issue)
					}
					t.On = trigger.Kind(s)
				}
			}},
			"when": {read: func(key, n *yaml.Node, path string) { c.issuePredicate(key, n, path, &t.When) }},
			// action の値は worker を起動するときに使う (#78)。ここでは書かれていることだけを確かめる
			"action": {required: true, read: func(_, n *yaml.Node, path string) {
				if s, ok := c.str(n, path); ok && strings.TrimSpace(s) == "" {
					c.fail(n, path, "空白だけにできない")
				}
			}},
		})
	}
	return triggers
}

func (c *checker) issuePredicate(owner, n *yaml.Node, path string, p *trigger.IssuePredicate) {
	var assignee, unassigned *yaml.Node
	c.mapping(owner, n, path, map[string]field{
		"labels": {read: func(key, n *yaml.Node, path string) {
			c.mapping(key, n, path, map[string]field{
				"all":  {read: func(_, n *yaml.Node, path string) { p.LabelsAll, _ = c.strList(n, path) }},
				"none": {read: func(_, n *yaml.Node, path string) { p.LabelsNone, _ = c.strList(n, path) }},
				"any": {read: func(_, n *yaml.Node, path string) {
					if l, ok := c.strList(n, path); ok {
						if len(l) == 0 {
							c.fail(n, path, "空の列は書けない (どの issue にも当たらない)")
						}
						p.LabelsAny = l
					}
				}},
			})
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
		"author": {read: func(_, n *yaml.Node, path string) {
			if s, ok := c.str(n, path); ok {
				switch a := trigger.Author(s); a {
				case trigger.Collaborator, trigger.NonCollaborator:
					p.Author = a
				default:
					c.fail(n, path, "未知の値 %q (%s / %s)", s, trigger.Collaborator, trigger.NonCollaborator)
				}
			}
		}},
		"milestone": {read: func(_, n *yaml.Node, path string) { p.Milestone, _ = c.str(n, path) }},
		"blocked": {read: func(_, n *yaml.Node, path string) {
			if b, ok := c.boolean(n, path); ok {
				p.Blocked = &b
			}
		}},
	})
	if assignee != nil && unassigned != nil {
		c.fail(assignee, path, "assignee と unassigned は一緒に書けない")
	}
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
