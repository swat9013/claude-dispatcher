// Package precheck は事前検査 (formats.md §2.9): trigger ごとに、action の先頭の `/名前` が呼べる skill か command かを、
// plugin・repo の `.claude/`・`~/.claude/` の file から確かめる。
package precheck

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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

// commands は manifest の `commands` が挙げる path の command。挙げていなければ `commands/`。
func (p plugin) commands() []string {
	listed := paths(p.manifest.Commands)
	if len(listed) == 0 {
		return commandsUnder(filepath.Join(p.dir, "commands"))
	}
	var list []string
	for _, path := range listed {
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
	list := installed(clone, home)
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

// installed は `~/.claude/plugins/installed_plugins.json` が挙げる plugin。scope が project / local のものは、
// projectPath が clone のものだけ。file が無いか読めなければ無い。
func installed(clone, home string) []plugin {
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		Plugins map[string][]struct {
			Scope       string `json:"scope"`
			InstallPath string `json:"installPath"`
			ProjectPath string `json:"projectPath"`
		} `json:"plugins"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	var list []plugin
	for key, entries := range doc.Plugins {
		name, _, _ := strings.Cut(key, "@")
		for _, e := range entries {
			if e.InstallPath == "" || (e.Scope == "project" || e.Scope == "local") && filepath.Clean(e.ProjectPath) != filepath.Clean(clone) {
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

// frontMatterName は markdown の先頭の `---` で囲んだ front matter から `name:` の値を読む。無ければ ""。
func frontMatterName(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	lines := bufio.NewScanner(f)
	if !lines.Scan() || strings.TrimSpace(lines.Text()) != "---" {
		return ""
	}
	for lines.Scan() {
		line := lines.Text()
		if strings.TrimSpace(line) == "---" {
			return ""
		}
		if value, ok := strings.CutPrefix(line, "name:"); ok {
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return ""
}

// commandsUnder は dir の下の `*.md` の command の名前。dir からの相対 path の `/` を `:` にする。
func commandsUnder(dir string) []string {
	var list []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, _ := filepath.Rel(dir, strings.TrimSuffix(path, ".md"))
		list = append(list, strings.ReplaceAll(filepath.ToSlash(rel), "/", ":"))
		return nil
	})
	return list
}
