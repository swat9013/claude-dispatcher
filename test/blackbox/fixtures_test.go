package blackbox_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// defaultWorkflow は ready-for-agent の issue に当たる trigger 1 つだけの workflow 定義。
const defaultWorkflow = `---
tracker:
  kind: github
  repo: acme/widgets
triggers:
  - name: implement
    on: issue
    when:
      labels:
        all: [ready-for-agent]
    action: |
      /implement
---
共通 prompt
`

// --- gh の応答 ---

// issue は gh が返す open な issue 1 件の fixture。gh の GraphQL の応答の形に写して返す。
type issue struct {
	number      int
	title       string
	createdAt   string
	labels      []string
	assignees   []string
	association string
	milestone   string
	// blockers は依存先 (blocked by) の state の列 (OPEN / CLOSED)
	blockers []string
	// overflow が空でなければ、その connection (labels / assignees / blockedBy) の totalCount を
	// 1 往復で読める 100 件より多くする (読み切れない応答)
	overflow string
	// closed なら issue は終端 (open な issue の一覧に出ず、1 件の読み直しで CLOSED を返す)
	closed bool
}

// readyIssue は ready-for-agent の付いた issue。作成日時は番号の順に並ぶ。
func readyIssue(number int) issue {
	return issue{number: number, title: "issue " + strconv.Itoa(number), createdAt: createdAt(number), labels: []string{readyLabel}, association: "OWNER"}
}

// createdAt は番号の順に並ぶ作成日時。
func createdAt(number int) string {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(number) * time.Minute).Format(time.RFC3339)
}

func (i issue) node() map[string]any {
	labels := []map[string]any{}
	for _, l := range i.labels {
		labels = append(labels, map[string]any{"name": l})
	}
	assignees := []map[string]any{}
	for _, a := range i.assignees {
		assignees = append(assignees, map[string]any{"login": a})
	}
	blockers := []map[string]any{}
	for _, b := range i.blockers {
		blockers = append(blockers, map[string]any{"state": b})
	}
	var milestone any
	if i.milestone != "" {
		milestone = map[string]any{"title": i.milestone}
	}
	association := i.association
	if association == "" {
		association = "OWNER"
	}
	node := map[string]any{
		"number":            i.number,
		"title":             i.title,
		"createdAt":         i.createdAt,
		"authorAssociation": association,
		"author":            map[string]any{"login": "someone"},
		"labels":            map[string]any{"totalCount": len(labels), "nodes": labels},
		"assignees":         map[string]any{"totalCount": len(assignees), "nodes": assignees},
		"milestone":         milestone,
		"blockedBy":         map[string]any{"totalCount": len(blockers), "nodes": blockers},
		"url":               "https://github.com/" + defaultIssueRepo + "/issues/" + strconv.Itoa(i.number),
		"state":             map[bool]string{false: "OPEN", true: "CLOSED"}[i.closed],
	}
	if i.overflow != "" {
		node[i.overflow].(map[string]any)["totalCount"] = 101
	}
	return node
}

// pages は `gh api graphql --paginate --slurp` の応答 (page の列) を、1 page 2 件で組む。connection は repository の下の
// 一覧の名前 (issues / pullRequests)。
func pages(connection string, nodes []map[string]any) string {
	var out []any
	for start := 0; start == 0 || start < len(nodes); start += 2 {
		end := min(start+2, len(nodes))
		page := []any{}
		for _, n := range nodes[start:end] {
			page = append(page, n)
		}
		out = append(out, map[string]any{"data": map[string]any{"repository": map[string]any{connection: map[string]any{
			"pageInfo": map[string]any{"hasNextPage": end < len(nodes), "endCursor": "c" + strconv.Itoa(end)},
			"nodes":    page,
		}}}})
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// issuePages は open な issue の一覧の応答。
func issuePages(issues ...issue) string {
	nodes := []map[string]any{}
	for _, i := range issues {
		nodes = append(nodes, i.node())
	}
	return pages("issues", nodes)
}

// gh の query を見分ける綴り。issue と CL の一覧と 1 件の読み直しを、query の中の field の名前で分ける
const (
	issueListQuery = "issues(states: OPEN"
	issueReadQuery = "issue(number:"
	clListQuery    = "pullRequests(states: OPEN"
	clReadQuery    = "pullRequest(number:"
)

// setIssues は gh が issues を返すようにする。open な issue の一覧には closed でないものを載せ、1 件の読み直しには
// どれも (closed なら CLOSED で) 返す。CL は 1 件も無い。
func (s *sandbox) setIssues(issues ...issue) {
	s.t.Helper()
	s.respondAll("gh", ghRules(issues, nil))
}

// setStore は gh が issues と cls を返すようにする。
func (s *sandbox) setStore(issues []issue, cls []cl) {
	s.t.Helper()
	s.respondAll("gh", ghRules(issues, cls))
}

// ghRules は setStore の応答 rule の列。worker の代役が gh の応答 file を書き換えるときにも使う。
func ghRules(issues []issue, cls []cl) []stubwire.Rule {
	var rules []stubwire.Rule
	var open []issue
	for _, i := range issues {
		rules = append(rules, readRule(i))
		if !i.closed {
			open = append(open, i)
		}
	}
	openCLs := []map[string]any{}
	for _, c := range cls {
		single, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": c.node()}}})
		rules = append(rules, stubwire.Rule{ArgsPrefix: []string{"api", "graphql"}, ArgsContain: []string{clReadQuery, "number=" + strconv.Itoa(c.number)}, Stdout: string(single)})
		if c.state == "" {
			openCLs = append(openCLs, c.node())
		}
	}
	return append(rules,
		stubwire.Rule{ArgsPrefix: []string{"api", "graphql"}, ArgsContain: []string{issueListQuery}, Stdout: issuePages(open...)},
		stubwire.Rule{ArgsPrefix: []string{"api", "graphql"}, ArgsContain: []string{clListQuery}, Stdout: pages("pullRequests", openCLs)},
	)
}

// readRule は issue 1 件の読み直しに i を返す gh の応答 rule。
func readRule(i issue) stubwire.Rule {
	single, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"issue": i.node()}}})
	return stubwire.Rule{ArgsPrefix: []string{"api", "graphql"}, ArgsContain: []string{issueReadQuery, "number=" + strconv.Itoa(i.number)}, Stdout: string(single)}
}

// ghResponses は ghRules を gh の応答 file の中身にしたもの。
func (s *sandbox) ghResponses(issues ...issue) stubwire.FileWrite {
	return s.storeResponses(issues, nil)
}

// clResponses は cls だけを返す gh の応答 file の中身。worker の代役が CL の状態を書き換えるときに使う。
func (s *sandbox) clResponses(cls ...cl) stubwire.FileWrite {
	return s.storeResponses(nil, cls)
}

// storeResponses は ghRules を gh の応答 file の中身にしたもの。
func (s *sandbox) storeResponses(issues []issue, cls []cl) stubwire.FileWrite {
	raw, err := json.Marshal(ghRules(issues, cls))
	if err != nil {
		s.t.Fatal(err)
	}
	return stubwire.FileWrite{Path: stubwire.ResponsesFile(s.stubRoot, "gh"), Content: string(raw)}
}

// cl は gh が返す open な CL 1 件の fixture。gh の GraphQL の応答の形に写して返す。既定は、同じ repo の head branch から
// 開いた、draft でない、conflict も未解決の review も CI の失敗も承認も無い CL。
type cl struct {
	number int
	head   string
	// fork なら head は fork の branch
	fork bool
	// forkGone なら head の repo (fork) が消えている (headRepository が null)
	forkGone bool
	draft    bool
	labels   []string
	// mergeable は MERGEABLE (既定) / CONFLICTING / UNKNOWN
	mergeable string
	// reviewDecision は APPROVED / CHANGES_REQUESTED / REVIEW_REQUIRED。空なら null
	reviewDecision string
	threads        []thread
	// checks は head commit の checks の集計 (SUCCESS / FAILURE / ERROR / PENDING)。空なら checks が無い
	checks string
	// state が空なら open。MERGED か CLOSED なら終端 (open な一覧に出ず、1 件の読み直しでこの state を返す)
	state string
}

// thread は review thread 1 本。association は最初の comment の作者の立場
type thread struct {
	resolved    bool
	association string
}

// readyCL は head branch が worktree-issue-<番号> の CL。作成日時は番号の順に並ぶ。
func readyCL(number int) cl {
	return cl{number: number, head: "worktree-issue-" + strconv.Itoa(number)}
}

func (c cl) node() map[string]any {
	labels := []map[string]any{}
	for _, l := range c.labels {
		labels = append(labels, map[string]any{"name": l})
	}
	threads := []map[string]any{}
	for _, t := range c.threads {
		threads = append(threads, map[string]any{"isResolved": t.resolved, "comments": map[string]any{
			"nodes": []map[string]any{{"authorAssociation": t.association}},
		}})
	}
	var rollup any
	if c.checks != "" {
		rollup = map[string]any{"state": c.checks}
	}
	var decision any
	if c.reviewDecision != "" {
		decision = c.reviewDecision
	}
	mergeable, state := c.mergeable, c.state
	if mergeable == "" {
		mergeable = "MERGEABLE"
	}
	if state == "" {
		state = "OPEN"
	}
	var headRepository any = map[string]any{"nameWithOwner": defaultIssueRepo}
	if c.fork {
		headRepository = map[string]any{"nameWithOwner": "stranger/widgets"}
	}
	if c.forkGone {
		headRepository = nil
	}
	return map[string]any{
		"number":            c.number,
		"title":             "cl " + strconv.Itoa(c.number),
		"url":               "https://github.com/" + defaultIssueRepo + "/pull/" + strconv.Itoa(c.number),
		"state":             state,
		"createdAt":         createdAt(c.number),
		"authorAssociation": "OWNER",
		"isDraft":           c.draft,
		"isCrossRepository": c.fork || c.forkGone,
		"headRefName":       c.head,
		"headRepository":    headRepository,
		"mergeable":         mergeable,
		"reviewDecision":    decision,
		"labels":            map[string]any{"totalCount": len(labels), "nodes": labels},
		"reviewThreads":     map[string]any{"totalCount": len(threads), "nodes": threads},
		"commits":           map[string]any{"nodes": []map[string]any{{"commit": map[string]any{"statusCheckRollup": rollup}}}},
	}
}

// failGh は gh の呼び出しを stderr と exit で失敗させる。
func (s *sandbox) failGh(stderr string, exit int) {
	s.respond("gh", stubwire.Rule{ArgsPrefix: []string{"api", "graphql"}, Stderr: stderr, Exit: exit})
}

// respond は name の stub の応答 rule を 1 つにする。
func (s *sandbox) respond(name string, r stubwire.Rule) {
	s.t.Helper()
	s.respondAll(name, []stubwire.Rule{r})
}

// respondAll は name の stub の応答 rule を rules にする。先頭から見て最初に当たった rule で応答する。
func (s *sandbox) respondAll(name string, rules []stubwire.Rule) {
	s.t.Helper()
	s.rules[name] = rules
	raw, err := json.Marshal(s.rules[name])
	if err != nil {
		s.t.Fatal(err)
	}
	mustWrite(s.t, stubwire.ResponsesFile(s.stubRoot, name), string(raw))
}

// --- stub の呼び出し ---

// calls は name の stub が受けた呼び出しを、受けた順に返す。
func (s *sandbox) calls(name string) []stubwire.Call {
	s.t.Helper()
	files, err := filepath.Glob(filepath.Join(stubwire.CallsDir(s.stubRoot, name), "*.json"))
	if err != nil {
		s.t.Fatal(err)
	}
	sort.Strings(files)
	var calls []stubwire.Call
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			s.t.Fatal(err)
		}
		var c stubwire.Call
		if err := json.Unmarshal(raw, &c); err != nil {
			s.t.Fatal(err)
		}
		calls = append(calls, c)
	}
	return calls
}

// waitCalls は name の stub が受けた呼び出しが n 件になるまで待ち、受けた順に返す。待ちきれなければ msg で落ちる。
// start の行は claude を起動した直後に書かれ、stub が呼び出しを記録するより先になりうるので、start の行を見てから
// 呼び出しを読むときもこれで待つ。
func (s *sandbox) waitCalls(name string, n int, msg string) []stubwire.Call {
	s.t.Helper()
	waitFor(s.t, func() bool { return len(s.calls(name)) >= n }, msg)
	return s.calls(name)
}

// assertNoGh は gh が 1 度も撃たれていないことを確かめる。
func (s *sandbox) assertNoGh(why string) {
	s.t.Helper()
	if calls := s.calls("gh"); len(calls) != 0 {
		s.t.Fatalf("%s gh を撃った: %v", why, calls[0].Argv)
	}
}

// argValue は argv の中の `-f key=value` の value を返す。無ければ "" と false。
func argValue(argv []string, key string) (string, bool) {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-f" && strings.HasPrefix(argv[i+1], key+"=") {
			return strings.TrimPrefix(argv[i+1], key+"="), true
		}
	}
	return "", false
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, file, content string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(file))
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, src, dst string, mode os.FileMode) {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, raw, mode); err != nil {
		t.Fatal(err)
	}
}

func assertExit(t *testing.T, r runResult, want int) {
	t.Helper()
	if r.exit != want {
		t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", r.exit, want, r.stdout, r.stderr)
	}
}
