package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/provider"
)

// 引数の宣言の表。subcommand ごとの flag と位置引数をここに 1 回だけ書き、parser (parse) と usage の Options 節 (options) が
// 同じ表を読む。flag の一覧と規則の正本は formats.md §10 (と §6・§7) で、この表はそれに合わせる側。

// flagSpec は flag 1 つの宣言。
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
	// workflow は位置引数 WORKFLOW (workflow 定義の path) を 1 つ受けるか
	workflow bool
	// required は必ず付ける flag の名前
	required []string
	// check は値付き flag を読んだ後、他の引数を読む前の、flag の組み合わせの検査。nil なら検査しない
	check func(parsedArgs) error
}

// commandSpecs は subcommand の宣言。並びは usage の Options 節の並び。
var commandSpecs = []commandSpec{
	{name: "loop", workflow: true, flags: []flagSpec{
		{name: "--dry-run", help: "起動するはずの作業対象と trigger を示す。何も起動せず、何も書かない"},
		{name: "--max-concurrent", value: "<N>", help: "limits.max_concurrent を上書きする並列上限 (1 以上の整数)", check: positiveInt("--max-concurrent")},
	}},
	{name: "status", workflow: true},
	{name: "paths", workflow: true, required: []string{"--json"}, flags: []flagSpec{
		{name: "--json", help: "JSON で出す (paths では必須)"},
	}},
	{name: "setup", workflow: true, check: checkSetupFlags, flags: []flagSpec{
		{name: "--kind", value: "jira", help: "Jira を issue 置き場にする。--host と --repo と一緒に書く"},
		{name: "--host", value: "<site>", help: "Jira の site", field: provider.HostField},
		{name: "--repo", value: "<key>", help: "Jira の project key", field: provider.RepoField},
	}},
	{name: "doctor", workflow: true},
	{name: "guide"},
}

// globalFlags は command の代わりに書く flag の宣言。usage の Options 節の末尾に並ぶ。
var globalFlags = []flagSpec{
	{name: "-h, --help", help: "この一覧を出す。help でも同じ"},
	{name: "--version", help: "版を出す"},
}

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
	values map[string]string
	set    map[string]bool
	// workflowPath は WORKFLOW。省けば defaultWorkflowFile
	workflowPath string
}

// value は値付き flag の値。渡されていなければ空。
func (a parsedArgs) value(name string) string { return a.values[name] }

// has は値を取らない flag が渡されたか。
func (a parsedArgs) has(name string) bool { return a.set[name] }

// parse は宣言に従って引数を読む (formats.md §6・§7)。未知の flag・値の無い値付き flag の検査はここだけで行う。
// 値付き flag は `--name value` と `--name=value` を読み、値が無い (直後が `-` で始まるときを含む)・空・2 回書いたら誤り。
// 位置引数と flag は混ざってよい。
func (c commandSpec) parse(argv []string) (parsedArgs, error) {
	a := parsedArgs{values: map[string]string{}, set: map[string]bool{}}
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
		case !c.workflow && len(c.flags) == 0:
			// 何も受けない subcommand は、flag の綴りも余った引数として名指しする
			return a, fmt.Errorf("引数が多い: %s", arg)
		case known:
			a.set[f.name] = true
		case strings.HasPrefix(arg, "-"):
			return a, fmt.Errorf("未知の flag: %s", arg)
		case c.workflow && a.workflowPath == "":
			a.workflowPath = arg
		default:
			return a, fmt.Errorf("引数が多い: %s", arg)
		}
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
	for _, f := range globalFlags {
		lines = append(lines, line{f.name, "(command の代わり) " + f.help})
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
