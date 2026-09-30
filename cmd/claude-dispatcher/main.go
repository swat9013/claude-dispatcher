// claude-dispatcher は tracker 上の作業対象を見張り、workflow 定義が宣言した trigger に当たったものへ Claude Code の
// セッションを無人で起動する CLI。
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/state"
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

// environment は subcommand が共有する実行環境。env は依存 CLI の PATH を解決した後の env。
type environment struct {
	env []string
}

func newEnvironment() environment {
	return environment{env: deps.WithEnv(os.Environ(), map[string]string{"PATH": deps.ResolvePATH(os.Getenv("PATH"), os.Getenv("HOME"))})}
}

func (e environment) getenv(key string) string { return deps.Getenv(e.env, key) }

// ghTimeout は gh の 1 回の呼び出しの上限
const ghTimeout = 120 * time.Second

// gh は workflow 定義の gh の撃ち方。tracker.token があれば gh に GH_TOKEN として渡す。
func (e environment) gh(def workflow.Definition) github.Exec {
	env := e.env
	if def.Tracker.Token != "" {
		env = deps.WithEnv(env, map[string]string{"GH_TOKEN": def.Tracker.Token})
	}
	return github.Exec{Env: env, Timeout: ghTimeout}
}

// openIssues は workflow 定義から issue 置き場の部品を組み立てる。
func (e environment) openIssues(def workflow.Definition) loop.Issues {
	return github.NewIssueStore(e.gh(def), def.Tracker.Repo)
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
	e := newEnvironment()
	load := func() (workflow.Definition, error) { return workflow.Load(abs, e.getenv) }
	def, err := load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if dryRun {
		return dryRunOnce(e, def, stdout, stderr)
	}

	// 起動時は人が画面の前にいるので、tick で落ちる前に gh と claude を解決できることを確かめる (system.md §8)
	if err := e.gh(def).Ready(); err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailed
	}
	if _, err := deps.Lookup(def.Claude.Command, e.env); err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailed
	}
	scopeKey := e.openIssues(def).ScopeKey()
	dir := state.Dir(state.Root(e.getenv), scopeKey)
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
	logFile, err := os.OpenFile(filepath.Join(dir, "log.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(stderr, "log.jsonl を開けない: %v\n", err)
		return exitFailed
	}
	defer logFile.Close()
	surviveClosedStdout()
	fmt.Fprintf(stdout, "%s loop を始めた: scope %s · state dir %s · workflow %s\n", time.Now().UTC().Format(time.RFC3339), scopeKey, dir, abs)
	return loop.Run(loop.Options{
		Load:       load,
		Definition: def,
		Open:       e.openIssues,
		Workspaces: func(def workflow.Definition) loop.Workspaces { return e.workspaces(def) },
		Launch: func(def workflow.Definition, job worker.Job, events chan<- worker.Event) loop.Worker {
			return worker.Runner{Workspaces: e.workspaces(def), Command: def.Claude.Command, Args: def.Claude.Args, Env: e.env, StateDir: dir}.Start(job, events)
		},
		NewSessionID: worker.NewSessionID,
		ScopeKey:     scopeKey,
		Log:          logFile,
		Stdout:       stdout,
		Signals:      stopRequests(),
		Now:          time.Now,
		After:        time.After,
	})
}

// workspaces は workflow 定義の workspace の置き場と hooks。hooks には loop の環境を渡す。
func (e environment) workspaces(def workflow.Definition) workspace.Manager {
	return workspace.Manager{Root: def.WorkspaceRoot, Clone: def.Dir, Env: e.env, Hooks: workspace.Hooks(def.Hooks)}
}

// dryRunOnce は試運転 (formats.md §5): snapshot を作って trigger を評価し、候補を 1 件 1 行で出す。何も書かない。
func dryRunOnce(e environment, def workflow.Definition, stdout, stderr io.Writer) int {
	open, err := e.openIssues(def).OpenIssues()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return failureExit(err)
	}
	for _, c := range trigger.Evaluate(def.Triggers, open) {
		fmt.Fprintf(stdout, "%s\t%s\t#%d\t%s\n", c.Trigger.Name, c.Trigger.On, c.Issue.Number, withoutControls(c.Issue.Title))
	}
	return 0
}

// withoutControls は制御文字 (タブ・改行・ESC など) を空白に置き換える。端末と、タブ区切りの列を守る。
func withoutControls(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
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
