// Package status は loop の今の状態を状態 file (state dir の status.json) に書き出し、読み、人が読む形に描く
// (system.md §9、formats.md §7)。状態 file は表示のためだけの痕跡で、loop は読み戻さない。
package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FileName は state dir の中の状態 file の名前
const FileName = "status.json"

// Snapshot は状態 file の中身。
type Snapshot struct {
	Scope     string    `json:"scope"`
	Workflow  string    `json:"workflow"`
	StartedAt time.Time `json:"started_at"`
	// Version と Commit は状態 file を書いた loop の binary の版と commit (`--version` と同じ値)。版を足す前の版が書いた
	// 状態 file を読むと空
	Version   string    `json:"version"`
	Commit    string    `json:"commit"`
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

// phaseTones は workers の表の段階の欄の色
var phaseTones = map[Phase]Tone{Running: Cyan, WaitingRetry: Yellow, Verifying: Gray}

// Liveness は loop が生きているか。
type Liveness bool

const (
	LoopGone  Liveness = false
	LoopAlive Liveness = true
)

// state は見出しの 1 行目の loop の状態。
func state(loop Liveness, stopping bool) Span {
	switch {
	case loop == LoopGone:
		return Span{Gray, "○ loop なし"}
	case stopping:
		return Span{BoldYellow, "● loop 停止待ち"}
	}
	return Span{BoldGreen, "● loop 稼働中"}
}

// Unrecorded は状態 file が無いときの見出し (formats.md §7.2)。loop が生きていれば、lock を取ってから最初に書き出すまでの間。
func Unrecorded(scope string, loop Liveness, d Display) string {
	return d.Line(state(loop, false), Span{Plain, "  "}, Span{Bold, scope}) + "\n" + d.Line(Span{Gray, "記録なし"}) + "\n"
}

// Render は状態を人が読む形に描く (formats.md §7.2): 見出しと、loop が生きていればセクション。時刻は loc の HH:MM:SS で出す。
func Render(s Snapshot, loop Liveness, now time.Time, loc *time.Location, d Display) string {
	if loop == LoopGone {
		// worker と打ち切りは loop の memory と一緒に消えている
		return Heading(s, loop, loc, d)
	}
	return Heading(s, loop, loc, d) + "\n" + Sections(s, now, loc, d)
}

func clock(t time.Time, loc *time.Location) string { return t.In(loc).Format("15:04:05") }

// Heading は見出し: loop の状態と scope key と版の行と、次の tick と直近の tick の行 (出す欄が無ければ出さない)。
// 版は loop が生きていて、状態 file が版を持つときだけ出す (止まった loop の状態 file の版は、今の loop のものでない)。
func Heading(s Snapshot, loop Liveness, loc *time.Location, d Display) string {
	first := []Span{state(loop, s.Stopping), {Plain, "  "}, {Bold, s.Scope}}
	if loop != LoopGone && s.Version != "" {
		first = append(first, Span{Gray, " · "}, Span{Plain, s.Version})
	}
	head := d.Line(first...) + "\n"
	var ticks [][]Span
	switch {
	case loop == LoopGone:
	case s.Stopping:
		ticks = append(ticks, []Span{{Gray, "次の tick なし"}})
	case s.NextTickAt != nil:
		ticks = append(ticks, []Span{{Gray, "次の tick "}, {Plain, clock(*s.NextTickAt, loc)}})
	}
	if t := s.LastTick; t != nil {
		last := []Span{{Gray, "直近の tick "}, {Plain, clock(t.At, loc) + " "}}
		if t.Result == TickError {
			last = append(last, Span{Red, "error: " + t.Error})
		} else {
			last = append(last, Span{Green, string(t.Result)}, Span{Gray, " · 候補 "}, Span{Plain, strconv.Itoa(t.Candidates)})
		}
		ticks = append(ticks, last)
	}
	if len(ticks) == 0 {
		return head
	}
	var line []Span
	for i, spans := range ticks {
		if i > 0 {
			line = append(line, Span{Gray, " · "})
		}
		line = append(line, spans...)
	}
	return head + d.Line(line...) + "\n"
}

// Sections は見出しの後に並べるセクション: workers と、人の手当てを待つものがあれば要対処。セクションは空行で区切る。
func Sections(s Snapshot, now time.Time, loc *time.Location, d Display) string {
	sections := d.Section(Span{Bold, fmt.Sprintf("workers %d", len(s.Workers))}) + "\n" + workers(s.Workers, now, loc, d)
	if attention := needsAttention(s); len(attention) > 0 {
		sections += "\n" + d.Section(Span{BoldYellow, fmt.Sprintf("要対処 %d", len(attention))}) + "\n"
		for _, line := range attention {
			sections += d.Line(line...) + "\n"
		}
	}
	return sections
}

// workers は claim の表。列は空白で揃え、行の終わりの空の欄は描かない。claim が無ければ列見出しも出さない。
func workers(ws []Worker, now time.Time, loc *time.Location, d Display) string {
	if len(ws) == 0 {
		return ""
	}
	rows := [][]Span{{{Gray, "作業対象"}, {Gray, "trigger"}, {Gray, "attempt"}, {Gray, "段階"}, {Gray, "経過"}, {Gray, "活動"}}}
	for _, w := range ws {
		when := ""
		switch {
		case w.Phase == WaitingRetry && w.RetryAt != nil:
			when = clock(*w.RetryAt, loc) + " に再起動"
		case (w.Phase == Running || w.Phase == Stopping) && w.StartedAt != nil:
			when = elapsed(now.Sub(*w.StartedAt))
		}
		activity := ""
		if w.Activity != nil {
			activity = w.Activity.Summary
		}
		rows = append(rows, []Span{{Plain, w.Target}, {Plain, w.Trigger}, {Plain, strconv.Itoa(w.Attempt)}, {phaseTones[w.Phase], string(w.Phase)}, {Plain, when}, {Plain, activity}})
	}
	// 最後の列 (活動) は揃えないので幅を数えない
	widths := make([]int, len(rows[0])-1)
	for _, row := range rows {
		for i := range widths {
			widths[i] = max(widths[i], cells(row[i].Text))
		}
	}
	var b strings.Builder
	for _, row := range rows {
		last := len(row) - 1
		for last > 0 && row[last].Text == "" {
			last--
		}
		var line []Span
		for i, cell := range row[:last+1] {
			line = append(line, cell)
			if i < last {
				line = append(line, Span{Plain, strings.Repeat(" ", widths[i]-cells(cell.Text)+2)})
			}
		}
		b.WriteString(d.Line(line...) + "\n")
	}
	return b.String()
}

// elapsed は経過を `45s`・`5m10s`・`1h02m05s` の形にする (秒未満は切り捨てる)。読み手の時計が起動の時刻より遅れて負に
// なったら 0s にする。
func elapsed(d time.Duration) string {
	sec := int(max(d, 0) / time.Second)
	h, m, s := sec/3600, sec/60%60, sec%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// needsAttention は要対処の行: 打ち切り・曖昧な CL・起動しない trigger。
func needsAttention(s Snapshot) [][]Span {
	var lines [][]Span
	for _, a := range s.Abandoned {
		lines = append(lines, []Span{{Red, "打ち切り"}, {Plain, fmt.Sprintf(" %s (%s): label を外して trigger から外し、1 周期待ってから付け直すと解ける", a.Target, a.Trigger)}})
	}
	for _, a := range s.Ambiguous {
		lines = append(lines, []Span{{Yellow, "曖昧な CL"}, {Plain, fmt.Sprintf(" %s: %s", a.Head, strings.Join(a.Targets, ", "))}})
	}
	for _, b := range s.Blocked {
		lines = append(lines, []Span{{Red, "起動しない trigger"}, {Plain, fmt.Sprintf(" %s: %s", b.Trigger, b.Error)}})
	}
	return lines
}
