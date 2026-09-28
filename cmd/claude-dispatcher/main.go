// claude-dispatcher は issue tracker の着手可 issue を Claude Code に無人で実装させ、CL まで運ぶ CLI。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/config"
	"github.com/swat9013/claude-dispatcher/internal/crontab"
	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/doctor"
	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/launch"
	"github.com/swat9013/claude-dispatcher/internal/paths"
	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/setup"
	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/tick"
	"github.com/swat9013/claude-dispatcher/internal/version"
)

// exit code は formats.md §3。引数の誤りは 2
const exitUsage = 2

const usage = `usage:
  claude-dispatcher tick <project> [--dry-run [--cron-env]]
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
	usageError(stderr, "project 名は [A-Za-z0-9._-]+: %q", name)
	return false
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

// probeTimeout は status / setup / doctor が撃つ外部 CLI (git / ps / claude / crontab) の上限
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

func (e environment) crontab() (crontab.Client, error) {
	c, err := e.command("crontab")
	return crontab.Client{Command: c}, err
}

// ghFor は config の token file の token を載せた env で gh を撃つ Runner を返す (tick と同じ認証の経路)。
func (e environment) ghFor(cfg config.Config) (github.Runner, error) {
	tokens, err := cfg.Tokens(func(key string) string { return deps.Getenv(e.env, key) })
	if err != nil {
		return nil, err
	}
	return newGh(deps.WithEnv(e.env, tokens.GH))
}

// --- tick ---

func runTick(args []string, stdout, stderr io.Writer) int {
	var project string
	var dryRun, cronEnv bool
	for _, arg := range args {
		switch {
		case arg == "--dry-run":
			dryRun = true
		case arg == "--cron-env":
			cronEnv = true
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
	if cronEnv {
		if !dryRun {
			return usageError(stderr, "--cron-env は --dry-run と組で使う (cron 相当の環境で撃つのは試運転だけ)")
		}
		return reexecInCronEnv(project, stdout, stderr)
	}

	e, err := newEnvironment()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	opts := tick.Options{
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
	if dryRun {
		return tick.DryRun(opts)
	}
	return tick.Run(opts)
}

// cronBasePATH は cron の既定 PATH
const cronBasePATH = "/usr/bin:/bin"

// reexecInCronEnv は HOME と最小の PATH だけを残した環境で自分を撃ち直す (system.md §9)。
// 撃ち直しを CLI が持つのは、Claude Code の sandbox の除外指定が Bash 呼び出しの先頭 token だけで照合されるため —
// 呼び出し側が `env -i …` を前置すると CLI が sandbox 内に落ち、gh が credential を読めない偽の失敗になる。
func reexecInCronEnv(project string, stdout, stderr io.Writer) int {
	return runSelf([]string{"HOME=" + os.Getenv("HOME"), "PATH=" + cronBasePATH}, stdout, stderr, "tick", project, "--dry-run")
}

// runSelf は自分を env で撃ち、出力を流して exit code を返す。
func runSelf(env []string, stdout, stderr io.Writer, args ...string) int {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "自分の path を解決できない: %v\n", err)
		return 1
	}
	cmd := exec.Command(self, args...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "撃ち直せない: %v\n", err)
		return 1
	}
	return 0
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
	probes := status.Probes{
		Machine: status.CommandObserver{Output: e.output},
		// worker は newLauncher の ClaudePrint で起動するので、生死もその形で見分ける
		Workers: launch.ClaudePrintCensus,
		Gh:      e.ghFor,
		Git:     e.git,
	}
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
	self, err := selfForCrontab(os.Args[0], e.env)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return setup.Run(setup.Options{
		Project: e.roots.Project(args[0]),
		Home:    e.home,
		Clone:   e.cwd,
		Self:    self,
		Stdin:   stdin,
		Stdout:  stdout,
		Stderr:  stderr,
		Gh:      e.ghFor,
		Git:     e.git,
		Crontab: e.crontab,
		// 試運転は親の env のまま撃つ (PATH の自己解決と cron 相当の撃ち直しは tick 自身が行う)
		DryRun: func(args ...string) int { return runSelf(os.Environ(), stdout, stderr, args...) },
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
		Self:    func() (string, error) { return selfForCrontab(os.Args[0], e.env) },
		Env:     e.env,
		Stdout:  stdout,
		Gh:      e.ghFor,
		Crontab: e.crontab,
		DryRun: func(args ...string) (string, int) {
			var out bytes.Buffer
			exit := runSelf(os.Environ(), &out, &out, args...)
			return out.String(), exit
		},
	})
}

// selfForCrontab は crontab の行に埋める自分の絶対 path。撃たれたときの綴り (PATH で引いた path) を使う —
// os.Executable は symlink を解決した実体 (Homebrew の版つき Cellar の path 等) を返すことがあり、更新で消える。
// go run の一時 build は crontab から撃てないので拒む。argv0 は撃たれたときの os.Args[0]。
func selfForCrontab(argv0 string, env []string) (string, error) {
	self := argv0
	if !strings.Contains(self, string(os.PathSeparator)) {
		found, err := deps.Lookup(self, env)
		if err != nil {
			return "", fmt.Errorf("自分の path を解決できない: %w", err)
		}
		self = found
	}
	abs, err := filepath.Abs(self)
	if err != nil {
		return "", fmt.Errorf("自分の path を解決できない: %w", err)
	}
	if strings.Contains(abs, string(os.PathSeparator)+"go-build") {
		return "", fmt.Errorf("go run の一時 build (%s) は crontab に書けない。go install 等で置いた binary から撃つ", abs)
	}
	return abs, nil
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
