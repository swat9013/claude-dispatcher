package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 活動として数える最新の完結した行の探し方。

// streamFile は content を書いた stream の file を開く。
func streamFile(t *testing.T, content string) *os.File {
	t.Helper()
	file := filepath.Join(t.TempDir(), "stream.log")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// summarizeAfter は content の stream から、after より後で終わる行の活動を読む。
func summarizeAfter(t *testing.T, content string, after int64) (summary string, ok bool, w *streamWatch) {
	t.Helper()
	w = &streamWatch{file: streamFile(t, content), size: int64(len(content)), summarized: after, workspace: "/work"}
	summary, ok = w.activity(time.Time{})
	return summary, ok, w
}

func text(s string) string {
	return `{"type":"assistant","message":{"content":[{"type":"text","text":"` + s + `"}]}}` + "\n"
}

const thinkingTokens = `{"type":"system","subtype":"thinking_tokens"}` + "\n"

func TestActivitySkipsTheUnfinishedLine(t *testing.T) {
	summary, _, _ := summarizeAfter(t, text("old")+text("new")+`{"type":"assist`, 0)

	if summary != "new" {
		t.Fatalf("活動 = %q, want new", summary)
	}
}

func TestActivityLooksBackPastLinesThatAreNotCounted(t *testing.T) {
	// 1s の間に tool_use の後へ thinking_tokens の行が続いても、tool_use を活動にする
	edit := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/work/a.go"}}]}}` + "\n"

	summary, ok, _ := summarizeAfter(t, text("old")+edit+thinkingTokens+thinkingTokens, 0)

	if !ok || summary != "Edit a.go" {
		t.Fatalf("活動 = %q, %v; want Edit a.go", summary, ok)
	}
}

func TestActivityDoesNotReturnALineAlreadySummarized(t *testing.T) {
	content := text("old") + text("new")
	_, _, w := summarizeAfter(t, content, 0)

	if summary, ok := w.activity(time.Time{}); ok {
		t.Fatalf("要約済みの行を返した: %q", summary)
	}
}

func TestActivityStaysWhenOnlyUncountedLinesFollow(t *testing.T) {
	old := text("old")

	if summary, ok, _ := summarizeAfter(t, old+thinkingTokens, int64(len(old))); ok {
		t.Fatalf("数えない行だけなのに活動を返した: %q", summary)
	}
}

func TestActivityFindsALineThatJustFitsTheTail(t *testing.T) {
	long := text(strings.Repeat("x", tailLimit-len(text(""))))

	if summary, ok, _ := summarizeAfter(t, "head\n"+long, 0); !ok || !strings.HasPrefix(summary, "xxx") {
		t.Fatalf("読んだ範囲に収まる行を数えない (長さ %d)", len(long))
	}
}

func TestActivitySkipsALineLongerThanTheTail(t *testing.T) {
	long := text(strings.Repeat("x", tailLimit))

	if summary, ok, _ := summarizeAfter(t, text("head")+long, 0); ok {
		t.Fatalf("読んだ範囲に収まらない行を数えた: %q", summary)
	}
}
