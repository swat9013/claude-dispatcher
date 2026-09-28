// claude-dispatcher は issue tracker の着手可 issue を Claude Code に無人で実装させ、CL まで運ぶ CLI。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/config"
	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/doctor"
	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/launch"
	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/paths"
	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/setup"
	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/tick"
	"github.com/swat9013/claude-dispatcher/internal/version"
	"golang.org/x/term"
)

// exit code は formats.md §3。引数の誤りは 2
const exitUsage = 2

const usage = `usage:
  claude-dispatcher loop <project> <interval>
  claude-dispatcher tick <project> [--dry-run]
  claude-dispatcher status ps [<project>]
  claude-dispatcher status watch [<project>] [--interval <秒>]
  claude-dispatcher setup <project>
  claude-dispatcher doctor <project>
  claude-dispatcher paths --json [<project>]
  claude-dispatcher --version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "loop":
		return runLoop(args[1:], stdout, stderr)
	case "tick":
		return runTick(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "setup":
		return runSetup(args[1:], stdin, stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "paths":
		return runPaths(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "--version":
		// config も state dir も読まない。不具合の報告に版を貼れるよう、導入が壊れていても撃てる
		fmt.Fprintf(stdout, "claude-dispatcher %s\n", version.Line())
		return 0
	}
	return usageError(stderr, "未知の subcommand: %s", args[0])
}

func usageError(stderr io.Writer, format string, args ...any) int {
	fmt.Fprintf(stderr, format+"\n%s", append(args, usage)...)
	return exitUsage
}

// checkProject は引数の project 名が置き場の綴りとして使えるかを見る。使えなければ usage を出して false。
func checkProject(stderr io.Writer, name string) bool {
	if paths.ValidProjectName(name) {
		return true
	}
	usageError(stderr, "%s", invalidProjectName(name))
	return false
}

// invalidProjectName は置き場の綴りに使えない project 名を拒む文言。
func invalidProjectName(name string) string {
	return fmt.Sprintf("project 名は [A-Za-z0-9._-]+: %q", name)
}

// environment は subcommand が共有する実行環境。env は依存 CLI の PATH を解決した後の env。
type environment struct {
	home  string
	cwd   string
	env   []string
	roots paths.Roots
}

func newEnvironment() (environment, error) {
	home := os.Getenv("HOME")
	cwd, err := os.Getwd()
	if err != nil {
		return environment{}, fmt.Errorf("cwd を読めない: %w", err)
	}
	return environment{
		home:  home,
		cwd:   cwd,
		env:   deps.WithEnv(os.Environ(), map[string]string{"PATH": deps.ResolvePATH(os.Getenv("PATH"), home)}),
		roots: paths.ResolveRoots(os.Getenv),
	}, nil
}

// probeTimeout は status / setup / doctor / loop の画面が撃つ外部 CLI (git / ps / claude) の上限
const probeTimeout = 30 * time.Second

// output は env の PATH で name を解決して撃ち、stdout を返す。
func (e environment) output(name string, args ...string) (string, error) {
	c, err := e.command(name)
	if err != nil {
		return "", err
	}
	return c.Output(args...)
}

func (e environment) command(name string) (proc.Command, error) {
	path, err := deps.Lookup(name, e.env)
	if err != nil {
		return proc.Command{}, err
	}
	return proc.Command{Path: path, Env: e.env, Dir: e.cwd, Timeout: probeTimeout}, nil
}

func (e environment) git(args ...string) (string, error) { return e.output("git", args...) }

// ghFor は config の token file の token を載せた env で gh を撃つ Runner を返す (tick と同じ認証の経路)。
func (e environment) ghFor(cfg config.Config) (github.Runner, error) {
	tokens, err := cfg.Tokens(func(key string) string { return deps.Getenv(e.env, key) })
	if err != nil {
		return nil, err
	}
	return newGh(deps.WithEnv(e.env, tokens.GH))
}

// tickOptions は project の tick の 1 回分の入力。出力は stdout / stderr へ流す。
func (e environment) tickOptions(project string, stdout, stderr io.Writer) tick.Options {
	return tick.Options{
		Project:  project,
		Roots:    e.roots,
		Home:     e.home,
		Cwd:      e.cwd,
		Env:      e.env,
		Now:      time.Now(),
		Stdout:   stdout,
		Stderr:   stderr,
		Gh:       newGh,
		Launcher: newLauncher(e.cwd),
	}
}

// statusProbes は status の現況が読む外部の口。
func (e environment) statusProbes() status.Probes {
	return status.Probes{
		Machine: status.CommandObserver{Output: e.output},
		// worker は newLauncher の ClaudePrint で起動するので、生死もその形で見分ける
		Workers: launch.ClaudePrintCensus,
		Gh:      e.ghFor,
		Git:     e.git,
	}
}

// --- tick ---

func runTick(args []string, stdout, stderr io.Writer) int {
	var project string
	var dryRun bool
	for _, arg := range args {
		switch {
		case arg == "--dry-run":
			dryRun = true
		case strings.HasPrefix(arg, "-"):
			return usageError(stderr, "未知の flag: %s", arg)
		case project == "":
			project = arg
		default:
			return usageError(stderr, "引数が多い: %s", arg)
		}
	}
	if !checkProject(stderr, project) {
		return exitUsage
	}
	e, err := newEnvironment()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	opts := e.tickOptions(project, stdout, stderr)
	if dryRun {
		return tick.DryRun(opts)
	}
	return tick.Run(opts)
}

const ghTimeout = 120 * time.Second

func newGh(env []string) (github.Runner, error) {
	gh, err := deps.Lookup("gh", env)
	if err != nil {
		return nil, err
	}
	return github.Exec{Path: gh, Env: env, Timeout: ghTimeout}, nil
}

func newLauncher(cwd string) func(env []string) (launch.Launcher, error) {
	return func(env []string) (launch.Launcher, error) {
		claude, err := deps.Lookup("claude", env)
		if err != nil {
			return nil, err
		}
		return launch.ClaudePrint{Claude: claude, Env: env, Cwd: cwd}, nil
	}
}

// --- status ---

const (
	defaultWatchInterval = 5 * time.Second
	minWatchInterval     = time.Second
	maxWatchInterval     = 24 * time.Hour
)

func runStatus(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "ps" && args[0] != "watch") {
		return usageError(stderr, "status の後は ps か watch")
	}
	watch := args[0] == "watch"
	var project string
	interval := defaultWatchInterval
	for i := 1; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--interval" && watch && i+1 < len(args):
			i++
			seconds, err := strconv.ParseFloat(args[i], 64)
			// 短すぎる周期は gh / git / claude を休みなく撃ち続け、tick と同じ gh の rate limit を食い潰す。
			// NaN / Inf / 桁あふれは Duration にすると 0 以下になるので、範囲で弾く
			if err != nil || !(seconds >= minWatchInterval.Seconds() && seconds <= maxWatchInterval.Seconds()) {
				return usageError(stderr, "--interval は %g 以上 %g 以下の秒数: %s", minWatchInterval.Seconds(), maxWatchInterval.Seconds(), args[i])
			}
			interval = time.Duration(seconds * float64(time.Second))
		case strings.HasPrefix(arg, "-"):
			return usageError(stderr, "未知の flag: %s", arg)
		case project == "":
			if !checkProject(stderr, arg) {
				return exitUsage
			}
			project = arg
		default:
			return usageError(stderr, "引数が多い: %s", arg)
		}
	}

	e, err := newEnvironment()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	projects, err := e.roots.Projects()
	if err != nil {
		fmt.Fprintf(stderr, "project の一覧を読めない: %v\n", err)
		return 1
	}
	if project != "" {
		if !slices.Contains(projects, project) {
			fmt.Fprintf(stderr, "project %s の宣言 config が無い: %s\n", project, e.roots.Project(project).ConfigFile())
			return exitUsage
		}
		projects = []string{project}
	}
	if len(projects) == 0 {
		fmt.Fprintf(stderr, "project が 1 つも無い: %s\n", e.roots.Config)
		return exitUsage
	}
	probes := e.statusProbes()
	places := make([]paths.Project, 0, len(projects))
	for _, name := range projects {
		places = append(places, e.roots.Project(name))
	}
	collect := func() []status.Report { return status.Collect(places, e.home, probes, time.Now) }

	if !watch {
		fmt.Fprintln(stdout, status.RenderTable(collect()))
		return 0
	}
	// Ctrl-C (SIGINT) で終わる。片付けるものを持たないので signal は既定の扱いに任せる
	for {
		fmt.Fprintf(stdout, "\033[H\033[2J%s\n", status.RenderTable(collect()))
		time.Sleep(interval)
	}
}

// --- setup / doctor ---

func runSetup(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return usageError(stderr, "setup は project を 1 つ取る")
	}
	if !checkProject(stderr, args[0]) {
		return exitUsage
	}
	e, err := newEnvironment()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return setup.Run(setup.Options{
		Project: e.roots.Project(args[0]),
		Home:    e.home,
		Clone:   e.cwd,
		Stdin:   stdin,
		Stdout:  stdout,
		Stderr:  stderr,
		Gh:      e.ghFor,
		Git:     e.git,
		// 試運転は tick の 1 回分の試運転を process の中で回す (自分の binary を撃ち直さない — system.md §13)
		DryRun: func() int { return tick.DryRun(e.tickOptions(args[0], stdout, stderr)) },
	})
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return usageError(stderr, "doctor は project を 1 つ取る")
	}
	if !checkProject(stderr, args[0]) {
		return exitUsage
	}
	e, err := newEnvironment()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return doctor.Run(doctor.Options{
		Project: e.roots.Project(args[0]),
		Home:    e.home,
		Clone:   e.cwd,
		Env:     e.env,
		Stdout:  stdout,
		Gh:      e.ghFor,
		DryRun: func() (string, int) {
			var out bytes.Buffer
			exit := tick.DryRun(e.tickOptions(args[0], &out, &out))
			return out.String(), exit
		},
	})
}

// --- loop ---

// runLoop は起動時の検査 (formats.md §13) を通して loop を回す。検査に落ちたら画面を出さず、stderr に理由を 1 行出す
// (usage の一覧は出さない)。
func runLoop(args []string, stdout, stderr io.Writer) int {
	refuse := func(exit int, format string, a ...any) int {
		fmt.Fprintf(stderr, format+"\n", a...)
		return exit
	}
	if len(args) != 2 {
		return refuse(exitUsage, "loop は <project> <interval> を取る (例: claude-dispatcher loop myproj 5m)")
	}
	name := args[0]
	if !paths.ValidProjectName(name) {
		return refuse(exitUsage, "%s", invalidProjectName(name))
	}
	interval, err := loop.ParseInterval(args[1])
	if err != nil {
		return refuse(exitUsage, "%v", err)
	}
	e, err := newEnvironment()
	if err != nil {
		return refuse(loop.ExitFailed, "%v", err)
	}
	project := e.roots.Project(name)
	// config の中身は検査しない (tick ごとに読み直し、落ちた tick は見出しに出る)
	if err := config.RequireFile(project.ConfigFile()); err != nil {
		return refuse(exitUsage, "%v", err)
	}
	lock, lockErr := loop.AcquireLock(project)
	if lockErr != nil {
		return refuse(lockErr.Exit, "%s", lockErr.Msg)
	}
	defer lock.Close()
	return loop.Run(e.loopOptions(project, interval, stdout, stderr))
}

// loopOptions は project の loop の入力を組む。
func (e environment) loopOptions(project paths.Project, interval loop.Interval, stdout, stderr io.Writer) loop.Options {
	probes := e.statusProbes()
	return loop.Options{
		Project:  project.Name,
		Interval: interval,
		Stdout:   stdout,
		Stderr:   stderr,
		Terminal: isTerminal(stdout),
		Signals:  stopRequests(),
		Tick: func(control tick.Control) tick.Outcome {
			// loop の中の tick は失敗行を出さない (見出しに出す)。stdout は試運転しか使わないので捨てる
			o := e.tickOptions(project.Name, io.Discard, stderr)
			o.Control = control
			return tick.Once(o)
		},
		Status: func() status.Report { return status.Collect([]paths.Project{project}, e.home, probes, time.Now)[0] },
		Now:    time.Now,
		Poll:   loop.DefaultPoll,
	}
}

// stopRequests は loop の停止要求 (SIGINT / SIGTERM / SIGHUP) を受ける channel を返す。
func stopRequests() <-chan os.Signal {
	signals := make(chan os.Signal, 4) // 素早い 2 回目の Ctrl+C を落とさないよう余裕を持たせる
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	// 読み手の消えた stdout へ書いても SIGPIPE で倒れず、書き込みの失敗として返させる (system.md §9)
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
	return signals
}

// isTerminal は w が端末か。端末でない character device (/dev/null 等) は端末に数えない。
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// --- paths ---

func runPaths(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "--json" || len(args) > 2 {
		return usageError(stderr, "paths は --json [<project>] を取る")
	}
	roots := paths.ResolveRoots(os.Getenv)
	var doc any
	if len(args) == 2 {
		if !checkProject(stderr, args[1]) {
			return exitUsage
		}
		doc = roots.Project(args[1]).Doc()
	} else {
		projects, err := roots.Projects()
		if err != nil {
			fmt.Fprintf(stderr, "project の一覧を読めない: %v\n", err)
			return 1
		}
		doc = roots.Doc(projects)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\n", raw)
	return 0
}
