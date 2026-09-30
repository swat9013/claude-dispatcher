// Package status は loop の今の状態を状態 file (state dir の status.json) に書き出し、読み、人が読む形に描く
// (system.md §9、formats.md §7)。状態 file は表示のためだけの痕跡で、loop は読み戻さない。
package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/printable"
)

// FileName は state dir の中の状態 file の名前
const FileName = "status.json"

// Snapshot は状態 file の中身。
type Snapshot struct {
	Scope     string    `json:"scope"`
	Workflow  string    `json:"workflow"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Stopping は停止要求を受けて、worker の終了を待っているか
	Stopping bool `json:"stopping"`
	// NextTickAt は次の tick の予定。停止要求の後は nil
	NextTickAt *time.Time  `json:"next_tick_at"`
	LastTick   *Tick       `json:"last_tick"`
	Workers    []Worker    `json:"workers"`
	Abandoned  []Abandoned `json:"abandoned"`
	Ambiguous  []Ambiguous `json:"ambiguous"`
	// Blocked は事前検査 (formats.md §2.9) に落ちて起動しない trigger
	Blocked []Blocked `json:"blocked"`
}

// Tick は直近の tick。
type Tick struct {
	At         time.Time  `json:"at"`
	Result     TickResult `json:"result"`
	Candidates int        `json:"candidates"`
	Error      string     `json:"error,omitempty"`
}

// TickResult は tick の結果。
type TickResult string

const (
	TickOK    TickResult = "ok"
	TickError TickResult = "error"
)

// Phase は claim の段階。
type Phase string

const (
	Running      Phase = "running"
	Stopping     Phase = "stopping"
	Verifying    Phase = "verifying"
	WaitingRetry Phase = "waiting_retry"
)

// Worker は claim 1 つ。
type Worker struct {
	Target    string `json:"target"`
	Trigger   string `json:"trigger"`
	Attempt   int    `json:"attempt"`
	SessionID string `json:"session_id"`
	Phase     Phase  `json:"phase"`
	// StartedAt は今の attempt の worker を起動した時刻。起動の前は nil
	StartedAt *time.Time `json:"started_at"`
	// RetryAt は再起動待ちの backoff が明ける時刻
	RetryAt  *time.Time `json:"retry_at,omitempty"`
	Activity *Activity  `json:"activity"`
}

// Activity は worker の stream の最新の完結した行の要約。
type Activity struct {
	At      time.Time `json:"at"`
	Summary string    `json:"summary"`
}

// Abandoned は打ち切った作業対象。
type Abandoned struct {
	Target  string `json:"target"`
	Trigger string `json:"trigger"`
}

// Blocked は事前検査に落ちた trigger と、その理由。
type Blocked struct {
	Trigger string `json:"trigger"`
	Error   string `json:"error"`
}

// Ambiguous は曖昧な CL の組。
type Ambiguous struct {
	Head    string   `json:"head"`
	Targets []string `json:"targets"`
}

// Write は状態 file を書き換える。読み手に書きかけを見せないよう、同じ dir の一時 file から rename する。
func Write(dir string, s Snapshot) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	file := filepath.Join(dir, FileName)
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("状態 file を書けない: %w", err)
	}
	if err := os.Rename(tmp, file); err != nil {
		return fmt.Errorf("状態 file を書けない: %w", err)
	}
	return nil
}

// Read は状態 file を読む。無ければ ok が false。
func Read(dir string) (s Snapshot, ok bool, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("状態 file を読めない: %w", err)
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return Snapshot{}, false, fmt.Errorf("状態 file を読めない (%s): %w", filepath.Join(dir, FileName), err)
	}
	return s, true, nil
}

// phaseLabels は worker の行の段階の欄
var phaseLabels = map[Phase]string{Running: "走っている", Stopping: "止めている", Verifying: "確かめ待ち", WaitingRetry: "再起動待ち"}

// Liveness は loop が生きているか。
type Liveness bool

const (
	LoopGone  Liveness = false
	LoopAlive Liveness = true
)

// heading は見出しの先頭の語。
func heading(loop Liveness, stopping bool) string {
	switch {
	case loop == LoopGone:
		return "loop なし"
	case stopping:
		return "loop 停止待ち"
	}
	return "loop 稼働中"
}

// Unrecorded は状態 file が無いときの見出し (formats.md §7.2)。loop が生きていれば、lock を取ってから最初に書き出すまでの間。
func Unrecorded(scope string, loop Liveness) string {
	return fmt.Sprintf("%s · scope %s · 記録なし\n", heading(loop, false), scope)
}

// Render は状態を人が読む形に描く (formats.md §7.2)。時刻は loc の HH:MM:SS で出す。
func Render(s Snapshot, loop Liveness, now time.Time, loc *time.Location) string {
	clock := func(t time.Time) string { return t.In(loc).Format("15:04:05") }
	alive := loop == LoopAlive
	head := heading(loop, s.Stopping) + " · scope " + s.Scope
	if alive && s.NextTickAt != nil {
		head += " · 次の tick " + clock(*s.NextTickAt)
	}
	if t := s.LastTick; t != nil {
		if t.Result == TickError {
			head += fmt.Sprintf(" · 直近の tick %s error: %s", clock(t.At), printable.Line(t.Error))
		} else {
			head += fmt.Sprintf(" · 直近の tick %s %s (候補 %d)", clock(t.At), t.Result, t.Candidates)
		}
	}
	lines := []string{head}
	if !alive {
		// worker と打ち切りは loop の memory と一緒に消えている
		return head + "\n"
	}
	for _, w := range s.Workers {
		when := ""
		switch {
		case w.Phase == WaitingRetry && w.RetryAt != nil:
			when = clock(*w.RetryAt) + " に再起動"
		case (w.Phase == Running || w.Phase == Stopping) && w.StartedAt != nil:
			when = now.Sub(*w.StartedAt).Truncate(time.Second).String()
		}
		activity := ""
		if w.Activity != nil {
			activity = w.Activity.Summary
		}
		lines = append(lines, strings.Join([]string{w.Target, w.Trigger, fmt.Sprintf("attempt %d", w.Attempt), phaseLabels[w.Phase], when, activity}, "\t"))
	}
	for _, a := range s.Abandoned {
		lines = append(lines, fmt.Sprintf("打ち切り %s (%s): label を外して trigger から外し、1 周期待ってから付け直すと解ける", a.Target, a.Trigger))
	}
	for _, a := range s.Ambiguous {
		lines = append(lines, fmt.Sprintf("曖昧な CL %s: %s", a.Head, strings.Join(a.Targets, ", ")))
	}
	for _, b := range s.Blocked {
		lines = append(lines, fmt.Sprintf("起動しない trigger %s: %s", b.Trigger, b.Error))
	}
	return strings.Join(lines, "\n") + "\n"
}
