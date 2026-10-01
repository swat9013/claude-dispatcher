package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 最新の完結した行の探し方。

// latest は content を書いた stream の file から、after より後で終わる最新の完結した行を読む。
func latest(t *testing.T, content string, after int64) (line []byte, end int64) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "stream.log")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	line, end, err := lastLine(file, int64(len(content)), after)
	if err != nil {
		t.Fatal(err)
	}
	return line, end
}

func TestLastLineSkipsTheUnfinishedLine(t *testing.T) {
	line, _ := latest(t, "old\nnew\npartial", 0)

	if string(line) != "new" {
		t.Fatalf("lastLine = %q, want new", line)
	}
}

func TestLastLineDoesNotReturnALineAlreadySummarized(t *testing.T) {
	_, end := latest(t, "old\nnew\n", 0)

	if line, _ := latest(t, "old\nnew\n", end); line != nil {
		t.Fatalf("要約済みの行を返した: %q", line)
	}
}

func TestLastLineFindsALineThatJustFitsTheTail(t *testing.T) {
	long := strings.Repeat("x", tailLimit-1)

	if line, _ := latest(t, "head\n"+long+"\n", 0); string(line) != long {
		t.Fatalf("読んだ範囲に収まる行を返さない (長さ %d)", len(line))
	}
}

func TestLastLineSkipsALineLongerThanTheTail(t *testing.T) {
	if line, _ := latest(t, "head\n"+strings.Repeat("x", tailLimit)+"\n", 0); line != nil {
		t.Fatalf("読んだ範囲に収まらない行を返した (長さ %d)", len(line))
	}
}
