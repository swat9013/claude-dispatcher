package worker

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/swat9013/claude-dispatcher/internal/printable"
)

// Activity は worker の stream の最新の完結した行の要約 (formats.md §6 の活動)。
type Activity struct {
	At      time.Time
	Summary string
}

// summaryLimit は要約の文字数の上限
const summaryLimit = 80

// tailLimit は最新の完結した行を探すために stream の file の末尾から読む大きさ。これより長い行は活動として数えない
const tailLimit = 64 << 10

// streamLine は stream-json の 1 行のうち、要約に使う項目。
type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Name string `json:"name"`
		} `json:"content"`
	} `json:"message"`
}

// Summarize は stream-json の 1 行を要約する。JSON として読めないか type が無ければ ok が false。
func Summarize(line []byte) (summary string, ok bool) {
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
			case "tool_use":
				summary = "tool " + last.Name
			}
		}
	case "user":
		summary = "tool の結果"
	case "system", "result":
		summary = l.Type + " " + l.Subtype
	default:
		summary = l.Type
	}
	summary = printable.Line(summary)
	if utf8.RuneCountInString(summary) > summaryLimit {
		summary = string([]rune(summary)[:summaryLimit]) + "…"
	}
	return summary, true
}

// lastLine は file の size までのうち、after より後で終わる最新の完結した行 (改行で終わる行) と、その行の終わりの
// offset (改行の次) を返す。見つからなければ line が nil。
func lastLine(file string, size, after int64) (line []byte, end int64, err error) {
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
	begin := bytes.LastIndexByte(buf[:last], '\n') + 1
	if begin == 0 && start > 0 {
		// 行の頭が読んだ範囲より前にある
		return nil, 0, nil
	}
	return buf[begin:last], start + int64(last) + 1, nil
}
