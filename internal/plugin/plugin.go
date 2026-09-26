// Package plugin は plugin swat-skills の install 先を解決し、playbook と原則索引の絶対 path を返す
// (system.md §10 / §11)。中身は解釈しない。版の照合・互換検査もしない。
package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const name = "swat-skills"

// Install は解決した plugin の置き場 (installPath か skills-dir)。
type Install struct{ Path string }

func (i Install) PrincipleIndex() string {
	return filepath.Join(i.Path, "skills", "knowledge", "principle-index", "SKILL.md")
}

func (i Install) procedureDir() string { return filepath.Join(i.Path, "skills", "procedure") }

// Playbook は名前の playbook の SKILL.md の path。
func (i Install) Playbook(name string) string {
	return filepath.Join(i.procedureDir(), name, "SKILL.md")
}

type entry struct {
	Scope       string `json:"scope"`
	ProjectPath string `json:"projectPath"`
	InstallPath string `json:"installPath"`
}

// Resolve は installed_plugins.json から swat-skills の installPath を選ぶ。
// 選択順: projectPath が cwd と一致する project scope → user scope → ~/.claude/skills/swat-skills。
// 別 marketplace 由来の entry が複数あれば、どれを使うか決められないので止める。
func Resolve(home, cwd string) (Install, error) {
	install, err := resolvePath(home, cwd)
	if err != nil {
		return Install{}, err
	}
	if _, err := os.Stat(install.PrincipleIndex()); err != nil {
		return Install{}, fmt.Errorf("plugin %s の原則索引が無い: %s", name, install.PrincipleIndex())
	}
	return install, nil
}

func resolvePath(home, cwd string) (Install, error) {
	file := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	raw, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return Install{}, fmt.Errorf("%s を読めない: %w", file, err)
	}
	if err == nil {
		var doc struct {
			Plugins map[string][]entry `json:"plugins"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return Install{}, fmt.Errorf("%s を読めない: %w", file, err)
		}
		var keys []string
		for key := range doc.Plugins {
			if strings.HasPrefix(key, name+"@") {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		if len(keys) > 1 {
			return Install{}, fmt.Errorf("plugin %s が別 marketplace から複数 install されていて、どれを使うか決められない: %s (%s)",
				name, strings.Join(keys, ", "), file)
		}
		if len(keys) == 1 {
			if path, ok := chooseEntry(doc.Plugins[keys[0]], cwd); ok {
				return Install{Path: path}, nil
			}
		}
	}
	skillsDir := filepath.Join(home, ".claude", "skills", name)
	if _, err := os.Stat(skillsDir); err != nil {
		return Install{}, fmt.Errorf("plugin %s が見つからない (%s にも %s にも無い)", name, file, skillsDir)
	}
	return Install{Path: skillsDir}, nil
}

func chooseEntry(entries []entry, cwd string) (string, bool) {
	for _, e := range entries {
		if e.Scope == "project" && samePath(e.ProjectPath, cwd) {
			return e.InstallPath, true
		}
	}
	for _, e := range entries {
		if e.Scope == "user" {
			return e.InstallPath, true
		}
	}
	return "", false
}

func samePath(a, b string) bool {
	if a == "" {
		return false
	}
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// StartPlaybook は start の選定母集合の 1 本。
type StartPlaybook struct {
	Path         string `json:"path"`
	DispatchWhen string `json:"dispatch_when"`
}

// StartPlaybooks は frontmatter の metadata に `deliverable: cl` を持つ playbook を集める。
// 印があるのに `dispatch-when` が無い / frontmatter が読めない playbook は黙って外さず失敗にする。
func (i Install) StartPlaybooks() ([]StartPlaybook, error) {
	files, err := filepath.Glob(filepath.Join(i.procedureDir(), "playbook-*", "SKILL.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	playbooks := []StartPlaybook{}
	for _, file := range files {
		front, err := frontmatter(file)
		if err != nil {
			return nil, err
		}
		var meta struct {
			Metadata struct {
				Deliverable  string `yaml:"deliverable"`
				DispatchWhen string `yaml:"dispatch-when"`
			} `yaml:"metadata"`
		}
		if err := yaml.Unmarshal(front, &meta); err != nil {
			return nil, fmt.Errorf("frontmatter が読めない: %s: %w", file, err)
		}
		if meta.Metadata.Deliverable != "cl" {
			continue
		}
		if strings.TrimSpace(meta.Metadata.DispatchWhen) == "" {
			return nil, fmt.Errorf("metadata.deliverable: cl なのに dispatch-when が無い: %s", file)
		}
		playbooks = append(playbooks, StartPlaybook{Path: file, DispatchWhen: meta.Metadata.DispatchWhen})
	}
	if len(playbooks) == 0 {
		return nil, fmt.Errorf("start の選定母集合が空 (%s)", filepath.Join(i.procedureDir(), "playbook-*", "SKILL.md"))
	}
	return playbooks, nil
}

var errNoFrontmatter = errors.New("frontmatter が無い")

func frontmatter(file string) ([]byte, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("playbook を読めない: %s: %w", file, err)
	}
	rest, ok := bytes.CutPrefix(raw, []byte("---\n"))
	if !ok {
		return nil, fmt.Errorf("%w: %s", errNoFrontmatter, file)
	}
	head, _, ok := bytes.Cut(rest, []byte("\n---"))
	if !ok {
		return nil, fmt.Errorf("%w: %s", errNoFrontmatter, file)
	}
	return head, nil
}
