package status

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 大きさや並びが black-box テストで作りにくい transcript を、本物の読み取りで読む。

func writeTranscriptFile(t *testing.T, configDir, projectDir, sessionID, content string) string {
	t.Helper()
	dir := filepath.Join(configDir, "projects", projectDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

const assistantToolUse = `{"type":"assistant","timestamp":"2026-09-30T00:00:00Z","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"make"}}]}}`

func TestReadActivityWidensTheTailWhenTheLastLineIsLargerThanTheFirstWindow(t *testing.T) {
	dir := t.TempDir()
	huge := `{"type":"user","message":{"content":"` + strings.Repeat("a", 300<<10) + `"}}`
	writeTranscriptFile(t, dir, "p", "s", assistantToolUse+"\n"+huge+"\n")
	now := time.Date(2026, 9, 30, 0, 0, 12, 0, time.UTC)

	a, err := readActivity(dir, "s", now)

	if err != nil || a.Since != 12*time.Second || a.Doing != "Bash make" {
		t.Fatalf("activity = %+v, err = %v, want 12s Bash make (大きな行の前の行まで読み直す)", a, err)
	}
}

func TestReadActivityIgnoresAHalfWrittenLastLine(t *testing.T) {
	dir := t.TempDir()
	writeTranscriptFile(t, dir, "p", "s", assistantToolUse+"\n"+`{"type":"assistant","timestamp":"2026-09-30T00:00:05Z","mess`)

	a, err := readActivity(dir, "s", time.Date(2026, 9, 30, 0, 0, 12, 0, time.UTC))

	if err != nil || a.Since != 12*time.Second {
		t.Fatalf("activity = %+v, err = %v, want 書きかけの行を数えずに 12s", a, err)
	}
}

func TestReadActivityReadsTheMostRecentlyWrittenTranscriptWhenSeveralMatch(t *testing.T) {
	dir := t.TempDir()
	old := writeTranscriptFile(t, dir, "a-old", "s", `{"type":"assistant","timestamp":"2026-09-29T00:00:00Z","message":{"content":[{"type":"text","text":"古い"}]}}`+"\n")
	writeTranscriptFile(t, dir, "b-new", "s", assistantToolUse+"\n")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	a, err := readActivity(dir, "s", time.Date(2026, 9, 30, 0, 0, 12, 0, time.UTC))

	if err != nil || a.Doing != "Bash make" {
		t.Fatalf("activity = %+v, err = %v, want 最後に書かれた transcript の Bash make", a, err)
	}
}

func TestReadActivityReplacesControlCharactersBeforeTheyReachTheTerminal(t *testing.T) {
	dir := t.TempDir()
	writeTranscriptFile(t, dir, "p", "s", `{"type":"assistant","timestamp":"2026-09-30T00:00:00Z","message":{"content":[{"type":"text","text":"a\u001b[2Jb\tc"}]}}`+"\n")

	a, err := readActivity(dir, "s", time.Date(2026, 9, 30, 0, 0, 1, 0, time.UTC))

	if err != nil || a.Doing != "a [2Jb c" {
		t.Fatalf("activity = %+v, err = %v, want 制御文字を空白にした発話", a, err)
	}
}

func TestReadActivityFailsWithoutASessionID(t *testing.T) {
	_, err := readActivity(t.TempDir(), "", time.Now())

	if err == nil || !strings.Contains(err.Error(), "session id") {
		t.Fatalf("err = %v, want session id が無いことを言う失敗", err)
	}
}
