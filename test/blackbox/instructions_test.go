package blackbox_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// 指示ファイル (formats.md §5.1) と、その中身を決める指示カタログ (system.md §6)。
// orchestrator には常に決定ファイルを書かせて tick を正常に終わらせ、指示ファイルだけを見る。

// skipAll は指示ファイルの全 issue を見送る決定を orchestrator に書かせる (網羅の検査を通すため)。
func (s *sandbox) orchestratorSkips(issues ...int) {
	s.t.Helper()
	var d decisions
	for _, n := range issues {
		d.Decisions = append(d.Decisions, decision{Issue: n, Action: "skip", Reason: "テスト"})
	}
	s.orchestratorWrites(d.json(s.t))
}

func TestInstructionFileIsNamedByTheTickStemAndReferencedFromTheLog(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.orchestratorSkips(42)

	s.tick()

	files := s.instructionFiles()
	if len(files) != 1 {
		t.Fatalf("指示ファイル = %v", files)
	}
	if stem := strings.TrimSuffix(filepath.Base(files[0]), ".json"); !tickStemPattern.MatchString(stem) {
		t.Fatalf("stem が YYYYMMDDTHHMMSS.ffffffZ でない: %q", stem)
	}
	if got := s.onlyTickLine()["instruction_file"]; got != files[0] {
		t.Fatalf("log の instruction_file = %v, want %s", got, files[0])
	}
}

func TestInstructionFileCarriesTheSnapshot(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42), issue{number: 40, labels: []string{wipLabel}}, issue{number: 38, labels: []string{humanLabel}})
	s.setPRs(pullRequest{number: 101, branch: workerBranch(40), closes: []int{40}, checks: "SUCCESS"})
	s.orchestratorSkips(42)

	s.tick()

	snapshot := s.onlyInstructionFile()["snapshot"].(map[string]any)
	wantKeys := []string{"cl_repo", "cls", "issue_repo", "issues", "limits", "linked_cls", "observed", "observed_at"}
	if got := keys(snapshot); !slices.Equal(got, wantKeys) {
		t.Fatalf("snapshot の key = %v, want %v", got, wantKeys)
	}
	if snapshot["issue_repo"] != defaultIssueRepo || snapshot["cl_repo"] != defaultIssueRepo {
		t.Fatalf("[cl] を省くと CL 置き場は issue 置き場を継ぐ: %v / %v", snapshot["issue_repo"], snapshot["cl_repo"])
	}
	limits := snapshot["limits"].(map[string]any)
	if number(limits["max_wip"]) != 2 || number(limits["wip_count"]) != 1 {
		t.Fatalf("limits = %v", limits)
	}
	issues := snapshot["issues"].(map[string]any)
	candidates := issues["candidates"].([]any)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %v", candidates)
	}
	candidate := candidates[0].(map[string]any)
	if got := keys(candidate); !slices.Equal(got, []string{"body", "number", "title", "url"}) {
		t.Fatalf("候補の key = %v (候補だけが body を持つ)", got)
	}
	wip := issues["wip"].([]any)
	if len(wip) != 1 || !slices.Equal(keys(wip[0].(map[string]any)), []string{"number", "title", "url"}) {
		t.Fatalf("wip = %v (wip は body を持たない)", wip)
	}
	if got := numbers(issues["ready_for_human"]); !slices.Equal(got, []int{38}) {
		t.Fatalf("ready_for_human = %v", got)
	}
	if got := numbers(snapshot["linked_cls"].(map[string]any)["40"]); !slices.Equal(got, []int{101}) {
		t.Fatalf("linked_cls = %v", snapshot["linked_cls"])
	}
	cl := snapshot["cls"].([]any)[0].(map[string]any)
	wantCL := []string{"base", "branch", "checks", "draft", "issues", "mergeable", "number", "unresolved_threads", "url"}
	if got := keys(cl); !slices.Equal(got, wantCL) {
		t.Fatalf("cls の key = %v, want %v", got, wantCL)
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

	starts := instructionsOfKind(s.onlyInstructionFile(), "start")
	if len(starts) != 1 {
		t.Fatalf("start 指示 = %v", starts)
	}
	start := starts[0]
	if got := keys(start); !slices.Equal(got, []string{"candidates", "free_slots", "kind", "playbooks"}) {
		t.Fatalf("start 指示の key = %v", got)
	}
	if number(start["free_slots"]) != 1 {
		t.Fatalf("free_slots = %v, want 1 (max_wip 2 - wip 1)", start["free_slots"])
	}
	var got []int
	for _, c := range start["candidates"].([]any) {
		candidate := c.(map[string]any)
		got = append(got, number(candidate["number"]))
		if candidate["body"] == nil {
			t.Fatalf("候補が body を持たない: %v", candidate)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, []int{42, 43}) {
		t.Fatalf("candidates = %v", got)
	}
}

func TestCandidateExcludesWipHumanAndIssuesWithAnOpenCL(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(strings.Replace(s.defaultConfig(), "max_wip = 2", "max_wip = 5", 1))
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

	snapshot := s.onlyInstructionFile()["snapshot"].(map[string]any)
	var got []int
	for _, c := range snapshot["issues"].(map[string]any)["candidates"].([]any) {
		got = append(got, number(c.(map[string]any)["number"]))
	}
	if !slices.Equal(got, []int{1}) {
		t.Fatalf("候補 = %v, want [1] (着手可 ∧ ¬wip ∧ ¬ready-for-human ∧ 紐づく open CL なし)", got)
	}
	linked := snapshot["linked_cls"].(map[string]any)
	if !slices.Equal(numbers(linked["5"]), []int{105}) {
		t.Fatalf("head branch の規約で紐づいていない: %v", linked)
	}
}

func TestClosingReferenceToAnotherRepoDoesNotLink(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.setPRs(pullRequest{number: 7, branch: "feature/x", closes: []int{42}, closesRepo: "acme/elsewhere"})
	s.orchestratorSkips(42)

	s.tick()

	if len(instructionsOfKind(s.onlyInstructionFile(), "start")) != 1 {
		t.Fatal("別 repo の issue を指す closing reference で候補から外れた")
	}
}

func TestNoFreeSlotYieldsNoStartAndNoInstructionFile(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42), issue{number: 40, labels: []string{wipLabel}}, issue{number: 41, labels: []string{wipLabel}})

	r := s.tick()

	assertExit(t, r, 0)
	if files := s.instructionFiles(); len(files) != 0 {
		t.Fatalf("空き slot が無いのに指示ファイルを書いた: %v", files)
	}
	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("指示 0 件なのに claude を起動した: %v", calls)
	}
}

// --- reenter ---

func conditionNames(instruction map[string]any) []string {
	var out []string
	for _, c := range instruction["conditions"].([]any) {
		out = append(out, c.(map[string]any)["name"].(string))
	}
	return out
}

func TestConflictingWorkerCLOfAnUnclaimedIssueYieldsReenter(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(issue{number: 39})
	s.setPRs(pullRequest{number: 100, branch: workerBranch(39), base: "worktree-issue-38", closes: []int{39}, mergeable: "CONFLICTING"})
	s.orchestratorSkips(39)

	s.tick()

	reenters := instructionsOfKind(s.onlyInstructionFile(), "reenter")
	if len(reenters) != 1 {
		t.Fatalf("reenter 指示 = %v", reenters)
	}
	reenter := reenters[0]
	if got := keys(reenter); !slices.Equal(got, []string{"cl", "conditions", "issue", "kind"}) {
		t.Fatalf("reenter 指示の key = %v", got)
	}
	if number(reenter["issue"]) != 39 {
		t.Fatalf("issue = %v", reenter["issue"])
	}
	cl := reenter["cl"].(map[string]any)
	if got := keys(cl); !slices.Equal(got, []string{"base", "branch", "number", "url"}) {
		t.Fatalf("cl の key = %v", got)
	}
	if number(cl["number"]) != 100 || cl["branch"] != workerBranch(39) || cl["base"] != "worktree-issue-38" {
		t.Fatalf("cl = %v", cl)
	}
	conditions := reenter["conditions"].([]any)
	condition := conditions[0].(map[string]any)
	want := playbookPath(s.defaultInstallPath(), "playbook-conflict-resolution")
	if len(conditions) != 1 || condition["name"] != "conflict" || condition["playbook"] != want {
		t.Fatalf("conditions = %v, want [{conflict %s}]", conditions, want)
	}
}

func TestAllConditionsOnOneCLAreCombinedInCatalogOrder(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(issue{number: 39})
	s.setPRs(pullRequest{number: 100, branch: workerBranch(39), closes: []int{39}, mergeable: "CONFLICTING", unresolved: 1, resolved: 2, checks: "FAILURE"})
	s.orchestratorSkips(39)

	s.tick()

	reenters := instructionsOfKind(s.onlyInstructionFile(), "reenter")
	if len(reenters) != 1 {
		t.Fatalf("1 CL の条件が 1 指示にまとまっていない: %v", reenters)
	}
	if got := conditionNames(reenters[0]); !slices.Equal(got, []string{"conflict", "review", "ci"}) {
		t.Fatalf("conditions = %v, want 条件カタログの順 [conflict review ci]", got)
	}
	var playbooks []string
	for _, c := range reenters[0]["conditions"].([]any) {
		playbooks = append(playbooks, c.(map[string]any)["playbook"].(string))
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

			s.tick()

			files := s.instructionFiles()
			if !tc.reenters {
				if len(files) != 0 {
					t.Fatalf("checks %q で指示が出た: %v", tc.checks, files)
				}
				return
			}
			reenters := instructionsOfKind(s.onlyInstructionFile(), "reenter")
			if len(reenters) != 1 || !slices.Equal(conditionNames(reenters[0]), []string{"ci"}) {
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

	if files := s.instructionFiles(); len(files) != 0 {
		t.Fatalf("解決済み thread だけの CL に指示が出た: %v", files)
	}
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

			if files := s.instructionFiles(); len(files) != 0 {
				t.Fatalf("指示が出た: %v", files)
			}
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
	var kinds []string
	for _, i := range instructionsOf(doc) {
		kinds = append(kinds, i["kind"].(string))
	}
	if !slices.Equal(kinds, []string{"reenter", "start"}) {
		t.Fatalf("指示の並び = %v, want [reenter start]", kinds)
	}
	if got := number(instructionsOfKind(doc, "start")[0]["free_slots"]); got != 1 {
		t.Fatalf("start の free_slots = %d, want 1 (reenter が 1 slot 取った残り)", got)
	}
}

func TestReenterBeyondFreeSlotsIsNotIssuedThisTick(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(strings.Replace(s.defaultConfig(), "max_wip = 2", "max_wip = 1", 1))
	s.setIssues(issue{number: 38}, issue{number: 39}, readyIssue(42))
	s.setPRs(
		pullRequest{number: 100, branch: workerBranch(38), closes: []int{38}, mergeable: "CONFLICTING"},
		pullRequest{number: 101, branch: workerBranch(39), closes: []int{39}, mergeable: "CONFLICTING"},
	)
	s.orchestratorSkips(38, 39)

	s.tick()

	doc := s.onlyInstructionFile()
	if got := len(instructionsOfKind(doc, "reenter")); got != 1 {
		t.Fatalf("空き 1 slot に reenter が %d 件", got)
	}
	if got := len(instructionsOfKind(doc, "start")); got != 0 {
		t.Fatal("slot が無いのに start が出た")
	}
}

// --- anomaly ---

func TestAnomaliesAreRaisedForUnclassifiableObservations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		issues []issue
		prs    []pullRequest
		reason string
		want   []int
	}{
		{
			name:   "wip が上限を超えている",
			issues: []issue{{number: 1, labels: []string{wipLabel}}, {number: 2, labels: []string{wipLabel}}, {number: 3, labels: []string{wipLabel}}},
			reason: "wip_over_limit", want: []int{1, 2, 3},
		},
		{
			name:   "wip と ready-for-human の同居",
			issues: []issue{{number: 5, labels: []string{wipLabel, humanLabel}}},
			reason: "wip_and_ready_for_human", want: []int{5},
		},
		{
			name:   "1 issue に open CL が複数 (label に依らない)",
			issues: []issue{{number: 37}},
			prs:    []pullRequest{{number: 98, branch: workerBranch(37), closes: []int{37}}, {number: 99, branch: "feature/y", closes: []int{37}}},
			reason: "multiple_open_cls", want: []int{37},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.setIssues(tc.issues...)
			s.setPRs(tc.prs...)
			s.orchestratorSkips(tc.want...)

			s.tick()

			anomalies := instructionsOfKind(s.onlyInstructionFile(), "anomaly")
			if len(anomalies) != 1 {
				t.Fatalf("anomaly = %v", anomalies)
			}
			anomaly := anomalies[0]
			if anomaly["reason"] != tc.reason || !slices.Equal(numbers(anomaly["issues"]), tc.want) {
				t.Fatalf("anomaly = %v, want reason %s issues %v", anomaly, tc.reason, tc.want)
			}
			_, hasCLs := anomaly["cls"]
			if hasCLs != (tc.reason == "multiple_open_cls") {
				t.Fatalf("cls を持つのは multiple_open_cls だけ: %v", anomaly)
			}
			if tc.reason == "multiple_open_cls" && !slices.Equal(numbers(anomaly["cls"]), []int{98, 99}) {
				t.Fatalf("cls = %v", anomaly["cls"])
			}
		})
	}
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

	if got := instructionsOfKind(s.onlyInstructionFile(), "reenter"); len(got) != 0 {
		t.Fatalf("CL が複数紐づく issue に reenter が出た: %v", got)
	}
}

func TestInstructionsAreOrderedReenterStartAnomaly(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(strings.Replace(s.defaultConfig(), "max_wip = 2", "max_wip = 3", 1))
	s.setIssues(issue{number: 39}, readyIssue(42), issue{number: 5, labels: []string{wipLabel, humanLabel}})
	s.setPRs(pullRequest{number: 100, branch: workerBranch(39), closes: []int{39}, mergeable: "CONFLICTING"})
	s.orchestratorSkips(39, 42, 5)

	s.tick()

	var kinds []string
	for _, i := range instructionsOf(s.onlyInstructionFile()) {
		kinds = append(kinds, i["kind"].(string))
	}
	if !slices.Equal(kinds, []string{"reenter", "start", "anomaly"}) {
		t.Fatalf("指示の並び = %v", kinds)
	}
}

func TestTruncatedObservationIsAnErrorWithoutAnInstructionFile(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.setPRPage(true, pullRequest{number: 1, branch: "feature/a"})

	r := s.tick()

	assertExit(t, r, 1)
	assertResult(t, s.onlyTickLine(), "error")
	if files := s.instructionFiles(); len(files) != 0 {
		t.Fatalf("切り詰めた観測から指示ファイルを書いた: %v", files)
	}
	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("切り詰めた観測で claude を起動した: %v", calls)
	}
}
