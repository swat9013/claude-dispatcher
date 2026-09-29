package blackbox_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// stub への応答と fixture。
//
// gh の stub は argv の先頭 2 token (`repo view` / `label list` / `issue list` / `api graphql`) で振り分け、
// GitHub の JSON の形で答える。この振り分けと GraphQL の応答の形は設計 doc の契約ではなく、gh という外部 CLI
// との通信の前提で、tick の実装 (#3) と一緒に決まる。実装が別の呼び方を選んだら、ここを合わせて直す。

const (
	// workerPromptMarker は spawn prompt の fixture に必ず入れる印。claude の stub はこれで worker と orchestrator を見分ける
	workerPromptMarker = "WORKER-PROMPT"
	workerStubOutput   = "worker stub ran\n"
	orchestratorOutput = "orchestrator stub ran\n"
)

// respond は name の stub に rule を足す。後から足した rule が先に当たる (既定の応答を上書きする)。
func (s *sandbox) respond(name string, r stubwire.Rule) {
	s.t.Helper()
	s.rules[name] = append([]stubwire.Rule{r}, s.rules[name]...)
	s.writeRules(name, s.rules[name])
}

func (s *sandbox) writeRules(name string, rules []stubwire.Rule) {
	s.t.Helper()
	file := stubwire.ResponsesFile(s.stubRoot, name)
	mustMkdir(s.t, filepath.Dir(file))
	mustWrite(s.t, file, mustJSON(s.t, rules))
}

// --- gh ---

func (s *sandbox) setLabels(names ...string) {
	s.t.Helper()
	labels := make([]map[string]string, 0, len(names))
	for _, name := range names {
		labels = append(labels, map[string]string{"name": name})
	}
	s.respond("gh", stubwire.Rule{ArgsPrefix: []string{"label", "list"}, Stdout: mustJSON(s.t, labels)})
}

// ghFails は argv が prefix で始まる gh の呼び出しを exit と stderr で失敗させる。
func (s *sandbox) ghFails(prefix []string, exit int, stderr string) {
	s.respond("gh", stubwire.Rule{ArgsPrefix: prefix, Exit: exit, Stderr: stderr})
}

// repoMissing は repo が実在しないときの gh の応答を足す。repo を渡さなければどの repo も実在しない。
func (s *sandbox) repoMissing(repo ...string) {
	s.respond("gh", stubwire.Rule{
		ArgsPrefix: append([]string{"repo", "view"}, repo...),
		Stderr:     "GraphQL: Could not resolve to a Repository\n", Exit: 1,
	})
}

type issue struct {
	number int
	body   string
	labels []string
}

// setIssues は `gh issue list` が返す open issue を決める (GitHub の JSON の形)。
func (s *sandbox) setIssues(issues ...issue) {
	s.t.Helper()
	out := make([]map[string]any, 0, len(issues))
	for _, i := range issues {
		labels := make([]map[string]string, 0, len(i.labels))
		for _, name := range i.labels {
			labels = append(labels, map[string]string{"name": name})
		}
		out = append(out, map[string]any{
			"number": i.number, "title": fmt.Sprintf("issue %d", i.number), "body": i.body, "labels": labels,
			"url": fmt.Sprintf("https://github.com/%s/issues/%d", defaultIssueRepo, i.number),
		})
	}
	s.respond("gh", stubwire.Rule{ArgsPrefix: []string{"issue", "list"}, Stdout: mustJSON(s.t, out)})
}

func readyIssue(number int) issue {
	return issue{number: number, body: fmt.Sprintf("本文 %d", number), labels: []string{defaultReadyLabel}}
}

type pullRequest struct {
	number     int
	branch     string
	base       string
	mergeable  string // MERGEABLE / CONFLICTING / UNKNOWN
	closes     []int
	closesRepo string // 省略すると issue 置き場
	checks     string // SUCCESS / PENDING / FAILURE / ERROR。空なら checks なし (null)
	unresolved int
	resolved   int
	fork       bool // head branch が fork の repo にある (isCrossRepository)
}

// setPRs は `gh api graphql` が返す open PR を決める (GitHub GraphQL の repository.pullRequests の形)。
func (s *sandbox) setPRs(prs ...pullRequest) {
	s.t.Helper()
	s.respondPRPage(map[string]bool{"hasNextPage": false}, prs)
}

// setTruncatedPRs は取得上限を超えて続きのページがある open PR を返す。
func (s *sandbox) setTruncatedPRs(prs ...pullRequest) {
	s.t.Helper()
	s.respondPRPage(map[string]bool{"hasNextPage": true}, prs)
}

func (s *sandbox) respondPRPage(pageInfo map[string]bool, prs []pullRequest) {
	s.t.Helper()
	nodes := make([]map[string]any, 0, len(prs))
	for _, pr := range prs {
		nodes = append(nodes, pr.graphqlNode())
	}
	payload := map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequests": map[string]any{
		"pageInfo": pageInfo,
		"nodes":    nodes,
	}}}}
	s.respond("gh", stubwire.Rule{ArgsPrefix: []string{"api", "graphql"}, Stdout: mustJSON(s.t, payload)})
}

func (pr pullRequest) graphqlNode() map[string]any {
	repo := pr.closesRepo
	if repo == "" {
		repo = defaultIssueRepo
	}
	refs := make([]map[string]any, 0, len(pr.closes))
	for _, n := range pr.closes {
		refs = append(refs, map[string]any{"number": n, "repository": map[string]string{"nameWithOwner": repo}})
	}
	threads := make([]map[string]bool, 0, pr.unresolved+pr.resolved)
	for range pr.unresolved {
		threads = append(threads, map[string]bool{"isResolved": false})
	}
	for range pr.resolved {
		threads = append(threads, map[string]bool{"isResolved": true})
	}
	var rollup any
	if pr.checks != "" {
		rollup = map[string]string{"state": pr.checks}
	}
	base, mergeable := pr.base, pr.mergeable
	if base == "" {
		base = "main"
	}
	if mergeable == "" {
		mergeable = "MERGEABLE"
	}
	return map[string]any{
		"number":                  pr.number,
		"url":                     fmt.Sprintf("https://github.com/%s/pull/%d", defaultIssueRepo, pr.number),
		"headRefName":             pr.branch,
		"isCrossRepository":       pr.fork,
		"baseRefName":             base,
		"isDraft":                 false,
		"mergeable":               mergeable,
		"closingIssuesReferences": map[string]any{"totalCount": len(refs), "nodes": refs},
		"reviewThreads":           map[string]any{"totalCount": len(threads), "nodes": threads},
		"commits": map[string]any{"nodes": []any{
			map[string]any{"commit": map[string]any{"statusCheckRollup": rollup}},
		}},
	}
}

func workerBranch(issue int) string { return fmt.Sprintf("worktree-issue-%d", issue) }

// --- claude ---

// quickWorkerRule は既定の worker への応答。すぐ終わる
var quickWorkerRule = stubwire.Rule{ArgContains: workerPromptMarker, Stdout: workerStubOutput}

// writeClaudeRules は claude の応答を worker → orchestrator の順で書き出す。
// orchestrator の rule は条件を持たず何にでも当たるので、worker の rule を必ず先に置く。
func (s *sandbox) writeClaudeRules() {
	s.t.Helper()
	rules := []stubwire.Rule{s.workerRule}
	if s.orchestratorRule != nil {
		rules = append(rules, *s.orchestratorRule)
	}
	s.writeRules("claude", rules)
}

// orchestratorWrites は orchestrator が正常終了し、決定ファイルに d を書くようにする。
func (s *sandbox) orchestratorWrites(d decisions) {
	s.orchestratorWritesRaw(d.fileContent(s.t))
}

// orchestratorWritesRaw は orchestrator が正常終了し、決定ファイルに content をそのまま書くようにする。
func (s *sandbox) orchestratorWritesRaw(content string) {
	s.orchestratorBehaves(stubwire.Rule{
		Stdout:    orchestratorOutput,
		Decisions: &stubwire.DecisionsWrite{StateDir: s.stateDir(), Content: content},
	})
}

// orchestratorWritesBlockingWorkerLogs は orchestratorWrites に加えて、issues の worker log の path を dir で塞ぐ。
// CLI はその worker の log を開けずに止まる (塞いでいない worker は先に起動しうる)。
func (s *sandbox) orchestratorWritesBlockingWorkerLogs(d decisions, issues ...int) {
	s.orchestratorBehaves(stubwire.Rule{
		Stdout: orchestratorOutput,
		Decisions: &stubwire.DecisionsWrite{
			StateDir: s.stateDir(), Content: d.fileContent(s.t), ObstructWorkerLogs: issues,
		},
	})
}

// orchestratorWritesNothing は orchestrator が正常終了するが決定ファイルを書かないようにする。
func (s *sandbox) orchestratorWritesNothing() {
	s.orchestratorBehaves(stubwire.Rule{Stdout: orchestratorOutput})
}

// orchestratorFailsAfterWriting は orchestrator が d を書いたうえで exit code で異常終了するようにする。
func (s *sandbox) orchestratorFailsAfterWriting(code int, d decisions) {
	s.orchestratorBehaves(stubwire.Rule{
		Stdout: orchestratorOutput, Exit: code,
		Decisions: &stubwire.DecisionsWrite{StateDir: s.stateDir(), Content: d.fileContent(s.t)},
	})
}

// orchestratorBehaves は orchestrator の起動 (worker の印を持たない claude 呼び出し) への応答を決める。
func (s *sandbox) orchestratorBehaves(r stubwire.Rule) {
	s.t.Helper()
	s.orchestratorRule = &r
	s.writeClaudeRules()
}

// orchestratorSkips は指示ファイルの issue を全件見送る決定を orchestrator に書かせる (網羅の検査を通すため)。
func (s *sandbox) orchestratorSkips(issues ...int) {
	s.t.Helper()
	var d decisions
	for _, n := range issues {
		d.Decisions = append(d.Decisions, decision{Issue: n, Action: "skip", Reason: "テスト"})
	}
	s.orchestratorWrites(d)
}

type decisions struct {
	Decisions []decision `json:"decisions"`
	Spawn     []spawn    `json:"spawn"`
}

type decision struct {
	Issue  int    `json:"issue"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

type spawn struct {
	Issue     int      `json:"issue"`
	Kind      string   `json:"kind"`
	Prompt    string   `json:"prompt"`
	Playbooks []string `json:"playbooks"`
}

// fileContent は決定ファイルの本文 (formats.md §5.2 の JSON) を返す。
func (d decisions) fileContent(t *testing.T) string {
	t.Helper()
	if d.Decisions == nil {
		d.Decisions = []decision{}
	}
	if d.Spawn == nil {
		d.Spawn = []spawn{}
	}
	return mustJSON(t, d)
}

// startDecisions は issues それぞれに playbook で start する決定と spawn を組む。
func startDecisions(playbook string, issues ...int) decisions {
	var d decisions
	for _, n := range issues {
		d.Decisions = append(d.Decisions, decision{Issue: n, Action: "start", Reason: "着手できる"})
		d.Spawn = append(d.Spawn, startSpawn(n, playbook))
	}
	return d
}

// startSpawn は playbook で issue に新規着手する worker の spawn を組む。
func startSpawn(issue int, playbook string) spawn {
	return spawn{Issue: issue, Kind: "start", Prompt: workerPrompt(issue, playbook), Playbooks: []string{playbook}}
}

// workerPrompt は playbook の path を本文に載せた spawn prompt の fixture。
func workerPrompt(issue int, playbooks ...string) string {
	return fmt.Sprintf("%s issue #%d\n%s\n", workerPromptMarker, issue, strings.Join(playbooks, "\n"))
}

// startScenario は候補 42 に start を決め、worker を 1 件起動させる。返り値は選んだ playbook の path。
func startScenario(s *sandbox) string {
	s.t.Helper()
	s.setIssues(readyIssue(42))
	playbook := playbookPath(s.defaultInstallPath(), "playbook-implementation")
	s.orchestratorWrites(startDecisions(playbook, 42))
	return playbook
}

// blockWorkerLogs は worker log の置き場を file で塞ぐ: orchestrator は走り終え、最初の worker の起動で tick が止まる。
func (s *sandbox) blockWorkerLogs() {
	mustWrite(s.t, s.workersDir(), "not a directory")
}

// --- plugin swat-skills ---

type pluginEntry struct {
	Scope       string `json:"scope"`
	ProjectPath string `json:"projectPath,omitempty"`
	InstallPath string `json:"installPath"`
	Version     string `json:"version"`
}

func (s *sandbox) installedPluginsFile() string {
	return filepath.Join(s.home, ".claude", "plugins", "installed_plugins.json")
}

// installPlugin は installed_plugins.json に entry を足し、installPath に playbook 群と原則索引を置く。
// 返り値は installPath。
func (s *sandbox) installPlugin(key, scope, projectPath string) string {
	s.t.Helper()
	installPath := filepath.Join(s.root, "plugin-cache", strings.ReplaceAll(key, "@", "_"), fmt.Sprintf("%s-%d", scope, len(s.plugins[key])))
	writePluginTree(s.t, installPath)
	s.plugins[key] = append(s.plugins[key], pluginEntry{Scope: scope, ProjectPath: projectPath, InstallPath: installPath, Version: "1.0.0"})
	s.writeInstalledPlugins()
	return installPath
}

func (s *sandbox) removeInstalledPlugins() {
	s.t.Helper()
	s.plugins = map[string][]pluginEntry{}
	if err := os.Remove(s.installedPluginsFile()); err != nil {
		s.t.Fatal(err)
	}
}

// clearPluginEntries は installed_plugins.json を残したまま entry を空にする。
func (s *sandbox) clearPluginEntries() {
	s.t.Helper()
	s.plugins = map[string][]pluginEntry{}
	s.writeInstalledPlugins()
}

func (s *sandbox) writeInstalledPlugins() {
	s.t.Helper()
	mustMkdir(s.t, filepath.Dir(s.installedPluginsFile()))
	raw, err := json.Marshal(map[string]any{"version": 2, "plugins": s.plugins})
	if err != nil {
		s.t.Fatal(err)
	}
	mustWrite(s.t, s.installedPluginsFile(), string(raw))
}

// defaultInstallPath は newSandbox が user scope に入れた plugin の installPath。
func (s *sandbox) defaultInstallPath() string {
	return s.plugins["swat-skills@swat9013"][0].InstallPath
}

const (
	defaultDispatchWhen = "既定。他の playbook の dispatch-when に明示的に当たらない issue"
	docsDispatchWhen    = "文書だけを書き換える issue"
)

func writePluginTree(t *testing.T, installPath string) {
	t.Helper()
	procedure := filepath.Join(installPath, "skills", "procedure")
	writeSkill(t, filepath.Join(procedure, "playbook-implementation"),
		"name: playbook-implementation\nmetadata:\n  deliverable: cl\n  dispatch-when: "+defaultDispatchWhen+"\n")
	writeSkill(t, filepath.Join(procedure, "playbook-docs"),
		"name: playbook-docs\nmetadata:\n  deliverable: cl\n  dispatch-when: "+docsDispatchWhen+"\n")
	for _, name := range []string{"playbook-conflict-resolution", "playbook-review-response", "playbook-ci-fix"} {
		writeSkill(t, filepath.Join(procedure, name), "name: "+name+"\n")
	}
	writeSkill(t, filepath.Join(installPath, "skills", "knowledge", "principle-index"), "name: principle-index\n")
}

func writeSkill(t *testing.T, dir, frontmatter string) {
	t.Helper()
	mustMkdir(t, dir)
	mustWrite(t, filepath.Join(dir, "SKILL.md"), "---\n"+frontmatter+"---\n\n# "+filepath.Base(dir)+"\n")
}

func playbookPath(installPath, name string) string {
	return filepath.Join(installPath, "skills", "procedure", name, "SKILL.md")
}

func principleIndexPath(installPath string) string {
	return filepath.Join(installPath, "skills", "knowledge", "principle-index", "SKILL.md")
}
