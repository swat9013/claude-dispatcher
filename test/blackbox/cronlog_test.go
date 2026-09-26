package blackbox_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// cron.log に出す行 (formats.md §6)。失敗 tick は stderr の末尾に、log.jsonl の行を指す前置付きの 1 行を置く。

var cronLogLinePattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z) \[([^\]]+)\] tick=(\S+) result=(\S+) (.+)$`)

// assertCronLogLine は stderr の最終行が前置付きの 1 行で、project / tick / result が一致することを確かめる。
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

func TestFailedTickEndsStderrWithALinePointingToItsLogLine(t *testing.T) {
	s := newSandbox(t)
	s.setLabels(defaultReadyLabel)

	r := s.tick()

	assertExit(t, r, 2)
	line := s.onlyTickLine()
	msg := assertCronLogLine(t, r.stderr, s.project, asString(t, line["ts"]), "config_error")
	if msg != line["error"] {
		t.Fatalf("cron.log の error %q と log.jsonl の error %q が違う", msg, line["error"])
	}
}

func TestSuccessfulTickWritesNothingToStderr(t *testing.T) {
	s := newSandbox(t)
	startScenario(t, s)

	r := s.tick()

	assertExit(t, r, 0)
	if r.stderr != "" {
		t.Fatalf("正常な tick が stderr に書いた: %q", r.stderr)
	}
}

func TestTickWithoutStateDirPointsToNoLogLine(t *testing.T) {
	s := newSandbox(t)
	if err := os.RemoveAll(s.stateDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.configFile()); err != nil {
		t.Fatal(err)
	}

	r := s.tick()

	assertExit(t, r, 2)
	assertCronLogLine(t, r.stderr, s.project, "-", "config_error")
}

func TestCronLogLineFoldsAMultilineErrorIntoOneLine(t *testing.T) {
	s := newSandbox(t)
	s.respond("gh", stubRule{ArgsPrefix: []string{"api", "graphql"}, Exit: 1, Stderr: "first problem\nsecond problem\n"})

	r := s.tick()

	assertExit(t, r, 1)
	msg := assertCronLogLine(t, r.stderr, s.project, asString(t, s.onlyTickLine()["ts"]), "error")
	if !strings.Contains(msg, "first problem / second problem") {
		t.Fatalf("error が 1 行に畳まれていない: %q", msg)
	}
}

func TestTickStoppedMidwayPointsToTheLogLineItLeft(t *testing.T) {
	s := newSandbox(t)
	startScenario(t, s)
	mustWrite(t, filepath.Join(s.stateDir(), "workers"), "not a directory")

	r := s.tick()

	assertExit(t, r, 1)
	assertCronLogLine(t, r.stderr, s.project, asString(t, s.onlyTickLine()["ts"]), "error")
}
