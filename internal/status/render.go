package status

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/termtext"
	"github.com/swat9013/claude-dispatcher/internal/tick"
	"github.com/swat9013/claude-dispatcher/internal/ticklog"
)

// unknownCell は読めなかった値の表の綴り (formats.md §10)
const unknownCell = "?"

var columns = []string{"ISSUE", "KIND", "STATE", "ELAPSED", "SESSION", "BRANCH", "WIP", "CL", "TICK", "ACTIVITY"}

// activityContentLimit は stdout が端末でないときに ACTIVITY の内容を切り詰める文字数 (formats.md §10)
const activityContentLimit = 60

// Fit は ACTIVITY の列と判断の行の reason の切り詰め方 (formats.md §10)。FitTerminal か FitPlain で作る。
type Fit struct {
	// terminalWidth は行を収める端末の幅。0 なら端末でない (FitPlain)
	terminalWidth int
}

// FitTerminal は行が端末の幅 width に収まるように ACTIVITY と判断の行の reason を切り詰める。width は 1 以上 (幅を
// 読めない端末は FitPlain にする)。
func FitTerminal(width int) Fit {
	if width < 1 {
		panic(fmt.Sprintf("status.FitTerminal: 端末の幅 %d は 1 以上でない", width))
	}
	return Fit{terminalWidth: width}
}

// FitPlain は stdout が端末でないときの切り詰め方: ACTIVITY の内容を 60 文字で切り、判断の行は切り詰めない。
func FitPlain() Fit { return Fit{} }

// RenderTable は project ごとに見出し・判断の行・注記・表を並べ、project の間を空行で区切る (formats.md §10)。
func RenderTable(reports []Report, fit Fit) string {
	blocks := make([]string, 0, len(reports))
	for _, r := range reports {
		lines := append([]string{r.Heading()}, r.JudgmentLines(fit)...)
		lines = append(lines, r.NoteLines()...)
		blocks = append(blocks, strings.Join(append(lines, r.TableLines(fit)...), "\n"))
	}
	return strings.Join(blocks, "\n\n")
}

// cell は読めた値を format で、読めなかった値を `?` で表す。
func cell[T any](p Probed[T], format func(T) string) string {
	if !p.Known {
		return unknownCell
	}
	return format(p.Value)
}

// Heading は project の見出し行 (project 名・loop の生死・走っている tick・最終 tick)。走っている tick の欄は loop の画面の
// 1 行目の状態欄と同じ綴り (formats.md §10 / §13.1)。
func (r Report) Heading() string {
	loop := "loop " + unknownCell
	if r.Loop.Known {
		loop = map[bool]string{true: "loop 稼働中", false: "loop なし"}[r.Loop.Value]
	}
	last := cell(r.LastTick, func(l *ticklog.Line) string {
		if l == nil {
			return "なし"
		}
		return ticklog.Summary(l.TS, l.Result)
	})
	fields := []string{r.Project, loop}
	if r.Tick != nil {
		fields = append(fields, "tick 実行中 · "+r.Tick.Progress())
	}
	return strings.Join(append(fields, "最終 tick "+last), "  ")
}

// Progress は走っている tick の経過 (`3m`)。orchestrator の実行中はその経過と上限 (`orchestrator 3m (上限 15m)`)。
// status の見出しと loop の画面の状態欄が使う (formats.md §10 / §13.1)。
func (t RunningTick) Progress() string {
	if t.Orchestrator != nil {
		return fmt.Sprintf("orchestrator %s (上限 %s)", FormatElapsed(t.Orchestrator.Elapsed), FormatElapsed(tick.OrchestratorTimeout))
	}
	return FormatElapsed(t.Elapsed)
}

// JudgmentLines は直近の orchestrator 行の decisions のうち、見送り (skip) と人返し (ready-for-human) の行 (formats.md §10)。
// 採った issue (start / reenter) は worker として表に出るので出さない。出す判断が無ければ、判断した時刻の行も出さない。
func (r Report) JudgmentLines(fit Fit) []string {
	if r.LastOrchestrator == nil {
		return nil
	}
	var lines []string
	for _, d := range r.LastOrchestrator.Decisions {
		if d.Action != ticklog.ActionSkip && d.Action != ticklog.ActionReadyForHuman {
			continue
		}
		prefix := fmt.Sprintf("  #%d %s: ", d.Issue, d.Action)
		lines = append(lines, prefix+fit.cutReason(oneLine(d.Reason), termtext.Width(prefix)))
	}
	if len(lines) == 0 {
		return nil
	}
	return append([]string{"判断 " + ticklog.ShortTS(r.LastOrchestrator.TS)}, lines...)
}

// oneLine は reason の改行と制御文字 (ESC など) を空白にする。reason は orchestrator が issue 本文を読んで書く文で、
// それらを含みうる。1 件 1 行を保ち、端末に制御文字を撃ち込ませないため。
func oneLine(reason string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, reason)
}

// cutReason は端末なら、前置き (表示幅 used) の後ろに残る幅で reason を切る。前置き (`#<issue> <action>:`) は切らない
// ので、端末が前置きより狭いと行は端末の幅を超える。
func (f Fit) cutReason(reason string, used int) string {
	if f.terminalWidth == 0 {
		return reason
	}
	return termtext.Cut(reason, max(0, f.terminalWidth-used))
}

// NoteLines は注記の行。
func (r Report) NoteLines() []string {
	lines := make([]string, 0, len(r.Notes))
	for _, note := range r.Notes {
		lines = append(lines, "  ! "+note)
	}
	return lines
}

// TableLines は表の行 (列の見出し行を含む)。orchestrator の実行中はその行を先頭に置く。載せる行が無ければ空。
// 最後の列 ACTIVITY は fit に従って切り詰める。
func (r Report) TableLines(fit Fit) []string {
	var orchestrator *RunningOrchestrator
	if r.Tick != nil {
		orchestrator = r.Tick.Orchestrator
	}
	if len(r.Workers) == 0 && orchestrator == nil {
		return nil
	}
	var lines []string
	rows := [][]string{columns}
	// ACTIVITY は他の列の幅が決まってから埋める (端末なら残りの幅に収める)
	var activities []Probed[*Activity]
	if orchestrator != nil {
		rows = append(rows, []string{
			"-", "orch", "running", FormatElapsed(orchestrator.Elapsed), cell(orchestrator.Session, sessionCell), "-", "-", "-", ticklog.ShortTS(r.Tick.TS), "",
		})
		activities = append(activities, orchestrator.Activity)
	}
	for _, w := range r.Workers {
		rows = append(rows, []string{
			fmt.Sprintf("#%d", w.Spawn.Issue), w.Spawn.Kind, cell(w.State(), stateCell), FormatElapsed(w.Elapsed),
			cell(w.Session, sessionCell), cell(w.Branch, branchCell), cell(w.WIP, wipCell), cell(w.CL, clCell), ticklog.ShortTS(w.TickTS), "",
		})
		activities = append(activities, w.Activity)
	}
	last := len(columns) - 1
	widths := make([]int, len(columns))
	for _, row := range rows {
		for i, c := range row[:last] {
			widths[i] = max(widths[i], termtext.Width(c))
		}
	}
	room := fit.activityRoom(widths[:last])
	rows[0][last] = termtext.Cut(rows[0][last], room)
	for i, a := range activities {
		rows[i+1][last] = cell(a, func(a *Activity) string { return fit.activityCell(a, room) })
	}
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = termtext.Pad(c, widths[i])
		}
		lines = append(lines, strings.TrimRight(strings.Join(cells, "  "), " "))
	}
	return lines
}

// FormatElapsed は経過を `45s` / `12m` / `3h05m` / `2d04h` にする (formats.md §10。loop の画面の残りと経過も同じ綴り)。
func FormatElapsed(d time.Duration) string {
	seconds := int(d.Seconds())
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm", seconds/60)
	case seconds < 86400:
		return fmt.Sprintf("%dh%02dm", seconds/3600, seconds%3600/60)
	}
	return fmt.Sprintf("%dd%02dh", seconds/86400, seconds%86400/3600)
}

// activityRoom は ACTIVITY の列に使える表示幅。端末なら端末の幅から他の列 (widths) と区切りを引いた残り、端末でなければ
// 上限なし (-1)。
func (f Fit) activityRoom(widths []int) int {
	if f.terminalWidth == 0 {
		return -1
	}
	room := f.terminalWidth
	for _, w := range widths {
		room -= w + 2
	}
	return max(room, 0)
}

// activityCell は `<最後の event からの経過> <内容>`。running でない行 (nil) は `-`。端末なら room に収め、端末でなければ
// 内容を 60 文字で切る (formats.md §10)。
func (f Fit) activityCell(a *Activity, room int) string {
	if a == nil {
		return termtext.Cut("-", room)
	}
	doing := a.Doing
	if f.terminalWidth == 0 {
		if runes := []rune(doing); len(runes) > activityContentLimit {
			doing = string(runes[:activityContentLimit])
		}
	}
	return termtext.Cut(strings.TrimSpace(FormatElapsed(a.Since)+" "+doing), room)
}

func stateCell(s WorkerState) string { return string(s) }

func wipCell(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func sessionCell(s *Session) string {
	if s == nil {
		return "-"
	}
	var progress []string
	for _, part := range []string{s.Status, s.State} {
		if part != "" {
			progress = append(progress, part)
		}
	}
	return orDash(s.ID) + " " + orDash(strings.Join(progress, "/"))
}

func branchCell(b *Branch) string {
	if b == nil {
		return "-"
	}
	return "+" + cell(b.Ahead, func(n int) string { return fmt.Sprint(n) })
}

func clCell(c *github.CLState) string {
	if c == nil {
		return "-"
	}
	return fmt.Sprintf("#%d %s", c.Number, c.State)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
