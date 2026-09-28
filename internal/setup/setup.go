// Package setup は `claude-dispatcher setup <project>` (formats.md §11) を進める。
//
// 段 (config の雛形 → config の検査 → label → 試運転 → loop の起動コマンド) を順に進め、済んでいる段は何もせずに通る
// (再実行で同じ終状態に収束する)。外 (tracker の label) へ書くのは、書く中身を示して導入者の承認を得た後だけ。
// Claude Code の settings は書かない。loop は起動しない。
package setup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/config"
	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/paths"
)

// exit code (formats.md §11)。試運転が落ちたときは試運転の exit code をそのまま返す。段は exitDone を「次の段へ進む」の
// 意味で返す
const (
	exitDone    = 0 // 試運転が通り loop の起動コマンドを示して終わった / 段が済んだ
	exitStopped = 1 // 途中で止まった (雛形を書いた / 承認されなかった / 外部 CLI の失敗)
	exitConfig  = 2
)

// Options は setup の入力。Gh / Git は外部 CLI の起動口、DryRun は tick の 1 回分の試運転。
type Options struct {
	Project paths.Project
	Home    string
	// Clone は実装 repo の clone (cwd)。loop の起動コマンドの cd 先
	Clone  string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	Gh  func(config.Config) (github.Runner, error)
	Git func(args ...string) (string, error)
	// DryRun は tick の 1 回分の試運転 (`tick <project> --dry-run` と同じ) を process の中で回し、出力を Stdout / Stderr へ
	// 流して exit code を返す
	DryRun func() int
}

type run struct {
	o       Options
	answers *bufio.Reader
}

// Run は段を順に進めて exit code を返す。
func Run(o Options) int {
	r := &run{o: o, answers: bufio.NewReader(o.Stdin)}
	if exit := r.ensureConfig(); exit != exitDone {
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
	r.say("試運転: claude-dispatcher tick %s --dry-run", o.Project.Name)
	if exit := o.DryRun(); exit != 0 {
		fmt.Fprintf(o.Stderr, "試運転が落ちた (exit %d)。上の行が名指しするものを直して setup を撃ち直す\n", exit)
		return exit
	}
	r.say("導入できた。clone を cwd にした端末で loop を起動する (Ctrl+C で止まる。間隔は 1m〜24h で変えてよい):")
	r.say("  cd %s && claude-dispatcher loop %s 5m", shellQuote(o.Clone), o.Project.Name)
	return exitDone
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./~:@+=,-]+$`)

// shellQuote は shell が 1 語として読む形にする。特殊な文字が無ければそのまま (人がそのまま貼って撃てるように)。
func shellQuote(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	const quote = "'"
	return quote + strings.ReplaceAll(s, quote, quote+`\`+quote+quote) + quote
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
		if !errors.Is(err, io.EOF) {
			fmt.Fprintf(r.o.Stderr, "答えを読めない (承認なしとして扱う): %v\n", err)
		}
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// --- 1. config の雛形と state dir ---

// ensureConfig は config.toml が無ければ雛形と state dir を作って止まる。あれば上書きせず、state dir だけを揃える。
func (r *run) ensureConfig() int {
	file := r.o.Project.ConfigFile()
	_, err := os.Stat(file)
	switch {
	case err == nil:
		if err := os.MkdirAll(r.o.Project.StateDir, 0o755); err != nil {
			return r.fail("state dir を作れない: %v", err)
		}
		return exitDone
	case !errors.Is(err, os.ErrNotExist):
		return r.fail("宣言 config を確かめられない: %s (%v)", file, err)
	}
	for _, dir := range []string{r.o.Project.ConfigDir, r.o.Project.StateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return r.fail("置き場を作れない: %v", err)
		}
	}
	// O_EXCL: 確かめてから書くまでの間に置かれた config も上書きしない
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return r.fail("宣言 config の雛形を書けない: %v", err)
	}
	_, err = fmt.Fprintf(f, template, r.originRepo())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return r.fail("宣言 config の雛形を書けない: %v", err)
	}
	r.say("宣言 config の雛形と state dir を作った: %s", file)
	r.say("置き場・着手可 label・並列上限を確かめて直してから、もう一度 `claude-dispatcher setup %s` を撃つ", r.o.Project.Name)
	return exitStopped
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

// originRepo は cwd の clone の origin を owner/name で返す。読めなければ理由を示して "" (config の検査が埋め忘れとして
// 落とす)。
func (r *run) originRepo() string {
	url, err := r.o.Git("remote", "get-url", "origin")
	var origin config.Repo
	if err == nil {
		origin, err = config.RepoFromRemote(url)
	}
	if err != nil {
		r.say("cwd の clone の origin を読めない — 雛形の [issue].repo は空にした (%v)", err)
		return ""
	}
	return origin.String()
}

// --- 3. label ---

func (r *run) ensureLabels(cfg config.Config) int {
	gh, err := r.o.Gh(cfg)
	if config.IsError(err) {
		// token file の不備は tick と同じく config の誤り
		fmt.Fprintln(r.o.Stderr, err)
		return exitConfig
	}
	if err != nil {
		return r.fail("gh を撃てない: %v", err)
	}
	var missing []config.Label
	for _, l := range cfg.Labels() {
		exists, err := github.LabelExists(gh, cfg.IssueRepo, l.Name)
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
		names = append(names, l.Name)
		commands = append(commands, fmt.Sprintf("gh label create %q -R %s --color %s --description %q", l.Name, cfg.IssueRepo, l.Color, l.Description))
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
		if err := github.CreateLabel(gh, cfg.IssueRepo, l.Name, l.Color, l.Description); err != nil {
			return r.fail("label %s を作れない: %v", l.Name, err)
		}
		r.say("label を作った: %s", l.Name)
	}
	return exitDone
}
