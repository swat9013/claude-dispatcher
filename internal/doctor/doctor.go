// Package doctor は `claude-dispatcher doctor <project>` (formats.md §12) の検査を回す。
//
// 導入の充足を 1 項目 1 行で示し、最後に Claude Code の settings に要る entry を示す。何も書かない
// (state dir・config・settings・外部 store のどれにも)。
package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/config"
	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/paths"
	"github.com/swat9013/claude-dispatcher/internal/plugin"
	"github.com/swat9013/claude-dispatcher/internal/termtext"
	"github.com/swat9013/claude-dispatcher/internal/tick"
	"github.com/swat9013/claude-dispatcher/internal/ticklog"
	"github.com/swat9013/claude-dispatcher/internal/version"
)

// Options は doctor の入力。Gh は外部 CLI の起動口、DryRun は tick の 1 回分の試運転。
type Options struct {
	Project paths.Project
	Home    string
	// Clone は実装 repo の clone (cwd)。plugin の project scope の照合に使う
	Clone string
	// Env は依存 CLI の PATH を解決した後の env
	Env    []string
	Stdout io.Writer

	Gh func(config.Config) (github.Runner, error)
	// DryRun は tick の 1 回分の試運転 (`tick <project> --dry-run` と同じ) を process の中で回し、stdout と stderr を
	// 合わせた出力と exit code を返す
	DryRun func() (output string, exit int)
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
	fmt.Fprintf(r.out, "%s  %s  %s\n", mark, termtext.Pad(item, 10), fmt.Sprintf(format, args...))
}

// Run は検査を回し、NG が 1 つでもあれば 1 を返す。
func Run(o Options) int {
	r := &report{out: o.Stdout}
	// 導入検査の結果と一緒に版を貼れるように先頭に置く (情報。判定しない)
	r.line(markInfo, "版", "%s", version.Line())
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
	checkDryRun(r, o, cfgErr)
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
	var names []string
	for _, repo := range cfg.Repos() {
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

	var required, missing []string
	for _, label := range cfg.Labels() {
		required = append(required, label.Name)
		exists, err := github.LabelExists(gh, cfg.IssueRepo, label.Name)
		if err != nil {
			r.line(markNG, "label", "確かめられない: %v", err)
			return
		}
		if !exists {
			missing = append(missing, label.Name)
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
	if err != nil {
		problems = append(problems, err.Error())
	}
	for _, c := range tick.Conditions {
		if file := install.Playbook(c.Playbook); !isFile(file) {
			problems = append(problems, fmt.Sprintf("条件 %s の playbook が無い: %s", c.Name, file))
		}
	}
	switch {
	case len(problems) > 0:
		r.line(markNG, "playbook", "%s", strings.Join(problems, " / "))
	case len(starts) == 0:
		// 母集合が空でも tick は動く (start を出さないだけ) ので情報にする (formats.md §12)
		r.line(markInfo, "playbook", "start の選定母集合 (metadata.deliverable: cl の playbook) が 0 本 — tick は start を出さない。条件 %d 本は在る", len(tick.Conditions))
	default:
		r.line(markOK, "playbook", "start %d 本 + 条件 %d 本", len(starts), len(tick.Conditions))
	}

	if err := install.RequirePrincipleIndex(); err != nil {
		r.line(markNG, "原則索引", "%v", err)
	} else {
		r.line(markOK, "原則索引", "%s", install.PrincipleIndex())
	}
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// checkDryRun は tick の 1 回分の試運転を回す。試運転は state dir に何も書かない (system.md §9)。
// config が読めなければ試運転も同じところで落ちるので回さない。
func checkDryRun(r *report, o Options, cfgErr error) {
	if cfgErr != nil {
		r.line(markInfo, "試運転", "config を直してから確かめる")
		return
	}
	out, exit := o.DryRun()
	out = strings.TrimSpace(out)
	if exit != 0 {
		r.line(markNG, "試運転", "`claude-dispatcher tick %s --dry-run` と同じ試運転が exit %d で落ちた:\n  %s", o.Project.Name, exit, strings.ReplaceAll(out, "\n", "\n  "))
		return
	}
	r.line(markOK, "試運転", "%s", out)
}

func checkLastTick(r *report, project paths.Project) {
	log, broken, err := ticklog.Read(project.LogFile())
	if err != nil {
		r.line(markInfo, "最終 tick", "log.jsonl を読めない: %v", err)
		return
	}
	var skipped string
	if broken > 0 {
		skipped = fmt.Sprintf(" (読めない %d 行を飛ばした)", broken)
	}
	last, ok := ticklog.Last(log.Ticks)
	if !ok {
		r.line(markInfo, "最終 tick", "なし%s", skipped)
		return
	}
	r.line(markInfo, "最終 tick", "%s %s%s", ticklog.ShortTS(last.TS), last.Result, skipped)
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
