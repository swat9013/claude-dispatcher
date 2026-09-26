// Package setup は `claude-dispatcher setup <project>` (formats.md §11) を進める。
//
// 段 (config の雛形 → config の検査 → label → 試運転 → crontab) を順に進め、済んでいる段は何もせずに通る
// (再実行で同じ終状態に収束する)。外 (tracker の label・ユーザーの crontab) へ書くのは、書く中身を示して導入者の承認を
// 得た後だけ。Claude Code の settings は書かない。
package setup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/config"
	"github.com/swat9013/claude-dispatcher/internal/crontab"
	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/paths"
)

// exit code (formats.md §11)。試運転が落ちたときは試運転の exit code をそのまま返す
const (
	exitDone    = 0 // crontab に tick 行がある状態で終わった
	exitStopped = 1 // 途中で止まった (雛形を書いた / 承認されなかった / 外部 CLI の失敗 / 既存の tick 行と食い違う)
	exitConfig  = 2
)

// Options は setup の入力。Gh / Git / Crontab / DryRun は外部 CLI の起動口。
type Options struct {
	Project paths.Project
	Home    string
	// Clone は実装 repo の clone (cwd)。crontab の行の cd 先
	Clone string
	// Self は claude-dispatcher の絶対 path。crontab の行に埋める
	Self   string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	Gh      func(config.Config) (github.Runner, error)
	Git     func(args ...string) (string, error)
	Crontab func() (crontab.Client, error)
	// DryRun は `claude-dispatcher <args>` を撃ち、出力を Stdout / Stderr へ流して exit code を返す
	DryRun func(args ...string) int
}

type run struct {
	o       Options
	answers *bufio.Reader
}

// Run は段を順に進めて exit code を返す。
func Run(o Options) int {
	r := &run{o: o, answers: bufio.NewReader(o.Stdin)}
	if stopped, exit := r.ensureConfig(); stopped {
		return exit
	}
	cfg, err := config.Load(o.Project.ConfigFile(), o.Home)
	if err != nil {
		fmt.Fprintln(o.Stderr, err)
		return exitConfig
	}
	if exit := r.ensureLabels(cfg); exit != exitDone {
		return exit
	}
	for _, args := range [][]string{
		{"tick", o.Project.Name, "--dry-run"},
		{"tick", o.Project.Name, "--dry-run", "--cron-env"},
	} {
		r.say("試運転: claude-dispatcher %s", strings.Join(args, " "))
		if exit := o.DryRun(args...); exit != 0 {
			fmt.Fprintf(o.Stderr, "試運転が落ちた (exit %d)。上の行が名指しするものを直して setup を撃ち直す\n", exit)
			return exit
		}
	}
	return r.ensureCrontab()
}

func (r *run) say(format string, args ...any) { fmt.Fprintf(r.o.Stdout, format+"\n", args...) }

func (r *run) fail(format string, args ...any) int {
	fmt.Fprintf(r.o.Stderr, format+"\n", args...)
	return exitStopped
}

// approve は question を示して stdin から答えを 1 行読む。`y` / `yes` だけを承認とする (空行・EOF は承認なし)。
func (r *run) approve(question string) bool {
	fmt.Fprintf(r.o.Stdout, "%s [y/N] ", question)
	line, err := r.answers.ReadString('\n')
	if err != nil {
		fmt.Fprintln(r.o.Stdout) // 端末の無い実行でも次の出力が同じ行に続かないように
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// --- 1. config の雛形と state dir ---

// ensureConfig は config.toml が無ければ雛形と state dir を作って止まる。あれば上書きせず、state dir だけを揃える。
func (r *run) ensureConfig() (stopped bool, exit int) {
	file := r.o.Project.ConfigFile()
	_, err := os.Stat(file)
	switch {
	case err == nil:
		if err := os.MkdirAll(r.o.Project.StateDir, 0o755); err != nil {
			return true, r.fail("state dir を作れない: %v", err)
		}
		return false, exitDone
	case !errors.Is(err, os.ErrNotExist):
		return true, r.fail("宣言 config を確かめられない: %s (%v)", file, err)
	}
	for _, dir := range []string{r.o.Project.ConfigDir, r.o.Project.StateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return true, r.fail("置き場を作れない: %v", err)
		}
	}
	// O_EXCL: 確かめてから書くまでの間に置かれた config も上書きしない
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return true, r.fail("宣言 config の雛形を書けない: %v", err)
	}
	_, err = fmt.Fprintf(f, template, r.originRepo())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return true, r.fail("宣言 config の雛形を書けない: %v", err)
	}
	r.say("宣言 config の雛形と state dir を作った: %s", file)
	r.say("置き場・着手可 label・並列上限を確かめて直してから、もう一度 `claude-dispatcher setup %s` を撃つ", r.o.Project.Name)
	return true, exitStopped
}

const template = `# claude-dispatcher の宣言 config (形式は docs/design/formats.md §2)

[issue]
repo = %q
ready_label = "ready-for-agent"
# triage_label = "needs-triage"

# CL 置き場が issue 置き場と別のときだけ書く
# [cl]
# repo = "owner/other"

[limits]
max_wip = 1
`

var githubRemote = regexp.MustCompile(`github\.com[:/]([^/\s]+)/([^/\s]+?)(\.git)?/?$`)

// originRepo は cwd の clone の origin を owner/name で返す。読めなければ "" (config の検査が埋め忘れとして落とす)。
func (r *run) originRepo() string {
	out, err := r.o.Git("remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	m := githubRemote.FindStringSubmatch(strings.TrimSpace(out))
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

// --- 3. label ---

type label struct{ name, color, description string }

func requiredLabels(cfg config.Config) []label {
	labels := []label{
		{config.WIPLabel, "fbca04", "dispatcher: worker が着手中"},
		{config.HumanLabel, "d93f0b", "dispatcher: 人の判断待ち"},
		{cfg.ReadyLabel, "0e8a16", "dispatcher: 着手可"},
	}
	if cfg.TriageLabel != "" {
		labels = append(labels, label{cfg.TriageLabel, "ededed", "dispatcher: worker の起票。triage 待ち"})
	}
	return labels
}

func (r *run) ensureLabels(cfg config.Config) int {
	gh, err := r.o.Gh(cfg)
	if err != nil {
		return r.fail("gh を撃てない: %v", err)
	}
	var missing []label
	for _, l := range requiredLabels(cfg) {
		exists, err := github.LabelExists(gh, cfg.IssueRepo, l.name)
		if err != nil {
			return r.fail("label を確かめられない: %v", err)
		}
		if !exists {
			missing = append(missing, l)
		}
	}
	if len(missing) == 0 {
		r.say("label: 揃っている")
		return exitDone
	}
	var names, commands []string
	for _, l := range missing {
		names = append(names, l.name)
		commands = append(commands, fmt.Sprintf("gh label create %q -R %s --color %s --description %q", l.name, cfg.IssueRepo, l.color, l.description))
	}
	r.say("issue 置き場 %s に無い label: %s", cfg.IssueRepo, strings.Join(names, ", "))
	if !r.approve("作ってよいか") {
		r.say("作っていない。自分で作るなら:")
		for _, c := range commands {
			r.say("  %s", c)
		}
		return exitStopped
	}
	for _, l := range missing {
		if err := github.CreateLabel(gh, cfg.IssueRepo, l.name, l.color, l.description); err != nil {
			return r.fail("label %s を作れない: %v", l.name, err)
		}
		r.say("label を作った: %s", l.name)
	}
	return exitDone
}

// --- 5. crontab ---

func (r *run) ensureCrontab() int {
	client, err := r.o.Crontab()
	if err != nil {
		return r.fail("crontab を撃てない: %v", err)
	}
	table, err := client.Read()
	if err != nil {
		return r.fail("crontab を読めない: %v", err)
	}
	line := crontab.Line(r.o.Clone, r.o.Self, r.o.Project.Name, r.o.Project.CronLog())
	existing := crontab.TickLines(table, r.o.Project.Name)
	switch {
	case slices.Contains(existing, line):
		r.say("crontab: 登録済み\n  %s", line)
		return exitDone
	case len(existing) > 0:
		// 周期や cd 先を人が変えた行かもしれない。黙って置き換えない
		r.say("crontab に %s の tick 行が別の形である。置き換えない — 変えるなら `crontab -e` で自分で直す", r.o.Project.Name)
		for _, e := range existing {
			r.say("  現行:   %s", e)
		}
		r.say("  組んだ行: %s", line)
		return exitStopped
	}
	r.say("crontab に足す行:\n  %s", line)
	if !r.approve("crontab に登録してよいか") {
		r.say("登録していない。自分で足すなら `crontab -e` で上の行を足す")
		return exitStopped
	}
	if err := client.Write(crontab.Append(table, line)); err != nil {
		return r.fail("crontab に書けない: %v。`crontab -e` で上の行を自分で足す", err)
	}
	after, err := client.Read()
	if err != nil || !slices.Contains(crontab.TickLines(after, r.o.Project.Name), line) {
		return r.fail("crontab に登録した行が見当たらない (%v)。`crontab -l` で確かめる", err)
	}
	r.say("crontab に登録した。周期の 2 倍待って、log.jsonl の最終行の ts が進むかで最初の tick を確かめる (`claude-dispatcher doctor %s`)", r.o.Project.Name)
	return exitDone
}
