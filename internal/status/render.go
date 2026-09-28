package status

import (
	"fmt"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/termtext"
	"github.com/swat9013/claude-dispatcher/internal/ticklog"
)

// unknownCell は読めなかった値の表の綴り (formats.md §10)
const unknownCell = "?"

var columns = []string{"ISSUE", "KIND", "STATE", "ELAPSED", "SESSION", "BRANCH", "WIP", "CL", "TICK"}

// RenderTable は project ごとに見出し・注記・表を並べ、project の間を空行で区切る (formats.md §10)。
func RenderTable(reports []Report) string {
	blocks := make([]string, 0, len(reports))
	for _, r := range reports {
		lines := append([]string{r.Heading()}, r.NoteLines()...)
		blocks = append(blocks, strings.Join(append(lines, r.TableLines()...), "\n"))
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

// Heading は project の見出し行 (project 名・loop の生死・最終 tick)。
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
	return fmt.Sprintf("%s  %s  最終 tick %s", r.Project, loop, last)
}

// NoteLines は注記の行。
func (r Report) NoteLines() []string {
	lines := make([]string, 0, len(r.Notes))
	for _, note := range r.Notes {
		lines = append(lines, "  ! "+note)
	}
	return lines
}

// TableLines は worker の表の行 (列の見出し行を含む)。載せる worker が居なければ空。
func (r Report) TableLines() []string {
	if len(r.Workers) == 0 {
		return nil
	}
	var lines []string
	rows := [][]string{columns}
	for _, w := range r.Workers {
		rows = append(rows, []string{
			fmt.Sprintf("#%d", w.Spawn.Issue), w.Spawn.Kind, cell(w.Alive, stateCell), FormatElapsed(w.Elapsed),
			cell(w.Session, sessionCell), cell(w.Branch, branchCell), cell(w.WIP, wipCell), cell(w.CL, clCell), ticklog.ShortTS(w.TickTS),
		})
	}
	widths := make([]int, len(columns))
	for _, row := range rows {
		for i, c := range row {
			widths[i] = max(widths[i], termtext.Width(c))
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
