// Package blackbox_test は claude-dispatcher の binary を subprocess として撃ち、外から観測できる契約
// (docs/design/formats.md) を固定する。
//
// binary の内部 package は import しない。観測するのは次の 3 つだけ:
//   - exit code / stdout / stderr
//   - state dir に書かれた file
//   - PATH に置いた stub (gh / claude / git / ps) が受け取った argv・cwd・env
package blackbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	dispatcherPackage = "github.com/swat9013/claude-dispatcher/cmd/claude-dispatcher"
	stubPackage       = "github.com/swat9013/claude-dispatcher/test/blackbox/stub"

	defaultProject    = "widgets"
	defaultIssueRepo  = "acme/widgets"
	defaultReadyLabel = "ready-for-agent"
	wipLabel          = "dispatcher:wip"
	humanLabel        = "ready-for-human"

	// workerPromptMarker は spawn prompt の fixture に必ず入れる印。claude の stub はこれで worker と orchestrator を見分ける
	workerPromptMarker = "WORKER-PROMPT"
	workerStubOutput   = "worker stub ran\n"
	orchestratorOutput = "orchestrator stub ran\n"

	runTimeout = 60 * time.Second
)

var stubNames = []string{"gh", "claude", "git", "ps"}

var (
	dispatcherBin string
	stubBin       string
)

var (
	tickStemPattern = regexp.MustCompile(`^\d{8}T\d{6}\.\d{6}Z$`)
	logTSPattern    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$`)
	uuidPattern     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "claude-dispatcher-blackbox-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	stubBin = filepath.Join(dir, "stub")
	if out, err := exec.Command("go", "build", "-o", stubBin, stubPackage).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "テスト用 stub を build できない (%s): %v\n%s", stubPackage, err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	dispatcherBin = filepath.Join(dir, "claude-dispatcher")
	if out, err := exec.Command("go", "build", "-o", dispatcherBin, dispatcherPackage).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "テスト対象の binary を build できない (%s): %v\n%s", dispatcherPackage, err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// --- sandbox: 1 テスト分の HOME / 置き場 / clone / stub ---

type sandbox struct {
	t          *testing.T
	root       string
	home       string
	clone      string
	configRoot string
	stateRoot  string
	stubRoot   string
	binDir     string
	project    string
	env        map[string]string
	rules      map[string][]stubRule
	plugins    map[string][]pluginEntry
}

type stubRule struct {
	ArgsPrefix  []string        `json:"args_prefix,omitempty"`
	ArgContains string          `json:"arg_contains,omitempty"`
	Stdout      string          `json:"stdout,omitempty"`
	Stderr      string          `json:"stderr,omitempty"`
	Exit        int             `json:"exit,omitempty"`
	SleepMillis int             `json:"sleep_ms,omitempty"`
	Decisions   *decisionsWrite `json:"decisions,omitempty"`
}

type decisionsWrite struct {
	StateDir string `json:"state_dir"`
	Content  string `json:"content"`
}

type pluginEntry struct {
	Scope       string `json:"scope"`
	ProjectPath string `json:"projectPath,omitempty"`
	InstallPath string `json:"installPath"`
	Version     string `json:"version"`
}

// newSandbox は XDG_CONFIG_HOME / XDG_STATE_HOME を tmp に向けた sandbox を作る。
// config は検証済みの既定 (formats.md §2 の必須 3 項目)、gh は repo と label が実在する応答、
// plugin swat-skills は user scope に 1 つ install 済み。state dir は setup が作った前提で置く。
func newSandbox(t *testing.T) *sandbox {
	s := newBareSandbox(t)
	s.env["XDG_CONFIG_HOME"] = filepath.Join(s.root, "xdg-config")
	s.env["XDG_STATE_HOME"] = filepath.Join(s.root, "xdg-state")
	s.configRoot = filepath.Join(s.root, "xdg-config", "claude-dispatcher")
	s.stateRoot = filepath.Join(s.root, "xdg-state", "claude-dispatcher")
	s.setUp()
	return s
}

// newSandboxWithHomeDefaults は XDG_* を渡さない sandbox (置き場は $HOME/.config と $HOME/.local/state)。
func newSandboxWithHomeDefaults(t *testing.T) *sandbox {
	s := newBareSandbox(t)
	s.configRoot = filepath.Join(s.home, ".config", "claude-dispatcher")
	s.stateRoot = filepath.Join(s.home, ".local", "state", "claude-dispatcher")
	s.setUp()
	return s
}

func newBareSandbox(t *testing.T) *sandbox {
	t.Helper()
	// macOS の t.TempDir() は /var/... を返すが、子 process の cwd は /private/var/... に解決される。
	// project scope の projectPath と cwd の照合を実環境どおりに通すため、実 path に揃える
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &sandbox{
		t:        t,
		root:     root,
		home:     filepath.Join(root, "home"),
		clone:    filepath.Join(root, "clone"),
		stubRoot: filepath.Join(root, "stub"),
		project:  defaultProject,
		rules:    map[string][]stubRule{},
		plugins:  map[string][]pluginEntry{},
	}
	s.binDir = filepath.Join(root, "stub-bin")
	for _, dir := range []string{s.home, s.clone, s.stubRoot} {
		mustMkdir(t, dir)
	}
	// TempDir の削除より先に走る (Cleanup は後に登録したものから走る)
	t.Cleanup(s.waitForSpawnedWorkers)
	s.installStubs(s.binDir)
	s.env = map[string]string{
		"HOME": s.home,
		// stub を先頭に置く。PATH の自己解決が HOME 配下や Homebrew の実物へ届かないよう、依存 CLI はすべて stub で埋める
		"PATH": s.binDir + ":/usr/bin:/bin",
	}
	return s
}

func (s *sandbox) setUp() {
	mustMkdir(s.t, s.stateDir())
	s.writeConfig(s.defaultConfig())
	s.setLabels(wipLabel, humanLabel, defaultReadyLabel, "needs-triage")
	s.respond("gh", stubRule{ArgsPrefix: []string{"repo", "view"}, Stdout: `{"nameWithOwner":"` + defaultIssueRepo + `"}`})
	s.setIssues()
	s.setPRs()
	s.respond("claude", stubRule{ArgContains: workerPromptMarker, Stdout: workerStubOutput})
	s.installPlugin("swat-skills@swat9013", "user", "")
}

// installStubs は stub binary を名前ごとに dir へ hard link し、stub が root を引く `.stub-root` を置く。
func (s *sandbox) installStubs(dir string) {
	s.t.Helper()
	mustMkdir(s.t, dir)
	for _, name := range stubNames {
		dst := filepath.Join(dir, name)
		if err := os.Link(stubBin, dst); err != nil {
			copyFile(s.t, stubBin, dst, 0o755)
		}
	}
	mustWrite(s.t, filepath.Join(dir, ".stub-root"), s.stubRoot+"\n")
}

func (s *sandbox) configDir() string  { return filepath.Join(s.configRoot, s.project) }
func (s *sandbox) configFile() string { return filepath.Join(s.configDir(), "config.toml") }
func (s *sandbox) stateDir() string   { return filepath.Join(s.stateRoot, s.project) }
func (s *sandbox) logFile() string    { return filepath.Join(s.stateDir(), "log.jsonl") }
func (s *sandbox) markerFile() string { return filepath.Join(s.stateDir(), "config-verified") }
func (s *sandbox) lockFile() string   { return filepath.Join(s.stateDir(), "tick.lock") }

func (s *sandbox) defaultConfig() string {
	return fmt.Sprintf("[issue]\nrepo = %q\nready_label = %q\n\n[limits]\nmax_wip = 2\n", defaultIssueRepo, defaultReadyLabel)
}

func (s *sandbox) writeConfig(content string) {
	s.t.Helper()
	mustMkdir(s.t, s.configDir())
	mustWrite(s.t, s.configFile(), content)
}

// --- stub の応答 ---

// respond は name の stub に rule を足す。後から足した rule が先に当たる (既定の応答を上書きする)。
func (s *sandbox) respond(name string, r stubRule) {
	s.t.Helper()
	s.rules[name] = append([]stubRule{r}, s.rules[name]...)
	raw, err := json.Marshal(s.rules[name])
	if err != nil {
		s.t.Fatal(err)
	}
	mustMkdir(s.t, filepath.Join(s.stubRoot, "responses"))
	mustWrite(s.t, filepath.Join(s.stubRoot, "responses", name+".json"), string(raw))
}

func (s *sandbox) setLabels(names ...string) {
	s.t.Helper()
	labels := make([]map[string]string, 0, len(names))
	for _, name := range names {
		labels = append(labels, map[string]string{"name": name})
	}
	s.respond("gh", stubRule{ArgsPrefix: []string{"label", "list"}, Stdout: mustJSON(s.t, labels)})
}

type issue struct {
	number int
	title  string
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
		title := i.title
		if title == "" {
			title = fmt.Sprintf("issue %d", i.number)
		}
		out = append(out, map[string]any{
			"number": i.number, "title": title, "body": i.body, "labels": labels,
			"url": fmt.Sprintf("https://github.com/%s/issues/%d", defaultIssueRepo, i.number),
		})
	}
	s.respond("gh", stubRule{ArgsPrefix: []string{"issue", "list"}, Stdout: mustJSON(s.t, out)})
}

func readyIssue(number int) issue {
	return issue{number: number, body: fmt.Sprintf("本文 %d", number), labels: []string{defaultReadyLabel}}
}

type pullRequest struct {
	number     int
	branch     string
	base       string
	draft      bool
	mergeable  string // MERGEABLE / CONFLICTING / UNKNOWN
	closes     []int
	closesRepo string // 省略すると issue 置き場
	checks     string // SUCCESS / PENDING / FAILURE / ERROR。空なら checks なし (null)
	unresolved int
	resolved   int
}

// setPRs は `gh api graphql` が返す open PR を決める (GitHub GraphQL の repository.pullRequests の形)。
func (s *sandbox) setPRs(prs ...pullRequest) { s.setPRPage(false, prs...) }

func (s *sandbox) setPRPage(hasNextPage bool, prs ...pullRequest) {
	s.t.Helper()
	nodes := make([]map[string]any, 0, len(prs))
	for _, pr := range prs {
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
		nodes = append(nodes, map[string]any{
			"number":                  pr.number,
			"url":                     fmt.Sprintf("https://github.com/%s/pull/%d", defaultIssueRepo, pr.number),
			"headRefName":             pr.branch,
			"baseRefName":             base,
			"isDraft":                 pr.draft,
			"mergeable":               mergeable,
			"closingIssuesReferences": map[string]any{"totalCount": len(refs), "nodes": refs},
			"reviewThreads":           map[string]any{"totalCount": len(threads), "nodes": threads},
			"commits": map[string]any{"nodes": []any{
				map[string]any{"commit": map[string]any{"statusCheckRollup": rollup}},
			}},
		})
	}
	payload := map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequests": map[string]any{
		"pageInfo": map[string]bool{"hasNextPage": hasNextPage},
		"nodes":    nodes,
	}}}}
	s.respond("gh", stubRule{ArgsPrefix: []string{"api", "graphql"}, Stdout: mustJSON(s.t, payload)})
}

func workerBranch(issue int) string { return fmt.Sprintf("worktree-issue-%d", issue) }

// orchestratorWrites は orchestrator (claude の stub) が正常終了し、決定ファイルに content を書くようにする。
func (s *sandbox) orchestratorWrites(content string) {
	s.orchestratorBehaves(stubRule{
		Stdout:    orchestratorOutput,
		Decisions: &decisionsWrite{StateDir: s.stateDir(), Content: content},
	})
}

// orchestratorBehaves は orchestrator の起動 (worker の印を持たない claude 呼び出し) への応答を決める。
func (s *sandbox) orchestratorBehaves(r stubRule) {
	s.t.Helper()
	// worker 用の rule を先頭に保つ (orchestrator の rule は条件を持たず何にでも当たるため)
	worker := stubRule{ArgContains: workerPromptMarker, Stdout: workerStubOutput}
	s.rules["claude"] = nil
	s.respond("claude", r)
	s.respond("claude", worker)
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

func (d decisions) json(t *testing.T) string {
	t.Helper()
	if d.Decisions == nil {
		d.Decisions = []decision{}
	}
	if d.Spawn == nil {
		d.Spawn = []spawn{}
	}
	return mustJSON(t, d)
}

// workerPrompt は playbook の path を本文に載せた spawn prompt の fixture。
func workerPrompt(issue int, playbooks ...string) string {
	return fmt.Sprintf("%s issue #%d\n%s\n", workerPromptMarker, issue, strings.Join(playbooks, "\n"))
}

// --- plugin swat-skills の fixture ---

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
	if err := os.Remove(filepath.Join(s.home, ".claude", "plugins", "installed_plugins.json")); err != nil {
		s.t.Fatal(err)
	}
}

func (s *sandbox) writeInstalledPlugins() {
	s.t.Helper()
	file := filepath.Join(s.home, ".claude", "plugins", "installed_plugins.json")
	mustMkdir(s.t, filepath.Dir(file))
	mustWrite(s.t, file, mustJSON(s.t, map[string]any{"version": 2, "plugins": s.plugins}))
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

// --- 実行 ---

type runResult struct {
	exit   int
	stdout string
	stderr string
}

// run は clone を cwd にして binary を撃つ。env は sandbox の env だけ (テスト runner の env を継がない)。
func (s *sandbox) run(args ...string) runResult {
	s.t.Helper()
	return s.runWithEnv(nil, args...)
}

// runWithEnv は sandbox の env に extra を足して撃つ。
func (s *sandbox) runWithEnv(extra map[string]string, args ...string) runResult {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, dispatcherBin, args...)
	cmd.Dir = s.clone
	for key, value := range s.env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	for key, value := range extra {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
	default:
		s.t.Fatalf("binary を撃てない: %v", err)
	}
	if ctx.Err() != nil {
		s.t.Fatalf("binary が %s で終わらない: %v\nstderr:\n%s", runTimeout, args, stderr.String())
	}
	return runResult{exit: cmd.ProcessState.ExitCode(), stdout: stdout.String(), stderr: stderr.String()}
}

func (s *sandbox) tick(flags ...string) runResult {
	s.t.Helper()
	return s.run(append([]string{"tick", s.project}, flags...)...)
}

// --- 観測 ---

type stubCall struct {
	Exe  string            `json:"exe"`
	Argv []string          `json:"argv"`
	Cwd  string            `json:"cwd"`
	Env  map[string]string `json:"env"`
}

func (c stubCall) args() []string { return c.Argv[1:] }

// flagValue は `--name value` の value を返す。無ければ "" と false。
func (c stubCall) flagValue(name string) (string, bool) {
	for i, arg := range c.Argv[:len(c.Argv)-1] {
		if arg == name {
			return c.Argv[i+1], true
		}
	}
	return "", false
}

func (c stubCall) hasArg(want string) bool {
	for _, arg := range c.args() {
		if arg == want {
			return true
		}
	}
	return false
}

func (c stubCall) hasPrefix(prefix ...string) bool {
	args := c.args()
	if len(args) < len(prefix) {
		return false
	}
	for i := range prefix {
		if args[i] != prefix[i] {
			return false
		}
	}
	return true
}

func (c stubCall) contains(fragment string) bool {
	for _, arg := range c.args() {
		if strings.Contains(arg, fragment) {
			return true
		}
	}
	return false
}

func (s *sandbox) calls(name string) []stubCall {
	s.t.Helper()
	dir := filepath.Join(s.stubRoot, "calls", name)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		s.t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	calls := make([]stubCall, 0, len(names))
	for _, n := range names {
		var c stubCall
		if err := json.Unmarshal(mustRead(s.t, filepath.Join(dir, n)), &c); err != nil {
			s.t.Fatalf("%s: %v", n, err)
		}
		calls = append(calls, c)
	}
	return calls
}

func (s *sandbox) callsMatching(name string, match func(stubCall) bool) []stubCall {
	var out []stubCall
	for _, c := range s.calls(name) {
		if match(c) {
			out = append(out, c)
		}
	}
	return out
}

func isWorkerCall(c stubCall) bool       { return c.contains(workerPromptMarker) }
func isOrchestratorCall(c stubCall) bool { return !isWorkerCall(c) }

// waitWorkerCalls は detach 起動された worker の stub 呼び出しが n 件記録されるまで待つ (tick は worker を待たずに終わる)。
func (s *sandbox) waitWorkerCalls(n int) []stubCall {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		calls := s.callsMatching("claude", isWorkerCall)
		if len(calls) >= n || time.Now().After(deadline) {
			return calls
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitFor(t *testing.T, done func() bool, failure string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal(failure)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitForSpawnedWorkers は log の spawned に載った worker の終了を待つ。detach 起動された worker の stub が
// テスト終了後に sandbox へ書くと、TempDir の削除が "directory not empty" で落ちる。
func (s *sandbox) waitForSpawnedWorkers() {
	if s.stateRoot == "" {
		return
	}
	for _, line := range s.tickLines() {
		spawned, _ := line["spawned"].([]any)
		for _, raw := range spawned {
			entry, _ := raw.(map[string]any)
			pid, _ := entry["pid"].(float64)
			if pid <= 0 {
				continue
			}
			deadline := time.Now().Add(10 * time.Second)
			for syscall.Kill(int(pid), 0) == nil && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
		}
	}
}

// assertNoWorkerCalls は worker が起動されていないことを、起動されていれば記録が届く猶予を置いてから確かめる。
func (s *sandbox) assertNoWorkerCalls() {
	s.t.Helper()
	time.Sleep(300 * time.Millisecond)
	if calls := s.callsMatching("claude", isWorkerCall); len(calls) != 0 {
		s.t.Fatalf("worker が起動された: %d 件", len(calls))
	}
}

type logLine map[string]any

func (s *sandbox) logLines() []logLine {
	s.t.Helper()
	raw, err := os.ReadFile(s.logFile())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		s.t.Fatal(err)
	}
	var lines []logLine
	for i, text := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if text == "" {
			continue
		}
		var line logLine
		if err := json.Unmarshal([]byte(text), &line); err != nil {
			s.t.Fatalf("log.jsonl %d 行目が JSON でない: %v\n%s", i+1, err, text)
		}
		lines = append(lines, line)
	}
	return lines
}

// tickLines は tick 行 (`actor` を持たない行) だけを返す。
func (s *sandbox) tickLines() []logLine {
	var out []logLine
	for _, line := range s.logLines() {
		if _, ok := line["actor"]; !ok {
			out = append(out, line)
		}
	}
	return out
}

func (s *sandbox) onlyTickLine() logLine {
	s.t.Helper()
	lines := s.tickLines()
	if len(lines) != 1 {
		s.t.Fatalf("tick 行がちょうど 1 行でない: %d 行\n%v", len(lines), lines)
	}
	return lines[0]
}

func (s *sandbox) orchestratorLines() []logLine {
	var out []logLine
	for _, line := range s.logLines() {
		if line["actor"] == "orchestrator" {
			out = append(out, line)
		}
	}
	return out
}

func (s *sandbox) instructionFiles() []string {
	s.t.Helper()
	files, err := filepath.Glob(filepath.Join(s.stateDir(), "instructions", "*.json"))
	if err != nil {
		s.t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

// onlyInstructionFile は書かれた指示ファイルがちょうど 1 つであることを確かめ、中身を返す。
func (s *sandbox) onlyInstructionFile() map[string]any {
	s.t.Helper()
	files := s.instructionFiles()
	if len(files) != 1 {
		s.t.Fatalf("指示ファイルがちょうど 1 つでない: %v", files)
	}
	var doc map[string]any
	if err := json.Unmarshal(mustRead(s.t, files[0]), &doc); err != nil {
		s.t.Fatal(err)
	}
	return doc
}

func instructionsOf(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range asList(t, doc["instructions"]) {
		out = append(out, asMap(t, raw))
	}
	return out
}

func instructionsOfKind(t *testing.T, doc map[string]any, kind string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, i := range instructionsOf(t, doc) {
		if i["kind"] == kind {
			out = append(out, i)
		}
	}
	return out
}

// tree は dir 配下の file の path → (size, mtime) を返す。書き込みが無かったことの比較に使う。
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		out[path] = fmt.Sprintf("%d %d %v", info.Size(), info.ModTime().UnixNano(), info.IsDir())
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return out
}

// --- 小道具 ---

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, file, content string) {
	t.Helper()
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, file string) []byte {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func copyFile(t *testing.T, src, dst string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(dst, mustRead(t, src), mode); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func assertExit(t *testing.T, r runResult, want int) {
	t.Helper()
	if r.exit != want {
		t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", r.exit, want, r.stdout, r.stderr)
	}
}

func assertResult(t *testing.T, line logLine, want string) {
	t.Helper()
	if line["result"] != want {
		t.Fatalf("result %v, want %q: %v", line["result"], want, line)
	}
}

// assertErrorNames は log 行の error が names をそれぞれ名指ししていることを確かめる。
//
// sandbox の path は test 名を含み ("…/TestConfigMissing…repo123/001/…") 部分文字列の検査を素通しにするので、
// "/" を含まない名前は sandbox の path を除いた本文に語として現れることを見る。"/" を含む名前 (path・owner/name) は
// 本文にそのまま含まれることを見る。
func (s *sandbox) assertErrorNames(line logLine, names ...string) {
	s.t.Helper()
	s.assertNames(asString(s.t, line["error"]), names...)
}

func (s *sandbox) assertNames(msg string, names ...string) {
	s.t.Helper()
	withoutPaths := regexp.MustCompile(`\S*`+regexp.QuoteMeta(s.root)+`\S*`).ReplaceAllString(msg, "<path>")
	for _, name := range names {
		if strings.Contains(name, "/") {
			if !strings.Contains(msg, name) {
				s.t.Fatalf("%q を名指ししていない: %q", name, msg)
			}
			continue
		}
		word := regexp.MustCompile(`(^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(name) + `([^A-Za-z0-9_-]|$)`)
		if !word.MatchString(withoutPaths) {
			s.t.Fatalf("%q を名指ししていない: %q", name, msg)
		}
	}
}

// tickStem は指示ファイル等の path から tick の stem (YYYYMMDDTHHMMSS.ffffffZ) を取り出す。
func tickStem(file string) string {
	return strings.TrimSuffix(filepath.Base(file), ".json")
}

// 型の合わない値は panic せず t.Fatal にする (panic は suite 全体を止め、残りの red を隠す)

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("object でない: %#v", v)
	}
	return m
}

func asList(t *testing.T, v any) []any {
	t.Helper()
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("array でない: %#v", v)
	}
	return l
}

func asString(t *testing.T, v any) string {
	t.Helper()
	str, ok := v.(string)
	if !ok {
		t.Fatalf("string でない: %#v", v)
	}
	return str
}

func number(t *testing.T, v any) int {
	t.Helper()
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("数値でない: %#v", v)
	}
	return int(f)
}

func numbers(t *testing.T, v any) []int {
	t.Helper()
	var out []int
	for _, x := range asList(t, v) {
		out = append(out, number(t, x))
	}
	return out
}
