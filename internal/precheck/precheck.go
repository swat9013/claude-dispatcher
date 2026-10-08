// Package precheck は事前検査 (formats.md §2.9): trigger ごとに、action の先頭の `/名前` が呼べる skill か command かを、
// plugin・repo の `.claude/`・`~/.claude/` の file から確かめる。
package precheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// Problem は事前検査に落ちた trigger 1 つ。
type Problem struct {
	Trigger string
	Error   string
}

// String は `trigger <名前>: <理由>` の 1 行。
func (p Problem) String() string { return fmt.Sprintf("trigger %s: %s", p.Trigger, p.Error) }

// Check は workflow 定義の trigger を宣言順に確かめ、落ちたものを返す。home は HOME (`~/.claude/` の親)。
func Check(def workflow.Definition, home string) []Problem {
	var problems []Problem
	var found *scan
	for _, t := range def.Triggers {
		action := strings.TrimLeft(t.Action, " \t\r\n")
		switch {
		case strings.HasPrefix(action, "{{"):
			problems = append(problems, Problem{t.Name, "action が template 変数で始まるので、先頭の skill を確かめられない"})
		case strings.HasPrefix(action, "/"):
			name := strings.Fields(action)[0][1:]
			if found == nil {
				found = available(def.Dir, home, def.Claude.Args)
			}
			if !found.names[name] {
				problems = append(problems, Problem{t.Name, "action の先頭の /" + name + " が見つからない (plugin・repo の .claude・~/.claude の skill と command)" + found.unreadableNote()})
			}
		}
	}
	return problems
}

// scan は置き場を探した結果: 呼べる名前と、読めなかった置き場 (無いのではなく、読み出しか解析に失敗したもの)。
type scan struct {
	names      map[string]bool
	unreadable []string
}

func (s *scan) add(names ...string) {
	for _, n := range names {
		s.names[n] = true
	}
}

// failed は、読めなかった置き場を覚える。無い (ErrNotExist) のは失敗に数えない。同じ dir に複数の経路から届くことがある
// (installed_plugins.json と --plugin-dir が同じ plugin を挙げる等) ので、同じ失敗は 1 度だけ覚える。
func (s *scan) failed(path string, err error) {
	if !unreadable(err) {
		return
	}
	if note := fmt.Sprintf("%s (%v)", path, err); !slices.Contains(s.unreadable, note) {
		s.unreadable = append(s.unreadable, note)
	}
}

// unreadable は、err が読めなかった置き場に数える失敗か (無い ErrNotExist は数えない)。
func unreadable(err error) bool {
	return err != nil && !errors.Is(err, fs.ErrNotExist)
}

// unreadableNote は、見つからない理由に添える、読めなかった置き場。無ければ ""。
func (s *scan) unreadableNote() string {
	if len(s.unreadable) == 0 {
		return ""
	}
	return " · 読めなかった置き場: " + strings.Join(s.unreadable, ", ")
}

// available は呼べる skill と command の名前を探す。
func available(clone, home string, claudeArgs []string) *scan {
	s := &scan{names: map[string]bool{}}
	var placed []string
	for _, base := range []string{filepath.Join(clone, ".claude"), filepath.Join(home, ".claude")} {
		skills, pluginDirs := s.skillsPlace(filepath.Join(base, "skills"))
		for _, k := range skills {
			s.add(k.name)
		}
		placed = append(placed, pluginDirs...)
		s.add(s.commandsUnder(filepath.Join(base, "commands"))...)
	}
	for _, p := range s.plugins(clone, home, claudeArgs, placed) {
		for _, k := range s.pluginSkills(p) {
			s.add(p.name + ":" + k.name)
			if k.named {
				// front matter に name を持つ plugin の skill は、接頭辞なしでも呼べる (同じ名前の command が他にあれば、そちらが
				// 呼ばれる。事前検査は名前が呼べることだけを見る)
				s.add(k.name)
			}
		}
		for _, c := range s.pluginCommands(p) {
			s.add(p.name + ":" + c)
		}
	}
	return s
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
func (s *scan) readPlugin(dir, fallback string) plugin {
	p := plugin{dir: dir, name: fallback}
	file := filepath.Join(dir, ".claude-plugin", "plugin.json")
	raw, err := os.ReadFile(file)
	if err != nil {
		s.failed(file, err)
		return p
	}
	if err := json.Unmarshal(raw, &p.manifest); err != nil {
		s.failed(file, err)
		return p
	}
	if p.manifest.Name != "" {
		p.name = p.manifest.Name
	}
	return p
}

// skill は skill 1 つ。named は、名前が front matter の name から来たか (dir の名前でなく)。
type skill struct {
	name  string
	named bool
}

// pluginSkills は plugin の `skills/`・manifest の `skills` が挙げる path の skill。どちらも無ければ、root の `SKILL.md` を
// 単一の skill として読む。
func (s *scan) pluginSkills(p plugin) []skill {
	skillsDir := filepath.Join(p.dir, "skills")
	if len(p.manifest.Skills) == 0 && !s.exists(skillsDir) {
		if s.exists(filepath.Join(p.dir, "SKILL.md")) {
			return []skill{s.readSkill(p.dir)}
		}
		return nil
	}
	list := s.skillsUnder(skillsDir)
	for _, path := range s.paths(p, "skills", p.manifest.Skills) {
		path = filepath.Join(p.dir, path)
		if s.exists(filepath.Join(path, "SKILL.md")) {
			list = append(list, s.readSkill(path))
		} else {
			list = append(list, s.skillsUnder(path)...)
		}
	}
	return list
}

// pluginCommands は plugin の command。manifest に `commands` があれば、それが `commands/` を置き換える: 対応表の形なら
// key が command の名前、path の形ならその path の command。
func (s *scan) pluginCommands(p plugin) []string {
	if len(p.manifest.Commands) == 0 {
		return s.commandsUnder(filepath.Join(p.dir, "commands"))
	}
	var named map[string]json.RawMessage
	if json.Unmarshal(p.manifest.Commands, &named) == nil {
		return slices.Collect(maps.Keys(named))
	}
	var list []string
	for _, path := range s.paths(p, "commands", p.manifest.Commands) {
		path = filepath.Join(p.dir, path)
		if strings.HasSuffix(path, ".md") {
			list = append(list, strings.TrimSuffix(filepath.Base(path), ".md"))
		} else {
			list = append(list, s.commandsUnder(path)...)
		}
	}
	return list
}

// paths は manifest の path の項目 (文字列か文字列の列) を読む。空の path は置き場にしない。それ以外の形は読めなかった
// 置き場に数える。
func (s *scan) paths(p plugin, key string, raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	var one string
	if json.Unmarshal(raw, &one) == nil {
		list = []string{one}
	} else if err := json.Unmarshal(raw, &list); err != nil {
		s.failed(filepath.Join(p.dir, ".claude-plugin", "plugin.json")+" の "+key, err)
	}
	return slices.DeleteFunc(list, func(path string) bool { return path == "" })
}

// exists は path があるかを確かめる。無い以外の失敗 (権限など) は読めなかった置き場に数える。
func (s *scan) exists(path string) bool {
	return s.stat(path) == nil
}

// stat は path を Stat し、無い以外の失敗 (権限など) を読めなかった置き場に数えて、Stat の失敗をそのまま返す。
func (s *scan) stat(path string) error {
	_, err := os.Stat(path)
	s.failed(path, err)
	return err
}

// skillsPlace は skills の置き場 (repo の `.claude/skills`・`~/.claude/skills`) を 1 度だけ読み、直下の dir ごとに skill の
// dir (`SKILL.md` を持つ) か plugin の dir (`.claude-plugin/plugin.json` を持つ) かに分ける。両方を持つ dir は両方に数える。
// plugin の dir は path で返し、plugins が他の plugin と並べて読む。
func (s *scan) skillsPlace(dir string) (skills []skill, pluginDirs []string) {
	for _, path := range s.dirsUnder(dir) {
		skillErr := s.stat(filepath.Join(path, "SKILL.md"))
		if skillErr == nil {
			skills = append(skills, s.readSkill(path))
		}
		manifest := filepath.Join(path, ".claude-plugin", "plugin.json")
		var found bool
		if unreadable(skillErr) {
			// SKILL.md を確かめられない dir (権限が無い等) は、plugin.json も同じ原因で確かめられないことが多い。同じ原因の失敗を
			// 別の path で並べないよう、失敗は SKILL.md の 1 件だけ覚え、plugin.json は確かめられたときだけ数える
			_, err := os.Stat(manifest)
			found = err == nil
		} else {
			found = s.exists(manifest)
		}
		if found {
			pluginDirs = append(pluginDirs, path)
		}
	}
	return skills, pluginDirs
}

// plugins は事前検査が探す plugin: installed_plugins.json が挙げるもの・skills の置き場に置いたもの (placed。skillsPlace が
// skill と一緒に見つけた dir)・--plugin-dir。
func (s *scan) plugins(clone, home string, claudeArgs, placed []string) []plugin {
	list := s.installed(clone, home)
	for _, dir := range placed {
		list = append(list, s.readPlugin(dir, filepath.Base(dir)))
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
		list = append(list, s.readPlugin(dir, filepath.Base(dir)))
	}
	return list
}

// installed は `~/.claude/plugins/installed_plugins.json` が挙げる plugin のうち、scope が user のものと、project で
// projectPath が clone のもの。project の plugin は commit された `.claude/settings.json` で有効になり、clone の worktree
// である workspace でも読まれる。local の plugin は commit しない `.claude/settings.local.json` で有効になり、workspace
// では読まれないので数えない。
func (s *scan) installed(clone, home string) []plugin {
	file := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	raw, err := os.ReadFile(file)
	if err != nil {
		s.failed(file, err)
		return nil
	}
	var doc struct {
		Plugins map[string][]struct {
			Scope       string `json:"scope"`
			InstallPath string `json:"installPath"`
			ProjectPath string `json:"projectPath"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		s.failed(file, err)
		return nil
	}
	var list []plugin
	for key, entries := range doc.Plugins {
		name, _, _ := strings.Cut(key, "@")
		for _, e := range entries {
			if e.InstallPath != "" && (e.Scope == "user" || e.Scope == "project" && e.ProjectPath == clone) {
				list = append(list, s.readPlugin(e.InstallPath, name))
			}
		}
	}
	return list
}

// skillsUnder は dir の直下の `<dir>/SKILL.md` の skill。
func (s *scan) skillsUnder(dir string) []skill {
	var list []skill
	for _, path := range s.dirsUnder(dir) {
		if s.exists(filepath.Join(path, "SKILL.md")) {
			list = append(list, s.readSkill(path))
		}
	}
	return list
}

// dirsUnder は dir の直下の dir (symlink は指す先が dir なら含める)。直下の通常の file (`.DS_Store`・README 等) は skill の
// dir にも plugin の dir にもなりえないので、読めなかった置き場に数えずに飛ばす。
func (s *scan) dirsUnder(dir string) []string {
	entries, err := os.ReadDir(dir)
	s.failed(dir, err)
	var list []string
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		info, err := os.Stat(path)
		if err != nil {
			s.failed(path, err)
			continue
		}
		if info.IsDir() {
			list = append(list, path)
		}
	}
	return list
}

// readSkill は skill の dir の `SKILL.md` の YAML の front matter の name を読む。無ければ dir の名前。
func (s *scan) readSkill(dir string) skill {
	file := filepath.Join(dir, "SKILL.md")
	raw, err := os.ReadFile(file)
	if err != nil {
		s.failed(file, err)
		return skill{name: filepath.Base(dir)}
	}
	rest, ok := strings.CutPrefix(strings.ReplaceAll(string(raw), "\r\n", "\n"), "---\n")
	front, _, closed := strings.Cut(rest, "\n---")
	if !ok || !closed {
		return skill{name: filepath.Base(dir)}
	}
	var doc struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal([]byte(front), &doc); err != nil {
		s.failed(file, err)
	}
	if doc.Name == "" {
		return skill{name: filepath.Base(dir)}
	}
	return skill{name: doc.Name, named: true}
}

// maxCommandDepth は command を探す dir の深さの上限 (symlink の輪で回り続けないため)
const maxCommandDepth = 8

// commandsUnder は dir の下の `*.md` の command の名前。dir からの相対 path の `/` を `:` にする。dotfiles で symlink に
// することが多いので、dir と途中の dir の symlink を辿る。
func (s *scan) commandsUnder(dir string) []string {
	var list []string
	var walk func(dir, prefix string, depth int)
	walk = func(dir, prefix string, depth int) {
		entries, err := os.ReadDir(dir)
		s.failed(dir, err)
		for _, e := range entries {
			path := filepath.Join(dir, e.Name())
			info, err := os.Stat(path)
			switch {
			case err != nil:
				s.failed(path, err)
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
