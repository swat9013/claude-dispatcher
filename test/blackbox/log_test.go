package blackbox_test

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// log.jsonl の行 (formats.md §4)。外部の読み手を持つ公開契約。

func TestQuietTickLineCarriesTheObservationKeysOnly(t *testing.T) {
	s := newSandbox(t)

	r := s.tick()

	line := s.assertOutcome(r, outcomeOK)
	want := []string{"candidates", "cwd", "instruction_file", "instructions", "observed", "project", "result", "ts", "wip"}
	if got := keys(line); !slices.Equal(got, want) {
		t.Fatalf("静止 tick の行の key = %v, want %v", got, want)
	}
	if line["instruction_file"] != nil {
		t.Fatalf("指示 0 件なのに instruction_file = %v", line["instruction_file"])
	}
	if got := asMap(t, line["instructions"]); len(got) != 0 {
		t.Fatalf("指示 0 件の instructions = %v, want {}", got)
	}
}

func TestQuietTickDoesNotLaunchClaude(t *testing.T) {
	s := newSandbox(t)

	assertExit(t, s.tick(), 0)

	s.assertNoClaude("指示 0 件なのに")
	s.assertNoInstructionFile("指示 0 件なのに")
}

func TestEachTickAppendsOneLineKeepingThePreviousOnes(t *testing.T) {
	s := newSandbox(t)
	assertExit(t, s.tick(), 0)
	first := string(mustRead(t, s.logFile()))

	assertExit(t, s.tick(), 0)

	after := string(mustRead(t, s.logFile()))
	if !strings.HasPrefix(after, first) || len(s.tickLines()) != 2 {
		t.Fatalf("log.jsonl が append-only で 1 tick 1 行になっていない:\n%s", after)
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
	s.orchestratorSkips(1, 2)

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

func TestObservationFailureIsAnErrorRatherThanAQuietTick(t *testing.T) {
	s := newSandbox(t)
	s.ghFails([]string{"api", "graphql"}, 1, "HTTP 502: Bad Gateway\n")

	r := s.tick()

	line := s.assertOutcome(r, outcomeError)
	if _, ok := line["error"]; !ok {
		t.Fatalf("error が無い: %v", line)
	}
	if _, ok := line["instructions"]; ok {
		t.Fatalf("観測に至っていない tick が instructions を載せた (指示 0 件と区別できない): %v", line)
	}
}

func TestMultilineErrorIsFoldedIntoOneLine(t *testing.T) {
	s := newSandbox(t)
	s.ghFails([]string{"api", "graphql"}, 1, "first problem\nsecond problem\n")

	s.tick()

	msg := asString(t, s.onlyTickLine()["error"])
	if strings.Contains(msg, "\n") || !strings.Contains(msg, "first problem / second problem") {
		t.Fatalf("複数行の error が ` / ` で 1 行に畳まれていない: %q", msg)
	}
}

// --- claude を起動した tick ---

func TestTickThatLaunchedClaudeLogsTheOrchestratorRun(t *testing.T) {
	s := newSandbox(t)
	startScenario(s)

	r := s.tick()

	orchestrator := asMap(t, s.assertOutcome(r, outcomeOK)["orchestrator"])
	if got := keys(orchestrator); !slices.Equal(got, []string{"exit_code", "seconds", "session_id", "timed_out"}) {
		t.Fatalf("orchestrator の key = %v", got)
	}
	if number(t, orchestrator["exit_code"]) != 0 || orchestrator["timed_out"] != false {
		t.Fatalf("orchestrator = %v", orchestrator)
	}
	if _, ok := orchestrator["seconds"].(float64); !ok {
		t.Fatalf("seconds が数値でない: %v", orchestrator["seconds"])
	}
}

func TestOrchestratorSessionIDIsTheOneGivenToClaude(t *testing.T) {
	s := newSandbox(t)
	startScenario(s)

	s.tick()

	sessionID := asString(t, asMap(t, s.onlyTickLine()["orchestrator"])["session_id"])
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

func onlySpawned(t *testing.T, line logLine) map[string]any {
	t.Helper()
	spawned := asList(t, line["spawned"])
	if len(spawned) != 1 {
		t.Fatalf("spawned = %v", spawned)
	}
	return asMap(t, spawned[0])
}

func TestSpawnedWorkerIsLoggedWithItsIssuePidAndLog(t *testing.T) {
	s := newSandbox(t)
	startScenario(s)

	s.tick()

	line := s.onlyTickLine()
	entry := onlySpawned(t, line)
	if got := keys(entry); !slices.Equal(got, []string{"issue", "kind", "log", "pid", "session_id"}) {
		t.Fatalf("spawned の key = %v", got)
	}
	if number(t, entry["issue"]) != 42 || entry["kind"] != "start" || number(t, entry["pid"]) <= 0 {
		t.Fatalf("spawned = %v", entry)
	}
	if want := s.workerLogFile(42, tickStem(asString(t, line["instruction_file"]))); entry["log"] != want {
		t.Fatalf("log = %v, want %s", entry["log"], want)
	}
}

func TestSpawnedSessionIDIsTheOneGivenToTheWorker(t *testing.T) {
	s := newSandbox(t)
	startScenario(s)

	s.tick()

	sessionID := onlySpawned(t, s.onlyTickLine())["session_id"]
	workers := s.waitWorkerCalls(1)
	if got, _ := workers[0].flagValue("--session-id"); got != sessionID {
		t.Fatalf("worker に渡した --session-id %q と log の session_id %v が違う", got, sessionID)
	}
}

func TestWorkerAndOrchestratorGetDistinctSessionIDs(t *testing.T) {
	s := newSandbox(t)
	startScenario(s)

	s.tick()

	line := s.onlyTickLine()
	if onlySpawned(t, line)["session_id"] == asMap(t, line["orchestrator"])["session_id"] {
		t.Fatal("worker と orchestrator が同じ session id を持つ")
	}
}

func TestOrchestratorDecisionsAreCopiedToAnOrchestratorLine(t *testing.T) {
	s := newSandbox(t)
	startScenario(s)

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

// --- 途中で止まった tick ---

func TestTickStoppedAfterTheOrchestratorKeepsItsSettledKeys(t *testing.T) {
	s := newSandbox(t)
	startScenario(s)
	s.blockWorkerLogs()

	r := s.tick()

	line := s.assertOutcome(r, outcomeError)
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

func TestTickStoppedMidSpawnKeepsTheWorkersAlreadyLaunched(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42), readyIssue(43))
	playbook := playbookPath(s.defaultInstallPath(), "playbook-implementation")
	d := startDecisions(playbook, 42, 43)
	// 43 の worker log の path を dir で塞ぐ: 42 を起動した後、43 の起動で止まる
	s.orchestratorWritesBlockingWorkerLogs(d, 43)

	r := s.tick()

	// 起動の順序と log を開く時機は設計 doc が決めていない。起動記録のある worker と spawned が一致することを見る
	line := s.assertOutcome(r, outcomeError)
	var logged []int
	for _, raw := range asList(t, line["spawned"]) {
		logged = append(logged, number(t, asMap(t, raw)["issue"]))
	}
	s.waitWorkerCalls(len(logged))
	settleDetachedWorkers()
	var launched []int
	for _, c := range s.callsMatching("claude", isWorkerCall) {
		for _, n := range []int{42, 43} {
			if c.hasArg(workerPrompt(n, playbook)) {
				launched = append(launched, n)
			}
		}
	}
	slices.Sort(launched)
	if !slices.Equal(logged, launched) || slices.Contains(logged, 43) {
		t.Fatalf("spawned %v と起動した worker %v が一致しない (43 は log を開けず起動できない)", logged, launched)
	}
}

func TestOrchestratorOutputGoesNextToTheDecisionsFile(t *testing.T) {
	s := newSandbox(t)
	startScenario(s)

	s.tick()

	log := s.orchestratorLogFile(tickStem(asString(t, s.onlyTickLine()["instruction_file"])))
	if got := string(mustRead(t, log)); !strings.Contains(got, strings.TrimSpace(orchestratorOutput)) {
		t.Fatalf("orchestrator の出力が %s に無い: %q", log, got)
	}
}

func TestWorkerOutputGoesToItsLog(t *testing.T) {
	s := newSandbox(t)
	startScenario(s)

	s.tick()

	s.waitWorkerCalls(1)
	file := asString(t, onlySpawned(t, s.onlyTickLine())["log"])
	waitFor(t, func() bool {
		raw, err := os.ReadFile(file)
		return err == nil && strings.Contains(string(raw), strings.TrimSpace(workerStubOutput))
	}, "worker の出力が "+file+" に落ちていない")
}
