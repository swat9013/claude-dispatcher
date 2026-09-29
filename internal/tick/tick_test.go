package tick_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/launch"
	"github.com/swat9013/claude-dispatcher/internal/paths"
	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/tick"
)

// 起動部の seam (launch.Launcher) と gh の起動口を差し替え、実 claude も gh も起動せずに tick を回す。
// black-box テスト (test/blackbox) では外から起こせない経路 (想定外の失敗・orchestrator の timeout) をここで見る。

var now = time.Date(2026, 9, 26, 3, 0, 0, 123456000, time.UTC)

const stem = "20260926T030000.123456Z"

// fakeGh は argv の先頭 2 token で振り分けて GitHub の JSON を返す。
type fakeGh struct{ issues string }

func (g fakeGh) Run(args ...string) ([]byte, error) {
	switch strings.Join(args[:2], " ") {
	case "repo view":
		return []byte(`{"nameWithOwner":"acme/widgets"}`), nil
	case "label list":
		return []byte(`[{"name":"dispatcher:wip"},{"name":"ready-for-human"},{"name":"ready-for-agent"}]`), nil
	case "issue list":
		return []byte(g.issues), nil
	case "api graphql":
		return []byte(`{"data":{"repository":{"pullRequests":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}}`), nil
	}
	return nil, &github.Error{Args: args, Exit: 1, Stderr: "unexpected"}
}

// fakeLauncher は claude の代わりに呼び出しを記録する。orchestrate は orchestrator の振る舞いを決める。
type fakeLauncher struct {
	orchestrate func(prompt string) launch.OrchestratorRun
	// stop は RunOrchestrator が受け取った停止要求 (orchestrate の中から見る)
	stop    <-chan struct{}
	spawn   func(n int) (launch.WorkerLaunch, error)
	prompts []string
	workers []string
}

func (f *fakeLauncher) RunOrchestrator(prompt, logFile string, timeout time.Duration, stop <-chan struct{}) (launch.OrchestratorRun, error) {
	f.prompts = append(f.prompts, prompt)
	f.stop = stop
	return f.orchestrate(prompt), nil
}

func (f *fakeLauncher) SpawnWorker(prompt, logFile string) (launch.WorkerLaunch, error) {
	f.workers = append(f.workers, prompt)
	return f.spawn(len(f.workers))
}

type env struct {
	t       *testing.T
	home    string
	cwd     string
	roots   paths.Roots
	project paths.Project
	stderr  bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{t: t, home: filepath.Join(root, "home"), cwd: filepath.Join(root, "clone")}
	e.roots = paths.Roots{Config: filepath.Join(root, "config"), State: filepath.Join(root, "state")}
	e.project = e.roots.Project("widgets")
	for _, dir := range []string{e.cwd, e.project.ConfigDir, e.project.StateDir} {
		must(t, os.MkdirAll(dir, 0o755))
	}
	must(t, os.WriteFile(e.project.ConfigFile(), []byte("[issue]\nrepo = \"acme/widgets\"\nready_label = \"ready-for-agent\"\n\n[limits]\nmax_wip = 2\n"), 0o644))
	plugin := filepath.Join(e.home, ".claude", "skills", "swat-skills")
	writeSkill(t, filepath.Join(plugin, "skills", "procedure", "playbook-implementation"), "metadata:\n  deliverable: cl\n  dispatch-when: 既定\n")
	writeSkill(t, filepath.Join(plugin, "skills", "knowledge", "principle-index"), "name: principle-index\n")
	return e
}

func (e *env) playbook() string {
	return filepath.Join(e.home, ".claude", "skills", "swat-skills", "skills", "procedure", "playbook-implementation", "SKILL.md")
}

func (e *env) options(gh fakeGh, launcher *fakeLauncher) tick.Options {
	return tick.Options{
		Project: "widgets", Roots: e.roots, Home: e.home, Cwd: e.cwd, Env: []string{"HOME=" + e.home},
		Now: now, Stdout: &bytes.Buffer{}, Stderr: &e.stderr,
		Gh:       func([]string) (github.Runner, error) { return gh, nil },
		Launcher: func([]string) (launch.Launcher, error) { return launcher, nil },
	}
}

func (e *env) run(gh fakeGh, launcher *fakeLauncher) int {
	return tick.Run(e.options(gh, launcher))
}

// once は停止要求の口 stop を渡して 1 tick を回す (loop と同じ呼び方)。
func (e *env) once(gh fakeGh, launcher *fakeLauncher, stop <-chan struct{}) tick.Outcome {
	o := e.options(gh, launcher)
	o.Control.StopOrchestrator = stop
	return tick.Once(o)
}

func (e *env) tickLine() map[string]any {
	e.t.Helper()
	raw, err := os.ReadFile(e.project.LogFile())
	must(e.t, err)
	var last map[string]any
	for _, text := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var line map[string]any
		must(e.t, json.Unmarshal([]byte(text), &line))
		if _, ok := line["actor"]; !ok {
			last = line
		}
	}
	return last
}

// writesDecisions は決定ファイルに 2 件の start を書いて正常終了する orchestrator。
func (e *env) writesDecisions(prompt string) launch.OrchestratorRun {
	d := fmt.Sprintf(`{"decisions":[{"issue":42,"action":"start","reason":"r"},{"issue":43,"action":"start","reason":"r"}],
"spawn":[{"issue":42,"kind":"start","prompt":"issue 42 %[1]s","playbooks":["%[1]s"]},{"issue":43,"kind":"start","prompt":"issue 43 %[1]s","playbooks":["%[1]s"]}]}`, e.playbook())
	must(e.t, os.WriteFile(e.project.DecisionsFile(stem), []byte(d), 0o644))
	return launch.OrchestratorRun{ExitCode: 0, Seconds: 1, SessionID: "orchestrator-session"}
}

const twoCandidates = `[{"number":42,"title":"a","url":"u","body":"b","labels":[{"name":"ready-for-agent"}]},
{"number":43,"title":"b","url":"u","body":"b","labels":[{"name":"ready-for-agent"}]}]`

func launchesWorkers(n int) (launch.WorkerLaunch, error) {
	return launch.WorkerLaunch{PID: 1000 + n, SessionID: fmt.Sprintf("worker-%d", n)}, nil
}

func TestOrchestratorIsLaunchedThroughTheSeamWithTheInstructionFile(t *testing.T) {
	e := newEnv(t)
	launcher := &fakeLauncher{orchestrate: e.writesDecisions, spawn: launchesWorkers}

	exit := e.run(fakeGh{issues: twoCandidates}, launcher)

	if exit != 0 || len(launcher.prompts) != 1 || !strings.Contains(launcher.prompts[0], e.project.InstructionFile(stem)) {
		t.Fatalf("exit %d / orchestrator の prompt %d 件 (指示ファイルの path を含むこと)\n%s", exit, len(launcher.prompts), e.stderr.String())
	}
}

func TestDecidedWorkersAreLaunchedThroughTheSeam(t *testing.T) {
	e := newEnv(t)
	launcher := &fakeLauncher{orchestrate: e.writesDecisions, spawn: launchesWorkers}

	e.run(fakeGh{issues: twoCandidates}, launcher)

	if len(launcher.workers) != 2 {
		t.Fatalf("worker の起動 %d 件", len(launcher.workers))
	}
}

func TestLaunchedWorkersAreListedOnTheTickLine(t *testing.T) {
	e := newEnv(t)

	e.run(fakeGh{issues: twoCandidates}, &fakeLauncher{orchestrate: e.writesDecisions, spawn: launchesWorkers})

	if spawned := e.tickLine()["spawned"].([]any); len(spawned) != 2 {
		t.Fatalf("spawned = %v", spawned)
	}
}

func TestTickThatCannotWriteItsLineLeavesTheReasonOnStderr(t *testing.T) {
	e := newEnv(t)
	must(t, os.Mkdir(e.project.LogFile(), 0o755)) // log.jsonl の位置に dir を置いて append を失敗させる

	e.run(fakeGh{issues: "[]"}, &fakeLauncher{})

	if !strings.Contains(e.stderr.String(), "tick=- result=ok") || !strings.Contains(e.stderr.String(), "log.jsonl に書けない") {
		t.Fatalf("stderr = %q", e.stderr.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestDryRunThatCannotWriteStdoutFails(t *testing.T) {
	e := newEnv(t)

	exit := tick.DryRun(tick.Options{
		Project: "widgets", Roots: e.roots, Home: e.home, Cwd: e.cwd, Env: []string{"HOME=" + e.home, "PATH=" + e.fakeDeps()},
		Now: now, Stdout: failingWriter{}, Stderr: &e.stderr,
		Gh: func([]string) (github.Runner, error) { return fakeGh{issues: "[]"}, nil },
	})

	if exit != 1 || !strings.Contains(e.stderr.String(), "stdout に書けない") {
		t.Fatalf("exit %d / stderr = %q", exit, e.stderr.String())
	}
}

// fakeDeps は試運転が PATH で探す依存 CLI (中身は空) を置いた dir を返す。
func (e *env) fakeDeps() string {
	dir := filepath.Join(e.t.TempDir(), "bin")
	must(e.t, os.MkdirAll(dir, 0o755))
	for _, name := range []string{"gh", "claude"} {
		must(e.t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755))
	}
	return dir
}

// panicsOnSecondWorker は 2 件目の worker の起動で壊れる launcher (1 件目は起動済み)。
func (e *env) panicsOnSecondWorker() *fakeLauncher {
	return &fakeLauncher{orchestrate: e.writesDecisions, spawn: func(n int) (launch.WorkerLaunch, error) {
		if n == 2 {
			panic("worker 起動の途中で壊れた")
		}
		return launchesWorkers(n)
	}}
}

func TestPanicMidTickLeavesAnErrorLineWithTheSettledKeys(t *testing.T) {
	e := newEnv(t)

	exit := e.run(fakeGh{issues: twoCandidates}, e.panicsOnSecondWorker())

	line := e.tickLine()
	spawned, _ := line["spawned"].([]any)
	msg, _ := line["error"].(string)
	if exit != 1 || line["result"] != "error" || !strings.HasPrefix(msg, "想定外の失敗で止まった: ") ||
		line["instruction_file"] != e.project.InstructionFile(stem) || line["orchestrator"] == nil || len(spawned) != 1 {
		t.Fatalf("exit %d / 想定外の失敗で止まった tick の行から確定済みの key が落ちた: %v", exit, line)
	}
}

func TestPanicMidTickEndsStderrWithThePrefixedLineAfterTheStackTrace(t *testing.T) {
	e := newEnv(t)

	e.run(fakeGh{issues: twoCandidates}, e.panicsOnSecondWorker())

	lines := strings.Split(strings.TrimRight(e.stderr.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(e.stderr.String(), "goroutine") || !strings.Contains(last, "tick="+e.tickLine()["ts"].(string)+" result=error 想定外の失敗で止まった: ") {
		t.Fatalf("stack trace の後ろに前置付きの 1 行が無い: %q", last)
	}
}

func TestTimedOutOrchestratorHasItsDecisionsIgnored(t *testing.T) {
	e := newEnv(t)
	launcher := &fakeLauncher{
		orchestrate: func(prompt string) launch.OrchestratorRun {
			run := e.writesDecisions(prompt)
			run.End, run.ExitCode = proc.TimedOut, -1
			return run
		},
		spawn: func(int) (launch.WorkerLaunch, error) {
			t.Fatal("worker を起動した")
			return launch.WorkerLaunch{}, nil
		},
	}

	exit := e.run(fakeGh{issues: twoCandidates}, launcher)

	if exit != 1 {
		t.Fatalf("exit %d, want 1", exit)
	}
	orchestrator := e.tickLine()["orchestrator"].(map[string]any)
	if orchestrator["timed_out"] != true {
		t.Fatalf("orchestrator = %v", orchestrator)
	}
}

func closed() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

func refusesWorkers(t *testing.T) func(int) (launch.WorkerLaunch, error) {
	return func(int) (launch.WorkerLaunch, error) {
		t.Fatal("worker を起動した")
		return launch.WorkerLaunch{}, nil
	}
}

func TestStopRequestBeforeTheOrchestratorKeepsItFromLaunching(t *testing.T) {
	e := newEnv(t)
	launcher := &fakeLauncher{
		orchestrate: func(string) launch.OrchestratorRun {
			t.Fatal("orchestrator を起動した")
			return launch.OrchestratorRun{}
		},
		spawn: refusesWorkers(t),
	}

	out := e.once(fakeGh{issues: twoCandidates}, launcher, closed())

	line := e.tickLine()
	if out.Result != tick.ResultError || out.Halt != tick.HaltedBeforeLaunch || line["error"] != "停止要求で orchestrator を起動しなかった" {
		t.Fatalf("outcome = %+v / line = %v", out, line)
	}
	if _, ok := line["orchestrator"]; ok {
		t.Fatalf("起動しなかった orchestrator の key を載せた: %v", line)
	}
}

func TestStopRequestDuringTheOrchestratorLeavesItsDecisionsUnread(t *testing.T) {
	e := newEnv(t)
	stop := make(chan struct{})
	var launcher *fakeLauncher
	launcher = &fakeLauncher{
		// 起動部の代わり: 決定ファイルを書いた後に届いた停止要求で止まる (届かなければ止まらずに正常終了する)
		orchestrate: func(prompt string) launch.OrchestratorRun {
			run := e.writesDecisions(prompt)
			close(stop)
			select {
			case <-launcher.stop:
				run.End, run.ExitCode = proc.Stopped, -1
			default:
			}
			return run
		},
		spawn: refusesWorkers(t),
	}

	out := e.once(fakeGh{issues: twoCandidates}, launcher, stop)

	line := e.tickLine()
	if out.Result != tick.ResultError || out.Halt != tick.HaltedDuringRun || line["error"] != "停止要求で orchestrator を止めた" {
		t.Fatalf("outcome = %+v / line = %v", out, line)
	}
	if line["orchestrator"] == nil || out.OrchestratorLog != e.project.OrchestratorLog(stem) {
		t.Fatalf("止めた orchestrator の key と log の path が無い: %+v / %v", out, line)
	}
}

func TestStopRequestAfterTheOrchestratorEndedNormallyIsIgnored(t *testing.T) {
	e := newEnv(t)
	stop := make(chan struct{})
	launcher := &fakeLauncher{
		orchestrate: func(prompt string) launch.OrchestratorRun {
			run := e.writesDecisions(prompt)
			close(stop) // 正常終了の直前に届いた
			return run
		},
		spawn: launchesWorkers,
	}

	out := e.once(fakeGh{issues: twoCandidates}, launcher, stop)

	if out.Result != tick.ResultOK || out.Halt != tick.NotHalted || len(out.Spawned) != 2 {
		t.Fatalf("outcome = %+v, want 決定どおり worker を起動して ok", out)
	}
}

func TestOnceLeavesTheFailureLineToTheCaller(t *testing.T) {
	e := newEnv(t)

	out := e.once(fakeGh{issues: "not json"}, &fakeLauncher{}, nil)

	if out.Result != tick.ResultError || !out.Logged || out.Error == "" {
		t.Fatalf("outcome = %+v", out)
	}
	if e.stderr.Len() != 0 {
		t.Fatalf("Once が失敗行を出した: %q", e.stderr.String())
	}
}

func writeSkill(t *testing.T, dir, front string) {
	t.Helper()
	must(t, os.MkdirAll(dir, 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\n"+front+"---\n"), 0o644))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
