package blackbox_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// log.jsonl の行 (formats.md §4)。外部の読み手を持つ公開契約。

func TestQuietTickLeavesOneLineAndDoesNotLaunchClaude(t *testing.T) {
	s := newSandbox(t)

	r := s.tick()

	assertExit(t, r, 0)
	if r.stderr != "" {
		t.Fatalf("正常な tick が stderr に書いた: %q", r.stderr)
	}
	line := s.onlyTickLine()
	want := []string{"candidates", "cwd", "instruction_file", "instructions", "observed", "project", "result", "ts", "wip"}
	if got := keys(line); !slices.Equal(got, want) {
		t.Fatalf("静止 tick の行の key = %v, want %v", got, want)
	}
	assertResult(t, line, "ok")
	if line["instruction_file"] != nil {
		t.Fatalf("指示 0 件なのに instruction_file = %v", line["instruction_file"])
	}
	if got := asMap(t, line["instructions"]); len(got) != 0 {
		t.Fatalf("指示 0 件の instructions = %v, want {}", got)
	}
	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("指示 0 件なのに claude を起動した: %v", calls)
	}
	if files := s.instructionFiles(); len(files) != 0 {
		t.Fatalf("指示 0 件なのに指示ファイルを書いた: %v", files)
	}
}

func TestTickLineCarriesTsProjectAndCwd(t *testing.T) {
	s := newSandbox(t)

	s.tick()

	line := s.onlyTickLine()
	if ts, _ := line["ts"].(string); !logTSPattern.MatchString(ts) {
		t.Fatalf("ts がマイクロ秒までの UTC RFC 3339 でない: %q", ts)
	}
	if line["project"] != s.project {
		t.Fatalf("project = %v", line["project"])
	}
	if line["cwd"] != s.clone {
		t.Fatalf("cwd = %v, want %s", line["cwd"], s.clone)
	}
}

func TestObservedCountsAreLogged(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(1), readyIssue(2), issue{number: 3, labels: []string{wipLabel}})
	s.setPRs(pullRequest{number: 10, branch: workerBranch(3), closes: []int{3}})
	s.orchestratorWrites(decisions{Decisions: []decision{
		{Issue: 1, Action: "skip", Reason: "テスト"}, {Issue: 2, Action: "skip", Reason: "テスト"},
	}}.json(t))

	s.tick()

	line := s.onlyTickLine()
	observed := asMap(t, line["observed"])
	if number(t, observed["issues"]) != 3 || number(t, observed["cls"]) != 1 {
		t.Fatalf("observed = %v", observed)
	}
	if number(t, line["candidates"]) != 2 || number(t, line["wip"]) != 1 {
		t.Fatalf("candidates = %v, wip = %v", line["candidates"], line["wip"])
	}
	if got := asMap(t, line["instructions"]); len(got) != 1 || number(t, got["start"]) != 1 {
		t.Fatalf("instructions = %v, want {start: 1}", got)
	}
}

func TestObservationFailureIsAnErrorNotAQuietTick(t *testing.T) {
	s := newSandbox(t)
	s.respond("gh", stubRule{ArgsPrefix: []string{"api", "graphql"}, Exit: 1, Stderr: "HTTP 502: Bad Gateway\n"})

	r := s.tick()

	assertExit(t, r, 1)
	line := s.onlyTickLine()
	assertResult(t, line, "error")
	if _, ok := line["error"]; !ok {
		t.Fatalf("error が無い: %v", line)
	}
	if _, ok := line["instructions"]; ok {
		t.Fatalf("観測に至っていない tick が instructions を載せた (指示 0 件と区別できない): %v", line)
	}
}

func TestMultilineErrorIsFoldedIntoOneLine(t *testing.T) {
	s := newSandbox(t)
	s.respond("gh", stubRule{ArgsPrefix: []string{"api", "graphql"}, Exit: 1, Stderr: "first problem\nsecond problem\n"})

	s.tick()

	msg := asString(t, s.onlyTickLine()["error"])
	if strings.Contains(msg, "\n") || !strings.Contains(msg, "first problem / second problem") {
		t.Fatalf("複数行の error が ` / ` で 1 行に畳まれていない: %q", msg)
	}
}

// startScenario は候補 42 に start を決め、worker を 1 件起動させる。返り値は選んだ playbook の path。
func startScenario(t *testing.T, s *sandbox) string {
	t.Helper()
	s.setIssues(readyIssue(42))
	playbook := playbookPath(s.defaultInstallPath(), "playbook-implementation")
	s.orchestratorWrites(decisions{
		Decisions: []decision{{Issue: 42, Action: "start", Reason: "着手できる"}},
		Spawn:     []spawn{{Issue: 42, Kind: "start", Prompt: workerPrompt(42, playbook), Playbooks: []string{playbook}}},
	}.json(t))
	return playbook
}

func TestTickThatLaunchedClaudeLogsTheOrchestratorRun(t *testing.T) {
	s := newSandbox(t)
	startScenario(t, s)

	r := s.tick()

	assertExit(t, r, 0)
	line := s.onlyTickLine()
	assertResult(t, line, "ok")
	orchestrator := asMap(t, line["orchestrator"])
	if got := keys(orchestrator); !slices.Equal(got, []string{"exit_code", "seconds", "session_id", "timed_out"}) {
		t.Fatalf("orchestrator の key = %v", got)
	}
	if number(t, orchestrator["exit_code"]) != 0 || orchestrator["timed_out"] != false {
		t.Fatalf("orchestrator = %v", orchestrator)
	}
	if _, ok := orchestrator["seconds"].(float64); !ok {
		t.Fatalf("seconds が数値でない: %v", orchestrator["seconds"])
	}
	sessionID := asString(t, orchestrator["session_id"])
	if !uuidPattern.MatchString(sessionID) {
		t.Fatalf("session_id が UUID でない: %q", sessionID)
	}
	calls := s.callsMatching("claude", isOrchestratorCall)
	if len(calls) != 1 {
		t.Fatalf("orchestrator の起動が 1 回でない: %d", len(calls))
	}
	if got, _ := calls[0].flagValue("--session-id"); got != sessionID {
		t.Fatalf("claude に渡した --session-id %q と log の session_id %q が違う", got, sessionID)
	}
}

func TestSpawnedWorkersAreLoggedWithPidLogAndSessionID(t *testing.T) {
	s := newSandbox(t)
	startScenario(t, s)

	s.tick()

	line := s.onlyTickLine()
	spawned := asList(t, line["spawned"])
	if len(spawned) != 1 {
		t.Fatalf("spawned = %v", spawned)
	}
	entry := asMap(t, spawned[0])
	if got := keys(entry); !slices.Equal(got, []string{"issue", "kind", "log", "pid", "session_id"}) {
		t.Fatalf("spawned の key = %v", got)
	}
	if number(t, entry["issue"]) != 42 || entry["kind"] != "start" || number(t, entry["pid"]) <= 0 {
		t.Fatalf("spawned = %v", entry)
	}
	stem := tickStem(asString(t, line["instruction_file"]))
	if want := filepath.Join(s.stateDir(), "workers", fmt.Sprintf("42-%s.log", stem)); entry["log"] != want {
		t.Fatalf("log = %v, want %s", entry["log"], want)
	}
	workers := s.waitWorkerCalls(1)
	if len(workers) != 1 {
		t.Fatalf("worker の起動が 1 回でない: %d", len(workers))
	}
	if got, _ := workers[0].flagValue("--session-id"); got != entry["session_id"] {
		t.Fatalf("worker に渡した --session-id %q と log の session_id %v が違う", got, entry["session_id"])
	}
	if entry["session_id"] == asMap(t, line["orchestrator"])["session_id"] {
		t.Fatal("worker と orchestrator が同じ session id を持つ")
	}
}

func TestOrchestratorDecisionsAreCopiedToAnOrchestratorLine(t *testing.T) {
	s := newSandbox(t)
	startScenario(t, s)

	s.tick()

	tickLine := s.onlyTickLine()
	lines := s.orchestratorLines()
	if len(lines) != 1 {
		t.Fatalf("orchestrator 行が 1 行でない: %v", lines)
	}
	line := lines[0]
	if got := keys(line); !slices.Equal(got, []string{"actor", "decisions", "instruction_file", "project", "ts"}) {
		t.Fatalf("orchestrator 行の key = %v", got)
	}
	if line["ts"] != tickLine["ts"] || line["instruction_file"] != tickLine["instruction_file"] || line["project"] != s.project {
		t.Fatalf("orchestrator 行が同じ tick を指していない: %v / %v", line, tickLine)
	}
	got := asList(t, line["decisions"])
	if len(got) != 1 {
		t.Fatalf("decisions = %v", got)
	}
	d := asMap(t, got[0])
	if number(t, d["issue"]) != 42 || d["action"] != "start" || d["reason"] != "着手できる" {
		t.Fatalf("決定ファイルの decisions がそのまま写っていない: %v", d)
	}
}

func TestTickStoppedAfterLaunchingTheOrchestratorKeepsWhatWasSettled(t *testing.T) {
	s := newSandbox(t)
	startScenario(t, s)
	// worker log の置き場を file で塞ぐ: orchestrator は走り終え、worker の起動で止まる
	mustWrite(t, filepath.Join(s.stateDir(), "workers"), "not a directory")

	r := s.tick()

	assertExit(t, r, 1)
	line := s.onlyTickLine()
	assertResult(t, line, "error")
	if file, _ := line["instruction_file"].(string); file == "" {
		t.Fatalf("止まった tick の行から instruction_file が落ちた: %v", line)
	}
	if _, ok := line["orchestrator"].(map[string]any); !ok {
		t.Fatalf("止まった tick の行から orchestrator が落ちた: %v", line)
	}
	if _, ok := line["spawned"].([]any); !ok {
		t.Fatalf("orchestrator を起動した tick の行に spawned が無い: %v", line)
	}
}

func TestOrchestratorOutputGoesNextToTheDecisionsFile(t *testing.T) {
	s := newSandbox(t)
	startScenario(t, s)

	s.tick()

	stem := tickStem(asString(t, s.onlyTickLine()["instruction_file"]))
	log := filepath.Join(s.stateDir(), "decisions", stem+".orchestrator.log")
	if got := string(mustRead(t, log)); !strings.Contains(got, strings.TrimSpace(orchestratorOutput)) {
		t.Fatalf("orchestrator の出力が %s に無い: %q", log, got)
	}
}

func TestWorkerOutputGoesToItsLog(t *testing.T) {
	s := newSandbox(t)
	startScenario(t, s)

	s.tick()

	s.waitWorkerCalls(1)
	file := asString(t, asMap(t, asList(t, s.onlyTickLine()["spawned"])[0])["log"])
	waitFor(t, func() bool {
		raw, err := os.ReadFile(file)
		return err == nil && strings.Contains(string(raw), strings.TrimSpace(workerStubOutput))
	}, "worker の出力が "+file+" に落ちていない")
}
