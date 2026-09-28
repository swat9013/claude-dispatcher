// Package ticklog は log.jsonl (formats.md §4) の tick 行を読む。読むのは status と doctor で、書くのは tick だけ。
package ticklog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"time"
)

// Line は tick 行のうち読み手が使う key。At は ts を読んだ時刻。
type Line struct {
	TS      string    `json:"ts"`
	Result  string    `json:"result"`
	Spawned []Spawned `json:"spawned"`
	At      time.Time `json:"-"`
}

// Spawned は tick 行の spawned の 1 件。
type Spawned struct {
	Issue     int    `json:"issue"`
	Kind      string `json:"kind"`
	PID       int    `json:"pid"`
	SessionID string `json:"session_id"`
}

// Read は file の tick 行 (orchestrator 行は除く) を順に返す。読めない行 (途中で切れた行など) は飛ばし、その件数を返す。
// file が無ければ行も無い。読み出しが途中で失敗したら行は返さない (途中までの行を全体として見せない)。
func Read(file string) (lines []Line, broken int, err error) {
	f, err := os.Open(file)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	// 行の長さに上限を置かない (bufio.Scanner は上限を超えた行で読むのを止め、それより後の tick が見えなくなる)
	reader := bufio.NewReader(f)
	for {
		raw, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			return nil, 0, readErr
		}
		if line, ok, bad := parse(raw); bad {
			broken++
		} else if ok {
			lines = append(lines, line)
		}
		if readErr == io.EOF {
			return lines, broken, nil
		}
	}
}

// parse は 1 行を tick 行として読む。ok は tick 行だった、bad は読めなかった (空行と orchestrator 行はどちらでもない)。
func parse(raw []byte) (line Line, ok, bad bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return Line{}, false, false
	}
	var doc struct {
		Line
		Actor *string `json:"actor"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Line{}, false, true
	}
	if doc.Actor != nil {
		return Line{}, false, false
	}
	at, err := time.Parse(time.RFC3339Nano, doc.TS)
	if err != nil {
		return Line{}, false, true
	}
	doc.Line.At = at
	return doc.Line, true, false
}

// Last は最後の tick 行を返す。無ければ false。
func Last(lines []Line) (Line, bool) {
	if len(lines) == 0 {
		return Line{}, false
	}
	return lines[len(lines)-1], true
}

// TimeLayout は人が読む時刻の綴り (UTC、秒まで)。画面・失敗行・終了行が使う (formats.md §1)
const TimeLayout = "2006-01-02T15:04:05Z"

// ShortTS は tick 行の ts を人が読む秒までの形にする (読めなければそのまま)。
func ShortTS(ts string) string {
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return at.UTC().Format(TimeLayout)
}

// Summary は最終 tick の欄の中身 (`<秒までの ts> <result>`)。status の見出しと loop の画面が使う。
func Summary(ts, result string) string { return ShortTS(ts) + " " + result }
