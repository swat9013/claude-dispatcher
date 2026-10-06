// Package blackbox_test は claude-dispatcher の binary を subprocess として撃ち、外から観測できる契約
// (docs/design/formats.md) を固定する。
//
// binary の内部 package は import しない。観測するのは次の 3 つだけ:
//   - exit code / stdout / stderr
//   - state dir に書かれた file
//   - PATH に置いた stub (gh / claude / git) が受け取った argv・cwd・env
//
// 本 file は sandbox (1 テスト分の HOME / 置き場 / clone / stub) と binary の実行を持つ。
// stub への応答・fixture・assert は fixtures_test.go。
package blackbox_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

const (
	dispatcherPackage = "github.com/swat9013/claude-dispatcher/cmd/claude-dispatcher"
	stubPackage       = "github.com/swat9013/claude-dispatcher/test/blackbox/stub"

	defaultIssueRepo = "acme/widgets"
	readyLabel       = "ready-for-agent"

	runTimeout = 60 * time.Second
)

// stubNames は PATH に置く stub。tracker の CLI (gh / glab / acli) と、worker の claude と、setup と CL 側の trigger が撃つ git
var stubNames = []string{"gh", "glab", "acli", "claude", "git"}

// selfResolutionDirs は PATH の自己解決 (internal/deps の candidates) が探す置き場のうち、sandbox の HOME の外にあるもの。
// sandbox の PATH は stub だけで閉じているが、自己解決はここまで探しに行くので、ここにある実物には stub で蓋ができない。
// black-box テストは internal を import せず外から撃つので、一覧は candidates と重複させて持つ (一致は internal/deps のテストが確かめる)
var selfResolutionDirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin"}

// skipIfSelfResolutionReachesARealOne は、自己解決が届く置き場に name の実物があれば t を skip する。
// 「解決できない」を確かめるテストは、実物に解決されてしまうと検査が成り立たない
func skipIfSelfResolutionReachesARealOne(t *testing.T, name string) {
	t.Helper()
	for _, dir := range selfResolutionDirs {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Skipf("PATH の自己解決が届く %s に %s の実物がある", dir, name)
		}
	}
}

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
	for _, root := range []string{"../../cmd", "../../internal", "../../go.mod", "../../go.sum", "stub"} {
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
	t         *testing.T
	root      string
	home      string
	clone     string
	stateRoot string
	stubRoot  string
	binDir    string
	env       map[string]string
	rules     map[string][]stubwire.Rule
}

// newSandbox は XDG_STATE_HOME を tmp に向けた sandbox を作る。clone には既定の workflow 定義
// (defaultWorkflow) を置き、gh は open な issue が 0 件の応答を返す。
func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	if err := registerSourcesOnce(); err != nil {
		t.Fatal(err)
	}
	// macOS の t.TempDir() は /var/... を返すが、子 process の cwd は /private/var/... に解決される。実 path に揃える
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &sandbox{
		t:         t,
		root:      root,
		home:      filepath.Join(root, "home"),
		clone:     filepath.Join(root, "clone"),
		stateRoot: filepath.Join(root, "xdg-state", "claude-dispatcher"),
		stubRoot:  filepath.Join(root, "stub"),
		binDir:    filepath.Join(root, "stub-bin"),
		rules:     map[string][]stubwire.Rule{},
	}
	for _, dir := range []string{s.home, s.clone, s.stubRoot} {
		mustMkdir(t, dir)
	}
	s.installStubs()
	s.env = map[string]string{
		"HOME":           s.home,
		"XDG_STATE_HOME": filepath.Join(root, "xdg-state"),
		// PATH は stub だけで閉じる。/usr/bin 等を足すと、stub を消したテストで runner の実物に届いてしまう
		"PATH": s.binDir,
	}
	s.writeWorkflowWithCommands(defaultWorkflow)
	s.setIssues()
	return s
}

// command は sandbox の ~/.claude/commands の command の file。
func (s *sandbox) command(name string) string {
	return filepath.Join(s.home, ".claude", "commands", strings.ReplaceAll(name, ":", "/")+".md")
}

// installStubs は stub binary を名前ごとに PATH の置き場 (binDir) へ hard link し、stub が root を引く file を置く。
func (s *sandbox) installStubs() {
	s.t.Helper()
	mustMkdir(s.t, s.binDir)
	for _, name := range stubNames {
		dst := filepath.Join(s.binDir, name)
		// TMPDIR と t.TempDir() が別 device だと hard link を張れないので、そのときは複製する
		if err := os.Link(stubBin, dst); err != nil {
			copyFile(s.t, stubBin, dst, 0o755)
		}
	}
	mustWrite(s.t, filepath.Join(s.binDir, stubwire.RootFile), s.stubRoot+"\n")
}

// --- 置き場 (formats.md §1) ---

// defaultScopeKey は defaultIssueRepo の scope key
const defaultScopeKey = "github.com/acme/widgets"

// defaultStateDir は defaultScopeKey の state dir。名前は無害化した scope key に、scope key の sha256 の先頭 8 文字を足したもの
func (s *sandbox) defaultStateDir() string {
	sum := sha256.Sum256([]byte(defaultScopeKey))
	return filepath.Join(s.stateRoot, "github.com_acme_widgets-"+hex.EncodeToString(sum[:])[:8])
}

// --- workflow 定義 (formats.md §2) ---

func (s *sandbox) workflowFile() string { return filepath.Join(s.clone, "WORKFLOW.md") }

// writeWorkflowWithCommands は workflow 定義を書き、action が先頭に書く command を ~/.claude/commands に置いて事前検査
// (formats.md §2.9) に通す。
func (s *sandbox) writeWorkflowWithCommands(content string) {
	s.t.Helper()
	for _, m := range leadingCommand.FindAllStringSubmatch(content, -1) {
		mustWrite(s.t, s.command(m[1]), m[1]+" の command\n")
	}
	s.writeWorkflowWithoutCommands(content)
}

// leadingCommand は workflow 定義の action の先頭の `/名前` (素の書き方・引用符で囲む書き方・`|` と `>` の複数行の書き方)
var leadingCommand = regexp.MustCompile(`action:\s*(?:[|>][-+]?|["'])?\s*/([\w:-]+)`)

// writeWorkflowWithoutCommands は command を置かずに workflow 定義を書く (事前検査に落とすテスト用)。
func (s *sandbox) writeWorkflowWithoutCommands(content string) {
	s.t.Helper()
	mustWrite(s.t, s.workflowFile(), content)
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
	cmd.Env = s.environ(extra)
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

func (s *sandbox) environ(extra map[string]string) []string {
	var env []string
	for key, value := range s.env {
		env = append(env, key+"="+value)
	}
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

// dryRun は `loop --dry-run` を撃つ (formats.md §5)。
func (s *sandbox) dryRun(args ...string) runResult {
	s.t.Helper()
	return s.run(append([]string{"loop", "--dry-run"}, args...)...)
}

// --- 走らせたままの loop ---

// syncBuffer は loop の出力を、走っている間にも読めるように受ける。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// backgroundRun は走らせたままの binary。
type backgroundRun struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdout *syncBuffer
	stderr *syncBuffer
	done   chan struct{}
}

// loopStopGrace は後片付けの停止要求から loop の終了までを待つ上限。worker の 2 段の停止 (SIGTERM → 5s → SIGKILL) が収まる長さにする
const loopStopGrace = 15 * time.Second

// startLoop は clone を cwd にして `loop <args>` を起動し、待たずに返す。stdout は端末でない (pipe)。
// 後片付けで、走っていれば停止要求を 2 回送って止める。kill で止めると、loop が別の process group で起動した子 (gh の stub・
// hook・worker) が残り、TempDir の削除と並んで sandbox へ書いて削除を落とす。停止要求なら、loop は走っている tick の子を
// 待ち、2 回目で worker を止め、子が終わってから exit する。
func (s *sandbox) startLoop(args ...string) *backgroundRun {
	s.t.Helper()
	cmd := exec.Command(dispatcherBin, append([]string{"loop"}, args...)...)
	cmd.Dir = s.clone
	cmd.Env = s.environ(nil)
	p := &backgroundRun{t: s.t, cmd: cmd, stdout: &syncBuffer{}, stderr: &syncBuffer{}, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = p.stdout, p.stderr
	if err := cmd.Start(); err != nil {
		s.t.Fatalf("%v を起動できない: %v", cmd.Args, err)
	}
	// 終わり方は wait が ProcessState から読むので、Wait の error は見ない
	go func() { _ = cmd.Wait(); close(p.done) }()
	s.t.Cleanup(func() {
		if !p.running() {
			return
		}
		// 同じ signal を続けて送ると 1 つに畳まれうるので、別の signal で 2 回にする
		for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
			if err := cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
				s.t.Errorf("%v に %v を送れない: %v", cmd.Args, sig, err)
			}
		}
		select {
		case <-p.done:
		case <-time.After(loopStopGrace):
			s.t.Errorf("%v が停止要求から %v 経っても止まらない", cmd.Args, loopStopGrace)
			_ = cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

func (p *backgroundRun) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *backgroundRun) signal(sig syscall.Signal) {
	p.t.Helper()
	if err := p.cmd.Process.Signal(sig); err != nil {
		p.t.Fatalf("%v に %v を送れない: %v", p.cmd.Args, sig, err)
	}
}

// wait は process の終了を待ち、exit code と出力を返す。
func (p *backgroundRun) wait() runResult {
	p.t.Helper()
	select {
	case <-p.done:
	case <-time.After(runTimeout):
		p.t.Fatalf("%v が %s で終わらない\nstdout:\n%s\nstderr:\n%s", p.cmd.Args, runTimeout, p.stdout, p.stderr)
	}
	return runResult{exit: p.cmd.ProcessState.ExitCode(), stdout: p.stdout.String(), stderr: p.stderr.String()}
}

// waitForOutput は stdout に pattern に当たる行が出るまで待ち、その行を返す。
func (p *backgroundRun) waitForOutput(pattern *regexp.Regexp) string {
	p.t.Helper()
	deadline := time.Now().Add(runTimeout)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(p.stdout.String(), "\n") {
			if pattern.MatchString(line) {
				return line
			}
		}
		if !p.running() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	p.t.Fatalf("stdout に %s の行が出ない\nstdout:\n%s\nstderr:\n%s", pattern, p.stdout, p.stderr)
	return ""
}
