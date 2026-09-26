// Package paths は config と state の置き場 (docs/design/formats.md §1) を解決する。
package paths

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

const appName = "claude-dispatcher"

var projectNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidProjectName は project 名が置き場の綴りとして使えるかを返す。
func ValidProjectName(name string) bool {
	return projectNamePattern.MatchString(name) && name != "." && name != ".."
}

// Roots は config root と state root。
type Roots struct {
	Config string
	State  string
}

// ResolveRoots は XDG_CONFIG_HOME / XDG_STATE_HOME を、無ければ HOME の既定を使う。macOS でも XDG に揃える。
func ResolveRoots(getenv func(string) string) Roots {
	home := getenv("HOME")
	config := getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	state := getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	return Roots{Config: filepath.Join(config, appName), State: filepath.Join(state, appName)}
}

// Projects は config root の下で config.toml を持つ dir 名を昇順で返す (formats.md §9)。root が無ければ空。
func (r Roots) Projects() ([]string, error) {
	entries, err := os.ReadDir(r.Config)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	projects := []string{}
	for _, e := range entries {
		// dotfiles 等から symlink で置いた project dir も数えるので、DirEntry の種別ではなく config.toml を stat で見る
		if !ValidProjectName(e.Name()) {
			continue
		}
		if info, err := os.Stat(r.Project(e.Name()).ConfigFile()); err == nil && !info.IsDir() {
			projects = append(projects, e.Name())
		}
	}
	sort.Strings(projects)
	return projects, nil
}

// Project は 1 project の置き場。
type Project struct {
	Name      string
	ConfigDir string
	StateDir  string
}

func (r Roots) Project(name string) Project {
	return Project{Name: name, ConfigDir: filepath.Join(r.Config, name), StateDir: filepath.Join(r.State, name)}
}

func (p Project) ConfigFile() string { return filepath.Join(p.ConfigDir, "config.toml") }
func (p Project) LogFile() string    { return filepath.Join(p.StateDir, "log.jsonl") }
func (p Project) CronLog() string    { return filepath.Join(p.StateDir, "cron.log") }
func (p Project) LockFile() string   { return filepath.Join(p.StateDir, "tick.lock") }
func (p Project) MarkerFile() string { return filepath.Join(p.StateDir, "config-verified") }

func (p Project) InstructionFile(stem string) string {
	return filepath.Join(p.StateDir, "instructions", stem+".json")
}

func (p Project) DecisionsFile(stem string) string {
	return filepath.Join(p.StateDir, "decisions", stem+".json")
}

func (p Project) OrchestratorLog(stem string) string {
	return filepath.Join(p.StateDir, "decisions", stem+".orchestrator.log")
}

// HandoffFilePattern は orchestrator が人へ返すときに引き渡し本文を書く先。issue 番号の位置を `<N>` で書く
// (書くのは orchestrator で、番号は orchestrator が決める)。
func (p Project) HandoffFilePattern(stem string) string {
	return filepath.Join(p.StateDir, "decisions", stem+".handoff-<N>.md")
}

// WorkersDir は worker log の置き場
func (p Project) WorkersDir() string { return filepath.Join(p.StateDir, "workers") }

func (p Project) WorkerLog(issue int, stem string) string {
	return filepath.Join(p.WorkersDir(), strconv.Itoa(issue)+"-"+stem+".log")
}

// ProjectDoc / RootsDoc は `paths --json` の形 (formats.md §9。外部の読み手を持つ公開契約)。
type ProjectDoc struct {
	Project    string `json:"project"`
	ConfigFile string `json:"config_file"`
	StateDir   string `json:"state_dir"`
	LogFile    string `json:"log_file"`
}

type RootsDoc struct {
	ConfigRoot string   `json:"config_root"`
	StateRoot  string   `json:"state_root"`
	Projects   []string `json:"projects"`
}

func (p Project) Doc() ProjectDoc {
	return ProjectDoc{Project: p.Name, ConfigFile: p.ConfigFile(), StateDir: p.StateDir, LogFile: p.LogFile()}
}

func (r Roots) Doc(projects []string) RootsDoc {
	return RootsDoc{ConfigRoot: r.Config, StateRoot: r.State, Projects: projects}
}
