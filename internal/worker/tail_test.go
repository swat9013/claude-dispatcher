package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 最新の完結した行の探し方。

func writeStream(t *testing.T, content string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "stream.log")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestLastLineSkipsTheUnfinishedLineAndLinesAlreadySummarized(t *testing.T) {
	content := "old\nnew\npartial"
	file := writeStream(t, content)
	size := int64(len(content))

	line, end, _ := lastLine(file, size, 0)
	if string(line) != "new" || end != int64(len("old\nnew\n")) {
		t.Fatalf("lastLine = %q, %d", line, end)
	}
	if line, _, _ := lastLine(file, size, end); line != nil {
		t.Fatalf("要約済みの行を返した: %q", line)
	}
}

func TestLastLineFindsALineThatFillsTheTail(t *testing.T) {
	long := strings.Repeat("x", tailLimit-1)
	content := "head\n" + long + "\n"
	file := writeStream(t, content)

	if line, _, _ := lastLine(file, int64(len(content)), 0); string(line) != long {
		t.Fatalf("読んだ範囲に収まる行を返さない (長さ %d)", len(line))
	}
	content = "head\nx" + long + "\n"
	file = writeStream(t, content)
	if line, _, _ := lastLine(file, int64(len(content)), 0); line != nil {
		t.Fatalf("読んだ範囲に収まらない行を返した (長さ %d)", len(line))
	}
}
