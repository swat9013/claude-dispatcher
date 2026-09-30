package status

import (
	"fmt"
	"strings"
	"time"

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

// Fit は ACTIVITY の切り詰め方 (formats.md §10)。FitTerminal か FitPlain で作る。
type Fit struct {
	// terminalWidth は行を収める端末の幅。0 なら端末でない
	terminalWidth int
}

// FitTerminal は行が端末の幅 width に収まるように ACTIVITY を切り詰める。
func FitTerminal(width int) Fit { return Fit{terminalWidth: width} }

// FitPlain は stdout が端末でないときの切り詰め方: ACTIVITY の内容を 60 文字で切る。
var FitPlain = Fit{}

// RenderTable は project ごとに見出し・注記・表を並べ、project の間を空行で区切る (formats.md §10)。
func RenderTable(reports []Report, fit Fit) string {
	blocks := make([]string, 0, len(reports))
	for _, r := range reports {
		lines := append([]string{r.Heading()}, r.NoteLines()...)
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
	if orchestrator != nil {
		rows = append(rows, []string{
			"-", "orch", "running", FormatElapsed(orchestrator.Elapsed), cell(orchestrator.Session, sessionCell), "-", "-", "-", ticklog.ShortTS(r.Tick.TS),
			cell(orchestrator.Activity, fit.activityCell),
		})
	}
	for _, w := range r.Workers {
		rows = append(rows, []string{
			fmt.Sprintf("#%d", w.Spawn.Issue), w.Spawn.Kind, cell(w.Alive, stateCell), FormatElapsed(w.Elapsed),
			cell(w.Session, sessionCell), cell(w.Branch, branchCell), cell(w.WIP, wipCell), cell(w.CL, clCell), ticklog.ShortTS(w.TickTS),
			cell(w.Activity, fit.activityCell),
		})
	}
	last := len(columns) - 1
	widths := make([]int, len(columns))
	for _, row := range rows {
		for i, c := range row[:last] {
			widths[i] = max(widths[i], termtext.Width(c))
		}
	}
	if fit.terminalWidth > 0 {
		// ACTIVITY 以外の列と区切りの残りに収める
		room := fit.terminalWidth
		for _, w := range widths[:last] {
			room -= w + 2
		}
		for _, row := range rows[1:] {
			row[last] = termtext.Cut(row[last], max(room, 0))
		}
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

// activityCell は `<最後の event からの経過> <内容>`。running でない行 (nil) は `-`。端末でなければ内容を 60 文字で切る
// (端末なら行の幅に収める切り詰めを TableLines が後で行う)。
func (f Fit) activityCell(a *Activity) string {
	if a == nil {
		return "-"
	}
	doing := a.Doing
	if f.terminalWidth == 0 {
		if runes := []rune(doing); len(runes) > activityContentLimit {
			doing = string(runes[:activityContentLimit])
		}
	}
	return strings.TrimSpace(FormatElapsed(a.Since) + " " + doing)
}

func stateCell(alive bool) string {
	if alive {
		return "running"
	}
	return "exited"
}

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
