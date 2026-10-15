package worker

import (
	"strings"
	"testing"
	"time"
)

// attempt の最後の result の行の要約の読み方 (formats.md §4)。

// summarizeStream は前の attempt が previous を書いた worker log に、今の attempt が content を追記したときの要約。
func summarizeStream(t *testing.T, previous, content string) (*ResultSummary, error) {
	t.Helper()
	w := watchFrom(t, previous, time.Now(), limits{})
	write(t, w, content)
	return w.lastResult()
}

func TestResultLineWithoutAnItemLeavesOnlyThatItemOut(t *testing.T) {
	summary, err := summarizeStream(t, "", `{"type":"result","is_error":true,"permission_denials":[{}]}`+"\n")

	if err != nil || summary == nil || summary.IsError == nil || !*summary.IsError || summary.NumTurns != nil ||
		summary.PermissionDenialCount == nil || *summary.PermissionDenialCount != 1 {
		t.Fatalf("要約 = %+v, %v, want is_error true・num_turns 無し・permission_denial_count 1", summary, err)
	}
}

func TestResultLineThatIsStillBeingWrittenIsNotRead(t *testing.T) {
	summary, err := summarizeStream(t, "", `{"type":"result","num_turns":3}`+"\n"+`{"type":"result","num_turns":5}`)

	if err != nil || summary == nil || summary.NumTurns == nil || *summary.NumTurns != 3 {
		t.Fatalf("要約 = %+v, %v, want 改行で終わらない 2 つ目を読まず num_turns 3", summary, err)
	}
}

func TestResultLineWithAnItemOfAnotherTypeLeavesThatItemOutAndReportsIt(t *testing.T) {
	summary, err := summarizeStream(t, "", `{"type":"result","is_error":true,"num_turns":"five"}`+"\n")

	if err == nil || summary == nil || summary.IsError == nil || !*summary.IsError || summary.NumTurns != nil {
		t.Fatalf("要約 = %+v, %v, want is_error true だけを載せ、num_turns を読めないことを返す", summary, err)
	}
}

func TestResultLinesBeforeTheOffsetAreNotRead(t *testing.T) {
	summary, err := summarizeStream(t, `{"type":"result","num_turns":3}`+"\n", `{"type":"assistant"}`+"\n")

	if err != nil || summary != nil {
		t.Fatalf("要約 = %+v, %v, want 前の attempt の result を読まない", summary, err)
	}
}

func TestResultLongerThanTheLineHeadIsReadWhole(t *testing.T) {
	long := `{"type":"result","num_turns":4,"result":"` + strings.Repeat("x", lineHeadLimit) + `"}` + "\n"

	summary, err := summarizeStream(t, "", long)

	if err != nil || summary == nil || summary.NumTurns == nil || *summary.NumTurns != 4 {
		t.Fatalf("要約 = %+v, %v, want 監視が持つ行の頭より長い result も行全体を読んで num_turns 4", summary, err)
	}
}

func TestResultWrittenAfterTheStreamShrankIsSummarized(t *testing.T) {
	w := watchFrom(t, strings.Repeat("x", 1024)+"\n", time.Now(), limits{})
	write(t, w, `{"type":"result","num_turns":1}`+"\n")
	shrinkTo(t, w, `{"type":"result","num_turns":2}`+"\n")

	summary, err := w.lastResult()

	if err != nil || summary == nil || summary.NumTurns == nil || *summary.NumTurns != 2 {
		t.Fatalf("要約 = %+v, %v, want attempt の途中で縮んだら頭から読み、縮んだ後の result で num_turns 2", summary, err)
	}
}

func TestWorkerLogThatCannotBeStattedIsReported(t *testing.T) {
	w := watchFrom(t, "", time.Now(), limits{})
	write(t, w, `{"type":"result","num_turns":1}`+"\n")
	failStat(w)

	summary, err := w.lastResult()

	if err == nil || summary != nil {
		t.Fatalf("要約 = %+v, %v, want 大きさを確かめられないことを返す", summary, err)
	}
}

func TestWorkerLogThatCannotBeReadIsReported(t *testing.T) {
	w := watchFrom(t, "", time.Now(), limits{})
	write(t, w, `{"type":"result","num_turns":1}`+"\n")
	failReads(w)

	summary, err := w.lastResult()

	if err == nil || summary != nil {
		t.Fatalf("要約 = %+v, %v, want 読み出せないことを返す", summary, err)
	}
}
