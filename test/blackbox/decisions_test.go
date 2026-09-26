package blackbox_test

import (
	"path/filepath"
	"slices"
	"testing"
)

// 決定ファイル (formats.md §5.2) の検査と、決定どおりの worker 起動 (system.md §9)。

func TestOrchestratorIsLaunchedWithTheContractFlagsInTheClone(t *testing.T) {
	s := newSandbox(t)
	startScenario(t, s)

	s.tick()

	calls := s.callsMatching("claude", isOrchestratorCall)
	if len(calls) != 1 {
		t.Fatalf("orchestrator の起動 = %d 回", len(calls))
	}
	c := calls[0]
	if _, ok := c.flagValue("-p"); !ok {
		t.Fatalf("-p で起動していない: %v", c.args())
	}
	if mode, _ := c.flagValue("--permission-mode"); mode != "auto" {
		t.Fatalf("--permission-mode = %q, want auto", mode)
	}
	if id, _ := c.flagValue("--session-id"); !uuidPattern.MatchString(id) {
		t.Fatalf("--session-id = %q", id)
	}
	if c.Cwd != s.clone {
		t.Fatalf("orchestrator の cwd = %s, want clone %s", c.Cwd, s.clone)
	}
	line := s.onlyTickLine()
	instructionFile := line["instruction_file"].(string)
	if !c.contains(instructionFile) {
		t.Fatalf("orchestrator の prompt に指示ファイルの path %s が無い", instructionFile)
	}
	stem := instructionFile[len(filepath.Dir(instructionFile))+1 : len(instructionFile)-len(".json")]
	if decisionsFile := filepath.Join(s.stateDir(), "decisions", stem+".json"); !c.contains(decisionsFile) {
		t.Fatalf("orchestrator の prompt に決定ファイルの path %s が無い", decisionsFile)
	}
}

func TestDecidedWorkerIsLaunchedWithItsPromptInTheClone(t *testing.T) {
	s := newSandbox(t)
	playbook := startScenario(t, s)

	assertExit(t, s.tick(), 0)

	workers := s.waitWorkerCalls(1)
	if len(workers) != 1 {
		t.Fatalf("worker の起動 = %d 回", len(workers))
	}
	w := workers[0]
	if prompt, _ := w.flagValue("-p"); prompt != workerPrompt(42, playbook) {
		t.Fatalf("worker の prompt が決定ファイルの spawn prompt と違う: %q", prompt)
	}
	if mode, _ := w.flagValue("--permission-mode"); mode != "auto" {
		t.Fatalf("--permission-mode = %q, want auto", mode)
	}
	if w.Cwd != s.clone {
		t.Fatalf("worker の cwd = %s, want clone %s", w.Cwd, s.clone)
	}
}

func TestDecisionsWithoutSpawnLaunchNoWorker(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.orchestratorSkips(42)

	r := s.tick()

	assertExit(t, r, 0)
	assertResult(t, s.onlyTickLine(), "ok")
	if spawned := s.onlyTickLine()["spawned"].([]any); len(spawned) != 0 {
		t.Fatalf("spawned = %v", spawned)
	}
	s.assertNoWorkerCalls()
}

// assertRejectedBeforeLaunch は決定ファイルが検査に落ち、1 件も起動せず error になったことを確かめる。
func assertRejectedBeforeLaunch(t *testing.T, s *sandbox, r runResult) {
	t.Helper()
	assertExit(t, r, 1)
	line := s.onlyTickLine()
	assertResult(t, line, "error")
	if spawned, _ := line["spawned"].([]any); len(spawned) != 0 {
		t.Fatalf("検査に落ちた決定ファイルで起動した: %v", spawned)
	}
	s.assertNoWorkerCalls()
}

func TestMissingDecisionsFileLaunchesNothing(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.orchestratorBehaves(stubRule{Stdout: orchestratorOutput})

	r := s.tick()

	assertRejectedBeforeLaunch(t, s, r)
}

func TestMalformedDecisionsFileLaunchesNothing(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.orchestratorWrites("{not json")

	r := s.tick()

	assertRejectedBeforeLaunch(t, s, r)
}

func TestInvalidSpawnRejectsTheWholeDecisionsFile(t *testing.T) {
	type fixture struct {
		spawn    spawn
		decision decision
	}
	for _, tc := range []struct {
		name string
		make func(s *sandbox) fixture
	}{
		{"action が語彙の外", func(s *sandbox) fixture {
			p := playbookPath(s.defaultInstallPath(), "playbook-implementation")
			return fixture{
				decision: decision{Issue: 42, Action: "implement", Reason: "r"},
				spawn:    spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p), Playbooks: []string{p}},
			}
		}},
		{"kind が同じ issue の action と違う", func(s *sandbox) fixture {
			p := playbookPath(s.defaultInstallPath(), "playbook-implementation")
			return fixture{
				decision: decision{Issue: 42, Action: "skip", Reason: "r"},
				spawn:    spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p), Playbooks: []string{p}},
			}
		}},
		{"prompt に未展開の変数", func(s *sandbox) fixture {
			p := playbookPath(s.defaultInstallPath(), "playbook-implementation")
			return fixture{
				decision: decision{Issue: 42, Action: "start", Reason: "r"},
				spawn:    spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p) + "${CLAUDE_SKILL_DIR}/x", Playbooks: []string{p}},
			}
		}},
		{"playbook が prompt に載っていない", func(s *sandbox) fixture {
			p := playbookPath(s.defaultInstallPath(), "playbook-implementation")
			return fixture{
				decision: decision{Issue: 42, Action: "start", Reason: "r"},
				spawn:    spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42), Playbooks: []string{p}},
			}
		}},
		{"playbook が実在しない", func(s *sandbox) fixture {
			p := filepath.Join(s.defaultInstallPath(), "skills", "procedure", "playbook-gone", "SKILL.md")
			return fixture{
				decision: decision{Issue: 42, Action: "start", Reason: "r"},
				spawn:    spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p), Playbooks: []string{p}},
			}
		}},
		{"playbooks が空", func(s *sandbox) fixture {
			return fixture{
				decision: decision{Issue: 42, Action: "start", Reason: "r"},
				spawn:    spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42), Playbooks: []string{}},
			}
		}},
		{"start の playbook が選定母集合の外", func(s *sandbox) fixture {
			p := playbookPath(s.defaultInstallPath(), "playbook-ci-fix")
			return fixture{
				decision: decision{Issue: 42, Action: "start", Reason: "r"},
				spawn:    spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p), Playbooks: []string{p}},
			}
		}},
		{"start の playbook が 2 本", func(s *sandbox) fixture {
			p := playbookPath(s.defaultInstallPath(), "playbook-implementation")
			q := playbookPath(s.defaultInstallPath(), "playbook-docs")
			return fixture{
				decision: decision{Issue: 42, Action: "start", Reason: "r"},
				spawn:    spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p, q), Playbooks: []string{p, q}},
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.setIssues(readyIssue(42))
			f := tc.make(s)
			s.orchestratorWrites(decisions{Decisions: []decision{f.decision}, Spawn: []spawn{f.spawn}}.json(t))

			r := s.tick()

			assertRejectedBeforeLaunch(t, s, r)
		})
	}
}

func TestOneInvalidSpawnKeepsTheValidOnesFromLaunching(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42), readyIssue(43))
	p := playbookPath(s.defaultInstallPath(), "playbook-implementation")
	s.orchestratorWrites(decisions{
		Decisions: []decision{{Issue: 42, Action: "start", Reason: "r"}, {Issue: 43, Action: "start", Reason: "r"}},
		Spawn: []spawn{
			{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p), Playbooks: []string{p}},
			{Issue: 43, Kind: "start", Prompt: workerPrompt(43, p) + "${UNSET}", Playbooks: []string{p}},
		},
	}.json(t))

	r := s.tick()

	assertRejectedBeforeLaunch(t, s, r)
}

func TestSecondDecisionForTheSameIssueIsRejected(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.orchestratorWrites(decisions{Decisions: []decision{
		{Issue: 42, Action: "skip", Reason: "r"}, {Issue: 42, Action: "skip", Reason: "r"},
	}}.json(t))

	r := s.tick()

	assertRejectedBeforeLaunch(t, s, r)
}

// --- reenter の spawn ---

// reenterScenario は issue 39 の CL に conflict と review を立て、reenter 指示を出させる。
func reenterScenario(s *sandbox) (conflict, review string) {
	s.setIssues(issue{number: 39})
	s.setPRs(pullRequest{number: 100, branch: workerBranch(39), closes: []int{39}, mergeable: "CONFLICTING", unresolved: 1})
	install := s.defaultInstallPath()
	return playbookPath(install, "playbook-conflict-resolution"), playbookPath(install, "playbook-review-response")
}

func TestReenterDecisionLaunchesAReenterWorker(t *testing.T) {
	s := newSandbox(t)
	conflict, review := reenterScenario(s)
	s.orchestratorWrites(decisions{
		Decisions: []decision{{Issue: 39, Action: "reenter", Reason: "r"}},
		Spawn:     []spawn{{Issue: 39, Kind: "reenter", Prompt: workerPrompt(39, conflict, review), Playbooks: []string{conflict, review}}},
	}.json(t))

	r := s.tick()

	assertExit(t, r, 0)
	spawned := s.onlyTickLine()["spawned"].([]any)
	if len(spawned) != 1 || spawned[0].(map[string]any)["kind"] != "reenter" {
		t.Fatalf("spawned = %v", spawned)
	}
	if got := len(s.waitWorkerCalls(1)); got != 1 {
		t.Fatalf("worker の起動 = %d 回", got)
	}
}

func TestReenterSpawnMayDropAConditionThatNoLongerHolds(t *testing.T) {
	s := newSandbox(t)
	_, review := reenterScenario(s)
	s.orchestratorWrites(decisions{
		Decisions: []decision{{Issue: 39, Action: "reenter", Reason: "r"}},
		Spawn:     []spawn{{Issue: 39, Kind: "reenter", Prompt: workerPrompt(39, review), Playbooks: []string{review}}},
	}.json(t))

	r := s.tick()

	assertExit(t, r, 0)
	if got := len(s.waitWorkerCalls(1)); got != 1 {
		t.Fatalf("worker の起動 = %d 回", got)
	}
}

func TestReenterSpawnMustKeepTheConditionOrderAndStayWithinIt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		playbooks func(conflict, review, other string) []string
	}{
		{"条件の順を入れ替えた", func(conflict, review, _ string) []string { return []string{review, conflict} }},
		{"指示に無い条件の playbook", func(conflict, _, other string) []string { return []string{conflict, other} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			conflict, review := reenterScenario(s)
			playbooks := tc.playbooks(conflict, review, playbookPath(s.defaultInstallPath(), "playbook-ci-fix"))
			s.orchestratorWrites(decisions{
				Decisions: []decision{{Issue: 39, Action: "reenter", Reason: "r"}},
				Spawn:     []spawn{{Issue: 39, Kind: "reenter", Prompt: workerPrompt(39, playbooks...), Playbooks: playbooks}},
			}.json(t))

			r := s.tick()

			assertRejectedBeforeLaunch(t, s, r)
		})
	}
}

// --- 網羅 ---

func TestCoverageGapLaunchesTheWrittenSpawnsThenFailsNamingTheIssue(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42), readyIssue(43))
	p := playbookPath(s.defaultInstallPath(), "playbook-implementation")
	s.orchestratorWrites(decisions{
		Decisions: []decision{{Issue: 42, Action: "start", Reason: "r"}},
		Spawn:     []spawn{{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p), Playbooks: []string{p}}},
	}.json(t))

	r := s.tick()

	assertExit(t, r, 1)
	line := s.onlyTickLine()
	assertResult(t, line, "error")
	assertErrorMentions(t, line, "43")
	if spawned := line["spawned"].([]any); len(spawned) != 1 {
		t.Fatalf("網羅の欠けでも書かれた spawn は起動する: %v", spawned)
	}
	if got := len(s.waitWorkerCalls(1)); got != 1 {
		t.Fatalf("worker の起動 = %d 回", got)
	}
	if got := s.orchestratorLines(); len(got) != 1 {
		t.Fatalf("網羅の欠けでも採否は log へ写す: %v", got)
	}
}

func TestEachInstructionKindAcceptsOnlyItsOwnActions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		setup     func(s *sandbox)
		decisions []decision
		covered   bool
	}{
		{"start 候補は skip で網羅", func(s *sandbox) { s.setIssues(readyIssue(42)) },
			[]decision{{Issue: 42, Action: "skip", Reason: "r"}}, true},
		{"start 候補を ready-for-human にしても網羅にならない", func(s *sandbox) { s.setIssues(readyIssue(42)) },
			[]decision{{Issue: 42, Action: "ready-for-human", Reason: "r"}}, false},
		{"reenter は skip で網羅", func(s *sandbox) { reenterScenario(s) },
			[]decision{{Issue: 39, Action: "skip", Reason: "r"}}, true},
		{"reenter を start にしても網羅にならない", func(s *sandbox) { reenterScenario(s) },
			[]decision{{Issue: 39, Action: "start", Reason: "r"}}, false},
		{"anomaly は ready-for-human で網羅", func(s *sandbox) { s.setIssues(issue{number: 5, labels: []string{wipLabel, humanLabel}}) },
			[]decision{{Issue: 5, Action: "ready-for-human", Reason: "r"}}, true},
		{"anomaly を start にしても網羅にならない", func(s *sandbox) { s.setIssues(issue{number: 5, labels: []string{wipLabel, humanLabel}}) },
			[]decision{{Issue: 5, Action: "start", Reason: "r"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			tc.setup(s)
			s.orchestratorWrites(decisions{Decisions: tc.decisions}.json(t))

			r := s.tick()

			want := map[bool]int{true: 0, false: 1}[tc.covered]
			assertExit(t, r, want)
		})
	}
}

func TestOrchestratorThatDidNotExitNormallyHasItsDecisionsIgnored(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	p := playbookPath(s.defaultInstallPath(), "playbook-implementation")
	valid := decisions{
		Decisions: []decision{{Issue: 42, Action: "start", Reason: "r"}},
		Spawn:     []spawn{{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p), Playbooks: []string{p}}},
	}.json(t)
	s.orchestratorBehaves(stubRule{Exit: 3, Decisions: &decisionsWrite{StateDir: s.stateDir(), Content: valid}})

	r := s.tick()

	assertRejectedBeforeLaunch(t, s, r)
	line := s.onlyTickLine()
	orchestrator := line["orchestrator"].(map[string]any)
	if number(orchestrator["exit_code"]) != 3 || orchestrator["timed_out"] != false {
		t.Fatalf("orchestrator = %v", orchestrator)
	}
	if got := s.orchestratorLines(); len(got) != 0 {
		t.Fatalf("正常終了しなかった orchestrator の決定を log へ写した: %v", got)
	}
}

func TestDecidedWorkersOfOneTickGetDistinctLogsAndSessions(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42), readyIssue(43))
	p := playbookPath(s.defaultInstallPath(), "playbook-implementation")
	s.orchestratorWrites(decisions{
		Decisions: []decision{{Issue: 42, Action: "start", Reason: "r"}, {Issue: 43, Action: "start", Reason: "r"}},
		Spawn: []spawn{
			{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p), Playbooks: []string{p}},
			{Issue: 43, Kind: "start", Prompt: workerPrompt(43, p), Playbooks: []string{p}},
		},
	}.json(t))

	assertExit(t, s.tick(), 0)

	spawned := s.onlyTickLine()["spawned"].([]any)
	var issues []int
	logs, sessions := map[any]bool{}, map[any]bool{}
	for _, raw := range spawned {
		entry := raw.(map[string]any)
		issues = append(issues, number(entry["issue"]))
		logs[entry["log"]] = true
		sessions[entry["session_id"]] = true
	}
	if !slices.Equal(issues, []int{42, 43}) || len(logs) != 2 || len(sessions) != 2 {
		t.Fatalf("spawned = %v", spawned)
	}
}
