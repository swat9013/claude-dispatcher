package tick

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/launch"
	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/ticklog"
)

const (
	// logTimeLayout は log.jsonl の ts (RFC 3339 の UTC、マイクロ秒まで)
	logTimeLayout = "2006-01-02T15:04:05.000000Z"
	// stemLayout は tick の開始時刻から作る file 名の幹。同じ秒に 2 tick 走っても前の file を上書きしない
	stemLayout = "20060102T150405.000000Z"
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

// tickLine は tick 行 (formats.md §4.1)。tick は段階ごとに中身を積み、最後に 1 行で書き出す。
// どこで止まっても、それまでに確定した段階の key が行に残る。段階ごとの key は embed した pointer で持ち、
// nil ならその段階の key は行に載らない (観測に至らなかった tick に observed は無く、起動しなかった tick に orchestrator は無い)。
type tickLine struct {
	TS      string `json:"ts"`
	Project string `json:"project"`
	Cwd     string `json:"cwd"`
	Result  string `json:"result"`
	Error   string `json:"error,omitempty"`
	*observedKeys
	*launchedKeys
}

// observedKeys は観測に至った tick の key。
type observedKeys struct {
	Observed     ObservedCounts `json:"observed"`
	Candidates   int            `json:"candidates"`
	WIP          int            `json:"wip"`
	Instructions map[string]int `json:"instructions"`
	// InstructionFile は指示 0 件なら null
	InstructionFile *string `json:"instruction_file"`
}

func newObservedKeys(obs observation) *observedKeys {
	return &observedKeys{
		Observed:     obs.snapshot.Observed,
		Candidates:   len(obs.snapshot.Issues.Candidates),
		WIP:          obs.snapshot.Limits.WIPCount,
		Instructions: Counts(obs.instructions),
	}
}

// launchedKeys は claude を起動した tick の key。Spawned は起動済みの worker (0 件なら [])。
type launchedKeys struct {
	Orchestrator orchestratorRecord `json:"orchestrator"`
	Spawned      []spawned          `json:"spawned"`
}

type orchestratorRecord struct {
	ExitCode  int     `json:"exit_code"`
	Seconds   float64 `json:"seconds"`
	TimedOut  bool    `json:"timed_out"`
	SessionID string  `json:"session_id"`
}

func newOrchestratorRecord(run launch.OrchestratorRun) orchestratorRecord {
	// 秒は log を読む人の目安なので 0.1 秒まで
	return orchestratorRecord{ExitCode: run.ExitCode, Seconds: math.Round(run.Seconds*10) / 10, TimedOut: run.End == proc.TimedOut, SessionID: run.SessionID}
}

type spawned struct {
	Issue     int    `json:"issue"`
	Kind      Action `json:"kind"`
	PID       int    `json:"pid"`
	Log       string `json:"log"`
	SessionID string `json:"session_id"`
}

// orchestratorLine は orchestrator の採否を写す行 (formats.md §4.2)。ts は同じ tick の tick 行と同じ値。
type orchestratorLine struct {
	TS              string          `json:"ts"`
	Project         string          `json:"project"`
	Actor           string          `json:"actor"`
	InstructionFile string          `json:"instruction_file"`
	Decisions       json.RawMessage `json:"decisions"`
}

// dryRunLine は試運転の stdout の 1 行 (formats.md §7)。
type dryRunLine struct {
	TS           string         `json:"ts"`
	Project      string         `json:"project"`
	DryRun       bool           `json:"dry_run"`
	Result       string         `json:"result"`
	Observed     ObservedCounts `json:"observed"`
	Candidates   int            `json:"candidates"`
	WIP          int            `json:"wip"`
	Instructions map[string]int `json:"instructions"`
}

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

// marshalCompact は v を HTML escape せずに JSON にする (末尾の改行なし)。MarshalJSON の中から使う。
func marshalCompact(v any) ([]byte, error) {
	line, err := marshalLine(v)
	return bytes.TrimSuffix(line, []byte("\n")), err
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

// failureLine は単発の tick が失敗したときに stderr の末尾に出す 1 行 (formats.md §6)。
// loggedTS は log.jsonl に書けた行の ts。書けなかったら "" (`tick=-`)。
func failureLine(at time.Time, project, loggedTS string, result Result, err string) string {
	if loggedTS == "" {
		loggedTS = "-"
	}
	return fmt.Sprintf("%s [%s] tick=%s result=%s %s", at.UTC().Format(ticklog.TimeLayout), project, loggedTS, result.Name, foldLines(err))
}
