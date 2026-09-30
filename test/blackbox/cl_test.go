package blackbox_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// CL 側の trigger (formats.md §2.3 / §2.4 / §6、system.md §6)。語彙と絞り込みは gh の応答から試運転の候補までを通して見る。
// loop が CL の worker を起動・除外・停止する振る舞いは、走らせた loop で見る。

// clWorkflow は周期 1s で、CL 側の trigger fix だけを持つ workflow 定義。when は trigger の when の中身 (flow style)。
func (s *sandbox) clWorkflow(when string) string {
	marks := s.marksDir()
	mustMkdir(s.t, marks)
	return `---
tracker:
  kind: github
  repo: acme/widgets
polling:
  interval: 1s
hooks:
  after_create: echo "$CLAUDE_DISPATCHER_KIND $CLAUDE_DISPATCHER_NUMBER" > "` + marks + `/after_create-$CLAUDE_DISPATCHER_KIND-$CLAUDE_DISPATCHER_NUMBER"
  before_remove: ': > "` + marks + `/before_remove-$CLAUDE_DISPATCHER_KIND-$CLAUDE_DISPATCHER_NUMBER"'
triggers:
  - name: fix
    on: cl
    when: ` + when + `
    action: "/fix CL #{{ .cl.number }} on {{ .cl.head }} ({{ .cl.title }}, {{ .cl.url }})"
---
共通 prompt
`
}

func clCandidate(trigger string, number int) string {
	return trigger + "\tcl\t#" + strconv.Itoa(number) + "\tcl " + strconv.Itoa(number)
}

func (s *sandbox) clWorkspace(number int) string {
	return filepath.Join(s.clone, ".claude-dispatcher", "workspaces", "cl-"+strconv.Itoa(number))
}

// numbered は cls に 1 から順に番号を振り、head branch を番号ごとに分ける。
func numbered(cls ...cl) []cl {
	for i := range cls {
		cls[i].number = i + 1
		if cls[i].head == "" {
			cls[i].head = "worktree-issue-" + strconv.Itoa(i+1)
		}
	}
	return cls
}

func TestEachCLStateMatchesOnlyTheCLsInThatState(t *testing.T) {
	cases := []struct {
		name string
		when string
		cls  []cl
		// matching は cls のうち当たるものの番号 (1 から)
		matching []int
	}{
		{"conflict: true は merge conflict を持つ CL に当たる", "{conflict: true}",
			[]cl{{mergeable: "CONFLICTING"}, {mergeable: "MERGEABLE"}, {mergeable: "UNKNOWN"}}, []int{1}},
		{"conflict: false は merge conflict を持たない CL に当たり、計算中の CL には当たらない", "{conflict: false}",
			[]cl{{mergeable: "CONFLICTING"}, {mergeable: "MERGEABLE"}, {mergeable: "UNKNOWN"}}, []int{2}},
		{"review_unresolved: true は collaborator の未解決の thread がある CL に当たる", "{review_unresolved: true}",
			[]cl{
				{threads: []thread{{resolved: true, association: "MEMBER"}, {resolved: false, association: "COLLABORATOR"}}},
				{threads: []thread{{resolved: true, association: "MEMBER"}}},
				{threads: []thread{{resolved: false, association: "NONE"}}},
				{},
			}, []int{1}},
		{"review_unresolved: false は collaborator の未解決の thread が無い CL に当たる", "{review_unresolved: false}",
			[]cl{{threads: []thread{{resolved: false, association: "OWNER"}}}, {threads: []thread{{resolved: false, association: "CONTRIBUTOR"}}}}, []int{2}},
		{"ci_failed: true は checks が失敗した CL に当たる", "{ci_failed: true}",
			[]cl{{checks: "FAILURE"}, {checks: "ERROR"}, {checks: "SUCCESS"}, {checks: "PENDING"}, {}}, []int{1, 2}},
		{"ci_failed: false は checks が失敗していない CL に当たる", "{ci_failed: false}",
			[]cl{{checks: "FAILURE"}, {checks: "SUCCESS"}, {}}, []int{2, 3}},
		{"approved: true は承認済みの CL に当たる", "{approved: true}",
			[]cl{{reviewDecision: "APPROVED"}, {reviewDecision: "CHANGES_REQUESTED"}, {reviewDecision: "REVIEW_REQUIRED"}, {}}, []int{1}},
		{"approved: false は承認済みでない CL に当たる", "{approved: false}",
			[]cl{{reviewDecision: "APPROVED"}, {reviewDecision: "CHANGES_REQUESTED"}}, []int{2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeWorkflow(s.clWorkflow(c.when))
			s.setStore(nil, numbered(c.cls...))

			r := s.dryRun()

			var want []string
			for _, n := range c.matching {
				want = append(want, clCandidate("fix", n))
			}
			assertCandidates(t, r, want...)
		})
	}
}

func TestDraftCLIsNotMatchedByDraftFalse(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.clWorkflow("{draft: false}"))
	s.setStore(nil, numbered(cl{draft: true}, cl{}))

	r := s.dryRun()

	assertCandidates(t, r, clCandidate("fix", 2))
}

func TestForkCLIsNotMatchedByAHeadPattern(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.clWorkflow("{head: worktree-issue-*}"))
	s.setStore(nil, numbered(cl{}, cl{fork: true}))

	r := s.dryRun()

	assertCandidates(t, r, clCandidate("fix", 1))
}

func TestHeadPatternLeavesOutBranchesWithOtherNames(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.clWorkflow("{head: worktree-issue-*}"))
	s.setStore(nil, numbered(cl{head: "feature/login"}, cl{}))

	r := s.dryRun()

	assertCandidates(t, r, clCandidate("fix", 2))
}

func TestForkCLIsNotMatchedBySameRepoTrue(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.clWorkflow("{same_repo: true}"))
	s.setStore(nil, numbered(cl{fork: true}, cl{}))

	r := s.dryRun()

	assertCandidates(t, r, clCandidate("fix", 2))
}

func TestAmbiguousCLsAreLeftOutOfTheCandidates(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.clWorkflow("{}"))
	s.setStore(nil, numbered(cl{head: "shared"}, cl{head: "shared"}, cl{}))

	r := s.dryRun()

	assertCandidates(t, r, clCandidate("fix", 3))
}

func TestForkBranchWithTheSameNameDoesNotMakeACLAmbiguous(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.clWorkflow("{}"))
	s.setStore(nil, numbered(cl{head: "shared"}, cl{head: "shared", fork: true}))

	r := s.dryRun()

	assertCandidates(t, r, clCandidate("fix", 1), clCandidate("fix", 2))
}

func TestAmbiguousCLsAreRecordedInTheTickRow(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.clWorkflow("{}"))
	s.setStore(nil, numbered(cl{head: "shared"}, cl{head: "shared"}))

	s.startLoop()

	tick := s.waitEvents("tick", 1)[0]
	want := []any{map[string]any{"head": "shared", "targets": []any{"cl#1", "cl#2"}}}
	if !reflect.DeepEqual(tick["ambiguous"], want) || tick["candidates"] != float64(0) {
		t.Fatalf("tick の行 = %v, want ambiguous %v で候補 0", tick, want)
	}
}

func TestCLMatchingATriggerGetsAWorkerWithTheCLVariablesInItsOwnWorkspace(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.clWorkflow("{ci_failed: true}"))
	s.setStore(nil, []cl{{number: 5, head: "worktree-issue-3", checks: "FAILURE"}})
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})

	s.startLoop()

	start := s.waitEvents("start", 1)[0]
	call := s.waitCalls("claude", 1)[0]
	action := call.Argv[len(call.Argv)-1]
	if start["target"] != "cl#5" || call.Cwd != s.clWorkspace(5) ||
		action != "/fix CL #5 on worktree-issue-3 (cl 5, https://github.com/acme/widgets/pull/5)" {
		t.Fatalf("start の行 = %v, cwd = %s, action = %q", start, call.Cwd, action)
	}
	if got := strings.TrimSpace(string(mustRead(t, s.mark("after_create-cl-5")))); got != "cl 5" {
		t.Fatalf("after_create の環境 = %q, want cl 5", got)
	}
}

// branchOf は git の stub が、workspace で checkout されている branch として name を返すようにする。
func (s *sandbox) branchOf(name string) {
	s.respond("git", stubwire.Rule{ArgsPrefix: []string{"symbolic-ref"}, Stdout: name + "\n"})
}

// issueAndCLWorkflow は、issue 側の trigger implement と CL 側の trigger fix を持つ workflow 定義。並列上限は 3。
func (s *sandbox) issueAndCLWorkflow() string {
	return strings.Replace(s.clWorkflow("{}"), "triggers:\n", `limits:
  max_concurrent: 3
triggers:
  - name: implement
    on: issue
    when: {labels: {all: [ready-for-agent]}}
    action: "/implement issue #{{ .issue.number }}"
`, 1)
}

func TestCLWhoseBranchIsCheckedOutInAClaimedWorkspaceIsNotLaunched(t *testing.T) {
	// issue#7 の worker が走る workspace で worktree-issue-7 が checkout されている。その間に開いた、同じ branch の cl#20 には
	// 当てず、別の branch の cl#21 には当てる
	s := newSandbox(t)
	s.writeWorkflow(s.issueAndCLWorkflow())
	s.setIssues(readyIssue(7))
	s.branchOf("worktree-issue-7")
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	s.startLoop()
	waitStart(t, s, "issue#7", 1)

	s.setStore([]issue{readyIssue(7)}, []cl{{number: 20, head: "worktree-issue-7"}, {number: 21, head: "worktree-issue-8"}})

	waitStart(t, s, "cl#21", 1)
	s.waitEvents("tick", len(s.events("tick"))+1)
	if starts := s.startsOf("cl#20"); len(starts) != 0 {
		t.Fatalf("claim 中の branch の CL に worker を起動した: %v", starts)
	}
}

func TestCLsAreNotLaunchedWhileTheBranchOfAClaimedWorkspaceCannotBeRead(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.issueAndCLWorkflow())
	s.setIssues(readyIssue(7))
	s.respond("git", stubwire.Rule{ArgsPrefix: []string{"symbolic-ref"}, Stderr: "fatal: bad object HEAD", Exit: 128})
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	s.startLoop()
	waitStart(t, s, "issue#7", 1)

	s.setStore([]issue{readyIssue(7)}, []cl{{number: 21, head: "worktree-issue-8"}})

	waitFor(t, func() bool {
		for _, e := range s.events("error") {
			if e["target"] == "issue#7" && strings.Contains(asString(e["error"]), "branch") {
				return true
			}
		}
		return false
	}, "branch を読めないことが error の行に残らない")
	s.waitEvents("tick", len(s.events("tick"))+1)
	if starts := s.startsOf("cl#21"); len(starts) != 0 {
		t.Fatalf("branch を読めない間に CL の worker を起動した: %v", starts)
	}
}

func TestWorkerOfACLThatIsMergedIsStoppedAndItsWorkspaceRemoved(t *testing.T) {
	s := newSandbox(t)
	s.writeWorkflow(s.clWorkflow("{}"))
	s.setStore(nil, []cl{readyCL(5)})
	s.onClaude(stubwire.Rule{ReleaseFile: s.releaseFile()})
	s.startLoop()
	s.waitEvents("start", 1)
	merged := readyCL(5)
	merged.state = "MERGED"

	s.setStore(nil, []cl{merged})

	end := s.waitEvents("end", 1)[0]
	if end["target"] != "cl#5" || end["outcome"] != "stopped" {
		t.Fatalf("end の行 = %v, want cl#5 の stopped", end)
	}
	if _, err := os.Stat(s.mark("before_remove-cl-5")); err != nil {
		t.Fatalf("before_remove を撃っていない: %v", err)
	}
	if _, err := os.Stat(s.clWorkspace(5)); !os.IsNotExist(err) {
		t.Fatalf("終端の CL の workspace が残っている: %v", err)
	}
}
