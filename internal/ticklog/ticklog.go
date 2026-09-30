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

// Decision は決定ファイルの decisions の 1 件 (formats.md §5.2)。orchestrator 行は決定ファイルの decisions をそのまま
// 写すので、書き手 (tick) と読み手 (status) が同じ型を使う。
type Decision struct {
	Issue  int    `json:"issue"`
	Action Action `json:"action"`
	Reason string `json:"reason"`
}

// Action は決定ファイルの採否の語彙 (formats.md §5.2)。起動する採否 (start / reenter) は spawn の kind と同じ綴り。
type Action string

const (
	ActionStart         Action = "start"
	ActionReenter       Action = "reenter"
	ActionSkip          Action = "skip"
	ActionReadyForHuman Action = "ready-for-human"
)

// Log は log.jsonl を読んだもの。
type Log struct {
	// Ticks は tick 行を file の順に並べたもの
	Ticks []Line
	// LastOrchestrator は file の最後の orchestrator 行。無いか、最後の orchestrator 行を読めなければ nil
	// (読めない行を飛ばして、それより古い判断を直近として見せない)
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
		line, bad := parse(raw)
		if bad {
			broken++
		}
		switch line := line.(type) {
		case Line:
			log.Ticks = append(log.Ticks, line)
		case *OrchestratorLine:
			log.LastOrchestrator = line
		case unreadableOrchestratorLine:
			log.LastOrchestrator = nil
		}
		if readErr == io.EOF {
			return log, broken, nil
		}
	}
}

// unreadableOrchestratorLine は actor の在る (orchestrator 行と分かる) のに key (ts / decisions) を読めない行。
// 最も新しい orchestrator 行がこれなら、それより古い判断を直近として出さない。
type unreadableOrchestratorLine struct{}

// parse は 1 行を読む。line は tick 行なら Line、orchestrator 行なら *OrchestratorLine か unreadableOrchestratorLine、
// 空行か JSON として読めない行なら nil。bad は読めなかった行。
func parse(raw []byte) (line any, bad bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, false
	}
	var head struct {
		Actor *string `json:"actor"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, true
	}
	if head.Actor != nil {
		var orchestrator OrchestratorLine
		if err := json.Unmarshal(raw, &orchestrator); err != nil {
			return unreadableOrchestratorLine{}, true
		}
		if _, err := ParseTS(orchestrator.TS); err != nil {
			return unreadableOrchestratorLine{}, true
		}
		return &orchestrator, false
	}
	var tick Line
	if err := json.Unmarshal(raw, &tick); err != nil {
		return nil, true
	}
	at, err := ParseTS(tick.TS)
	if err != nil {
		return nil, true
	}
	tick.At = at
	return tick, false
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

// TSLayout は log.jsonl の ts の綴り (RFC 3339 の UTC、マイクロ秒まで — formats.md §1)。tick.now の時刻も同じ綴り
const TSLayout = "2006-01-02T15:04:05.000000Z"

// FormatTS は時刻を ts の綴りにする。
func FormatTS(t time.Time) string { return t.UTC().Format(TSLayout) }

// ParseTS は ts の綴りの時刻を読む。
func ParseTS(ts string) (time.Time, error) { return time.Parse(time.RFC3339Nano, ts) }

// ShortTS は tick 行の ts を人が読む秒までの形にする (読めなければそのまま)。
func ShortTS(ts string) string {
	at, err := ParseTS(ts)
	if err != nil {
		return ts
	}
	return at.UTC().Format(TimeLayout)
}

// Summary は最終 tick の欄の中身 (`<秒までの ts> <result>`)。status の見出しと loop の画面が使う。
func Summary(ts, result string) string { return ShortTS(ts) + " " + result }
