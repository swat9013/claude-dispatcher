// Package ticknow は tick が走っている間だけ state dir に置く `tick.now` (formats.md §1 / §10) を書き、読む。
//
// 書くのは tick の 1 回分だけで、読むのは status の現況だけ。表示のための痕跡で、tick は読まない (指示の導出にも lock の判定にも使わない)。
package ticknow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Stage は tick の段階。
type Stage string

const (
	Observing    Stage = "observe"
	Orchestrator Stage = "orchestrator"
	Spawning     Stage = "spawn"
)

// State は `tick.now` の中身。
type State struct {
	// PID は tick を走らせている process (単発の tick か loop)
	PID int `json:"pid"`
	// TS は tick の開始時刻。log.jsonl の tick 行の ts と同じ値
	TS    string `json:"ts"`
	Stage Stage  `json:"stage"`
	// Orchestrator は orchestrator を起動した後だけ埋まる
	Orchestrator *OrchestratorRun `json:"orchestrator,omitempty"`
}

// OrchestratorRun は起動した orchestrator。
type OrchestratorRun struct {
	Started   string `json:"started"`
	SessionID string `json:"session_id"`
}

// Write は state を file へ書く。同じ dir の一時 file に書いてから rename する — 並行して読む status に書きかけを見せない。
func Write(file string, state State) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // rename できたら消す先は無い
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

// Remove は file を消す。無ければ何もしない。
func Remove(file string) error {
	if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Read は file を読む。無ければ nil を返す。
func Read(file string) (*State, error) {
	raw, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("%s の形が読めない: %w", file, err)
	}
	return &state, nil
}

// ParseTime は TS / Started の綴り (RFC 3339 の UTC) を読む。
func ParseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }
