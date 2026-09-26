// Package doctor は `claude-dispatcher doctor <project>` (formats.md §12) の検査を回す。
//
// 導入の充足を 1 項目 1 行で示し、最後に Claude Code の settings に要る entry を示す。何も書かない
// (state dir・config・settings・crontab・外部 store のどれにも)。
package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/config"
	"github.com/swat9013/claude-dispatcher/internal/crontab"
	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/paths"
	"github.com/swat9013/claude-dispatcher/internal/plugin"
	"github.com/swat9013/claude-dispatcher/internal/tick"
	"github.com/swat9013/claude-dispatcher/internal/ticklog"
)

// Options は doctor の入力。Gh / Crontab は外部 CLI の起動口。
type Options struct {
	Project paths.Project
	Home    string
	// Clone は実装 repo の clone (cwd)。plugin の project scope を照合する
	Clone string
	// Env は依存 CLI の PATH を解決した後の env
	Env    []string
	Stdout io.Writer

	Gh      func(config.Config) (github.Runner, error)
	Crontab func() (crontab.Client, error)
}

// 行の先頭の印 (formats.md §12)
const (
	markOK   = "ok"
	markNG   = "NG"
	markInfo = "--"
)

type report struct {
	out io.Writer
	ng  bool
}

func (r *report) line(mark, item, format string, args ...any) {
	if mark == markNG {
		r.ng = true
	}
	pad := max(0, 10-displayWidth(item))
	fmt.Fprintf(r.out, "%s  %s%s  %s\n", mark, item, strings.Repeat(" ", pad), fmt.Sprintf(format, args...))
}

// displayWidth は端末での表示幅。項目名の日本語 (全角) は 2 桁で数える
func displayWidth(s string) int {
	width := 0
	for _, r := range s {
		if r >= 0x2E80 {
			width += 2
		} else {
			width++
		}
	}
	return width
}

// Run は検査を回し、NG が 1 つでもあれば 1 を返す。
func Run(o Options) int {
	r := &report{out: o.Stdout}
	cfg, cfgErr := config.Load(o.Project.ConfigFile(), o.Home)
	if cfgErr != nil {
		r.line(markNG, "config", "%v", cfgErr)
	} else {
		r.line(markOK, "config", "%s", cfg.Path)
	}
	checkStateDir(r, o.Project)
	checkDeps(r, o.Env)
	checkTracker(r, o, cfg, cfgErr)
	checkPlugin(r, o)
	checkCrontab(r, o)
	checkLastTick(r, o.Project)
	showSettings(o.Stdout, o.Project)
	if r.ng {
		return 1
	}
	return 0
}

func checkStateDir(r *report, project paths.Project) {
	if info, err := os.Stat(project.StateDir); err != nil || !info.IsDir() {
		r.line(markNG, "state dir", "%s が無い — `claude-dispatcher setup %s` で作る", project.StateDir, project.Name)
		return
	}
	r.line(markOK, "state dir", "%s", project.StateDir)
}

func checkDeps(r *report, env []string) {
	var found, missing []string
	for _, name := range deps.Names {
		if path, err := deps.Lookup(name, env); err == nil {
			found = append(found, path)
		} else {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		r.line(markNG, "依存 CLI", "%s が PATH にも自己解決の置き場にも無い (PATH=%s)", strings.Join(missing, " / "), deps.Getenv(env, "PATH"))
		return
	}
	r.line(markOK, "依存 CLI", "%s", strings.Join(found, " "))
}

// checkTracker は置き場 repo と label を gh で確かめる。config が読めなければ確かめられない (情報として示す)。
func checkTracker(r *report, o Options, cfg config.Config, cfgErr error) {
	if cfgErr != nil {
		for _, item := range []string{"置き場", "label"} {
			r.line(markInfo, item, "config を直してから確かめる")
		}
		return
	}
	gh, err := o.Gh(cfg)
	if err != nil {
		r.line(markNG, "置き場", "gh を撃てない: %v", err)
		r.line(markInfo, "label", "gh を撃てないので確かめられない")
		return
	}
	repos := []config.Repo{cfg.IssueRepo}
	if cfg.CLRepo != cfg.IssueRepo {
		repos = append(repos, cfg.CLRepo)
	}
	var names []string
	for _, repo := range repos {
		if err := github.RepoExists(gh, repo); err != nil {
			if github.IsNotFound(err) {
				r.line(markNG, "置き場", "%s が gh から見えない (綴りか権限を確かめる)", repo)
			} else {
				r.line(markNG, "置き場", "%s を確かめられない: %v", repo, err)
			}
			r.line(markInfo, "label", "置き場を確かめてから確かめる")
			return
		}
		names = append(names, repo.String())
	}
	r.line(markOK, "置き場", "%s", strings.Join(names, " / "))

	required := []string{config.WIPLabel, config.HumanLabel, cfg.ReadyLabel}
	if cfg.TriageLabel != "" {
		required = append(required, cfg.TriageLabel)
	}
	var missing []string
	for _, name := range required {
		exists, err := github.LabelExists(gh, cfg.IssueRepo, name)
		if err != nil {
			r.line(markNG, "label", "確かめられない: %v", err)
			return
		}
		if !exists {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		r.line(markNG, "label", "置き場 %s に %s が無い — `claude-dispatcher setup %s` で作る", cfg.IssueRepo, strings.Join(missing, ", "), o.Project.Name)
		return
	}
	r.line(markOK, "label", "%s", strings.Join(required, ", "))
}

// checkPlugin は plugin の解決 (system.md §11 の選択順と重複時の停止) と、worker に渡す file の実在を確かめる。
func checkPlugin(r *report, o Options) {
	install, err := plugin.Resolve(o.Home, o.Clone)
	if err != nil {
		r.line(markNG, "plugin", "%v", err)
		for _, item := range []string{"playbook", "原則索引"} {
			r.line(markInfo, item, "plugin を解決してから確かめる")
		}
		return
	}
	r.line(markOK, "plugin", "%s", install.Path)

	var problems []string
	starts, err := install.StartPlaybooks()
	switch {
	case err != nil:
		problems = append(problems, err.Error())
	case len(starts) == 0:
		problems = append(problems, "start の選定母集合 (metadata.deliverable: cl の playbook) が 1 本も無い")
	}
	for _, c := range tick.Conditions {
		if file := install.Playbook(c.Playbook); !isFile(file) {
			problems = append(problems, fmt.Sprintf("条件 %s の playbook が無い: %s", c.Name, file))
		}
	}
	if len(problems) > 0 {
		r.line(markNG, "playbook", "%s", strings.Join(problems, " / "))
	} else {
		r.line(markOK, "playbook", "start %d 本 + 条件 %d 本", len(starts), len(tick.Conditions))
	}

	if file := install.PrincipleIndex(); !isFile(file) {
		r.line(markNG, "原則索引", "無い: %s", file)
	} else {
		r.line(markOK, "原則索引", "%s", file)
	}
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func checkCrontab(r *report, o Options) {
	client, err := o.Crontab()
	var table string
	if err == nil {
		table, err = client.Read()
	}
	if err != nil {
		r.line(markNG, "crontab", "読めない: %v", err)
		return
	}
	lines := crontab.TickLines(table, o.Project.Name)
	if len(lines) == 0 {
		r.line(markNG, "crontab", "%s の tick 行が無い — `claude-dispatcher setup %s` で登録する", o.Project.Name, o.Project.Name)
		return
	}
	r.line(markOK, "crontab", "%s", strings.Join(lines, " / "))
}

func checkLastTick(r *report, project paths.Project) {
	lines, _, err := ticklog.Read(project.LogFile())
	if err != nil {
		r.line(markInfo, "最終 tick", "log.jsonl を読めない: %v", err)
		return
	}
	last, ok := ticklog.Last(lines)
	if !ok {
		r.line(markInfo, "最終 tick", "なし")
		return
	}
	r.line(markInfo, "最終 tick", "%s %s", last.At.UTC().Format("2006-01-02T15:04:05Z"), last.Result)
}

// showSettings は Claude Code の settings に要る entry を示す (system.md §11)。settings は書かない。
func showSettings(out io.Writer, project paths.Project) {
	entries := map[string]any{"sandbox": map[string]any{
		// orchestrator が決定ファイルを state dir に書く
		"filesystem": map[string]any{"allowWrite": []string{project.StateDir}},
		// orchestrator / worker の tracker 操作と、セッション内から status / doctor を撃つときの gh が credential を読めるように
		"excludedCommands": []string{"gh", "claude-dispatcher"},
	}}
	raw, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		panic(err) // map と string の組なので落ちない
	}
	fmt.Fprintf(out, "\nClaude Code の settings に要る entry (doctor は書かない。settings.json に自分で足す):\n%s\n", raw)
}
