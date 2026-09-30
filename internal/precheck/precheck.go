// Package precheck は事前検査 (formats.md §2.9): trigger ごとに、action の先頭の `/名前` が呼べる skill か command かを、
// plugin・repo の `.claude/`・`~/.claude/` の file から確かめる。
package precheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// Problem は事前検査に落ちた trigger 1 つ。
type Problem struct {
	Trigger string
	Error   string
}

// Check は workflow 定義の trigger を宣言順に確かめ、落ちたものを返す。home は HOME (`~/.claude/` の親)。
func Check(def workflow.Definition, home string) []Problem {
	var problems []Problem
	var names map[string]bool
	for _, t := range def.Triggers {
		action := strings.TrimLeft(t.Action, " \t\r\n")
		switch {
		case strings.HasPrefix(action, "{{"):
			problems = append(problems, Problem{t.Name, "action が template 変数で始まるので、先頭の skill を確かめられない"})
		case strings.HasPrefix(action, "/"):
			name := strings.Fields(action)[0][1:]
			if names == nil {
				names = available(def.Dir, home, def.Claude.Args)
			}
			if !names[name] {
				problems = append(problems, Problem{t.Name, "action の先頭の /" + name + " が見つからない (plugin・repo の .claude・~/.claude の skill と command)"})
			}
		}
	}
	return problems
}

// available は呼べる skill と command の名前の組。
func available(clone, home string, claudeArgs []string) map[string]bool {
	names := map[string]bool{}
	add := func(prefix string, list []string) {
		for _, n := range list {
			names[prefix+n] = true
		}
	}
	for _, base := range []string{filepath.Join(clone, ".claude"), filepath.Join(home, ".claude")} {
		add("", skillsUnder(filepath.Join(base, "skills")))
		add("", commandsUnder(filepath.Join(base, "commands")))
	}
	for _, p := range plugins(clone, home, claudeArgs) {
		add(p.name+":", p.skills())
		add(p.name+":", p.commands())
	}
	return names
}

// plugin は plugin 1 つ。manifest は `.claude-plugin/plugin.json` の中身。
type plugin struct {
	dir      string
	name     string
	manifest manifest
}

type manifest struct {
	Name     string          `json:"name"`
	Skills   json.RawMessage `json:"skills"`
	Commands json.RawMessage `json:"commands"`
}

// readPlugin は dir の plugin を読む。manifest が無いか読めなければ fallback の名前で、既定の置き場だけを見る。
func readPlugin(dir, fallback string) plugin {
	p := plugin{dir: dir, name: fallback}
	raw, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	if err == nil && json.Unmarshal(raw, &p.manifest) == nil && p.manifest.Name != "" {
		p.name = p.manifest.Name
	}
	return p
}

// skills は plugin の `skills/` と、manifest の `skills` が挙げる path の skill。
func (p plugin) skills() []string {
	list := skillsUnder(filepath.Join(p.dir, "skills"))
	for _, path := range paths(p.manifest.Skills) {
		path = filepath.Join(p.dir, path)
		if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err == nil {
			list = append(list, skillName(path))
		} else {
			list = append(list, skillsUnder(path)...)
		}
	}
	return list
}

// commands は plugin の `commands/` と、manifest の `commands` が挙げる path の command。manifest の path が既定の置き場を
// 置き換えるか足すかは確かめられていないので、見つからないと取り違えない側 (足す) に倒す。
func (p plugin) commands() []string {
	list := commandsUnder(filepath.Join(p.dir, "commands"))
	for _, path := range paths(p.manifest.Commands) {
		path = filepath.Join(p.dir, path)
		if strings.HasSuffix(path, ".md") {
			list = append(list, strings.TrimSuffix(filepath.Base(path), ".md"))
		} else {
			list = append(list, commandsUnder(path)...)
		}
	}
	return list
}

// paths は manifest の path の項目 (文字列か文字列の列) を読む。それ以外の形 (対応表など) は読まない。
func paths(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil && one != "" {
		return []string{one}
	}
	var list []string
	_ = json.Unmarshal(raw, &list)
	return list
}

// plugins は事前検査が探す plugin: installed_plugins.json が挙げるもの・skills の dir に置いたもの・--plugin-dir。
func plugins(clone, home string, claudeArgs []string) []plugin {
	list := installed(home)
	for _, base := range []string{filepath.Join(clone, ".claude", "skills"), filepath.Join(home, ".claude", "skills")} {
		entries, _ := os.ReadDir(base)
		for _, e := range entries {
			dir := filepath.Join(base, e.Name())
			if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); err == nil {
				list = append(list, readPlugin(dir, e.Name()))
			}
		}
	}
	for i, arg := range claudeArgs {
		var dir string
		switch {
		case arg == "--plugin-dir" && i+1 < len(claudeArgs):
			dir = claudeArgs[i+1]
		case strings.HasPrefix(arg, "--plugin-dir="):
			dir = strings.TrimPrefix(arg, "--plugin-dir=")
		default:
			continue
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(clone, dir)
		}
		list = append(list, readPlugin(dir, filepath.Base(dir)))
	}
	return list
}

// installed は `~/.claude/plugins/installed_plugins.json` が挙げる plugin のうち、scope が user のもの。project / local の
// plugin は install した path (projectPath) でだけ読まれ、worker の cwd (workspace) では読まれないので数えない。file が
// 無いか読めなければ無い。
func installed(home string) []plugin {
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		Plugins map[string][]struct {
			Scope       string `json:"scope"`
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	var list []plugin
	for key, entries := range doc.Plugins {
		name, _, _ := strings.Cut(key, "@")
		for _, e := range entries {
			if e.InstallPath == "" || e.Scope != "user" {
				continue
			}
			list = append(list, readPlugin(e.InstallPath, name))
		}
	}
	return list
}

// skillsUnder は dir の直下の `<dir>/SKILL.md` の skill の名前。
func skillsUnder(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var list []string
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err == nil {
			list = append(list, skillName(path))
		}
	}
	return list
}

// skillName は skill の dir の `SKILL.md` の front matter の name。無ければ dir の名前。
func skillName(dir string) string {
	if name := frontMatterName(filepath.Join(dir, "SKILL.md")); name != "" {
		return name
	}
	return filepath.Base(dir)
}

// frontMatterName は markdown の先頭の `---` で囲んだ YAML の front matter の `name` を読む。無いか読めなければ ""。
func frontMatterName(file string) string {
	raw, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	rest, ok := strings.CutPrefix(strings.ReplaceAll(string(raw), "\r\n", "\n"), "---\n")
	if !ok {
		return ""
	}
	front, _, ok := strings.Cut(rest, "\n---")
	if !ok {
		return ""
	}
	var doc struct {
		Name string `yaml:"name"`
	}
	if yaml.Unmarshal([]byte(front), &doc) != nil {
		return ""
	}
	return doc.Name
}

// maxCommandDepth は command を探す dir の深さの上限 (symlink の輪で回り続けないため)
const maxCommandDepth = 8

// commandsUnder は dir の下の `*.md` の command の名前。dir からの相対 path の `/` を `:` にする。dotfiles で symlink に
// することが多いので、dir と途中の dir の symlink を辿る。
func commandsUnder(dir string) []string {
	var list []string
	var walk func(dir, prefix string, depth int)
	walk = func(dir, prefix string, depth int) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			path := filepath.Join(dir, e.Name())
			info, err := os.Stat(path)
			switch {
			case err != nil:
			case info.IsDir() && depth < maxCommandDepth:
				walk(path, prefix+e.Name()+":", depth+1)
			case !info.IsDir() && strings.HasSuffix(e.Name(), ".md"):
				list = append(list, prefix+strings.TrimSuffix(e.Name(), ".md"))
			}
		}
	}
	walk(dir, "", 0)
	return list
}
