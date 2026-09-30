package status

import (
	"slices"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/ticklog"
)

func reportWithSkip(reason string) Report {
	return Report{LastOrchestrator: &ticklog.OrchestratorLine{TS: "2026-09-26T03:00:00Z", Decisions: []ticklog.Decision{{Issue: 51, Action: "skip", Reason: reason}}}}
}

func TestJudgmentLinesKeepTheIssueAndActionOnATerminalNarrowerThanThem(t *testing.T) {
	got := reportWithSkip("仕様に受け入れ条件が無い").JudgmentLines(FitTerminal(5))

	if want := []string{"判断 2026-09-26T03:00:00Z", "  #51 skip: "}; !slices.Equal(got, want) {
		t.Fatalf("JudgmentLines = %q, want %q", got, want)
	}
}

func TestJudgmentLinesCutTheReasonToFitTheTerminalWidth(t *testing.T) {
	got := reportWithSkip("仕様に受け入れ条件が無い").JudgmentLines(FitTerminal(20))

	if want := []string{"判断 2026-09-26T03:00:00Z", "  #51 skip: 仕様に受"}; !slices.Equal(got, want) {
		t.Fatalf("JudgmentLines = %q, want %q", got, want)
	}
}

func TestJudgmentLinesKeepTheWholeReasonOffATerminal(t *testing.T) {
	got := reportWithSkip("仕様に受け入れ条件が無い").JudgmentLines(FitPlain())

	if want := []string{"判断 2026-09-26T03:00:00Z", "  #51 skip: 仕様に受け入れ条件が無い"}; !slices.Equal(got, want) {
		t.Fatalf("JudgmentLines = %q, want %q", got, want)
	}
}

func TestJudgmentLinesFoldAMultiLineReasonIntoOneLine(t *testing.T) {
	got := reportWithSkip("仕様が\n無い").JudgmentLines(FitPlain())

	if want := []string{"判断 2026-09-26T03:00:00Z", "  #51 skip: 仕様が 無い"}; !slices.Equal(got, want) {
		t.Fatalf("JudgmentLines = %q, want %q", got, want)
	}
}

func TestJudgmentLinesKeepTheSpacesOfTheReason(t *testing.T) {
	got := reportWithSkip("仕様が\n\n無い  (2 回目)").JudgmentLines(FitPlain())

	if want := []string{"判断 2026-09-26T03:00:00Z", "  #51 skip: 仕様が  無い  (2 回目)"}; !slices.Equal(got, want) {
		t.Fatalf("JudgmentLines = %q, want %q", got, want)
	}
}

func TestJudgmentLinesDoNotPassTerminalControlCharactersThrough(t *testing.T) {
	got := reportWithSkip("仕様が\x1b[2J無い").JudgmentLines(FitPlain())

	if want := []string{"判断 2026-09-26T03:00:00Z", "  #51 skip: 仕様が [2J無い"}; !slices.Equal(got, want) {
		t.Fatalf("JudgmentLines = %q, want %q", got, want)
	}
}

func TestJudgmentLinesCountAnEmojiAsTwoColumnsWhenCutting(t *testing.T) {
	got := reportWithSkip("✅ 済み").JudgmentLines(FitTerminal(16))

	if want := []string{"判断 2026-09-26T03:00:00Z", "  #51 skip: ✅ "}; !slices.Equal(got, want) {
		t.Fatalf("JudgmentLines = %q, want %q", got, want)
	}
}
