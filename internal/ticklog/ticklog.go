// Package ticklog は log.jsonl (formats.md §4) の tick 行と orchestrator 行を読む。読むのは status と doctor で、書くのは tick だけ。
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

// OrchestratorLine は orchestrator 行 (formats.md §4.2) のうち読み手が使う key。
type OrchestratorLine struct {
	TS        string     `json:"ts"`
	Decisions []Decision `json:"decisions"`
}

// Decision は orchestrator 行の decisions の 1 件。Action は決定ファイルの語彙 (formats.md §5.2)。
type Decision struct {
	Issue  int    `json:"issue"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// Log は log.jsonl を読んだもの。
type Log struct {
	// Ticks は tick 行を file の順に並べたもの
	Ticks []Line
	// LastOrchestrator は file の最後の orchestrator 行。無ければ nil
	LastOrchestrator *OrchestratorLine
}

// Read は file の tick 行と最後の orchestrator 行を返す。読めない行 (途中で切れた行など) は飛ばし、その件数を返す。
// file が無ければ行も無い。読み出しが途中で失敗したら行は返さない (途中までの行を全体として見せない)。
func Read(file string) (log Log, broken int, err error) {
	f, err := os.Open(file)
	if os.IsNotExist(err) {
		return Log{}, 0, nil
	}
	if err != nil {
		return Log{}, 0, err
	}
	defer f.Close()
	// 行の長さに上限を置かない (bufio.Scanner は上限を超えた行で読むのを止め、それより後の tick が見えなくなる)
	reader := bufio.NewReader(f)
	for {
		raw, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			return Log{}, 0, readErr
		}
		tickLine, orchestratorLine, bad := parse(raw)
		switch {
		case bad:
			broken++
		case tickLine != nil:
			log.Ticks = append(log.Ticks, *tickLine)
		case orchestratorLine != nil:
			log.LastOrchestrator = orchestratorLine
		}
		if readErr == io.EOF {
			return log, broken, nil
		}
	}
}

// parse は 1 行を読む。tick 行なら tickLine を、orchestrator 行なら orchestratorLine を返し、読めなければ bad
// (空行はどれでもない)。
func parse(raw []byte) (tickLine *Line, orchestratorLine *OrchestratorLine, bad bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, nil, false
	}
	var doc struct {
		Line
		Actor     *string    `json:"actor"`
		Decisions []Decision `json:"decisions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, true
	}
	if doc.Actor != nil {
		return nil, &OrchestratorLine{TS: doc.TS, Decisions: doc.Decisions}, false
	}
	at, err := time.Parse(time.RFC3339Nano, doc.TS)
	if err != nil {
		return nil, nil, true
	}
	doc.At = at
	return &doc.Line, nil, false
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
