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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/guide"
	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/precheck"
	"github.com/swat9013/claude-dispatcher/internal/printable"
	"github.com/swat9013/claude-dispatcher/internal/provider"
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

// usage の形式は formats.md §10。Options 節は引数の宣言 (args.go) から生成し、Commands と Arguments は手で書く
var usage = `Usage: claude-dispatcher <command> [options] [WORKFLOW]
       claude-dispatcher -h | --help | help
       claude-dispatcher --version

Commands:
  loop      前面で tick を回し続け、止めるまで戻らない (Ctrl+C で止める)
  status    loop の今の状態を描く。何も書かず、tracker の CLI も撃たない
  paths     state dir・log・状態 file・workspace の置き場を出す
  setup     cwd に WORKFLOW.md の雛形を書く。既にある file は上書きしない
  doctor    workflow 定義・issue 置き場・依存 CLI・事前検査を確かめる
  guide     導入の流れ・回し方・WORKFLOW.md の書き方の案内を出す

Options:
` + options() + `
Arguments:
  WORKFLOW   workflow 定義の path。省くと cwd の WORKFLOW.md (guide では受けない)

` + "`WORKFLOW.md` の書き方と導入の流れは `claude-dispatcher guide` を参照\n"

// guidePointer は workflow 定義の誤りの行の後ろに足す、案内を指す行 (formats.md §2.8)。誤りは案内を読めば直せる
const guidePointer = "書き方は `claude-dispatcher guide` を参照"

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
	case "guide":
		return runGuide(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "--version":
		// workflow 定義も state dir も読まない。不具合の報告に版を貼れるよう、導入が壊れていても撃てる
		fmt.Fprintf(stdout, "claude-dispatcher %s\n", version.Current())
		return 0
	}
	return usageError(stderr, "未知の subcommand: %s", args[0])
}

// runGuide は `guide` を撃つ (formats.md §9)。workflow 定義も state dir も読まず、tracker の CLI も撃たない。導入が壊れて
// いても、利用者のセッションが書き方を読めるようにする
func runGuide(args []string, stdout, stderr io.Writer) int {
	if _, err := commandSpecOf("guide").parse(args); err != nil {
		return usageError(stderr, "%v", err)
	}
	fmt.Fprint(stdout, guide.Text())
	return 0
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

// definitionEnvironment は、workflow 定義で loop・試運転・doctor が撃つ依存 CLI (置き場の CLI・claude.command・git) を
// PATH で解決できるようにした環境。git は CL 側の trigger と、loop の PATH を継ぐ worker と hooks が撃つ。
func definitionEnvironment(def workflow.Definition) environment {
	return commandEnvironment(append(placeCommands(def), def.Claude.Command, "git")...)
}

func (e environment) getenv(key string) string { return deps.Getenv(e.env, key) }

// commandTimeout は外部 CLI (gh / glab / acli / git) の 1 回の呼び出しの上限
const commandTimeout = 120 * time.Second

// runtime は provider が組み立てる部品に渡す実行環境。
func (e environment) runtime() provider.Runtime {
	return provider.Runtime{Env: e.env, Timeout: commandTimeout}
}

// withToken は workflow 定義の tracker.token を、tracker の CLI が読む環境変数に置いた環境 (system.md §8)。
// tracker.token が書かれていなければ e のまま。
func (e environment) withToken(def workflow.Definition) environment {
	return environment{env: def.Tracker.Provider().WithToken(e.env, def.Tracker.Token)}
}

// trackerParts は workflow 定義の tracker.kind の provider で置き場の部品を組み立てる。
func (e environment) trackerParts(def workflow.Definition) provider.Parts {
	return def.Tracker.Provider().Build(e.withToken(def).runtime(), provider.Setting{Place: def.Tracker.Place, Triggers: def.Triggers})
}

// separateCLs は cl: の CL 置き場の部品を、cl.kind の provider で組み立てる。cl: は token を持たず、CL 置き場の CLI 自身の
// 認証を使う。
// cl: が無ければ false。
func (e environment) separateCLs(def workflow.Definition) (provider.CLStore, bool) {
	p, place, ok := def.SeparateCLHost()
	if !ok {
		return nil, false
	}
	return p.Build(e.runtime(), provider.Setting{Place: place, Triggers: def.Triggers}).CLs, true
}

// store は loop が読む置き場。issue 置き場の部品は tracker.kind の provider から、CL 置き場の部品は cl: があれば cl.kind の
// provider から組む (system.md §13)。
func (e environment) store(def workflow.Definition) loop.Store {
	parts := e.trackerParts(def)
	cls := parts.CLs
	if separate, ok := e.separateCLs(def); ok {
		cls = separate
	}
	return loop.Store{Issues: parts.Issues, CLs: cls}
}

// confirmPlace は起動時の置き場の確認 (formats.md §6): issue 置き場の確認と、cl: の CL 置き場の確認。
func (e environment) confirmPlace(def workflow.Definition) error {
	if err := e.confirmIssuePlace(def); err != nil {
		return err
	}
	err := e.readSeparateCLs(def)
	var nonFatal *target.NonFatal
	if errors.As(err, &nonFatal) {
		// 一覧は読めた。補助の要求だけの失敗は確認を落とさない
		return nil
	}
	return err
}

// confirmIssuePlace は起動時の issue 置き場の確認 (formats.md §6)。確認の手順を持たない種類は何もしない。
func (e environment) confirmIssuePlace(def workflow.Definition) error {
	if confirm := e.trackerParts(def).Confirm; confirm != nil {
		return confirm()
	}
	return nil
}

// readSeparateCLs は cl: の CL 置き場の open な一覧を 1 回読む (formats.md §6 の CL 置き場の確認)。cl: が無ければ何もしない。
func (e environment) readSeparateCLs(def workflow.Definition) error {
	cls, ok := e.separateCLs(def)
	if !ok {
		return nil
	}
	_, err := cls.OpenCLs()
	return err
}

// placeCommands は置き場の CLI: tracker の CLI と、cl: があれば CL 置き場の CLI。
func placeCommands(def workflow.Definition) []string {
	commands := []string{def.Tracker.Provider().Command}
	if p, _, ok := def.SeparateCLHost(); ok {
		commands = append(commands, p.Command)
	}
	return commands
}

// requiredCommands は loop が撃つ依存 CLI: 置き場の CLI と claude.command。CL 側の trigger があれば、claim の workspace の
// branch を読むのに git も撃つ。
func requiredCommands(def workflow.Definition) []string {
	commands := append(placeCommands(def), def.Claude.Command)
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
	var unknown *target.UnknownNames
	if errors.As(err, &unknown) {
		return exitUsage
	}
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

// loopOptions は `loop` の引数を読んだもの。
type loopOptions struct {
	path   string
	dryRun bool
	// maxConcurrent は --max-concurrent の値。0 は渡されていない (1 以上しか受けない)
	maxConcurrent int
}

// loopFlags は `loop [--dry-run] [--max-concurrent <N>] [<workflow の path>]` の引数を読む (formats.md §5・§6)。
func loopFlags(argv []string) (loopOptions, error) {
	a, err := commandSpecOf("loop").parse(argv)
	if err != nil {
		return loopOptions{}, err
	}
	o := loopOptions{path: a.workflowPath, dryRun: a.has("--dry-run")}
	if v := a.value("--max-concurrent"); v != "" {
		// 値は宣言の検査 (positiveInt) を通っている
		o.maxConcurrent, _ = strconv.Atoi(v)
	}
	return o, nil
}

// runLoop は `loop [--dry-run] [--max-concurrent <N>] [<workflow の path>]` を撃つ。
func runLoop(args []string, stdout, stderr io.Writer) int {
	opts, err := loopFlags(args)
	if err != nil {
		return usageError(stderr, "%v", err)
	}
	path := opts.path
	abs, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintf(stderr, "workflow 定義の path を解決できない (%s): %v\n", path, err)
		return exitFailed
	}
	// workflow 定義は PATH を使わずに読めるので、撃つ依存 CLI が決まる前の環境で読む
	// --max-concurrent は読み直すたびに limits.max_concurrent を上書きし、loop が止まるまで変わらない (system.md §8)
	load := func() (workflow.Definition, error) {
		def, err := workflow.Load(abs, unresolvedEnvironment().getenv)
		if err == nil && opts.maxConcurrent != 0 {
			def.MaxConcurrent = opts.maxConcurrent
		}
		return def, err
	}
	def, err := load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, guidePointer)
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
	if opts.dryRun {
		if err := e.confirmPlace(def); err != nil {
			fmt.Fprintln(stderr, err)
			return failureExit(err)
		}
		return dryRunOnce(e, def, stdout, stderr)
	}

	// 起動時は人が画面の前にいるので、tick で落ちる前に依存 CLI を解決できることを確かめる (system.md §8)
	for _, name := range requiredCommands(def) {
		if _, err := deps.Lookup(name, e.env); err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailed
		}
	}
	if err := e.confirmPlace(def); err != nil {
		fmt.Fprintln(stderr, err)
		return failureExit(err)
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
	hookLock := &sync.Mutex{}
	return loop.Run(loop.Options{
		Load:       load,
		Definition: def,
		Precheck:   e.precheck,
		Store:      e.store,
		Workspaces: func(def workflow.Definition) loop.Workspaces { return e.workspaces(def, hookLock) },
		Launch: func(def workflow.Definition, job worker.Job, events chan<- worker.Event) loop.Worker {
			return worker.Runner{Workspaces: e.workspaces(def, hookLock), Definition: def, StateDir: dir}.Start(job, events)
		},
		NewSessionID: worker.NewSessionID,
		Publish:      publish,
		Workflow:     abs,
		Build:        version.Current(),
		ScopeKey:     scopeKey,
		Log:          logFile,
		Output:       output,
		Signals:      stopRequests(),
		Now:          time.Now,
		After:        time.After,
		Refresh:      time.Tick(time.Second),
	})
}

// workspaces は workflow 定義の workspace の置き場と hooks。hooks と worker には loop の環境に tracker.token を置いた環境を
// 渡す。
// hookLock は loop が 1 つ持つ、after_create と before_remove を直列に撃つ lock。
func (e environment) workspaces(def workflow.Definition, hookLock *sync.Mutex) workspace.Manager {
	h := def.Hooks
	return workspace.Manager{Root: def.WorkspaceRoot, Clone: def.Dir, Env: e.withToken(def).env, HookLock: hookLock, Hooks: workspace.Hooks{
		AfterCreate: h.AfterCreate, BeforeRun: h.BeforeRun, AfterRun: h.AfterRun, BeforeRemove: h.BeforeRemove, Timeout: h.Timeout,
	}}
}

// dryRunOnce は試運転 (formats.md §5): snapshot を作って trigger を評価し、候補を loop の起動の直前と同じく 1 件ずつ
// 読み直して、loop が起動する候補だけを 1 件 1 行で出す。候補を 1 件でも読み直せなければ、何も出さずに失敗する。何も書かない。
func dryRunOnce(e environment, def workflow.Definition, stdout, stderr io.Writer) int {
	store := e.store(def)
	open, err := loop.OpenItems(store, def)
	var nonFatal *target.NonFatal
	switch {
	case errors.As(err, &nonFatal):
		// 作業対象は読めた。補助の要求の失敗を stderr に 1 行で残して続ける (formats.md §5)
		fmt.Fprintln(stderr, printable.Line(err.Error()))
	case err != nil:
		fmt.Fprintln(stderr, err)
		return failureExit(err)
	}
	candidates, _ := trigger.Evaluate(def.Triggers, open)
	var lines strings.Builder
	for _, c := range candidates {
		item, skip, err := loop.RecheckCandidate(store, def.Triggers, c)
		if err != nil {
			fmt.Fprintf(stderr, "候補 %s を読み直せない: %v\n", def.Reference(c.Item.Ref()), err)
			return failureExit(err)
		}
		if skip != "" {
			continue
		}
		fmt.Fprintf(&lines, "%s\t%s\t%s\t%s\n", c.Trigger.Name, c.Trigger.On, def.Reference(item.Ref()), printable.Line(item.Heading()))
	}
	fmt.Fprint(stdout, lines.String())
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

// workflowArg は引数を subcommand の宣言で読み、workflow 定義の path を返す。誤りなら stderr に出して false。
func workflowArg(command string, argv []string, stderr io.Writer) (string, bool) {
	a, err := commandSpecOf(command).parse(argv)
	if err != nil {
		usageError(stderr, "%v", err)
		return "", false
	}
	return a.workflowPath, true
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
	path, ok := workflowArg("status", args, stderr)
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
	path, ok := workflowArg("paths", args, stderr)
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
