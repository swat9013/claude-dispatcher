// Package paths は config と state の置き場 (docs/design/formats.md §1) を解決する。
package paths

import (
	"path/filepath"
	"regexp"
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

func (p Project) WorkerLog(issue int, stem string) string {
	return filepath.Join(p.StateDir, "workers", strconv.Itoa(issue)+"-"+stem+".log")
}
