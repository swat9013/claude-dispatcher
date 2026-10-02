package worker

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/swat9013/claude-dispatcher/internal/printable"
)

// summaryLimit は要約の文字数の上限
const summaryLimit = 80

// tailLimit は活動として数える最新の完結した行を探すために stream の file の末尾から読む大きさ。これより長い行は活動として
// 数えない
const tailLimit = 64 << 10

// streamLine は stream-json の 1 行のうち、要約に使う項目。
type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content []struct {
			Type  string    `json:"type"`
			Text  string    `json:"text"`
			Name  string    `json:"name"`
			Input toolInput `json:"input"`
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

// gist は tool の入力の要点 (formats.md §6)。要点を持たない tool は ""。
func (in toolInput) gist(tool, workspace string) string {
	switch tool {
	case "Bash":
		first, _, _ := strings.Cut(in.Command, "\n")
		return first
	case "Read", "Edit", "Write":
		return relativeTo(workspace, in.FilePath)
	case "Grep", "Glob":
		return in.Pattern
	case "Skill":
		return in.Skill
	case "Agent":
		return in.Description
	}
	return ""
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
// 活動として数えない行 (JSON として読めない・type が無い・init 以外の system) なら ok が false。
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
				summary = strings.TrimSpace(last.Name + " " + last.Input.gist(last.Name, workspace))
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
		summary = l.Type
	}
	summary = printable.Line(summary)
	if utf8.RuneCountInString(summary) > summaryLimit {
		summary = string([]rune(summary)[:summaryLimit]) + "…"
	}
	return summary, true
}

// completeLines は file の size までのうち、after より後で終わる完結した行 (改行で終わる行) を古い順に返す。end は最後の行の
// 終わりの offset (改行の次)。末尾から tailLimit を超えて遡らないので、頭が読んだ範囲より前にある行は返さない。
func completeLines(file string, size, after int64) (lines [][]byte, end int64, err error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	// 行の頭の前の改行まで読めるよう、1 byte 余分に読む
	start := max(size-tailLimit-1, 0)
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, 0, err
	}
	last := bytes.LastIndexByte(buf, '\n')
	if last < 0 || start+int64(last)+1 <= after {
		return nil, 0, nil
	}
	// 読んだ範囲の頭は、前の行の途中かもしれない (file の頭から読んだときだけ行の頭)
	begin := 0
	if start > 0 {
		begin = bytes.IndexByte(buf, '\n') + 1
	}
	// after より前で終わった行は要約済みか、前の attempt の行
	begin = max(begin, int(after-start))
	end = start + int64(last) + 1
	if begin > last {
		return nil, end, nil
	}
	return bytes.Split(buf[begin:last], []byte{'\n'}), end, nil
}
