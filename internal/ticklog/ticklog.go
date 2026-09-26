// Package ticklog は log.jsonl (formats.md §4) の tick 行を読む。読むのは status と doctor で、書くのは tick だけ。
package ticklog

import (
	"bufio"
	"encoding/json"
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
// file が無ければ行も無い。
func Read(file string) (lines []Line, broken int, err error) {
	f, err := os.Open(file)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	// 指示の多い tick の行は既定の 64KiB を超えうる
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var raw struct {
			Line
			Actor *string `json:"actor"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			broken++
			continue
		}
		if raw.Actor != nil {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, raw.TS)
		if err != nil {
			broken++
			continue
		}
		raw.Line.At = at
		lines = append(lines, raw.Line)
	}
	return lines, broken, scanner.Err()
}

// Last は最後の tick 行を返す。無ければ false。
func Last(lines []Line) (Line, bool) {
	if len(lines) == 0 {
		return Line{}, false
	}
	return lines[len(lines)-1], true
}
