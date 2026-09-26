// claude-dispatcher は issue tracker の着手可 issue を Claude Code に無人で実装させ、CL まで運ぶ CLI。
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/launch"
	"github.com/swat9013/claude-dispatcher/internal/paths"
	"github.com/swat9013/claude-dispatcher/internal/tick"
)

// exit code は formats.md §3。引数の誤りは 2
const exitUsage = 2

const usage = `usage:
  claude-dispatcher tick <project> [--dry-run [--cron-env]]
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "tick":
		return runTick(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "未知の subcommand: %s\n%s", args[0], usage)
	return exitUsage
}

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
			fmt.Fprintf(stderr, "未知の flag: %s\n%s", arg, usage)
			return exitUsage
		case project == "":
			project = arg
		default:
			fmt.Fprintf(stderr, "引数が多い: %s\n%s", arg, usage)
			return exitUsage
		}
	}
	if !paths.ValidProjectName(project) {
		fmt.Fprintf(stderr, "project 名は [A-Za-z0-9._-]+: %q\n%s", project, usage)
		return exitUsage
	}
	if cronEnv {
		if !dryRun {
			fmt.Fprintf(stderr, "--cron-env は --dry-run と組で使う (cron 相当の環境で撃つのは試運転だけ)\n%s", usage)
			return exitUsage
		}
		return reexecInCronEnv(project, stdout, stderr)
	}

	home := os.Getenv("HOME")
	env := deps.WithEnv(os.Environ(), map[string]string{"PATH": deps.ResolvePATH(os.Getenv("PATH"), home)})
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "cwd を読めない: %v\n", err)
		return 1
	}
	opts := tick.Options{
		Project:  project,
		Roots:    paths.ResolveRoots(os.Getenv),
		Home:     home,
		Cwd:      cwd,
		Env:      env,
		Now:      time.Now(),
		Stdout:   stdout,
		Stderr:   stderr,
		Gh:       newGh,
		Launcher: newLauncher(cwd),
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
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "自分の path を解決できない: %v\n", err)
		return 1
	}
	cmd := exec.Command(self, "tick", project, "--dry-run")
	cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=" + cronBasePATH}
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

func newGh(env []string) github.Runner {
	path := deps.LookPath("gh", deps.Getenv(env, "PATH"))
	if path == "" {
		path = "gh" // 起動で失敗させ、観測できなかった tick として残す
	}
	return github.Exec{Path: path, Env: env, Timeout: ghTimeout}
}

func newLauncher(cwd string) func(env []string) (launch.Launcher, error) {
	return func(env []string) (launch.Launcher, error) {
		claude := deps.LookPath("claude", deps.Getenv(env, "PATH"))
		if claude == "" {
			return nil, fmt.Errorf("claude が PATH に無い (PATH=%s)", deps.Getenv(env, "PATH"))
		}
		return launch.ClaudePrint{Claude: claude, Env: env, Cwd: cwd}, nil
	}
}
