package blackbox_test

import (
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// 決定ファイル (formats.md §5.2) の検査と、決定どおりの worker 起動 (system.md §9)。

func onlyOrchestratorCall(s *sandbox) stubCall {
	s.t.Helper()
	calls := s.callsMatching("claude", isOrchestratorCall)
	if len(calls) != 1 {
		s.t.Fatalf("orchestrator の起動 = %d 回", len(calls))
	}
	return calls[0]
}

func TestOrchestratorIsLaunchedHeadlessInAutoMode(t *testing.T) {
	s := newSandbox(t)
	startScenario(s)

	s.tick()

	c := onlyOrchestratorCall(s)
	if !c.hasArg("-p") {
		t.Fatalf("-p で起動していない: %v", c.args())
	}
	if mode, _ := c.flagValue("--permission-mode"); mode != "auto" {
		t.Fatalf("--permission-mode = %q, want auto", mode)
	}
	if id, _ := c.flagValue("--session-id"); !uuidPattern.MatchString(id) {
		t.Fatalf("--session-id = %q", id)
	}
}

func TestOrchestratorRunsInTheClone(t *testing.T) {
	s := newSandbox(t)
	startScenario(s)

	s.tick()

	if c := onlyOrchestratorCall(s); c.Cwd != s.clone {
		t.Fatalf("orchestrator の cwd = %s, want clone %s", c.Cwd, s.clone)
	}
}

func TestOrchestratorPromptCarriesTheInstructionAndDecisionsPaths(t *testing.T) {
	s := newSandbox(t)
	startScenario(s)

	s.tick()

	c := onlyOrchestratorCall(s)
	instructionFile := asString(t, s.onlyTickLine()["instruction_file"])
	if !c.contains(instructionFile) {
		t.Fatalf("orchestrator の prompt に指示ファイルの path %s が無い", instructionFile)
	}
	if decisionsFile := s.decisionsFile(tickStem(instructionFile)); !c.contains(decisionsFile) {
		t.Fatalf("orchestrator の prompt に決定ファイルの path %s が無い", decisionsFile)
	}
}

func TestDecidedWorkerIsLaunchedHeadlessWithItsPromptInTheClone(t *testing.T) {
	s := newSandbox(t)
	playbook := startScenario(s)

	assertExit(t, s.tick(), 0)

	w := s.waitWorkerCalls(1)[0]
	// -p (--print) は値を取らない。prompt は argv のどこかに 1 引数として逐語で載る
	if !w.hasArg("-p") || !w.hasArg(workerPrompt(42, playbook)) {
		t.Fatalf("worker に -p と決定ファイルの spawn prompt が逐語で渡っていない: %q", w.args())
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

	if spawned := asList(t, s.assertOutcome(r, outcomeOK)["spawned"]); len(spawned) != 0 {
		t.Fatalf("spawned = %v", spawned)
	}
}

func TestMissingDecisionsFileLaunchesNothing(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.orchestratorExits(0, nil)

	r := s.tick()

	s.assertRejectedBeforeLaunch(r)
}

func TestMalformedDecisionsFileLaunchesNothing(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.orchestratorWritesRaw("{not json")

	r := s.tick()

	s.assertRejectedBeforeLaunch(r)
}

func TestInvalidSpawnRejectsTheWholeDecisionsFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(install string) (decision, spawn)
	}{
		{"action が語彙の外", func(install string) (decision, spawn) {
			p := playbookPath(install, "playbook-implementation")
			return decision{Issue: 42, Action: "implement", Reason: "r"},
				spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p), Playbooks: []string{p}}
		}},
		{"kind が同じ issue の action と違う", func(install string) (decision, spawn) {
			p := playbookPath(install, "playbook-implementation")
			return decision{Issue: 42, Action: "skip", Reason: "r"},
				spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p), Playbooks: []string{p}}
		}},
		{"prompt に未展開の変数", func(install string) (decision, spawn) {
			p := playbookPath(install, "playbook-implementation")
			return decision{Issue: 42, Action: "start", Reason: "r"},
				spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p) + "${CLAUDE_SKILL_DIR}/x", Playbooks: []string{p}}
		}},
		{"playbook が prompt に載っていない", func(install string) (decision, spawn) {
			p := playbookPath(install, "playbook-implementation")
			return decision{Issue: 42, Action: "start", Reason: "r"},
				spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42), Playbooks: []string{p}}
		}},
		{"playbook が実在しない", func(install string) (decision, spawn) {
			p := filepath.Join(install, "skills", "procedure", "playbook-gone", "SKILL.md")
			return decision{Issue: 42, Action: "start", Reason: "r"},
				spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p), Playbooks: []string{p}}
		}},
		{"playbooks が空", func(install string) (decision, spawn) {
			return decision{Issue: 42, Action: "start", Reason: "r"},
				spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42), Playbooks: []string{}}
		}},
		{"start の playbook が選定母集合の外", func(install string) (decision, spawn) {
			p := playbookPath(install, "playbook-ci-fix")
			return decision{Issue: 42, Action: "start", Reason: "r"},
				spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p), Playbooks: []string{p}}
		}},
		{"start の playbook が 2 本", func(install string) (decision, spawn) {
			p := playbookPath(install, "playbook-implementation")
			q := playbookPath(install, "playbook-docs")
			return decision{Issue: 42, Action: "start", Reason: "r"},
				spawn{Issue: 42, Kind: "start", Prompt: workerPrompt(42, p, q), Playbooks: []string{p, q}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.setIssues(readyIssue(42))
			d, sp := tc.make(s.defaultInstallPath())
			s.orchestratorWrites(decisions{Decisions: []decision{d}, Spawn: []spawn{sp}})

			r := s.tick()

			s.assertRejectedBeforeLaunch(r)
		})
	}
}

func TestOneInvalidSpawnKeepsTheValidOnesFromLaunching(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42), readyIssue(43))
	d := startDecisions(playbookPath(s.defaultInstallPath(), "playbook-implementation"), 42, 43)
	d.Spawn[1].Prompt += "${UNSET}"
	s.orchestratorWrites(d)

	r := s.tick()

	s.assertRejectedBeforeLaunch(r)
}

// --- reenter の spawn ---

// reenterScenario は issue 39 の CL に conflict と review を立て、reenter 指示を出させる。
func reenterScenario(s *sandbox) (conflict, review string) {
	s.setIssues(issue{number: 39})
	s.setPRs(pullRequest{number: 100, branch: workerBranch(39), closes: []int{39}, mergeable: "CONFLICTING", unresolved: 1})
	install := s.defaultInstallPath()
	return playbookPath(install, "playbook-conflict-resolution"), playbookPath(install, "playbook-review-response")
}

func reenterDecisions(issue int, playbooks ...string) decisions {
	return decisions{
		Decisions: []decision{{Issue: issue, Action: "reenter", Reason: "r"}},
		Spawn:     []spawn{{Issue: issue, Kind: "reenter", Prompt: workerPrompt(issue, playbooks...), Playbooks: playbooks}},
	}
}

func TestReenterDecisionLaunchesAReenterWorker(t *testing.T) {
	s := newSandbox(t)
	conflict, review := reenterScenario(s)
	s.orchestratorWrites(reenterDecisions(39, conflict, review))

	r := s.tick()

	if entry := onlySpawned(t, s.assertOutcome(r, outcomeOK)); entry["kind"] != "reenter" {
		t.Fatalf("spawned = %v", entry)
	}
	s.waitWorkerCalls(1)
}

func TestReenterSpawnMayDropAConditionThatNoLongerHolds(t *testing.T) {
	s := newSandbox(t)
	_, review := reenterScenario(s)
	s.orchestratorWrites(reenterDecisions(39, review))

	r := s.tick()

	onlySpawned(t, s.assertOutcome(r, outcomeOK))
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
			s.orchestratorWrites(reenterDecisions(39, tc.playbooks(conflict, review, playbookPath(s.defaultInstallPath(), "playbook-ci-fix"))...))

			r := s.tick()

			s.assertRejectedBeforeLaunch(r)
		})
	}
}

// --- 網羅 ---

func TestCoverageGapFailsNamingTheUndecidedIssue(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42), readyIssue(43))
	s.orchestratorWrites(startDecisions(playbookPath(s.defaultInstallPath(), "playbook-implementation"), 42))

	r := s.tick()

	s.assertErrorNames(s.assertOutcome(r, outcomeError), "43")
}

func TestCoverageGapStillLaunchesTheWrittenSpawns(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42), readyIssue(43))
	s.orchestratorWrites(startDecisions(playbookPath(s.defaultInstallPath(), "playbook-implementation"), 42))

	s.tick()

	if entry := onlySpawned(t, s.onlyTickLine()); number(t, entry["issue"]) != 42 {
		t.Fatalf("網羅の欠けでも書かれた spawn は起動する: %v", entry)
	}
	if got := s.orchestratorLines(); len(got) != 1 {
		t.Fatalf("網羅の欠けでも採否は log へ写す: %v", got)
	}
}

func TestEachInstructionKindAcceptsOnlyItsOwnActions(t *testing.T) {
	anomalyIssue := func(s *sandbox) { s.setIssues(issue{number: 5, labels: []string{wipLabel, humanLabel}}) }
	for _, tc := range []struct {
		name     string
		setup    func(s *sandbox)
		decision decision
		covered  bool
	}{
		{"start 候補は skip で網羅", func(s *sandbox) { s.setIssues(readyIssue(42)) },
			decision{Issue: 42, Action: "skip", Reason: "r"}, true},
		{"start 候補を ready-for-human にしても網羅にならない", func(s *sandbox) { s.setIssues(readyIssue(42)) },
			decision{Issue: 42, Action: "ready-for-human", Reason: "r"}, false},
		{"reenter は skip で網羅", func(s *sandbox) { reenterScenario(s) },
			decision{Issue: 39, Action: "skip", Reason: "r"}, true},
		{"reenter を start にしても網羅にならない", func(s *sandbox) { reenterScenario(s) },
			decision{Issue: 39, Action: "start", Reason: "r"}, false},
		{"anomaly は ready-for-human で網羅", anomalyIssue,
			decision{Issue: 5, Action: "ready-for-human", Reason: "r"}, true},
		{"anomaly を start にしても網羅にならない", anomalyIssue,
			decision{Issue: 5, Action: "start", Reason: "r"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			tc.setup(s)
			s.orchestratorWrites(decisions{Decisions: []decision{tc.decision}})

			r := s.tick()

			if tc.covered {
				s.assertOutcome(r, outcomeOK)
				return
			}
			// 網羅の欠けとして落ちたこと (他の検査で落ちたのではないこと) を、欠けた issue の名指しで見る
			s.assertErrorNames(s.assertOutcome(r, outcomeError), strconv.Itoa(tc.decision.Issue))
		})
	}
}

func TestOrchestratorThatDidNotExitNormallyHasItsDecisionsIgnored(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	valid := startDecisions(playbookPath(s.defaultInstallPath(), "playbook-implementation"), 42)
	s.orchestratorExits(3, &valid)

	r := s.tick()

	s.assertRejectedBeforeLaunch(r)
	orchestrator := asMap(t, s.onlyTickLine()["orchestrator"])
	if number(t, orchestrator["exit_code"]) != 3 || orchestrator["timed_out"] != false {
		t.Fatalf("orchestrator = %v", orchestrator)
	}
	if got := s.orchestratorLines(); len(got) != 0 {
		t.Fatalf("正常終了しなかった orchestrator の決定を log へ写した: %v", got)
	}
}

func TestWorkersOfOneTickGetDistinctLogsAndSessions(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42), readyIssue(43))
	s.orchestratorWrites(startDecisions(playbookPath(s.defaultInstallPath(), "playbook-implementation"), 42, 43))

	assertExit(t, s.tick(), 0)

	spawned := asList(t, s.onlyTickLine()["spawned"])
	var issues []int
	logs, sessions := map[any]bool{}, map[any]bool{}
	for _, raw := range spawned {
		entry := asMap(t, raw)
		issues = append(issues, number(t, entry["issue"]))
		logs[entry["log"]] = true
		sessions[entry["session_id"]] = true
	}
	if !slices.Equal(issues, []int{42, 43}) || len(logs) != 2 || len(sessions) != 2 {
		t.Fatalf("spawned = %v", spawned)
	}
}
