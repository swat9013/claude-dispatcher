package blackbox_test

import (
	"slices"
	"testing"
)

// 指示ファイル (formats.md §5.1) と、その中身を決める指示カタログ (system.md §6)。
// orchestrator には常に全件見送りの決定ファイルを書かせて tick を正常に終わらせ、指示ファイルだけを見る。

func TestInstructionFileIsNamedByTheTickStem(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.orchestratorSkips(42)

	s.tick()

	files := s.instructionFiles()
	if len(files) != 1 {
		t.Fatalf("指示ファイル = %v", files)
	}
	if stem := tickStem(files[0]); !tickStemPattern.MatchString(stem) {
		t.Fatalf("stem が YYYYMMDDTHHMMSS.ffffffZ でない: %q", stem)
	}
	if got := s.onlyTickLine()["instruction_file"]; got != files[0] {
		t.Fatalf("log の instruction_file = %v, want %s", got, files[0])
	}
}

// snapshotScenario は候補 42・wip 40 (open CL 101 付き)・人待ち 38 の snapshot を書かせる。
func snapshotScenario(t *testing.T) map[string]any {
	t.Helper()
	s := newSandbox(t)
	s.setIssues(readyIssue(42), issue{number: 40, labels: []string{wipLabel}}, issue{number: 38, labels: []string{humanLabel}})
	s.setPRs(pullRequest{number: 101, branch: workerBranch(40), closes: []int{40}, checks: "SUCCESS"})
	s.orchestratorSkips(42)
	s.tick()
	return asMap(t, s.onlyInstructionFile()["snapshot"])
}

func TestSnapshotCarriesItsKeys(t *testing.T) {
	snapshot := snapshotScenario(t)

	want := []string{"cl_repo", "cls", "issue_repo", "issues", "limits", "linked_cls", "observed", "observed_at"}
	if got := keys(snapshot); !slices.Equal(got, want) {
		t.Fatalf("snapshot の key = %v, want %v", got, want)
	}
	limits := asMap(t, snapshot["limits"])
	if number(t, limits["max_wip"]) != 2 || number(t, limits["wip_count"]) != 1 {
		t.Fatalf("limits = %v", limits)
	}
}

func TestClRepoDefaultsToTheIssueRepo(t *testing.T) {
	snapshot := snapshotScenario(t)

	if snapshot["issue_repo"] != defaultIssueRepo || snapshot["cl_repo"] != defaultIssueRepo {
		t.Fatalf("[cl] を省くと CL 置き場は issue 置き場を継ぐ: %v / %v", snapshot["issue_repo"], snapshot["cl_repo"])
	}
}

func TestOnlyCandidatesCarryTheIssueBody(t *testing.T) {
	issues := asMap(t, snapshotScenario(t)["issues"])

	candidates := asList(t, issues["candidates"])
	if len(candidates) != 1 || !slices.Equal(keys(asMap(t, candidates[0])), []string{"body", "number", "title", "url"}) {
		t.Fatalf("candidates = %v (候補は body を持つ)", candidates)
	}
	wip := asList(t, issues["wip"])
	if len(wip) != 1 || !slices.Equal(keys(asMap(t, wip[0])), []string{"number", "title", "url"}) {
		t.Fatalf("wip = %v (wip は body を持たない)", wip)
	}
	if got := numbers(t, issues["ready_for_human"]); !slices.Equal(got, []int{38}) {
		t.Fatalf("ready_for_human = %v", got)
	}
}

func TestSnapshotNormalizesOpenCLs(t *testing.T) {
	snapshot := snapshotScenario(t)

	if got := numbers(t, asMap(t, snapshot["linked_cls"])["40"]); !slices.Equal(got, []int{101}) {
		t.Fatalf("linked_cls = %v", snapshot["linked_cls"])
	}
	cl := asMap(t, asList(t, snapshot["cls"])[0])
	wantKeys := []string{"base", "branch", "checks", "draft", "issues", "mergeable", "number", "unresolved_threads", "url"}
	if got := keys(cl); !slices.Equal(got, wantKeys) {
		t.Fatalf("cls の key = %v, want %v", got, wantKeys)
	}
	if cl["branch"] != workerBranch(40) || cl["base"] != "main" || cl["checks"] != "SUCCESS" || cl["mergeable"] != "MERGEABLE" {
		t.Fatalf("cls = %v", cl)
	}
}

func TestCandidatesWithFreeSlotsYieldAStartInstruction(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42), readyIssue(43), issue{number: 40, labels: []string{wipLabel}})
	s.orchestratorSkips(42, 43)

	s.tick()

	starts := instructionsOfKind(t, s.onlyInstructionFile(), "start")
	if len(starts) != 1 {
		t.Fatalf("start 指示 = %v", starts)
	}
	start := starts[0]
	if got := keys(start); !slices.Equal(got, []string{"candidates", "free_slots", "kind", "playbooks"}) {
		t.Fatalf("start 指示の key = %v", got)
	}
	if number(t, start["free_slots"]) != 1 {
		t.Fatalf("free_slots = %v, want 1 (max_wip 2 - wip 1)", start["free_slots"])
	}
	var got []int
	for _, c := range asList(t, start["candidates"]) {
		candidate := asMap(t, c)
		if _, ok := candidate["body"]; !ok {
			t.Fatalf("start の候補が body を持たない: %v", candidate)
		}
		got = append(got, number(t, candidate["number"]))
	}
	slices.Sort(got)
	if !slices.Equal(got, []int{42, 43}) {
		t.Fatalf("candidates = %v", got)
	}
}

func TestCandidateExcludesWipHumanAndIssuesWithAnOpenCL(t *testing.T) {
	s := newSandbox(t)
	s.setMaxWIP(5)
	s.setIssues(
		readyIssue(1),
		issue{number: 2, labels: []string{defaultReadyLabel, wipLabel}},
		issue{number: 3, labels: []string{defaultReadyLabel, humanLabel}},
		readyIssue(4), // closing reference の open CL がある
		readyIssue(5), // worker の branch 規約の open CL がある (closing reference なし)
		issue{number: 6, labels: []string{"bug"}},
	)
	s.setPRs(
		pullRequest{number: 104, branch: "feature/four", closes: []int{4}},
		pullRequest{number: 105, branch: workerBranch(5)},
	)
	s.orchestratorSkips(1)

	s.tick()

	snapshot := asMap(t, s.onlyInstructionFile()["snapshot"])
	var got []int
	for _, c := range asList(t, asMap(t, snapshot["issues"])["candidates"]) {
		got = append(got, number(t, asMap(t, c)["number"]))
	}
	if !slices.Equal(got, []int{1}) {
		t.Fatalf("候補 = %v, want [1] (着手可 ∧ ¬wip ∧ ¬ready-for-human ∧ 紐づく open CL なし)", got)
	}
	if linked := asMap(t, snapshot["linked_cls"]); !slices.Equal(numbers(t, linked["5"]), []int{105}) {
		t.Fatalf("head branch の規約で紐づいていない: %v", linked)
	}
}

func TestClosingReferenceToAnotherRepoDoesNotLink(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.setPRs(pullRequest{number: 7, branch: "feature/x", closes: []int{42}, closesRepo: "acme/elsewhere"})
	s.orchestratorSkips(42)

	s.tick()

	if len(instructionsOfKind(t, s.onlyInstructionFile(), "start")) != 1 {
		t.Fatal("別 repo の issue を指す closing reference で候補から外れた")
	}
}

// forkCLOnBranchScenario は issue 5 に、worker の branch 名で closing reference の無い fork の CL (#105、conflict あり) を置く。
func forkCLOnBranchScenario(s *sandbox) {
	s.setIssues(readyIssue(5))
	s.setPRs(pullRequest{number: 105, branch: workerBranch(5), mergeable: "CONFLICTING", fork: true})
	s.orchestratorSkips(5)
}

func TestForkCLOnAWorkerBranchNameDoesNotLinkByBranch(t *testing.T) {
	s := newSandbox(t)
	forkCLOnBranchScenario(s)

	s.tick()

	snapshot := asMap(t, s.onlyInstructionFile()["snapshot"])
	if linked := asMap(t, snapshot["linked_cls"]); len(linked) != 0 {
		t.Fatalf("fork の CL が branch 名で紐づいた: %v", linked)
	}
	var got []int
	for _, c := range asList(t, asMap(t, snapshot["issues"])["candidates"]) {
		got = append(got, number(t, asMap(t, c)["number"]))
	}
	if !slices.Equal(got, []int{5}) {
		t.Fatalf("候補 = %v, want [5]", got)
	}
}

func TestForkCLOnAWorkerBranchNameYieldsNoReenter(t *testing.T) {
	s := newSandbox(t)
	forkCLOnBranchScenario(s)

	s.tick()

	if reenters := instructionsOfKind(t, s.onlyInstructionFile(), "reenter"); len(reenters) != 0 {
		t.Fatalf("fork の CL へ reenter を出した: %v", reenters)
	}
}

// forkCLClosingScenario は issue 39 を closing reference で指す、worker の branch 名の fork の CL (#100、conflict あり) を置く。
// 42 は指示ファイルを書かせるための候補。
func forkCLClosingScenario(s *sandbox) {
	s.setIssues(issue{number: 39}, readyIssue(42))
	s.setPRs(pullRequest{number: 100, branch: workerBranch(39), closes: []int{39}, mergeable: "CONFLICTING", fork: true})
	s.orchestratorSkips(42)
}

func TestForkCLWithAClosingReferenceStaysLinked(t *testing.T) {
	s := newSandbox(t)
	forkCLClosingScenario(s)

	s.tick()

	if linked := asMap(t, asMap(t, s.onlyInstructionFile()["snapshot"])["linked_cls"]); !slices.Equal(numbers(t, linked["39"]), []int{100}) {
		t.Fatalf("closing reference で紐づいていない: %v", linked)
	}
}

func TestForkCLWithAClosingReferenceYieldsNoReenter(t *testing.T) {
	s := newSandbox(t)
	forkCLClosingScenario(s)

	s.tick()

	if reenters := instructionsOfKind(t, s.onlyInstructionFile(), "reenter"); len(reenters) != 0 {
		t.Fatalf("fork の CL へ reenter を出した: %v", reenters)
	}
}

// workerAndForkCLScenario は issue 39 に、worker の CL (#100、conflict あり) と同じ branch 名の fork の CL (#101) を並べる。
func workerAndForkCLScenario(s *sandbox) {
	s.setIssues(issue{number: 39})
	s.setPRs(
		pullRequest{number: 100, branch: workerBranch(39), mergeable: "CONFLICTING"},
		pullRequest{number: 101, branch: workerBranch(39), fork: true},
	)
	s.orchestratorSkips(39)
}

func TestForkCLBesideAWorkerCLOfTheSameBranchNameRaisesNoAnomaly(t *testing.T) {
	s := newSandbox(t)
	workerAndForkCLScenario(s)

	s.tick()

	if anomalies := instructionsOfKind(t, s.onlyInstructionFile(), "anomaly"); len(anomalies) != 0 {
		t.Fatalf("fork の CL を数えて anomaly を出した: %v", anomalies)
	}
}

func TestWorkerCLBesideAForkCLOfTheSameBranchNameYieldsReenter(t *testing.T) {
	s := newSandbox(t)
	workerAndForkCLScenario(s)

	s.tick()

	reenters := instructionsOfKind(t, s.onlyInstructionFile(), "reenter")
	if len(reenters) != 1 || number(t, asMap(t, reenters[0]["cl"])["number"]) != 100 {
		t.Fatalf("worker の CL #100 への reenter = %v", reenters)
	}
}

func TestNoFreeSlotYieldsNoInstruction(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42), issue{number: 40, labels: []string{wipLabel}}, issue{number: 41, labels: []string{wipLabel}})

	assertExit(t, s.tick(), 0)

	s.assertNoInstructionFile("空き slot が無いのに")
	s.assertNoClaude("指示 0 件なのに")
}

// --- reenter ---

func conditionNames(t *testing.T, instruction map[string]any) []string {
	t.Helper()
	var out []string
	for _, c := range asList(t, instruction["conditions"]) {
		out = append(out, asString(t, asMap(t, c)["name"]))
	}
	return out
}

func TestConflictingWorkerCLOfAnUnclaimedIssueYieldsReenter(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(issue{number: 39})
	s.setPRs(pullRequest{number: 100, branch: workerBranch(39), base: "worktree-issue-38", closes: []int{39}, mergeable: "CONFLICTING"})
	s.orchestratorSkips(39)

	s.tick()

	reenters := instructionsOfKind(t, s.onlyInstructionFile(), "reenter")
	if len(reenters) != 1 {
		t.Fatalf("reenter 指示 = %v", reenters)
	}
	reenter := reenters[0]
	if got := keys(reenter); !slices.Equal(got, []string{"cl", "conditions", "issue", "kind"}) {
		t.Fatalf("reenter 指示の key = %v", got)
	}
	if number(t, reenter["issue"]) != 39 {
		t.Fatalf("issue = %v", reenter["issue"])
	}
	cl := asMap(t, reenter["cl"])
	if got := keys(cl); !slices.Equal(got, []string{"base", "branch", "number", "url"}) {
		t.Fatalf("cl の key = %v", got)
	}
	if number(t, cl["number"]) != 100 || cl["branch"] != workerBranch(39) || cl["base"] != "worktree-issue-38" {
		t.Fatalf("cl = %v", cl)
	}
	conditions := asList(t, reenter["conditions"])
	want := playbookPath(s.defaultInstallPath(), "playbook-conflict-resolution")
	if len(conditions) != 1 || asMap(t, conditions[0])["name"] != "conflict" || asMap(t, conditions[0])["playbook"] != want {
		t.Fatalf("conditions = %v, want [{conflict %s}]", conditions, want)
	}
}

func TestAllConditionsOnOneCLAreCombinedInCatalogOrder(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(issue{number: 39})
	s.setPRs(pullRequest{number: 100, branch: workerBranch(39), closes: []int{39}, mergeable: "CONFLICTING", unresolved: 1, resolved: 2, checks: "FAILURE"})
	s.orchestratorSkips(39)

	s.tick()

	reenters := instructionsOfKind(t, s.onlyInstructionFile(), "reenter")
	if len(reenters) != 1 {
		t.Fatalf("1 CL の条件が 1 指示にまとまっていない: %v", reenters)
	}
	if got := conditionNames(t, reenters[0]); !slices.Equal(got, []string{"conflict", "review", "ci"}) {
		t.Fatalf("conditions = %v, want 条件カタログの順 [conflict review ci]", got)
	}
	var playbooks []string
	for _, c := range asList(t, reenters[0]["conditions"]) {
		playbooks = append(playbooks, asString(t, asMap(t, c)["playbook"]))
	}
	install := s.defaultInstallPath()
	want := []string{
		playbookPath(install, "playbook-conflict-resolution"),
		playbookPath(install, "playbook-review-response"),
		playbookPath(install, "playbook-ci-fix"),
	}
	if !slices.Equal(playbooks, want) {
		t.Fatalf("条件の playbook = %v, want %v", playbooks, want)
	}
}

func TestCiConditionStandsOnlyForFailedChecks(t *testing.T) {
	for _, tc := range []struct {
		checks   string
		reenters bool
	}{
		{"FAILURE", true}, {"ERROR", true}, {"PENDING", false}, {"SUCCESS", false}, {"", false},
	} {
		t.Run("checks="+tc.checks, func(t *testing.T) {
			s := newSandbox(t)
			s.setIssues(issue{number: 39})
			s.setPRs(pullRequest{number: 100, branch: workerBranch(39), closes: []int{39}, checks: tc.checks})
			s.orchestratorSkips(39)

			assertExit(t, s.tick(), 0)

			if !tc.reenters {
				s.assertNoInstructionFile("checks " + tc.checks + " で")
				return
			}
			reenters := instructionsOfKind(t, s.onlyInstructionFile(), "reenter")
			if len(reenters) != 1 || !slices.Equal(conditionNames(t, reenters[0]), []string{"ci"}) {
				t.Fatalf("reenter = %v", reenters)
			}
		})
	}
}

func TestResolvedThreadsAloneYieldNoReenter(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(issue{number: 39})
	s.setPRs(pullRequest{number: 100, branch: workerBranch(39), closes: []int{39}, resolved: 3})

	assertExit(t, s.tick(), 0)

	s.assertNoInstructionFile("解決済み thread だけの CL に")
}

func TestNoReenterIsIssuedFor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		issue  issue
		branch string
	}{
		{"人が開いた CL (branch が worker の規約と違う)", issue{number: 39}, "feature/by-human"},
		{"wip の付いた issue", issue{number: 39, labels: []string{wipLabel}}, workerBranch(39)},
		{"ready-for-human の付いた issue", issue{number: 39, labels: []string{humanLabel}}, workerBranch(39)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.setIssues(tc.issue)
			s.setPRs(pullRequest{number: 100, branch: tc.branch, closes: []int{39}, mergeable: "CONFLICTING"})

			assertExit(t, s.tick(), 0)

			s.assertNoInstructionFile(tc.name + "に")
		})
	}
}

func TestReenterTakesSlotsBeforeStart(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(issue{number: 39}, readyIssue(42))
	s.setPRs(pullRequest{number: 100, branch: workerBranch(39), closes: []int{39}, mergeable: "CONFLICTING"})
	s.orchestratorSkips(39, 42)

	s.tick()

	doc := s.onlyInstructionFile()
	if got := instructionKinds(t, doc); !slices.Equal(got, []string{"reenter", "start"}) {
		t.Fatalf("指示の並び = %v, want [reenter start]", got)
	}
	if got := number(t, instructionsOfKind(t, doc, "start")[0]["free_slots"]); got != 1 {
		t.Fatalf("start の free_slots = %d, want 1 (reenter が 1 slot 取った残り)", got)
	}
}

func TestReenterBeyondFreeSlotsIsNotIssuedThisTick(t *testing.T) {
	s := newSandbox(t)
	s.setMaxWIP(1)
	s.setIssues(issue{number: 38}, issue{number: 39}, readyIssue(42))
	s.setPRs(
		pullRequest{number: 100, branch: workerBranch(38), closes: []int{38}, mergeable: "CONFLICTING"},
		pullRequest{number: 101, branch: workerBranch(39), closes: []int{39}, mergeable: "CONFLICTING"},
	)
	s.orchestratorSkips(38, 39)

	s.tick()

	doc := s.onlyInstructionFile()
	if got := len(instructionsOfKind(t, doc, "reenter")); got != 1 {
		t.Fatalf("空き 1 slot に reenter が %d 件", got)
	}
	if got := len(instructionsOfKind(t, doc, "start")); got != 0 {
		t.Fatal("slot が無いのに start が出た")
	}
}

// --- anomaly ---

func TestAnomaliesAreRaisedForUnclassifiableObservations(t *testing.T) {
	for _, tc := range []struct {
		name    string
		issues  []issue
		prs     []pullRequest
		reason  string
		anomaly []int // anomaly の issues が含むべき issue (網羅の検査のため orchestrator はこれを見送る)
	}{
		{
			name:   "wip が上限を超えている",
			issues: []issue{{number: 1, labels: []string{wipLabel}}, {number: 2, labels: []string{wipLabel}}, {number: 3, labels: []string{wipLabel}}},
			reason: "wip_over_limit", anomaly: []int{1, 2, 3},
		},
		{
			name:   "wip と ready-for-human の同居",
			issues: []issue{{number: 5, labels: []string{wipLabel, humanLabel}}},
			reason: "wip_and_ready_for_human", anomaly: []int{5},
		},
		{
			name:   "1 issue に open CL が複数 (label に依らない)",
			issues: []issue{{number: 37}},
			prs:    []pullRequest{{number: 98, branch: workerBranch(37), closes: []int{37}}, {number: 99, branch: "feature/y", closes: []int{37}}},
			reason: "multiple_open_cls", anomaly: []int{37},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.setIssues(tc.issues...)
			s.setPRs(tc.prs...)
			s.orchestratorSkips(tc.anomaly...)

			s.tick()

			anomalies := instructionsOfKind(t, s.onlyInstructionFile(), "anomaly")
			if len(anomalies) != 1 || anomalies[0]["reason"] != tc.reason {
				t.Fatalf("anomaly = %v, want reason %s", anomalies, tc.reason)
			}
			got := numbers(t, anomalies[0]["issues"])
			// wip_over_limit の issues に何を並べるかは formats.md が決めていない。wip の issue から成ることだけを見る
			if len(got) == 0 || !isSubset(got, tc.anomaly) || (tc.reason != "wip_over_limit" && !slices.Equal(got, tc.anomaly)) {
				t.Fatalf("anomaly の issues = %v, want %v", got, tc.anomaly)
			}
			_, hasCLs := anomalies[0]["cls"]
			if hasCLs != (tc.reason == "multiple_open_cls") {
				t.Fatalf("cls を持つのは multiple_open_cls だけ: %v", anomalies[0])
			}
			if hasCLs && !slices.Equal(numbers(t, anomalies[0]["cls"]), []int{98, 99}) {
				t.Fatalf("cls = %v", anomalies[0]["cls"])
			}
		})
	}
}

func isSubset(sub, of []int) bool {
	for _, n := range sub {
		if !slices.Contains(of, n) {
			return false
		}
	}
	return true
}

func TestMultipleOpenCLsOfOneIssueYieldOnlyTheAnomaly(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(issue{number: 37})
	s.setPRs(
		pullRequest{number: 98, branch: workerBranch(37), closes: []int{37}, mergeable: "CONFLICTING"},
		pullRequest{number: 99, branch: "feature/y", closes: []int{37}},
	)
	s.orchestratorSkips(37)

	s.tick()

	if got := instructionsOfKind(t, s.onlyInstructionFile(), "reenter"); len(got) != 0 {
		t.Fatalf("CL が複数紐づく issue に reenter が出た: %v", got)
	}
}

func TestInstructionsAreOrderedReenterStartAnomaly(t *testing.T) {
	s := newSandbox(t)
	s.setMaxWIP(3)
	s.setIssues(issue{number: 39}, readyIssue(42), issue{number: 5, labels: []string{wipLabel, humanLabel}})
	s.setPRs(pullRequest{number: 100, branch: workerBranch(39), closes: []int{39}, mergeable: "CONFLICTING"})
	s.orchestratorSkips(39, 42, 5)

	s.tick()

	if got := instructionKinds(t, s.onlyInstructionFile()); !slices.Equal(got, []string{"reenter", "start", "anomaly"}) {
		t.Fatalf("指示の並び = %v", got)
	}
}

func TestTruncatedObservationIsAnError(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.setTruncatedPRs(pullRequest{number: 1, branch: "feature/a"})

	r := s.tick()

	s.assertOutcome(r, outcomeError)
	s.assertNoInstructionFile("切り詰めた観測から")
	s.assertNoClaude("切り詰めた観測で")
}
