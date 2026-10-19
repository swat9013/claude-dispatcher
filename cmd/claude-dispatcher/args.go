package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/provider"
)

// 引数の宣言の表。subcommand ごとの flag と位置引数、command の代わりに書く flag をここに書き、parser (parse と run の
// 振り分け) と usage の Usage 節の command の代わりの形 (usageForms) と Options 節 (usageForms・options) が同じ表を読む。command の代わりの flag
// ごとの処理は run が helpFlag・versionFlag を名指しで持つ。flag の一覧と規則の正本は
// formats.md §10 (と §6・§7) で、この表はそれに合わせる側。setup の --kind が受ける種類と案内の綴りは provider の表も持つ。

// flagSpec は subcommand の flag 1 つの宣言。
type flagSpec struct {
	name string
	// value は値付き flag の usage 上の綴り (`<N>`・`jira` 等)。空なら値を取らない flag
	value string
	help  string
	// field は setup の置き場の項目を受ける flag なら、その項目。誤りを flag の名前で名指しするのに使う
	field provider.PlaceField
	// check は値付き flag の値の検査。nil なら検査しない
	check func(value string) error
}

func (f flagSpec) takesValue() bool { return f.value != "" }

// commandSpec は subcommand 1 つの引数の宣言。
type commandSpec struct {
	name  string
	flags []flagSpec
	// arguments は flag 以外の引数の受け方
	arguments arguments
	// operands は WORKFLOW の前に必ず書く位置引数の usage 上の綴り (run の TRIGGER・TARGET)。workflowArgument に限る
	operands []string
	// required は必ず付ける flag の名前
	required []string
	// check は値付き flag を読んだ後、他の引数を読む前の、値付き flag の組み合わせの検査。nil なら検査しない
	// check の中では値を取らない flag はまだ読まれていないので has を使わない
	check func(parsedArgs) error
}

// arguments は subcommand が flag 以外に受けるもの。
type arguments int

const (
	// undeclaredArguments は宣言の書き忘れ。parse は panic する
	undeclaredArguments arguments = iota
	// workflowArgument は位置引数 WORKFLOW (workflow 定義の path) を 1 つまで受ける
	workflowArgument
	// noArguments は何も受けない。flag を宣言しない subcommand に限り、flag の綴りも余った引数として名指しする
	noArguments
)

// commandSpecs は subcommand の宣言。並びは usage の Options 節の並び。
var commandSpecs = []commandSpec{
	{name: "loop", arguments: workflowArgument, flags: []flagSpec{
		{name: "--dry-run", help: "起動するはずの作業対象と trigger を示す。何も起動せず、何も書かない"},
		{name: "--max-concurrent", value: "<N>", help: "limits.max_concurrent を上書きする並列上限 (1 以上の整数)", check: positiveInt("--max-concurrent")},
	}},
	{name: "status", arguments: workflowArgument},
	{name: "paths", arguments: workflowArgument, required: []string{"--json"}, flags: []flagSpec{
		{name: "--json", help: "JSON で出す (paths では必須)"},
	}},
	{name: "setup", arguments: workflowArgument, check: checkSetupFlags, flags: []flagSpec{
		{name: "--kind", value: "jira", help: "Jira を issue 置き場にする。--host と --repo と一緒に書く"},
		{name: "--host", value: "<site>", help: "Jira の site", field: provider.HostField},
		{name: "--repo", value: "<key>", help: "Jira の project key", field: provider.RepoField},
	}},
	{name: "doctor", arguments: workflowArgument},
	{name: "run", arguments: workflowArgument, operands: []string{"TRIGGER", "TARGET"}},
	{name: "guide", arguments: noArguments},
}

// globalFlag は command の代わりに書く flag の宣言。
type globalFlag struct {
	// spellings は受ける flag の綴り。usage の Options 節には `, ` で並べる
	spellings []string
	// word は flag と同じに受ける、`-` の無い綴り。無ければ空
	word string
	help string
}

// allSpellings は受ける綴りすべて: flag の綴りと、あれば word。
func (g globalFlag) allSpellings() []string {
	if g.word == "" {
		return g.spellings
	}
	return append(g.spellings[:len(g.spellings):len(g.spellings)], g.word)
}

// accepts は arg がこの flag の綴りか。
func (g globalFlag) accepts(arg string) bool { return slices.Contains(g.allSpellings(), arg) }

var (
	helpFlag    = globalFlag{spellings: []string{"-h", "--help"}, word: "help", help: "この一覧を出す。help でも同じ"}
	versionFlag = globalFlag{spellings: []string{"--version"}, help: "版を出す"}
	// globalFlags は command の代わりに書く flag。並びは usage の Usage 節と Options 節の並び
	globalFlags = []globalFlag{helpFlag, versionFlag}
)

// commandSpecOf は subcommand の宣言。宣言に無い名前は呼び出し側の誤りなので panic する。
func commandSpecOf(name string) commandSpec {
	for _, c := range commandSpecs {
		if c.name == name {
			return c
		}
	}
	panic(fmt.Sprintf("subcommand %s の宣言が無い", name))
}

// parsedArgs は subcommand の引数を宣言で読んだもの。
type parsedArgs struct {
	spec   commandSpec
	values map[string]string
	set    map[string]bool
	// operands は spec.operands の位置引数の値。spec.operands と同じ並び
	operands []string
	// workflowPath は WORKFLOW。省けば defaultWorkflowFile
	workflowPath string
}

// value は値付き flag の値。渡されていなければ空。宣言に無い値付き flag の名前は呼び出し側の誤りなので panic する。
func (a parsedArgs) value(name string) string {
	if f, ok := a.spec.flag(name); !ok || !f.takesValue() {
		panic(fmt.Sprintf("%s に値付き flag %s の宣言が無い", a.spec.name, name))
	}
	return a.values[name]
}

// has は値を取らない flag が渡されたか。宣言に無い値を取らない flag の名前は呼び出し側の誤りなので panic する。
func (a parsedArgs) has(name string) bool {
	if f, ok := a.spec.flag(name); !ok || f.takesValue() {
		panic(fmt.Sprintf("%s に値を取らない flag %s の宣言が無い", a.spec.name, name))
	}
	return a.set[name]
}

// parse は宣言に従って引数を読む (formats.md §6・§7)。未知の flag・値の無い値付き flag の検査はここだけで行う。
// 値付き flag は `--name value` と `--name=value` を読み、値が無い (直後が `-` で始まるときを含む)・空・2 回書いたら誤り。
// 位置引数と flag は混ざってよい。
func (c commandSpec) parse(argv []string) (parsedArgs, error) {
	if c.arguments == undeclaredArguments || c.arguments == noArguments && len(c.flags) > 0 ||
		c.arguments != workflowArgument && len(c.operands) > 0 {
		panic(fmt.Sprintf("subcommand %s の引数の受け方の宣言が誤り", c.name))
	}
	a := parsedArgs{spec: c, values: map[string]string{}, set: map[string]bool{}}
	// 値付き flag を先に抜く。値として取った綴りが後段で flag として読まれないようにする
	var rest []string
	for i := 0; i < len(argv); i++ {
		name, value, inline := strings.Cut(argv[i], "=")
		f, ok := c.flag(name)
		if !ok || !f.takesValue() {
			rest = append(rest, argv[i])
			continue
		}
		if !inline {
			if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "-") {
				return a, fmt.Errorf("%s に値が要る", name)
			}
			i++
			value = argv[i]
		}
		if _, dup := a.values[name]; dup {
			return a, fmt.Errorf("%s を 2 回書いている", name)
		}
		if value == "" {
			return a, fmt.Errorf("%s の値が空", name)
		}
		a.values[name] = value
	}
	for _, f := range c.flags {
		if v, ok := a.values[f.name]; ok && f.check != nil {
			if err := f.check(v); err != nil {
				return a, err
			}
		}
	}
	if c.check != nil {
		if err := c.check(a); err != nil {
			return a, err
		}
	}
	for _, arg := range rest {
		f, known := c.flag(arg)
		switch {
		case c.arguments == noArguments:
			return a, fmt.Errorf("引数が多い: %s", arg)
		case known:
			a.set[f.name] = true
		case strings.HasPrefix(arg, "-"):
			return a, fmt.Errorf("未知の flag: %s", arg)
		case len(a.operands) < len(c.operands):
			a.operands = append(a.operands, arg)
		case a.workflowPath == "":
			a.workflowPath = arg
		default:
			return a, fmt.Errorf("引数が多い: %s", arg)
		}
	}
	if len(a.operands) < len(c.operands) {
		return a, fmt.Errorf("%s が要る", c.operands[len(a.operands)])
	}
	for _, name := range c.required {
		if !a.set[name] {
			return a, fmt.Errorf("%s が要る", name)
		}
	}
	if a.workflowPath == "" {
		a.workflowPath = defaultWorkflowFile
	}
	return a, nil
}

func (c commandSpec) flag(name string) (flagSpec, bool) {
	for _, f := range c.flags {
		if f.name == name {
			return f, true
		}
	}
	return flagSpec{}, false
}

// positiveInt は値が 1 以上の整数かを検査する。
func positiveInt(name string) func(string) error {
	return func(value string) error {
		if n, err := strconv.Atoi(value); err != nil || n < 1 {
			return fmt.Errorf("%s は 1 以上の整数 (%q)", name, value)
		}
		return nil
	}
}

// usageForms は usage の Usage 節の 3 行目からの (2 行目は main.go が手で書く run の形)、command の代わりに flag を書く形。1 行目と桁を揃える字下げで始める。
func usageForms() string {
	var b strings.Builder
	for _, g := range globalFlags {
		fmt.Fprintf(&b, "       claude-dispatcher %s\n", strings.Join(g.allSpellings(), " | "))
	}
	return b.String()
}

// options は usage の Options 節の行。宣言の並びで、説明の頭に subcommand か `command の代わり` を書く。
func options() string {
	type line struct{ label, help string }
	var lines []line
	for _, c := range commandSpecs {
		for _, f := range c.flags {
			label := f.name
			if f.takesValue() {
				label += " " + f.value
			}
			lines = append(lines, line{label, "(" + c.name + ") " + f.help})
		}
	}
	for _, g := range globalFlags {
		lines = append(lines, line{strings.Join(g.spellings, ", "), "(command の代わり) " + g.help})
	}
	width := 0
	for _, l := range lines {
		width = max(width, len(l.label))
	}
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "  %-*s   %s\n", width, l.label, l.help)
	}
	return b.String()
}
