// Package blackbox_test は claude-dispatcher の binary を subprocess として撃ち、外から観測できる契約
// (docs/design/formats.md) を固定する。
//
// binary の内部 package は import しない。観測するのは次の 3 つだけ:
//   - exit code / stdout / stderr
//   - state dir に書かれた file
//   - PATH に置いた stub (gh / claude / git / ps) が受け取った argv・cwd・env
//
// 本 file は sandbox (1 テスト分の HOME / 置き場 / clone / stub) と binary の実行を持つ。
// stub への応答と fixture は fixtures_test.go、観測は observe_test.go、assert は assert_test.go。
package blackbox_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

const (
	dispatcherPackage = "github.com/swat9013/claude-dispatcher/cmd/claude-dispatcher"
	stubPackage       = "github.com/swat9013/claude-dispatcher/test/blackbox/stub"

	defaultProject    = "widgets"
	defaultIssueRepo  = "acme/widgets"
	defaultReadyLabel = "ready-for-agent"
	defaultMaxWIP     = 2
	wipLabel          = "dispatcher:wip"
	humanLabel        = "ready-for-human"

	runTimeout = 60 * time.Second
)

// git / ps の stub は status / setup / doctor (#4) の観測点。tick の契約は gh と claude の呼び出しだけで決まる
var stubNames = []string{"gh", "claude", "git", "ps"}

var registerSourcesOnce = sync.OnceValue(registerBinarySources)

var (
	dispatcherBin string
	stubBin       string
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

// registerBinarySources はテスト対象の binary のソースを open しておく。binary は go build の subprocess で作るので、
// そのままでは go test の cache がソースの変化を追えず、binary を変えても古い結果を返す。
// test process が open した file は cache key に入るので、ここで open して変化を拾わせる。
// open の記録は m.Run の中でしか取られないので、TestMain ではなく sandbox を作るときに 1 度だけ呼ぶ。
func registerBinarySources() error {
	for _, root := range []string{"../../cmd", "../../internal", "../../go.mod", "../../go.sum"} {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			return f.Close()
		})
		if err != nil {
			return fmt.Errorf("テスト対象のソースを辿れない: %w", err)
		}
	}
	return nil
}

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
	// rules は stub の名前ごとの応答 rule。claude は orchestratorRule と組み合わせて書き出す
	rules            map[string][]stubwire.Rule
	orchestratorRule *stubwire.Rule
	plugins          map[string][]pluginEntry
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
	if err := registerSourcesOnce(); err != nil {
		t.Fatal(err)
	}
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
		binDir:   filepath.Join(root, "stub-bin"),
		project:  defaultProject,
		rules:    map[string][]stubwire.Rule{},
		plugins:  map[string][]pluginEntry{},
	}
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
	s.respond("gh", stubwire.Rule{ArgsPrefix: []string{"repo", "view"}, Stdout: `{"nameWithOwner":"` + defaultIssueRepo + `"}`})
	s.setIssues()
	s.setPRs()
	s.writeClaudeRules()
	// git / ps は tick が使わない。status / doctor (#4) が呼ぶまで、呼ばれたら成功だけを返す
	s.respond("git", stubwire.Rule{})
	s.respond("ps", stubwire.Rule{})
	s.installPlugin("swat-skills@swat9013", "user", "")
}

// installStubs は stub binary を名前ごとに dir へ hard link し、stub が root を引く file を置く。
func (s *sandbox) installStubs(dir string) {
	s.t.Helper()
	mustMkdir(s.t, dir)
	for _, name := range stubNames {
		dst := filepath.Join(dir, name)
		// TMPDIR と t.TempDir() が別 device だと hard link を張れないので、そのときは複製する
		if err := os.Link(stubBin, dst); err != nil {
			copyFile(s.t, stubBin, dst, 0o755)
		}
	}
	mustWrite(s.t, filepath.Join(dir, stubwire.RootFile), s.stubRoot+"\n")
}

// --- 置き場 (formats.md §1) ---

func (s *sandbox) configDir() string  { return filepath.Join(s.configRoot, s.project) }
func (s *sandbox) configFile() string { return filepath.Join(s.configDir(), "config.toml") }
func (s *sandbox) stateDir() string   { return filepath.Join(s.stateRoot, s.project) }
func (s *sandbox) logFile() string    { return filepath.Join(s.stateDir(), "log.jsonl") }
func (s *sandbox) markerFile() string { return filepath.Join(s.stateDir(), "config-verified") }
func (s *sandbox) lockFile() string   { return filepath.Join(s.stateDir(), "tick.lock") }
func (s *sandbox) workersDir() string { return filepath.Join(s.stateDir(), "workers") }

func (s *sandbox) decisionsFile(stem string) string {
	return stubwire.DecisionsFile(s.stateDir(), stem)
}

func (s *sandbox) orchestratorLogFile(stem string) string {
	return filepath.Join(s.stateDir(), "decisions", stem+".orchestrator.log")
}

func (s *sandbox) workerLogFile(issue int, stem string) string {
	return stubwire.WorkerLogFile(s.stateDir(), issue, stem)
}

// --- config.toml ---

// configWith は必須 3 項目の config に、[issue] へ issueExtra の行を足し、[limits] を limits にしたものを返す。
func (s *sandbox) configWith(issueExtra, limits string) string {
	return fmt.Sprintf("[issue]\nrepo = %q\nready_label = %q\n%s\n[limits]\n%s\n", defaultIssueRepo, defaultReadyLabel, issueExtra, limits)
}

func (s *sandbox) defaultConfig() string {
	return s.configWith("", maxWIPLine(defaultMaxWIP))
}

func maxWIPLine(n int) string { return fmt.Sprintf("max_wip = %d", n) }

func (s *sandbox) writeConfig(content string) {
	s.t.Helper()
	mustMkdir(s.t, s.configDir())
	mustWrite(s.t, s.configFile(), content)
}

func (s *sandbox) setMaxWIP(n int) {
	s.writeConfig(s.configWith("", maxWIPLine(n)))
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
