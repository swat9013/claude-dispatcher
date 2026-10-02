package worker

import (
	"os"
	"path/filepath"
	"testing"
)

// attempt の最後の result の行の要約の読み方 (formats.md §4)。

// summarizeStream は content を書いた worker log から、offset より後の行の最後の result を要約する。
func summarizeStream(t *testing.T, content string, offset int64) (*ResultSummary, error) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "stream.log")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return summarizeLastResult(file, offset)
}

func TestResultLineWithoutAnItemLeavesOnlyThatItemOut(t *testing.T) {
	summary, err := summarizeStream(t, `{"type":"result","is_error":true,"permission_denials":[{}]}`+"\n", 0)

	if err != nil || summary == nil || summary.IsError == nil || !*summary.IsError || summary.NumTurns != nil ||
		summary.PermissionDenialCount == nil || *summary.PermissionDenialCount != 1 {
		t.Fatalf("要約 = %+v, %v, want is_error true・num_turns 無し・permission_denial_count 1", summary, err)
	}
}

func TestResultLineThatIsStillBeingWrittenIsNotRead(t *testing.T) {
	summary, err := summarizeStream(t, `{"type":"result","num_turns":3}`+"\n"+`{"type":"result","num_turns":5}`, 0)

	if err != nil || summary == nil || summary.NumTurns == nil || *summary.NumTurns != 3 {
		t.Fatalf("要約 = %+v, %v, want 改行で終わらない 2 つ目を読まず num_turns 3", summary, err)
	}
}

func TestResultLineWithAnItemOfAnotherTypeLeavesThatItemOutAndReportsIt(t *testing.T) {
	summary, err := summarizeStream(t, `{"type":"result","num_turns":3}`+"\n"+`{"type":"result","is_error":true,"num_turns":"five"}`+"\n", 0)

	if err == nil || summary == nil || summary.IsError == nil || !*summary.IsError || summary.NumTurns != nil {
		t.Fatalf("要約 = %+v, %v, want 最後の result の is_error true だけを載せ、num_turns を読めないことを返す", summary, err)
	}
}

func TestResultLinesBeforeTheOffsetAreNotRead(t *testing.T) {
	previous := `{"type":"result","num_turns":3}` + "\n"

	summary, err := summarizeStream(t, previous+`{"type":"assistant"}`+"\n", int64(len(previous)))

	if err != nil || summary != nil {
		t.Fatalf("要約 = %+v, %v, want 前の attempt の result を読まない", summary, err)
	}
}

func TestWorkerLogThatCannotBeOpenedIsReported(t *testing.T) {
	summary, err := summarizeLastResult(filepath.Join(t.TempDir(), "missing.log"), 0)

	if err == nil || summary != nil {
		t.Fatalf("要約 = %+v, %v, want 開けないことを返す", summary, err)
	}
}
