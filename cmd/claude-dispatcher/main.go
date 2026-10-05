// claude-dispatcher は tracker 上の作業対象を見張り、workflow 定義が宣言した trigger に当たったものへ Claude Code の
// セッションを無人で起動する CLI。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/gitlab"
	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/precheck"
	"github.com/swat9013/claude-dispatcher/internal/printable"
	"github.com/swat9013/claude-dispatcher/internal/state"
	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/version"
	"github.com/swat9013/claude-dispatcher/internal/worker"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
	"github.com/swat9013/claude-dispatcher/internal/workspace"
)

// exit code は formats.md §3
const (
	exitFailed      = 1
	exitUsage       = 2
	exitLoopRunning = 3
	exitAuth        = 4
)

const usage = `usage:
  claude-dispatcher loop [<workflow の path>]
  claude-dispatcher loop --dry-run [<workflow の path>]
  claude-dispatcher status [<workflow の path>]
  claude-dispatcher paths --json [<workflow の path>]
  claude-dispatcher setup [<workflow の path>]
  claude-dispatcher doctor [<workflow の path>]
  claude-dispatcher --version
`

// defaultWorkflowFile は workflow 定義の path を省いたときに cwd から読む file
const defaultWorkflowFile = "WORKFLOW.md"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "loop":
		return runLoop(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "paths":
		return runPaths(args[1:], stdout, stderr)
	case "setup":
		return runSetup(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "--version":
		// workflow 定義も state dir も読まない。不具合の報告に版を貼れるよう、導入が壊れていても撃てる
		fmt.Fprintf(stdout, "claude-dispatcher %s\n", version.Line())
		return 0
	}
	return usageError(stderr, "未知の subcommand: %s", args[0])
}

func usageError(stderr io.Writer, format string, args ...any) int {
	fmt.Fprintf(stderr, format+"\n%s", append(args, usage)...)
	return exitUsage
}

// environment は subcommand が共有する実行環境。env は撃つ依存 CLI の PATH を解決した後の env。
type environment struct {
	env []string
}

// unresolvedEnvironment は PATH を書き換えない環境。依存 CLI を撃たない読み取り (workflow 定義・状態 file) に使う。
func unresolvedEnvironment() environment {
	return environment{env: os.Environ()}
}

// commandEnvironment は commands (撃つ依存 CLI) を PATH で解決できるようにした環境。
func commandEnvironment(commands ...string) environment {
	path := deps.ResolvePATH(os.Getenv("PATH"), os.Getenv("HOME"), commands)
	return environment{env: deps.WithEnv(os.Environ(), map[string]string{"PATH": path})}
}

// definitionEnvironment は、workflow 定義で loop・試運転・doctor が撃つ依存 CLI (tracker の CLI・claude.command・git) を
// PATH で解決できるようにした環境。git は CL 側の trigger と、loop の PATH を継ぐ worker と hooks が撃つ。
func definitionEnvironment(def workflow.Definition) environment {
	return commandEnvironment(trackerCommand(def), def.Claude.Command, "git")
}

func (e environment) getenv(key string) string { return deps.Getenv(e.env, key) }

// commandTimeout は外部 CLI (gh / glab / git) の 1 回の呼び出しの上限
const commandTimeout = 120 * time.Second

// gh は gh の撃ち方。token (workflow 定義の tracker.token) があれば gh に GH_TOKEN として渡す。
func (e environment) gh(token string) github.Exec {
	env := e.env
	if token != "" {
		env = deps.WithEnv(env, map[string]string{"GH_TOKEN": token})
	}
	return github.Exec{Env: env, Timeout: commandTimeout}
}

// glab は glab の撃ち方。認証は glab 自身のもの (glab auth login の結果) を使う。
func (e environment) glab() gitlab.Exec {
	return gitlab.Exec{Env: e.env, Timeout: commandTimeout}
}

// store は workflow 定義の tracker.kind の adapter で置き場の部品を組み立てる。
func (e environment) store(def workflow.Definition) loop.Store {
	if def.Tracker.Kind == workflow.GitLab {
		return gitlab.NewStore(e.glab(), def.Tracker.Project)
	}
	return github.NewStore(e.gh(def.Tracker.Token), def.Tracker.Repo)
}

// trackerCommand は workflow 定義の tracker.kind の CLI。
func trackerCommand(def workflow.Definition) string {
	if def.Tracker.Kind == workflow.GitLab {
		return "glab"
	}
	return "gh"
}

// requiredCommands は loop が撃つ依存 CLI: tracker の CLI と claude.command。CL 側の trigger があれば、claim の workspace の
// branch を読むのに git も撃つ。
func requiredCommands(def workflow.Definition) []string {
	commands := []string{trackerCommand(def), def.Claude.Command}
	if slices.Contains(def.Kinds(), target.KindCL) {
		commands = append(commands, "git")
	}
	return commands
}

// precheck は workflow 定義の事前検査 (formats.md §2.9)。~/.claude は loop の環境の HOME から引く。
func (e environment) precheck(def workflow.Definition) []precheck.Problem {
	return precheck.Check(def, e.getenv("HOME"))
}

// stateDir は workflow 定義の scope key と、その state dir。
func (e environment) stateDir(def workflow.Definition) (scopeKey, dir string) {
	scopeKey = e.store(def).ScopeKey()
	return scopeKey, state.Dir(state.Root(e.getenv), scopeKey)
}

// failureExit は観測の失敗の exit code (formats.md §3)。
func failureExit(err error) int {
	var f *target.Failure
	if errors.As(err, &f) {
		switch f.Kind {
		case target.Auth:
			return exitAuth
		case target.NotVisible:
			return exitUsage
		}
	}
	return exitFailed
}

// --- loop ---

// runLoop は `loop [--dry-run] [<workflow の path>]` を撃つ。
func runLoop(args []string, stdout, stderr io.Writer) int {
	var path string
	var dryRun bool
	for _, arg := range args {
		switch {
		case arg == "--dry-run":
			dryRun = true
		case strings.HasPrefix(arg, "-"):
			return usageError(stderr, "未知の flag: %s", arg)
		case path == "":
			path = arg
		default:
			return usageError(stderr, "引数が多い: %s", arg)
		}
	}
	if path == "" {
		path = defaultWorkflowFile
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintf(stderr, "workflow 定義の path を解決できない (%s): %v\n", path, err)
		return exitFailed
	}
	// workflow 定義は PATH を使わずに読めるので、撃つ依存 CLI が決まる前の環境で読む
	load := func() (workflow.Definition, error) { return workflow.Load(abs, unresolvedEnvironment().getenv) }
	def, err := load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	// PATH は起動時の workflow 定義が撃つ依存 CLI で解決する
	e := definitionEnvironment(def)
	// 起動時と試運転は人が画面の前にいるので、事前検査のどれが落ちても失敗させ、全部を直させる (system.md §8)
	if problems := e.precheck(def); len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(stderr, p)
		}
		return exitUsage
	}
	if dryRun {
		return dryRunOnce(e, def, stdout, stderr)
	}

	// 起動時は人が画面の前にいるので、tick で落ちる前に依存 CLI を解決できることを確かめる (system.md §8)
	for _, name := range requiredCommands(def) {
		if _, err := deps.Lookup(name, e.env); err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailed
		}
	}
	scopeKey, dir := e.stateDir(def)
	lock, err := state.Lock(dir, scopeKey)
	if errors.Is(err, state.ErrAlreadyRunning) {
		fmt.Fprintln(stderr, err)
		return exitLoopRunning
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailed
	}
	defer lock.Close()
	logFile, err := os.OpenFile(filepath.Join(dir, state.LogFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(stderr, "log.jsonl を開けない: %v\n", err)
		return exitFailed
	}
	defer logFile.Close()
	surviveClosedStdout()
	publish := func(snap status.Snapshot) error { return status.Write(dir, snap) }
	// 端末なら画面を描き直し、そうでなければログの行を追記する (formats.md §6)
	var output loop.Output = loop.Appender{W: stdout}
	if f, ok := stdout.(*os.File); ok && isTerminal(f) {
		sc := &screen{out: stdout, now: time.Now, loc: time.Local, display: func() status.Display { return display(f, e.getenv) }}
		output = sc
		publish = func(snap status.Snapshot) error {
			sc.showStatus(snap)
			return status.Write(dir, snap)
		}
	}
	output.Show(loop.Line{At: time.Now(), Label: "loop を始めた", Rest: fmt.Sprintf(": scope %s · state dir %s · workflow %s", scopeKey, dir, abs)})
	return loop.Run(loop.Options{
		Load:       load,
		Definition: def,
		Precheck:   e.precheck,
		Store:      e.store,
		Workspaces: func(def workflow.Definition) loop.Workspaces { return e.workspaces(def) },
		Launch: func(def workflow.Definition, job worker.Job, events chan<- worker.Event) loop.Worker {
			return worker.Runner{Workspaces: e.workspaces(def), Definition: def, Env: e.env, StateDir: dir}.Start(job, events)
		},
		NewSessionID: worker.NewSessionID,
		Publish:      publish,
		Workflow:     abs,
		ScopeKey:     scopeKey,
		Log:          logFile,
		Output:       output,
		Signals:      stopRequests(),
		Now:          time.Now,
		After:        time.After,
		Refresh:      time.Tick(time.Second),
	})
}

// workspaces は workflow 定義の workspace の置き場と hooks。hooks には loop の環境を渡す。
func (e environment) workspaces(def workflow.Definition) workspace.Manager {
	h := def.Hooks
	return workspace.Manager{Root: def.WorkspaceRoot, Clone: def.Dir, Env: e.env, Hooks: workspace.Hooks{
		AfterCreate: h.AfterCreate, BeforeRun: h.BeforeRun, AfterRun: h.AfterRun, BeforeRemove: h.BeforeRemove, Timeout: h.Timeout,
	}}
}

// dryRunOnce は試運転 (formats.md §5): snapshot を作って trigger を評価し、候補を 1 件 1 行で出す。何も書かない。
func dryRunOnce(e environment, def workflow.Definition, stdout, stderr io.Writer) int {
	open, err := loop.OpenItems(e.store(def), def)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return failureExit(err)
	}
	candidates, _ := trigger.Evaluate(def.Triggers, open)
	for _, c := range candidates {
		fmt.Fprintf(stdout, "%s\t%s\t#%d\t%s\n", c.Trigger.Name, c.Trigger.On, c.Item.Ref().Number, printable.Line(c.Item.Heading()))
	}
	return 0
}

// stopRequests は loop の停止要求 (SIGINT / SIGTERM / SIGHUP) を受ける channel を返す。
func stopRequests() <-chan os.Signal {
	signals := make(chan os.Signal, 4) // loop が読む前に届いた停止要求を落とさないよう余裕を持たせる
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	return signals
}

// surviveClosedStdout は、読み手の消えた stdout へ書いても SIGPIPE で倒れず、書き込みの失敗として返させる。
func surviveClosedStdout() {
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
}

// --- status / paths ---

// workflowArg は subcommand の引数から workflow 定義の path を読む。required は必ず付ける flag。
func workflowArg(args []string, required []string, stderr io.Writer) (string, bool) {
	var path string
	seen := map[string]bool{}
	for _, arg := range args {
		switch {
		case slices.Contains(required, arg):
			seen[arg] = true
		case strings.HasPrefix(arg, "-"):
			usageError(stderr, "未知の flag: %s", arg)
			return "", false
		case path == "":
			path = arg
		default:
			usageError(stderr, "引数が多い: %s", arg)
			return "", false
		}
	}
	for _, flag := range required {
		if !seen[flag] {
			usageError(stderr, "%s が要る", flag)
			return "", false
		}
	}
	if path == "" {
		path = defaultWorkflowFile
	}
	return path, true
}

// loadForReading は、status と paths のために workflow 定義を読む。誤りなら stderr に出して exit code を返す。
func loadForReading(e environment, path string, stderr io.Writer) (workflow.Definition, int) {
	abs, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintf(stderr, "workflow 定義の path を解決できない (%s): %v\n", path, err)
		return workflow.Definition{}, exitFailed
	}
	def, err := workflow.Load(abs, e.getenv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return workflow.Definition{}, exitUsage
	}
	return def, 0
}

// runStatus は `status [<workflow の path>]` を撃つ (formats.md §7.2)。何も書かず、gh も撃たない。
func runStatus(args []string, stdout, stderr io.Writer) int {
	path, ok := workflowArg(args, nil, stderr)
	if !ok {
		return exitUsage
	}
	e := unresolvedEnvironment()
	def, code := loadForReading(e, path, stderr)
	if code != 0 {
		return code
	}
	scopeKey, dir := e.stateDir(def)
	alive, err := state.Alive(dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailed
	}
	snap, found, err := status.Read(dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailed
	}
	d := status.Display{Palette: status.Monochrome}
	if f, ok := stdout.(*os.File); ok {
		d = display(f, e.getenv)
	}
	if !found {
		fmt.Fprint(stdout, status.Unrecorded(scopeKey, status.Liveness(alive), d))
		return 0
	}
	fmt.Fprint(stdout, status.Render(snap, status.Liveness(alive), time.Now(), time.Local, d))
	return 0
}

// runPaths は `paths --json [<workflow の path>]` を撃つ (formats.md §7.3)。
func runPaths(args []string, stdout, stderr io.Writer) int {
	path, ok := workflowArg(args, []string{"--json"}, stderr)
	if !ok {
		return exitUsage
	}
	e := unresolvedEnvironment()
	def, code := loadForReading(e, path, stderr)
	if code != 0 {
		return code
	}
	scopeKey, dir := e.stateDir(def)
	raw, err := json.Marshal(map[string]string{
		"scope_key": scopeKey, "state_dir": dir, "log": filepath.Join(dir, state.LogFile),
		"status_file": filepath.Join(dir, status.FileName), "workspace_root": def.WorkspaceRoot,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailed
	}
	fmt.Fprintf(stdout, "%s\n", raw)
	return 0
}
