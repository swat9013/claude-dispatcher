package worker_test

import (
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/worker"
)

// stream の行の要約 (formats.md §6 の活動)。

const workspace = "/work/issue-42"

func TestSummarizeStreamLines(t *testing.T) {
	cases := []struct {
		name, line, want string
	}{
		{"text は最初の行", `{"type":"assistant","message":{"content":[{"type":"text","text":"一行目\n二行目"}]}}`, "一行目"},
		{"thinking", `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"考える","signature":"x"}]}}`, "thinking"},
		{"最後の content を見る", `{"type":"assistant","message":{"content":[{"type":"text","text":"調べる"},{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`, "Bash go test ./..."},
		{"それ以外の content", `{"type":"assistant","message":{"content":[{"type":"server_tool_use","name":"advisor"}]}}`, "assistant"},
		{"tool の結果", `{"type":"user","message":{"content":[{"type":"tool_result","content":"秘密"}]}}`, "tool の結果"},
		{"system init", `{"type":"system","subtype":"init"}`, "system init"},
		{"result", `{"type":"result","subtype":"success"}`, "result success"},
		{"それ以外の type", `{"type":"stream_event"}`, "stream_event"},
		{"制御文字は空白", `{"type":"assistant","message":{"content":[{"type":"text","text":"a\tb\u001b[2Jc"}]}}`, "a b [2Jc"},
		{"80 文字で切る", `{"type":"assistant","message":{"content":[{"type":"text","text":"` + strings.Repeat("あ", 81) + `"}]}}`, strings.Repeat("あ", 80) + "…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := worker.Summarize([]byte(c.line), workspace)
			if !ok || got != c.want {
				t.Fatalf("Summarize = %q, %v; want %q", got, ok, c.want)
			}
		})
	}
}

func TestSummarizeToolUseShowsTheGistOfItsInput(t *testing.T) {
	cases := []struct {
		name, tool, input, want string
	}{
		{"Bash は command の最初の行", "Bash", `{"command":"git diff --stat\ngit status","description":"差分"}`, "Bash git diff --stat"},
		{"Read は workspace からの相対 path", "Read", `{"file_path":"/work/issue-42/internal/status/status.go"}`, "Read internal/status/status.go"},
		{"Edit は workspace の外なら絶対 path", "Edit", `{"file_path":"/home/u/.claude/CLAUDE.md","old_string":"a","new_string":"b"}`, "Edit /home/u/.claude/CLAUDE.md"},
		{"Write は本文を載せない", "Write", `{"file_path":"/work/issue-42/secret.env","content":"TOKEN=abc"}`, "Write secret.env"},
		{"Grep は pattern", "Grep", `{"pattern":"func Render","path":"internal"}`, "Grep func Render"},
		{"Glob は pattern", "Glob", `{"pattern":"**/*.go"}`, "Glob **/*.go"},
		{"Skill は skill の名前", "Skill", `{"skill":"swat-skills:review-stage","args":"issue=#67"}`, "Skill swat-skills:review-stage"},
		{"Agent は description", "Agent", `{"description":"差分をレビューする","prompt":"長い指示","subagent_type":"reviewer"}`, "Agent 差分をレビューする"},
		{"MCP の tool は名前だけ", "mcp__slack__send_message", `{"channel":"C1","text":"秘密"}`, "mcp__slack__send_message"},
		{"要点が無ければ名前だけ", "Bash", `{}`, "Bash"},
		{"要点を持たない tool の入力は読まない", "mcp__x__search", `{"description":{"nested":true},"pattern":["a"]}`, "mcp__x__search"},
		{"入力を読めなければ名前だけ", "Agent", `{"description":42}`, "Agent"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"` + c.tool + `","input":` + c.input + `}]}}`

			got, ok := worker.Summarize([]byte(line), workspace)

			if !ok || got != c.want {
				t.Fatalf("Summarize = %q, %v; want %q", got, ok, c.want)
			}
		})
	}
}

func TestSummarizeSkipsLinesThatAreNotActivity(t *testing.T) {
	for _, line := range []string{
		`{"type":"assist`, `plain text`, `{"message":{}}`,
		`{"type":"system","subtype":"thinking_tokens"}`, `{"type":"system","subtype":"task_progress"}`,
	} {
		if got, ok := worker.Summarize([]byte(line), workspace); ok {
			t.Errorf("Summarize(%q) = %q, want 数えない", line, got)
		}
	}
}
