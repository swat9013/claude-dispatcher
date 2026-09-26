package blackbox_test

import (
	"os"
	"strings"
	"testing"
)

// cron.log に出す行 (formats.md §6)。失敗 tick は stderr の末尾に、log.jsonl の行を指す前置付きの 1 行を置く。

func TestFailedTickEndsStderrWithALinePointingToItsLogLine(t *testing.T) {
	s := newSandbox(t)
	s.setLabels(defaultReadyLabel)

	r := s.tick()

	line := s.assertOutcome(r, outcomeConfigError)
	msg := assertCronLogLine(t, r.stderr, s.project, asString(t, line["ts"]), "config_error")
	if msg != line["error"] {
		t.Fatalf("cron.log の error %q と log.jsonl の error %q が違う", msg, line["error"])
	}
}

func TestSuccessfulTickWritesNothingToStderr(t *testing.T) {
	s := newSandbox(t)
	startScenario(s)

	r := s.tick()

	assertExit(t, r, 0)
	if r.stderr != "" {
		t.Fatalf("正常な tick が stderr に書いた: %q", r.stderr)
	}
}

func TestTickWithoutAStateDirPointsToNoLogLine(t *testing.T) {
	s := newSandbox(t)
	if err := os.RemoveAll(s.stateDir()); err != nil {
		t.Fatal(err)
	}

	r := s.tick()

	if r.exit == 0 {
		t.Fatal("state dir の無い tick が成功した")
	}
	lines := strings.Split(strings.TrimRight(r.stderr, "\n"), "\n")
	if m := cronLogLinePattern.FindStringSubmatch(lines[len(lines)-1]); m == nil || m[3] != "-" {
		t.Fatalf("log.jsonl を書けない tick の最終行が tick=- でない: %q", r.stderr)
	}
}

func TestCronLogLineFoldsAMultilineErrorIntoOneLine(t *testing.T) {
	s := newSandbox(t)
	s.ghFails([]string{"api", "graphql"}, 1, "first problem\nsecond problem\n")

	r := s.tick()

	line := s.assertOutcome(r, outcomeError)
	msg := assertCronLogLine(t, r.stderr, s.project, asString(t, line["ts"]), "error")
	if !strings.Contains(msg, "first problem / second problem") {
		t.Fatalf("error が 1 行に畳まれていない: %q", msg)
	}
}

func TestTickStoppedMidwayPointsToTheLogLineItLeft(t *testing.T) {
	s := newSandbox(t)
	startScenario(s)
	s.blockWorkerLogs()

	r := s.tick()

	line := s.assertOutcome(r, outcomeError)
	assertCronLogLine(t, r.stderr, s.project, asString(t, line["ts"]), "error")
}
