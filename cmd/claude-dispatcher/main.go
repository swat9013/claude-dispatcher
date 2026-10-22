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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/coord"
	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/guide"
	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/places"
	"github.com/swat9013/claude-dispatcher/internal/precheck"
	"github.com/swat9013/claude-dispatcher/internal/printable"
	"github.com/swat9013/claude-dispatcher/internal/proc"
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
	// exitNotLaunched は単発起動が worker を起動しなかったこと
	exitNotLaunched = 5
	// exitNotCompleted は単発起動が worker を起動したが完了しなかったこと
	exitNotCompleted = 6
)

// usage の形式は formats.md §10。Usage 節の command の代わりに flag を書く形と Options 節は引数の宣言 (args.go) から生成し、
// Usage 節の位置引数付きの形 (run と max-concurrent)・Commands・Arguments は手で書く
var usage = "Usage: claude-dispatcher <command> [options] [WORKFLOW]\n" +
	"       claude-dispatcher run TRIGGER TARGET [WORKFLOW]\n" +
	"       claude-dispatcher max-concurrent N [WORKFLOW]\n" + usageForms() + `
Commands:
  loop            前面で tick を回し続け、止めるまで戻らない (Ctrl+C で止める)
  status          loop の今の状態を描く。何も書かず、tracker の CLI も撃たない
  paths           state dir・log・状態 file・workspace の置き場を出す
  setup           cwd に WORKFLOW.md の雛形を書く。既にある file は上書きしない
  doctor          workflow 定義・issue 置き場・依存 CLI・事前検査を確かめる
  run             TRIGGER と TARGET を指名して worker を 1 回だけ起動し、完了したかを exit code で返す
  max-concurrent  走っている loop の並列上限を N にする。次の tick から効く
  guide           導入の流れ・回し方・WORKFLOW.md の書き方の案内を出す

Options:
` + options() + `
Arguments:
  WORKFLOW   workflow 定義の path。省くと cwd の WORKFLOW.md (guide では受けない)
  TRIGGER    (run) workflow 定義の trigger の名前
  TARGET     (run) 作業対象。issue#<番号> か cl#<番号>
  N          (max-concurrent) 並列上限。1 以上の整数

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
	switch {
	case helpFlag.accepts(args[0]):
		fmt.Fprint(stdout, usage)
		return 0
	case versionFlag.accepts(args[0]):
		// workflow 定義も state dir も読まない。不具合の報告に版を貼れるよう、導入が壊れていても撃てる
		fmt.Fprintf(stdout, "claude-dispatcher %s\n", version.Current())
		return 0
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
	case "run":
		return runOnce(args[1:], stdout, stderr)
	case "max-concurrent":
		return runMaxConcurrent(args[1:], stdout, stderr)
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

// definitionEnvironment は、workflow 定義で loop・試運転・単発起動・doctor が撃ちうる依存 CLI (places.Commands) を PATH で解決
// できるようにした環境。
func definitionEnvironment(def workflow.Definition) environment {
	return commandEnvironment(places.Commands(def)...)
}

func (e environment) getenv(key string) string { return deps.Getenv(e.env, key) }

// commandTimeout は外部 CLI (gh / glab / acli / git) の 1 回の呼び出しの上限
const commandTimeout = 120 * time.Second

// runtime は provider が組み立てる部品に渡す実行環境。
func (e environment) runtime() provider.Runtime {
	return provider.Runtime{Env: e.env, Timeout: commandTimeout}
}

// places は workflow 定義の置き場の組 (system.md §13)。tracker.token の渡し先は組が決める。
func (e environment) places(def workflow.Definition) places.Places {
	return places.New(def, e.runtime())
}

// precheck は workflow 定義の事前検査 (formats.md §2.9)。~/.claude は loop の環境の HOME から引く。
func (e environment) precheck(def workflow.Definition) []precheck.Problem {
	return precheck.Check(def, e.getenv("HOME"))
}

// stateDir は workflow 定義の scope key と、その state dir。
func (e environment) stateDir(def workflow.Definition) (scopeKey, dir string) {
	scopeKey = places.ScopeKey(def)
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
		n, err := parsePositiveInt("--max-concurrent", v)
		if err != nil {
			panic(fmt.Sprintf("--max-concurrent の値 %q が宣言の検査 (positiveInt) を通っていない", v))
		}
		o.maxConcurrent = n
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
	// 並列上限は workflow 定義に書けないので、--max-concurrent を渡したらその値にする (省けば Load の既定の 1。system.md §8)
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
		// 確認で読んだ open な一覧を snapshot に使い、一覧を 1 回だけ読む (formats.md §5)
		store, err := e.places(def).ConfirmedStore(slowToStderr(def, stderr))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return failureExit(err)
		}
		return dryRunOnce(store, def, stdout, stderr)
	}

	// 起動時の確認の遅い一覧の行から stdout に書くので、その前に読み手の消えた pipe で死なないようにする
	surviveClosedStdout()
	// 端末なら画面を描き直し、そうでなければログの行を追記する (formats.md §6)。起動時の確認の遅い一覧も、tick と同じく
	// この人が読む行に出す
	var output loop.Output = loop.Appender{W: stdout}
	var sc *screen
	if f, ok := stdout.(*os.File); ok && isTerminal(f) {
		sc = &screen{out: stdout, now: time.Now, loc: time.Local, display: func() status.Display { return display(f, e.getenv) }}
		output = sc
	}
	if code := e.confirmLaunchable(def, stderr, loop.SlowToOutput(def, output, time.After, time.Now)); code != 0 {
		return code
	}
	scopeKey, dir := e.stateDir(def)
	lock, code := lockState(state.Lock, dir, scopeKey, stderr)
	if code != 0 {
		return code
	}
	defer lock.Close()
	logFile, err := os.OpenFile(filepath.Join(dir, state.LogFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(stderr, "log.jsonl を開けない: %v\n", err)
		return exitFailed
	}
	defer logFile.Close()
	// 最初の生存報告は、起動を失敗させうる処理を済ませてから置く (後で失敗すると、置いた生存報告が remote に残る)
	party, code := e.join(def, stderr)
	if code != 0 {
		return code
	}
	publish := func(snap status.Snapshot) error { return status.Write(dir, snap) }
	if sc != nil {
		publish = func(snap status.Snapshot) error {
			sc.showStatus(snap)
			return status.Write(dir, snap)
		}
	}
	scope := scopeKey
	var coordination loop.Coordination
	if party != nil {
		scope += " · loop " + party.LoopName()
		coordination = party
	}
	output.Show(loop.Line{At: time.Now(), Label: "loop を始めた", Rest: fmt.Sprintf(": scope %s · state dir %s · workflow %s", scope, dir, abs)})
	hookLock := &sync.Mutex{}
	return loop.Run(loop.Options{
		Coordination:   coordination,
		HeartbeatAfter: time.After,
		Load:           load,
		MaxConcurrent:  func() (int, bool, error) { return state.ReadMaxConcurrent(dir) },
		Definition:     def,
		Precheck:       e.precheck,
		Store:          func(def workflow.Definition) loop.Store { return e.places(def).Store() },
		Workspaces:     func(def workflow.Definition) loop.Workspaces { return e.workspaces(def, hookLock) },
		Launch: func(def workflow.Definition, job worker.Job, events chan<- worker.Event) loop.Worker {
			return worker.Runner{Workspaces: e.workspaces(def, hookLock), Definition: def, StateDir: dir}.Start(job, events)
		},
		NewSessionID: worker.NewSessionID,
		Orphaned:     func(ref target.Ref) (bool, error) { return worker.Orphaned(dir, ref) },
		Publish:      publish,
		Workflow:     abs,
		Build:        version.Current(),
		ScopeKey:     scopeKey,
		ScopeKeyOf:   places.ScopeKey,
		Log:          logFile,
		Output:       output,
		Signals:      stopRequests(),
		Now:          time.Now,
		After:        time.After,
		Refresh:      time.Tick(time.Second),
		SlowAfter:    time.After,
	})
}

// confirmLaunchable は起動時の検査のうち、依存 CLI の解決と置き場の確認を通す (formats.md §6)。loop と単発起動が使う。
// 落ちたら stderr に理由を出して exit code を返す。確認が読む open な一覧が遅ければ slow で知らせる (formats.md §6 の
// 「遅い一覧」)。
func (e environment) confirmLaunchable(def workflow.Definition, stderr io.Writer, slow loop.SlowNotice) int {
	// 起動時は人が画面の前にいるので、起動した後に落ちる前に依存 CLI を解決できることを確かめる (system.md §8)
	for _, name := range places.RequiredCommands(def) {
		if _, err := deps.Lookup(name, e.env); err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailed
		}
	}
	if _, err := e.places(def).ConfirmedStore(slow); err != nil {
		fmt.Fprintln(stderr, err)
		return failureExit(err)
	}
	if def.CoordinationRemote != "" {
		if _, err := e.coordinationRemote(def).Confirm(); err != nil {
			fmt.Fprintln(stderr, printable.Line(err.Error()))
			return exitFailed
		}
	}
	return 0
}

// coordinationRemote は workflow 定義の調停 remote (formats.md §12)。git は workflow 定義のある dir から撃ち、remote の URL と
// 認証はその clone の設定をそのまま使う。
func (e environment) coordinationRemote(def workflow.Definition) coord.Remote {
	git, err := deps.Lookup("git", e.env)
	if err != nil {
		// 解決できない git は、撃ったときの失敗として名前のまま返す (起動時は依存 CLI の検査が先に落とす)
		git = "git"
	}
	return coord.Remote{Name: def.CoordinationRemote, ScopeDir: state.DirName(places.ScopeKey(def)), Git: proc.Command{Path: git, Env: e.env, Dir: def.Dir, Timeout: commandTimeout}}
}

// join は、調停 remote を指した project で loop 名を決め、最初の生存報告を置く (formats.md §12 の起動時の確認)。指さない
// project では nil。置けなければ stderr に理由を出して exit code を返す。
func (e environment) join(def workflow.Definition, stderr io.Writer) (*coord.Party, int) {
	if def.CoordinationRemote == "" {
		return nil, 0
	}
	name, err := coord.NewLoopName()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, exitFailed
	}
	party := &coord.Party{Remote: e.coordinationRemote(def), Loop: name}
	if err := party.Heartbeat(coord.HeartbeatInterval(def.Interval)); err != nil {
		fmt.Fprintf(stderr, "調停 remote %s に生存報告を置けない: %s\n", def.CoordinationRemote, printable.Line(err.Error()))
		return nil, exitFailed
	}
	return party, 0
}

// lockState は state dir を作って scope key の lock を take に取らせる (formats.md §6・§11)。loop (state.Lock) と単発起動
// (state.LockRun) が同じ loop.lock を取り合う。
func lockState(take func(dir, scopeKey string) (*state.Held, error), dir, scopeKey string, stderr io.Writer) (*state.Held, int) {
	lock, err := take(dir, scopeKey)
	if errors.Is(err, state.ErrAlreadyRunning) {
		fmt.Fprintln(stderr, err)
		return nil, exitLoopRunning
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, exitFailed
	}
	return lock, 0
}

// workspaces は workflow 定義の workspace の置き場と hooks。hooks と worker には loop の環境に tracker.token を置いた環境を
// 渡す。
// hookLock は loop が 1 つ持つ、after_create と before_remove を直列に撃つ lock。
func (e environment) workspaces(def workflow.Definition, hookLock *sync.Mutex) workspace.Manager {
	h := def.Hooks
	return workspace.Manager{Root: def.WorkspaceRoot, Clone: def.Dir, Env: e.places(def).TokenEnv(), HookLock: hookLock, Hooks: workspace.Hooks{
		AfterCreate: h.AfterCreate, BeforeRun: h.BeforeRun, AfterRun: h.AfterRun, BeforeRemove: h.BeforeRemove, Timeout: h.Timeout,
	}}
}

// dryRunOnce は試運転 (formats.md §5): store から snapshot を作って trigger を評価し、候補を loop の起動の直前と同じく
// 1 件ずつ読み直して、loop が起動する候補だけを 1 件 1 行で出す。候補を 1 件でも読み直せなければ、何も出さずに失敗する。
// 何も書かない。
func dryRunOnce(store loop.Store, def workflow.Definition, stdout, stderr io.Writer) int {
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

// slowToStderr は、一覧の読み出しが遅いことを読み終わる前に stderr へ 1 行で知らせる口 (formats.md §6 の「遅い一覧」)。
// 試運転・doctor・単発起動が使う。
func slowToStderr(def workflow.Definition, stderr io.Writer) loop.SlowNotice {
	return loop.SlowNotice{After: time.After, Notify: func(kind target.Kind) { fmt.Fprintln(stderr, loop.SlowListingLine(def, kind)) }}
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

// loadForReading は、status と paths と max-concurrent のために workflow 定義を読む。誤りなら stderr に出して exit code を返す。
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

// runMaxConcurrent は `max-concurrent <N> [<workflow の path>]` を撃つ (formats.md §13)。走っている loop の state dir に並列上限
// を書く。loop が立っていないか、max_concurrent を読まない版の loop なら書かない (書いても効かない)。tracker の CLI は撃たない。
func runMaxConcurrent(args []string, stdout, stderr io.Writer) int {
	a, err := commandSpecOf("max-concurrent").parse(args)
	if err != nil {
		return usageError(stderr, "%v", err)
	}
	n, err := parsePositiveInt("N", a.operands[0])
	if err != nil {
		return usageError(stderr, "%v", err)
	}
	e := unresolvedEnvironment()
	def, code := loadForReading(e, a.workflowPath, stderr)
	if code != 0 {
		return code
	}
	scopeKey, dir := e.stateDir(def)
	alive, err := state.Alive(dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailed
	}
	if !alive {
		fmt.Fprintf(stderr, "loop が立っていない: %s\n", scopeKey)
		return exitFailed
	}
	// 書いても次の tick で読まれない loop には書かない (formats.md §13)。読む版の loop は状態 file に max_concurrent を書く
	snap, found, err := status.Read(dir)
	switch {
	case err != nil:
		fmt.Fprintln(stderr, err)
		return exitFailed
	case !found:
		fmt.Fprintf(stderr, "loop の状態 file がまだ無い: %s (起動した直後なら少し待って撃ち直す)\n", scopeKey)
		return exitFailed
	case snap.MaxConcurrent == 0:
		// 走っている loop は起動した時点の binary で回るので、binary を入れ替える前の版の loop は読まない
		fmt.Fprintf(stderr, "この loop は max-concurrent を読まない: %s (この版の binary で loop を起動し直す)\n", scopeKey)
		return exitFailed
	case snap.Stopping:
		// 停止待ちの loop は tick を回さない
		fmt.Fprintf(stderr, "loop は停止待ち: %s\n", scopeKey)
		return exitFailed
	}
	if err := state.WriteMaxConcurrent(dir, n); err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailed
	}
	fmt.Fprintf(stdout, "並列上限を %d にした: 次の tick から効く (%s)\n", n, scopeKey)
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
