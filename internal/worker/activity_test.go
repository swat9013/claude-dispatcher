package worker_test

import (
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/worker"
)

// stream の行の要約 (formats.md §6 の活動)。

func TestSummarizeStreamLines(t *testing.T) {
	cases := []struct {
		name, line, want string
	}{
		{"text は最初の行", `{"type":"assistant","message":{"content":[{"type":"text","text":"一行目\n二行目"}]}}`, "一行目"},
		{"最後の content を見る", `{"type":"assistant","message":{"content":[{"type":"text","text":"調べる"},{"type":"tool_use","name":"Bash","input":{"command":"rm -rf x"}}]}}`, "tool Bash"},
		{"tool の結果", `{"type":"user","message":{"content":[{"type":"tool_result","content":"秘密"}]}}`, "tool の結果"},
		{"system", `{"type":"system","subtype":"init"}`, "system init"},
		{"result", `{"type":"result","subtype":"success"}`, "result success"},
		{"それ以外の type", `{"type":"stream_event"}`, "stream_event"},
		{"制御文字は空白", `{"type":"assistant","message":{"content":[{"type":"text","text":"a\tb\u001b[2Jc"}]}}`, "a b [2Jc"},
		{"80 文字で切る", `{"type":"assistant","message":{"content":[{"type":"text","text":"` + strings.Repeat("あ", 81) + `"}]}}`, strings.Repeat("あ", 80) + "…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := worker.Summarize([]byte(c.line))
			if !ok || got != c.want {
				t.Fatalf("Summarize = %q, %v; want %q", got, ok, c.want)
			}
		})
	}
}

func TestSummarizeSkipsLinesThatAreNotStreamJSON(t *testing.T) {
	for _, line := range []string{`{"type":"assist`, `plain text`, `{"message":{}}`} {
		if got, ok := worker.Summarize([]byte(line)); ok {
			t.Errorf("Summarize(%q) = %q, want 数えない", line, got)
		}
	}
}
