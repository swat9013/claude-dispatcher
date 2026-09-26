package tick

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	// logTimeLayout は log.jsonl の ts (RFC 3339 の UTC、マイクロ秒まで)
	logTimeLayout = "2006-01-02T15:04:05.000000Z"
	// stemLayout は tick の開始時刻から作る file 名の幹。同じ秒に 2 tick 走っても前の file を上書きしない
	stemLayout = "20060102T150405.000000Z"
	// cronTimeLayout は cron.log の行の前置 (秒まで)
	cronTimeLayout = "2006-01-02T15:04:05Z"
)

// Result は tick の結果の種別と exit code (formats.md §3)。
type Result struct {
	Name string
	Exit int
}

var (
	ResultOK          = Result{"ok", 0}
	ResultError       = Result{"error", 1}
	ResultConfigError = Result{"config_error", 2}
	ResultLocked      = Result{"locked", 3}
	ResultAuthError   = Result{"auth_error", 4}
)

// record は tick 行 (formats.md §4.1)。tick は段階ごとに中身を積み、最後に 1 行で書き出す。
// どこで止まっても、それまでに確定した key (instruction_file / orchestrator / 起動済みの spawned) が行に残る。
type record map[string]any

// foldLines は複数行の error を ` / ` で 1 行に畳む。
func foldLines(s string) string {
	var parts []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, " / ")
}

// marshalLine は v を HTML escape せずに 1 行の JSON にする。
func marshalLine(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// appendLine は log.jsonl へ 1 行 append する。
func appendLine(file string, v any) error {
	line, err := marshalLine(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

// cronLogLine は失敗 tick が stderr (= crontab の行が append する cron.log) の末尾に出す 1 行 (formats.md §6)。
// loggedTS は log.jsonl に書けた行の ts。書けなかったら "" (`tick=-`)。
func cronLogLine(at time.Time, project, loggedTS string, result Result, err string) string {
	if loggedTS == "" {
		loggedTS = "-"
	}
	return fmt.Sprintf("%s [%s] tick=%s result=%s %s", at.UTC().Format(cronTimeLayout), project, loggedTS, result.Name, foldLines(err))
}
