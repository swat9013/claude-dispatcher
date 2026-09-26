package blackbox_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// 観測: stub の呼び出し記録・log.jsonl・指示ファイル・file の指紋。

const (
	// detach 起動された worker の記録が届くのを待つ上限と、その間の見直し間隔
	waitDeadline = 10 * time.Second
	waitInterval = 20 * time.Millisecond
)

// --- stub の呼び出し ---

type stubCall stubwire.Call

func (c stubCall) args() []string { return c.Argv[1:] }

// flagValue は `--name value` の value を返す。無ければ "" と false。
func (c stubCall) flagValue(name string) (string, bool) {
	args := c.args()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1], true
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
	dir := stubwire.CallsDir(s.stubRoot, name)
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

func (s *sandbox) observationCalls() []stubCall {
	return s.callsMatching("gh", func(c stubCall) bool {
		return c.hasPrefix("issue", "list") || c.hasPrefix("api", "graphql")
	})
}

func (s *sandbox) verificationCalls() []stubCall {
	return s.callsMatching("gh", func(c stubCall) bool {
		return c.hasPrefix("repo", "view") || c.hasPrefix("label", "list")
	})
}

func waitFor(t *testing.T, done func() bool, failure string) {
	t.Helper()
	deadline := time.Now().Add(waitDeadline)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal(failure)
		}
		time.Sleep(waitInterval)
	}
}

// waitWorkerCalls は detach 起動された worker の stub 呼び出しが n 件記録されるまで待つ (tick は worker を待たずに終わる)。
func (s *sandbox) waitWorkerCalls(n int) []stubCall {
	s.t.Helper()
	waitFor(s.t, func() bool { return len(s.callsMatching("claude", isWorkerCall)) >= n },
		fmt.Sprintf("worker の起動が %d 件記録されない", n))
	return s.callsMatching("claude", isWorkerCall)
}

// settleDetachedWorkers は、tick が起動したかもしれない worker の呼び出し記録が届くまでの猶予を置く。
// detach 起動は tick と同期する点を持たないので、「起動されていない」ことは猶予の後の不在でしか確かめられない。
// 起動の有無の主な観測点は log の spawned で、これは補助。
func settleDetachedWorkers() { time.Sleep(300 * time.Millisecond) }

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
			deadline := time.Now().Add(waitDeadline)
			for syscall.Kill(int(pid), 0) == nil {
				if time.Now().After(deadline) {
					s.t.Logf("worker (pid %d) が %s 経っても終わらない。TempDir の削除が落ちうる", int(pid), waitDeadline)
					break
				}
				time.Sleep(waitInterval)
			}
		}
	}
}

// --- log.jsonl ---

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

// --- 指示ファイル ---

func (s *sandbox) instructionFiles() []string {
	s.t.Helper()
	files, err := filepath.Glob(stubwire.InstructionsGlob(s.stateDir()))
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

func instructionKinds(t *testing.T, doc map[string]any) []string {
	t.Helper()
	var kinds []string
	for _, i := range instructionsOf(t, doc) {
		kinds = append(kinds, asString(t, i["kind"]))
	}
	return kinds
}

// tickStem は指示ファイル等の path から tick の stem (YYYYMMDDTHHMMSS.ffffffZ) を取り出す。
func tickStem(file string) string {
	return strings.TrimSuffix(filepath.Base(file), ".json")
}

// --- file ---

// fileFingerprints は dir 配下の path → (size, mtime, dir か) を返す。書き込みが無かったことの比較に使う。
func fileFingerprints(t *testing.T, dir string) map[string]string {
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
