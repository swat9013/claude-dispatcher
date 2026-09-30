package status

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Claude Code の transcript (`<設定 dir>/projects/*/<session_id>.jsonl`) から、走っているセッションの最新の活動を読む
// (formats.md §10 の ACTIVITY)。transcript の中身の形は Claude Code の内部の仕様で、それへの依存はこの file に閉じる。

// transcriptTails は transcript の末尾から読む量。transcript は大きくなるので全体は読まない。末尾の行が大きく (画像の
// tool_result 等) 最初の量に時刻を持つ行が入らなければ、次の量で読み直す
var transcriptTails = []int64{256 << 10, 4 << 20}

// Activity は最新の活動。
type Activity struct {
	// Since は最後の event からの経過
	Since time.Duration
	// Doing は最後の tool 呼び出し (`Bash go test ./...`) か、その後の発話の頭。末尾に見つからなければ空
	Doing string
}

// ClaudeConfigDir は Claude Code の設定 dir。CLAUDE_CONFIG_DIR があればそれ、無ければ `<home>/.claude`。
func ClaudeConfigDir(getenv func(string) string, home string) string {
	if dir := getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(home, ".claude")
}

// readActivity は sessionID の transcript の末尾から最新の活動を読む。cwd から dir 名を作る Claude Code の規則は
// 文書化されていないので再現せず、projects の下を glob で探す。
func readActivity(configDir, sessionID string, now time.Time) (*Activity, error) {
	switch {
	case configDir == "":
		return nil, errors.New("transcript を探す設定 dir (Claude Code) が決まっていない")
	case sessionID == "":
		return nil, errors.New("起動記録に session id が無い")
	}
	file, err := newestTranscript(configDir, sessionID)
	if err != nil {
		return nil, err
	}
	for _, tail := range transcriptTails {
		raw, whole, err := tailLines(file, tail)
		if err != nil {
			return nil, err
		}
		lines := decodeLines(raw)
		if last, ok := lastEventTime(lines); ok {
			return &Activity{Since: now.Sub(last), Doing: lastDoing(lines)}, nil
		}
		if whole {
			break
		}
	}
	return nil, fmt.Errorf("%s の末尾に時刻を持つ行が無い (形が読めない)", file)
}

// newestTranscript は projects の下で sessionID の transcript を探す。複数あれば最後に書かれたもの。
func newestTranscript(configDir, sessionID string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(configDir, "projects", "*", sessionID+".jsonl"))
	if err != nil {
		return "", err
	}
	newest, newestAt := "", time.Time{}
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && (newest == "" || info.ModTime().After(newestAt)) {
			newest, newestAt = m, info.ModTime()
		}
	}
	if newest == "" {
		return "", fmt.Errorf("%s の下に %s.jsonl が無い", filepath.Join(configDir, "projects"), sessionID)
	}
	return newest, nil
}

// tailLines は file の末尾 tail 分のうち、完全な行を返す (先頭の切れた行は捨てる)。whole は file 全体を読んだか。
func tailLines(file string, tail int64) (lines [][]byte, whole bool, err error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	offset := max(info.Size()-tail, 0)
	buf := make([]byte, info.Size()-offset)
	if _, err := f.ReadAt(buf, offset); err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	if offset > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
		buf = buf[:i] // 書きかけの末尾の行は数えない
	} else {
		buf = nil
	}
	return bytes.Split(buf, []byte("\n")), offset == 0, nil
}

// transcriptLine は transcript の 1 行のうち読む分。
type transcriptLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentItem struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// decodeLines は読めた行だけを返す (形の読めない行は飛ばす — 読めた行から時刻を拾えなければ readActivity が失敗にする)。
func decodeLines(raw [][]byte) []transcriptLine {
	lines := make([]transcriptLine, 0, len(raw))
	for _, r := range raw {
		var line transcriptLine
		if json.Unmarshal(r, &line) == nil {
			lines = append(lines, line)
		}
	}
	return lines
}

// lastEventTime は後ろから見て最初に時刻を読めた行の時刻 (メタデータの行は時刻を持たない)。
func lastEventTime(lines []transcriptLine) (time.Time, bool) {
	for _, line := range slices.Backward(lines) {
		if at, err := time.Parse(time.RFC3339Nano, line.Timestamp); err == nil {
			return at, true
		}
	}
	return time.Time{}, false
}

// lastDoing は後ろから見て最初の assistant の発話か tool 呼び出しを 1 行にする。発話が tool 呼び出しより後なら発話の頭。
func lastDoing(lines []transcriptLine) string {
	for _, line := range slices.Backward(lines) {
		if line.Type != "assistant" {
			continue
		}
		var items []contentItem
		if json.Unmarshal(line.Message.Content, &items) != nil {
			continue
		}
		for _, item := range slices.Backward(items) {
			switch item.Type {
			case "text":
				if head := firstLine(item.Text); head != "" {
					return head
				}
			case "tool_use":
				return describeToolUse(item)
			}
		}
	}
	return ""
}

// describeToolUse は tool 名と主な引数 (Bash は command の 1 行目、Edit / Write / Read は file の path、それ以外は無し)。
func describeToolUse(item contentItem) string {
	var input struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
	}
	_ = json.Unmarshal(item.Input, &input) // 引数を読めなければ tool 名だけにする
	arg := ""
	switch item.Name {
	case "Bash":
		arg = firstLine(input.Command)
	case "Edit", "Write", "Read":
		arg = input.FilePath
	}
	return strings.TrimSpace(item.Name + " " + arg)
}

// firstLine は s の 1 行目。端末へ出すので、制御文字 (ESC・tab・CR 等) は空白に置き換える — transcript の中身は model が書く。
func firstLine(s string) string {
	head, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, head))
}
