package worker

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/swat9013/claude-dispatcher/internal/printable"
)

// summaryLimit は要約の文字数の上限
const summaryLimit = 80

// tailLimit は活動として数える最新の完結した行を探す、stream の file の末尾の大きさ。これより長い行は活動として数えない
const tailLimit = 64 << 10

// streamLine は stream-json の 1 行のうち、要約に使う項目。
type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// toolInput は tool の入力のうち、要点に使う項目。秘密が混ざりやすい項目 (Write の本文・MCP の tool の引数) は読まない。
type toolInput struct {
	Command     string `json:"command"`
	FilePath    string `json:"file_path"`
	Pattern     string `json:"pattern"`
	Skill       string `json:"skill"`
	Description string `json:"description"`
}

// gists は tool ごとの入力の要点の取り出し方 (formats.md §6)。ここに無い tool は要点を持たない
var gists = map[string]func(in toolInput, workspace string) string{
	"Bash": func(in toolInput, _ string) string {
		first, _, _ := strings.Cut(in.Command, "\n")
		return first
	},
	"Read":  relativeFilePath,
	"Edit":  relativeFilePath,
	"Write": relativeFilePath,
	"Grep":  pattern,
	"Glob":  pattern,
	"Skill": func(in toolInput, _ string) string { return in.Skill },
	"Agent": func(in toolInput, _ string) string { return in.Description },
}

func relativeFilePath(in toolInput, workspace string) string {
	return relativeTo(workspace, in.FilePath)
}

func pattern(in toolInput, _ string) string { return in.Pattern }

// gist は tool の入力の要点。要点を持たない tool と、入力を読めない tool は ""。入力は要点を持つ tool のときだけ読む (ほかの
// tool の入力は、同じ名前の項目が文字列でないことがある)。
func gist(tool string, input json.RawMessage, workspace string) string {
	pick, ok := gists[tool]
	if !ok {
		return ""
	}
	var in toolInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ""
	}
	return pick(in, workspace)
}

// relativeTo は workspace の中の path を workspace からの相対 path にする。外の path はそのまま返す (`../` を重ねた path は
// どこを指すかを読み取りにくい)。
func relativeTo(workspace, path string) string {
	rel, err := filepath.Rel(workspace, path)
	if err != nil || !filepath.IsAbs(path) || rel == ".." || strings.HasPrefix(rel, "../") {
		return path
	}
	return rel
}

// Summarize は stream-json の 1 行を要約する。workspace は worker の workspace の path (tool の入力の path を相対にする)。
// formats.md §6 で活動として数えない行なら ok が false。
func Summarize(line []byte, workspace string) (summary string, ok bool) {
	var l streamLine
	if err := json.Unmarshal(line, &l); err != nil || l.Type == "" {
		return "", false
	}
	switch l.Type {
	case "assistant":
		summary = "assistant"
		if content := l.Message.Content; len(content) > 0 {
			switch last := content[len(content)-1]; last.Type {
			case "text":
				summary, _, _ = strings.Cut(last.Text, "\n")
			case "thinking":
				summary = "thinking"
			case "tool_use":
				summary = strings.TrimSpace(last.Name + " " + gist(last.Name, last.Input, workspace))
			}
		}
	case "user":
		summary = "tool の結果"
	case "system":
		if l.Subtype != "init" {
			return "", false
		}
		summary = "system init"
	case "result":
		summary = "result " + l.Subtype
	default:
		return "", false
	}
	summary = printable.Line(summary)
	if utf8.RuneCountInString(summary) > summaryLimit {
		summary = string([]rune(summary)[:summaryLimit]) + "…"
	}
	return summary, true
}

// inTail は start で始まる行が、大きさ size の stream の file の末尾 tailLimit に収まるか。活動は末尾に収まる行からだけ
// 取る (formats.md §6)。
func inTail(start, size int64) bool { return start >= size-tailLimit }
