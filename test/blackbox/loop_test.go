package blackbox_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// `loop <project> <interval>` (formats.md §13)。起動直後に tick を撃ち、段階的に止まる。
// interval の下限 (1m) を待つ検査 (周期・result が ok 以外でも次へ進む) は internal/loop の単体テストが持つ。

const loopInterval = "5m"

// loopEndLinePattern は終了行 (formats.md §13.2): <時刻> [<project>] loop を止めた (<理由>)。止めずに走っている worker: <n> 本
var loopEndLinePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z \[([^\]]+)\] loop を止めた \((.+)\)。止めずに走っている worker: (\S+) 本$`)

// syncBuffer は loop の出力を、走っている間にも読めるように受ける。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// loopProcess は走らせたままの loop。
type loopProcess struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdout *syncBuffer
	stderr *syncBuffer
	done   chan struct{}
}

// startLoop は clone を cwd にして `loop <project> 5m` を起動し、待たずに返す。stdout は端末でない (pipe)。
func (s *sandbox) startLoop() *loopProcess {
	s.t.Helper()
	p := &loopProcess{t: s.t, stdout: &syncBuffer{}, stderr: &syncBuffer{}, done: make(chan struct{})}
	p.cmd = s.loopCommand()
	p.cmd.Stdout, p.cmd.Stderr = p.stdout, p.stderr
	if err := p.cmd.Start(); err != nil {
		s.t.Fatalf("loop を起動できない: %v", err)
	}
	go func() { p.cmd.Wait(); close(p.done) }()
	s.t.Cleanup(func() {
		if p.running() {
			p.cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

func (s *sandbox) loopCommand() *exec.Cmd {
	cmd := exec.Command(dispatcherBin, "loop", s.project, loopInterval)
	cmd.Dir = s.clone
	for key, value := range s.env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd
}

func (p *loopProcess) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *loopProcess) signal(sig syscall.Signal) {
	p.t.Helper()
	if err := p.cmd.Process.Signal(sig); err != nil {
		p.t.Fatalf("loop に %v を送れない: %v", sig, err)
	}
}

// wait は loop の終了を待ち、exit code と出力を返す。
func (p *loopProcess) wait() runResult {
	p.t.Helper()
	select {
	case <-p.done:
	case <-time.After(runTimeout):
		p.t.Fatalf("loop が %s で終わらない\nstdout:\n%s\nstderr:\n%s", runTimeout, p.stdout, p.stderr)
	}
	return runResult{exit: p.cmd.ProcessState.ExitCode(), stdout: p.stdout.String(), stderr: p.stderr.String()}
}

// waitTickLines は log.jsonl の tick 行が n 行になるまで待つ。
func (s *sandbox) waitTickLines(n int) []logLine {
	s.t.Helper()
	waitFor(s.t, func() bool { return len(s.tickLinesIfAny()) >= n }, fmt.Sprintf("tick 行が %d 行にならない", n))
	return s.tickLinesIfAny()
}

// tickLinesIfAny は走っている loop の横から読む tickLines。log.jsonl がまだ無ければ空、書きかけの末尾の行は数えない。
func (s *sandbox) tickLinesIfAny() []logLine {
	raw, err := os.ReadFile(s.logFile())
	if err != nil {
		return nil
	}
	complete := string(raw[:bytes.LastIndexByte(raw, '\n')+1])
	var lines []logLine
	for _, text := range strings.Split(complete, "\n") {
		var line logLine
		if text == "" || json.Unmarshal([]byte(text), &line) != nil {
			continue
		}
		if _, ok := line["actor"]; !ok {
			lines = append(lines, line)
		}
	}
	return lines
}

func (s *sandbox) lastTickLine() logLine {
	s.t.Helper()
	lines := s.tickLines()
	if len(lines) == 0 {
		s.t.Fatal("tick 行が無い")
	}
	return lines[len(lines)-1]
}

func jsonUnmarshalFile(file string, v any) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// waitOrchestratorCalls は orchestrator の起動が n 件記録されるまで待つ。
func (s *sandbox) waitOrchestratorCalls(n int) []stubCall {
	s.t.Helper()
	waitFor(s.t, func() bool { return len(s.callsMatching("claude", isOrchestratorCall)) >= n },
		fmt.Sprintf("orchestrator の起動が %d 件記録されない", n))
	return s.callsMatching("claude", isOrchestratorCall)
}

// releaseFile は長く走る stub を終わらせる合図の file。作ると終わる。
func (s *sandbox) releaseFile(name string) string { return filepath.Join(s.root, "release-"+name) }

func (s *sandbox) release(name string) { mustWrite(s.t, s.releaseFile(name), "") }

// orchestratorWaitsAfterWriting は orchestrator が d を書き、release("orchestrator") まで終わらないようにする。
func (s *sandbox) orchestratorWaitsAfterWriting(d decisions) {
	s.orchestratorBehaves(stubwire.Rule{
		Stdout:      orchestratorOutput,
		Decisions:   &stubwire.DecisionsWrite{StateDir: s.stateDir(), Content: d.fileContent(s.t)},
		ReleaseFile: s.releaseFile("orchestrator"),
	})
}

// workersRunUntilReleased は worker が release("worker") まで終わらないようにする。後片付けで release する。
func (s *sandbox) workersRunUntilReleased() {
	s.workerRule = stubwire.Rule{ArgContains: workerPromptMarker, Stdout: workerStubOutput, ReleaseFile: s.releaseFile("worker")}
	s.writeClaudeRules()
	// waitForSpawnedWorkers より先に走る (Cleanup は後に登録したものから走る)
	s.t.Cleanup(func() { s.release("worker") })
}

// orchestratorPIDs は stub の呼び出し記録の file 名 (<unixnano>-<pid>.json) から、呼ばれた stub の pid を読む。
func (s *sandbox) orchestratorPIDs() []int {
	s.t.Helper()
	entries, err := os.ReadDir(stubwire.CallsDir(s.stubRoot, "claude"))
	if err != nil {
		s.t.Fatal(err)
	}
	var pids []int
	for _, e := range entries {
		var c stubCall
		if err := jsonUnmarshalFile(filepath.Join(stubwire.CallsDir(s.stubRoot, "claude"), e.Name()), &c); err != nil {
			s.t.Fatal(err)
		}
		if !isOrchestratorCall(c) {
			continue
		}
		_, pid, _ := strings.Cut(strings.TrimSuffix(e.Name(), ".json"), "-")
		n, err := strconv.Atoi(pid)
		if err != nil {
			s.t.Fatalf("呼び出し記録の名前から pid を読めない: %s", e.Name())
		}
		pids = append(pids, n)
	}
	return pids
}

// processState は pid の process の状態 (ps の STAT)。process が無ければ ""。sandbox の PATH の ps は stub なので実物を撃つ。
func processState(t *testing.T, pid int) string {
	t.Helper()
	out, err := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "" // 該当する process が無いと ps は exit 1
	}
	if err != nil {
		t.Fatalf("ps を撃てない: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// alive は pid の process が居て zombie でないか。
func alive(t *testing.T, pid int) bool {
	state := processState(t, pid)
	return state != "" && !strings.HasPrefix(state, "Z")
}

func spawnedPIDs(t *testing.T, line logLine) []int {
	t.Helper()
	var pids []int
	for _, raw := range asList(t, line["spawned"]) {
		entry, _ := raw.(map[string]any)
		pid, _ := entry["pid"].(float64)
		pids = append(pids, int(pid))
	}
	return pids
}

// endLine は loop の stdout の最終行を終了行として分解する ([全体, project, 理由, worker 数])。
func endLine(t *testing.T, stdout string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	m := loopEndLinePattern.FindStringSubmatch(lines[len(lines)-1])
	if m == nil {
		t.Fatalf("stdout の最終行が終了行の形でない:\n%s", stdout)
	}
	return m
}

// --- 起動時の検査 ---

func TestLoopRejectsBadArgumentsWithExit2(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no-arguments", []string{"loop"}},
		{"no-interval", []string{"loop", defaultProject}},
		{"interval-without-unit", []string{"loop", defaultProject, "5"}},
		{"interval-misspelled", []string{"loop", defaultProject, "five"}},
		{"interval-under-a-minute", []string{"loop", defaultProject, "59s"}},
		{"interval-over-a-day", []string{"loop", defaultProject, "24h1m"}},
		{"project-name-with-a-slash", []string{"loop", "a/b", loopInterval}},
		{"too-many-arguments", []string{"loop", defaultProject, loopInterval, "extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)

			r := s.run(tc.args...)

			assertExit(t, r, 2)
			if r.stdout != "" {
				t.Fatalf("起動時の検査で落ちたのに画面を出した:\n%s", r.stdout)
			}
			s.assertNoLog()
		})
	}
}

func TestLoopAcceptsTheIntervalBounds(t *testing.T) {
	for _, interval := range []string{"1m", "24h", "1h30m", "90s"} {
		t.Run(interval, func(t *testing.T) {
			s := newSandbox(t)
			cmd := s.loopCommand()
			cmd.Args[3] = interval
			var stdout syncBuffer
			cmd.Stdout = &stdout
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			s.waitTickLines(1)
			cmd.Process.Signal(syscall.SIGINT)
			if err := cmd.Wait(); err != nil {
				t.Fatalf("interval %s の loop が exit 0 で終わらない: %v\n%s", interval, err, stdout.String())
			}
			if !strings.Contains(stdout.String(), "loop "+interval+" ") {
				t.Fatalf("見出しに撃たれた綴りの interval が無い:\n%s", stdout.String())
			}
		})
	}
}

func TestLoopWithoutAConfigIsExit2(t *testing.T) {
	s := newSandbox(t)
	if err := os.Remove(s.configFile()); err != nil {
		t.Fatal(err)
	}

	r := s.run("loop", s.project, loopInterval)

	assertExit(t, r, 2)
	s.assertNames(r.stderr, s.configFile())
	s.assertNoLog()
}

func TestLoopWithoutAStateDirIsExit1(t *testing.T) {
	s := newSandbox(t)
	if err := os.RemoveAll(s.stateDir()); err != nil {
		t.Fatal(err)
	}

	r := s.run("loop", s.project, loopInterval)

	assertExit(t, r, 1)
	s.assertNames(r.stderr, s.stateDir())
}

func TestLoopRefusesASecondLoopOfTheSameProject(t *testing.T) {
	s := newSandbox(t)
	first := s.startLoop()
	s.waitTickLines(1)

	second := s.run("loop", s.project, loopInterval)

	assertExit(t, second, 3)
	if second.stdout != "" || second.stderr == "" {
		t.Fatalf("2 本目は画面を出さずに stderr へ理由を出す:\nstdout:\n%s\nstderr:\n%s", second.stdout, second.stderr)
	}
	if len(s.tickLines()) != 1 {
		t.Fatal("拒まれた 2 本目が tick を撃った")
	}
	first.signal(syscall.SIGINT)
	assertExit(t, first.wait(), 0)
}

// --- 周期と画面 ---

func TestLoopStopsOnTheFirstSignalBetweenTicksWithoutStartingAnother(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			s := newSandbox(t)
			p := s.startLoop()
			s.waitTickLines(1)

			p.signal(sig)
			r := p.wait()

			assertExit(t, r, 0)
			if n := len(s.tickLines()); n != 1 {
				t.Fatalf("tick 行 = %d 行, want 起動直後の 1 行だけ (合間の停止は次の tick を始めない)", n)
			}
			m := endLine(t, r.stdout)
			if m[1] != s.project || m[2] != "停止要求 "+signalName(sig) || m[3] != "0" {
				t.Fatalf("終了行 = %q", m[0])
			}
		})
	}
}

func signalName(sig syscall.Signal) string {
	return map[syscall.Signal]string{syscall.SIGINT: "SIGINT", syscall.SIGTERM: "SIGTERM", syscall.SIGHUP: "SIGHUP"}[sig]
}

func TestLoopAppendsTheScreenWithoutClearingOrGuideWhenStdoutIsNotATerminal(t *testing.T) {
	s := newSandbox(t)
	p := s.startLoop()
	lines := s.waitTickLines(1)
	waitFor(t, func() bool { return strings.Contains(p.stdout.String(), "最終 tick ") }, "tick の直後に画面を追記しない")

	p.signal(syscall.SIGINT)
	r := p.wait()

	if strings.Contains(r.stdout, "\033[") {
		t.Fatalf("端末でない stdout に画面を消す制御列を出した:\n%q", r.stdout)
	}
	if strings.Contains(r.stdout, "Ctrl+C") {
		t.Fatalf("端末でない stdout に操作案内を出した:\n%s", r.stdout)
	}
	heading := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(s.project) + `  loop 5m  待機 · 次の tick \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z \(あと \S+\)$`)
	if !heading.MatchString(r.stdout) {
		t.Fatalf("待機の見出し行が無い:\n%s", r.stdout)
	}
	last := "最終 tick " + shortTS(asString(t, lines[0]["ts"])) + " ok · 指示 0"
	if !strings.Contains(r.stdout, "\n"+last+"\n") {
		t.Fatalf("最終 tick の行 %q が無い:\n%s", last, r.stdout)
	}
	if !strings.Contains(r.stdout, "\n\n") {
		t.Fatalf("追記した中身を空行で区切っていない:\n%s", r.stdout)
	}
}

func TestLoopShowsTheErrorOfTheLastTickInsteadOfWritingTheFailureLine(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(s.configWith(`readylabel = "x"`, "max_wip = 2"))
	p := s.startLoop()
	lines := s.waitTickLines(1)
	waitFor(t, func() bool { return strings.Contains(p.stdout.String(), "最終 tick ") }, "tick の直後に画面を追記しない")

	p.signal(syscall.SIGINT)
	r := p.wait()

	assertExit(t, r, 0)
	if !strings.Contains(r.stdout, "最終 tick "+shortTS(asString(t, lines[0]["ts"]))+" config_error") {
		t.Fatalf("最終 tick の result を出していない:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "  ! "+asString(t, lines[0]["error"])) {
		t.Fatalf("直近の tick の error を ! の行に出していない:\n%s", r.stdout)
	}
	if strings.Contains(r.stderr, "result=config_error") {
		t.Fatalf("loop の中の tick が失敗行を stderr に出した:\n%s", r.stderr)
	}
}

func TestLoopKeepsStoppingInStepsWhenStdoutIsGone(t *testing.T) {
	s := newSandbox(t)
	cmd := s.loopCommand()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// 読み手の消えた pipe: 以後の書き込みは EPIPE (Go の既定なら SIGPIPE で倒れる)
	stdout.Close()
	s.waitTickLines(1)

	cmd.Process.Signal(syscall.SIGINT)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stdout の読み手が消えた loop が停止要求で exit 0 にならない: %v", err)
		}
	case <-time.After(runTimeout):
		cmd.Process.Kill()
		t.Fatal("stdout の読み手が消えた loop が停止要求で止まらない")
	}
}

// --- 段階的な停止 ---

func TestLoopFirstSignalDuringATickFinishesTheTickThenStops(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			s := newSandbox(t)
			s.setIssues(readyIssue(42))
			s.workersRunUntilReleased()
			s.orchestratorWaitsAfterWriting(startDecisions(playbookPath(s.defaultInstallPath(), "playbook-implementation"), 42))
			p := s.startLoop()
			s.waitOrchestratorCalls(1)
			p.signal(sig)
			s.release("orchestrator")

			r := p.wait()

			assertExit(t, r, 0)
			// 1 回目で止まっていれば ok の行も起動記録も無い
			line := s.lastTickLine()
			if line["result"] != "ok" || len(spawnedPIDs(t, line)) != 1 {
				t.Fatalf("tick を最後まで進めていない (決定どおり worker を起動して ok の行を書く): %v", line)
			}
			// 走っている worker の数は status の現況から数える (sandbox の ps は stub なので、ここでは数の形だけを見る)
			if m := endLine(t, r.stdout); m[2] != "停止要求 "+signalName(sig) || !regexp.MustCompile(`^\d+$`).MatchString(m[3]) {
				t.Fatalf("終了行 = %q, want 停止要求 %s と走っている worker の数", m[0], signalName(sig))
			}
		})
	}
}

func TestLoopFirstSignalLeavesTheWorkerOfTheTickRunning(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.workersRunUntilReleased()
	s.orchestratorWaitsAfterWriting(startDecisions(playbookPath(s.defaultInstallPath(), "playbook-implementation"), 42))
	p := s.startLoop()
	s.waitOrchestratorCalls(1)
	p.signal(syscall.SIGINT)
	s.release("orchestrator")

	assertExit(t, p.wait(), 0)

	if pid := spawnedPIDs(t, s.lastTickLine())[0]; !alive(t, pid) {
		t.Fatalf("loop の停止で起動済みの worker (pid %d) が止まった", pid)
	}
}

// secondSignalDuringTheOrchestrator は orchestrator が決定ファイルを書いて待っている間に、2 回目の停止要求で loop を止める。
func (s *sandbox) secondSignalDuringTheOrchestrator() runResult {
	s.t.Helper()
	s.setIssues(readyIssue(42))
	s.orchestratorWaitsAfterWriting(startDecisions(playbookPath(s.defaultInstallPath(), "playbook-implementation"), 42))
	p := s.startLoop()
	s.waitOrchestratorCalls(1)
	p.signal(syscall.SIGINT)
	p.signal(syscall.SIGINT)
	return p.wait()
}

func TestLoopSecondSignalLeavesAnErrorLineWithoutReadingTheDecisions(t *testing.T) {
	s := newSandbox(t)

	r := s.secondSignalDuringTheOrchestrator()

	assertExit(t, r, 0)
	line := s.lastTickLine()
	if line["result"] != "error" || line["error"] != "停止要求で orchestrator を止めた" {
		t.Fatalf("tick 行 = %v, want result error と停止要求で止めた error", line)
	}
	// 決定ファイルには start 42 が書かれている。読んでいれば worker を起動して spawned に載る
	if _, ok := line["orchestrator"]; !ok || len(asList(t, line["spawned"])) != 0 {
		t.Fatalf("起動して止めた orchestrator を載せ、worker は起動しない: %v", line)
	}
}

func TestLoopSecondSignalStopsTheOrchestratorAndPointsToItsLog(t *testing.T) {
	s := newSandbox(t)

	r := s.secondSignalDuringTheOrchestrator()

	for _, pid := range s.orchestratorPIDs() {
		if alive(t, pid) {
			t.Fatalf("orchestrator (pid %d) が止まっていない", pid)
		}
	}
	orchestratorLog := s.orchestratorLogFile(stemOf(t, s, s.lastTickLine()))
	if want := "2 回目の停止要求で orchestrator を止めた — 経過は " + orchestratorLog + "。wip を付けたまま残った issue が無いか確かめる"; endLine(t, r.stdout)[2] != want {
		t.Fatalf("終了行の理由 = %q, want %q", endLine(t, r.stdout)[2], want)
	}
}

func TestLoopSecondSignalLeavesWorkersOfEarlierTicksRunning(t *testing.T) {
	s := newSandbox(t)
	playbook := startScenario(s)
	s.workersRunUntilReleased()
	assertExit(t, s.tick(), 0)
	worker := spawnedPIDs(t, s.lastTickLine())[0]
	s.orchestratorWaitsAfterWriting(startDecisions(playbook, 42))
	p := s.startLoop()
	s.waitOrchestratorCalls(2)

	p.signal(syscall.SIGTERM)
	p.signal(syscall.SIGTERM)
	assertExit(t, p.wait(), 0)

	if !alive(t, worker) {
		t.Fatalf("2 回目の停止要求で前の tick の worker (pid %d) が止まった", worker)
	}
}

func TestLoopReapsWorkersThatHaveEnded(t *testing.T) {
	s := newSandbox(t)
	startScenario(s)
	p := s.startLoop()
	line := s.waitTickLines(1)[0]
	pid := spawnedPIDs(t, line)[0]

	// worker の stub はすぐ終わる。loop が回収しなければ loop が生きている間 zombie として残る
	waitFor(t, func() bool { return processState(t, pid) == "" },
		fmt.Sprintf("終わった worker (pid %d) が残っている (state %q)", pid, processState(t, pid)))

	p.signal(syscall.SIGINT)
	assertExit(t, p.wait(), 0)
}

// stemOf は tick 行の ts から、その tick の file 名の幹 (formats.md §1) を作る。
func stemOf(t *testing.T, s *sandbox, line logLine) string {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, asString(t, line["ts"]))
	if err != nil {
		t.Fatal(err)
	}
	return at.UTC().Format("20060102T150405.000000Z")
}

func shortTS(ts string) string {
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return at.UTC().Format("2006-01-02T15:04:05Z")
}
