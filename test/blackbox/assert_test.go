package blackbox_test

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	tickStemPattern = regexp.MustCompile(`^\d{8}T\d{6}\.\d{6}Z$`)
	logTSPattern    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$`)
	uuidPattern     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// cron.log の行 (formats.md §6): <時刻 (UTC, 秒まで)> [<project>] tick=<ts か -> result=<result> <error>
	cronLogLinePattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z) \[([^\]]+)\] tick=(\S+) result=(\S+) (.+)$`)
)

// outcome は exit code と log.jsonl の result の組 (formats.md §3)。
type outcome struct {
	exit   int
	result string
}

var (
	outcomeOK          = outcome{0, "ok"}
	outcomeError       = outcome{1, "error"}
	outcomeConfigError = outcome{2, "config_error"}
	outcomeLocked      = outcome{3, "locked"}
	outcomeAuthError   = outcome{4, "auth_error"}
)

// assertOutcome は exit code と、ただ 1 行の tick 行の result が want であることを確かめ、その行を返す。
func (s *sandbox) assertOutcome(r runResult, want outcome) logLine {
	s.t.Helper()
	assertExit(s.t, r, want.exit)
	line := s.onlyTickLine()
	if line["result"] != want.result {
		s.t.Fatalf("result %v, want %q: %v", line["result"], want.result, line)
	}
	return line
}

func assertExit(t *testing.T, r runResult, want int) {
	t.Helper()
	if r.exit != want {
		t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", r.exit, want, r.stdout, r.stderr)
	}
}

// assertErrorNames は log 行の error が names をそれぞれ名指ししていることを確かめる。
func (s *sandbox) assertErrorNames(line logLine, names ...string) {
	s.t.Helper()
	s.assertNames(asString(s.t, line["error"]), names...)
}

// assertNames は msg が names をそれぞれ名指ししていることを確かめる。
//
// sandbox の path は test 名を含み ("…/TestConfigMissing…repo123/001/…") 部分文字列の検査を素通しにするので、
// "/" を含まない名前は sandbox の path を除いた本文に語として現れることを見る。"/" を含む名前 (path・owner/name) は
// 本文にそのまま含まれることを見る。
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

// assertCronLogLine は stderr の最終行が前置付きの 1 行で、project / tick / result が一致することを確かめ、error 部分を返す。
func assertCronLogLine(t *testing.T, stderr, project, tick, result string) string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	last := lines[len(lines)-1]
	m := cronLogLinePattern.FindStringSubmatch(last)
	if m == nil {
		t.Fatalf("stderr の最終行が `<時刻> [<project>] tick=<ts> result=<result> <error>` でない: %q", last)
	}
	if m[2] != project || m[3] != tick || m[4] != result {
		t.Fatalf("前置 = [%s] tick=%s result=%s, want [%s] tick=%s result=%s", m[2], m[3], m[4], project, tick, result)
	}
	return m[5]
}

// assertRejectedBeforeLaunch は決定ファイルが検査に落ち、1 件も起動せず error になったことを確かめる。
func (s *sandbox) assertRejectedBeforeLaunch(r runResult) {
	s.t.Helper()
	line := s.assertOutcome(r, outcomeError)
	if spawned, _ := line["spawned"].([]any); len(spawned) != 0 {
		s.t.Fatalf("検査に落ちた決定ファイルで起動した: %v", spawned)
	}
	if calls := s.callsMatching("claude", isWorkerCall); len(calls) != 0 {
		s.t.Fatalf("worker が起動された: %d 件", len(calls))
	}
}

// assertNoClaude は claude を 1 度も起動していないことを確かめる。
func (s *sandbox) assertNoClaude(why string) {
	s.t.Helper()
	if calls := s.calls("claude"); len(calls) != 0 {
		s.t.Fatalf("%s claude を起動した: %v", why, calls)
	}
}

// assertNoInstructionFile は指示ファイルを書いていないことを確かめる。
func (s *sandbox) assertNoInstructionFile(why string) {
	s.t.Helper()
	if files := s.instructionFiles(); len(files) != 0 {
		s.t.Fatalf("%s指示ファイルを書いた: %v", why, files)
	}
}

// --- 型の合わない値は panic せず t.Fatal にする (panic は suite 全体を止め、残りの red を隠す) ---

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

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
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
