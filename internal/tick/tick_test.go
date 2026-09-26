package tick_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/launch"
	"github.com/swat9013/claude-dispatcher/internal/paths"
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
	spawn       func(n int) (launch.WorkerLaunch, error)
	prompts     []string
	workers     []string
}

func (f *fakeLauncher) RunOrchestrator(prompt, logFile string, timeout time.Duration) (launch.OrchestratorRun, error) {
	f.prompts = append(f.prompts, prompt)
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

func (e *env) run(gh fakeGh, launcher *fakeLauncher) int {
	return tick.Run(tick.Options{
		Project: "widgets", Roots: e.roots, Home: e.home, Cwd: e.cwd, Env: []string{"HOME=" + e.home},
		Now: now, Stdout: &bytes.Buffer{}, Stderr: &e.stderr,
		Gh:       func([]string) github.Runner { return gh },
		Launcher: func([]string) (launch.Launcher, error) { return launcher, nil },
	})
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

func TestTickLaunchesSessionsOnlyThroughTheLauncherSeam(t *testing.T) {
	e := newEnv(t)
	launcher := &fakeLauncher{orchestrate: e.writesDecisions, spawn: func(n int) (launch.WorkerLaunch, error) {
		return launch.WorkerLaunch{PID: 1000 + n, SessionID: fmt.Sprintf("worker-%d", n)}, nil
	}}

	exit := e.run(fakeGh{issues: twoCandidates}, launcher)

	if exit != 0 {
		t.Fatalf("exit %d\n%s", exit, e.stderr.String())
	}
	if len(launcher.prompts) != 1 || len(launcher.workers) != 2 {
		t.Fatalf("orchestrator %d 回 / worker %d 回", len(launcher.prompts), len(launcher.workers))
	}
	if !strings.Contains(launcher.prompts[0], e.project.InstructionFile(stem)) {
		t.Fatal("orchestrator の prompt に指示ファイルの path が無い")
	}
	if spawned := e.tickLine()["spawned"].([]any); len(spawned) != 2 {
		t.Fatalf("spawned = %v", spawned)
	}
}

func TestPanicMidTickLeavesTheSettledKeysAndAPrefixedLastLine(t *testing.T) {
	e := newEnv(t)
	launcher := &fakeLauncher{orchestrate: e.writesDecisions, spawn: func(n int) (launch.WorkerLaunch, error) {
		if n == 2 {
			panic("worker 起動の途中で壊れた")
		}
		return launch.WorkerLaunch{PID: 1000 + n, SessionID: "worker-1"}, nil
	}}

	exit := e.run(fakeGh{issues: twoCandidates}, launcher)

	if exit != 1 {
		t.Fatalf("exit %d, want 1", exit)
	}
	line := e.tickLine()
	if line["result"] != "error" || line["instruction_file"] != e.project.InstructionFile(stem) || line["orchestrator"] == nil {
		t.Fatalf("想定外の失敗で止まった tick の行から確定済みの key が落ちた: %v", line)
	}
	if spawned := line["spawned"].([]any); len(spawned) != 1 {
		t.Fatalf("止まる前に起動した worker が spawned に無い: %v", spawned)
	}
	if msg, _ := line["error"].(string); !strings.HasPrefix(msg, "想定外の失敗で止まった: ") {
		t.Fatalf("error = %q", msg)
	}
	lines := strings.Split(strings.TrimRight(e.stderr.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(e.stderr.String(), "goroutine") || !strings.Contains(last, "tick="+line["ts"].(string)+" result=error 想定外の失敗で止まった: ") {
		t.Fatalf("stack trace の後ろに前置付きの 1 行が無い: %q", last)
	}
}

func TestTimedOutOrchestratorHasItsDecisionsIgnored(t *testing.T) {
	e := newEnv(t)
	launcher := &fakeLauncher{
		orchestrate: func(prompt string) launch.OrchestratorRun {
			run := e.writesDecisions(prompt)
			run.TimedOut, run.ExitCode = true, -1
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
