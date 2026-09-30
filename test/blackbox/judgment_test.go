package blackbox_test

import (
	"strings"
	"testing"
)

// status の判断の行 (formats.md §10): 直近の orchestrator 行の decisions のうち、skip と ready-for-human だけを見出しの直後に出す。

// orchestratorLine は ts の tick が残した orchestrator 行 (formats.md §4.2)。
func (s *sandbox) orchestratorLine(ts string, decisions ...map[string]any) map[string]any {
	return map[string]any{"ts": ts, "project": s.project, "actor": "orchestrator", "instruction_file": "/x", "decisions": decisions}
}

// tickLine は orchestrator を起動しなかった tick 行。
func (s *sandbox) tickLine(ts string) map[string]any {
	return map[string]any{"ts": ts, "project": s.project, "cwd": s.clone, "result": "ok", "instructions": map[string]int{}, "instruction_file": nil}
}

// writeLogLines は log.jsonl を lines の順の行で置き換える。
func (s *sandbox) writeLogLines(lines ...map[string]any) {
	s.t.Helper()
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(mustJSON(s.t, line) + "\n")
	}
	mustWrite(s.t, s.logFile(), b.String())
}

func judged(issue int, action, reason string) map[string]any {
	return map[string]any{"issue": issue, "action": action, "reason": reason}
}

// linesAfterHeading は stdout の見出しの行の直後から n 行を返す。
func linesAfterHeading(t *testing.T, stdout string, n int) []string {
	t.Helper()
	lines := strings.Split(stdout, "\n")
	if len(lines) < 1+n {
		t.Fatalf("見出しの後に %d 行無い:\n%s", n, stdout)
	}
	return lines[1 : 1+n]
}

func TestStatusShowsOnlyTheSkipAndReadyForHumanDecisionsRightAfterTheHeading(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.writeLogLines(s.tickLine("2026-09-26T03:00:00.123456Z"), s.orchestratorLine("2026-09-26T03:00:00.123456Z",
		judged(50, "start", "受け入れ条件が揃っている"),
		judged(51, "skip", "仕様に受け入れ条件が無い"),
		judged(52, "ready-for-human", "wip と ready-for-human が両方付いている"),
	))

	r := s.statusPS()

	want := []string{"判断 2026-09-26T03:00:00Z", "  #51 skip: 仕様に受け入れ条件が無い", "  #52 ready-for-human: wip と ready-for-human が両方付いている"}
	if got := linesAfterHeading(t, r.stdout, 3); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("見出しの直後 = %q, want %q\n%s", got, want, r.stdout)
	}
	if strings.Contains(r.stdout, "#50 start") {
		t.Fatalf("start の判断を出した:\n%s", r.stdout)
	}
}

func TestStatusKeepsShowingTheDecisionsOfAnEarlierOrchestratorLineAfterTicksWithoutOne(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.writeLogLines(
		s.tickLine("2026-09-26T03:00:00.000000Z"),
		s.orchestratorLine("2026-09-26T03:00:00.000000Z", judged(51, "skip", "仕様に受け入れ条件が無い")),
		s.tickLine("2026-09-26T03:05:00.000000Z"),
	)

	r := s.statusPS()

	want := []string{"判断 2026-09-26T03:00:00Z", "  #51 skip: 仕様に受け入れ条件が無い"}
	if got := linesAfterHeading(t, r.stdout, 2); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("見出しの直後 = %q, want %q\n%s", got, want, r.stdout)
	}
}

func TestStatusShowsOnlyTheDecisionsOfTheNewestOrchestratorLine(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.writeLogLines(
		s.tickLine("2026-09-26T03:00:00.000000Z"),
		s.orchestratorLine("2026-09-26T03:00:00.000000Z", judged(51, "skip", "古い判断")),
		s.tickLine("2026-09-26T03:05:00.000000Z"),
		s.orchestratorLine("2026-09-26T03:05:00.000000Z", judged(52, "skip", "新しい判断")),
	)

	r := s.statusPS()

	if strings.Contains(r.stdout, "古い判断") || !strings.Contains(r.stdout, "判断 2026-09-26T03:05:00Z\n  #52 skip: 新しい判断\n") {
		t.Fatalf("最も新しい orchestrator 行の判断だけを出していない:\n%s", r.stdout)
	}
}

func TestStatusShowsNoDecisionLinesWhenNoneIsSkipOrReadyForHuman(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.writeLogLines(s.tickLine("2026-09-26T03:00:00.000000Z"), s.orchestratorLine("2026-09-26T03:00:00.000000Z",
		judged(50, "start", "受け入れ条件が揃っている"),
		judged(49, "reenter", "CL が conflict している"),
	))

	r := s.statusPS()

	if strings.Contains(r.stdout, "判断") || strings.Contains(r.stdout, "#50") || strings.Contains(r.stdout, "#49") {
		t.Fatalf("判断の行を出した:\n%s", r.stdout)
	}
}
