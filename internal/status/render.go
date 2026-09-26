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

// RenderTable は project ごとの表を空行で区切って並べる (formats.md §10)。
func RenderTable(reports []Report) string {
	blocks := make([]string, 0, len(reports))
	for _, r := range reports {
		blocks = append(blocks, strings.Join(renderProject(r), "\n"))
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

func renderProject(r Report) []string {
	running := "tick " + unknownCell
	if r.Tick.Running.Known {
		running = map[bool]string{true: "tick 実行中", false: "待機"}[r.Tick.Running.Value]
	}
	last := cell(r.Tick.Last, func(l *ticklog.Line) string {
		if l == nil {
			return "なし"
		}
		return ticklog.ShortTS(l.TS) + " " + l.Result
	})
	lines := []string{fmt.Sprintf("%s  %s  最終 tick %s", r.Project, running, last)}
	for _, note := range r.Notes {
		lines = append(lines, "  ! "+note)
	}
	if len(r.Workers) == 0 {
		return lines
	}
	rows := [][]string{columns}
	for _, w := range r.Workers {
		rows = append(rows, []string{
			fmt.Sprintf("#%d", w.Spawn.Issue), w.Spawn.Kind, cell(w.Alive, state), formatElapsed(w.Elapsed),
			cell(w.Session, session), cell(w.Branch, branch), cell(w.WIP, yesNo), cell(w.CL, cl), ticklog.ShortTS(w.TickTS),
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

// formatElapsed は経過を `45s` / `12m` / `3h05m` / `2d04h` にする。
func formatElapsed(d time.Duration) string {
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

func state(alive bool) string {
	if alive {
		return "running"
	}
	return "exited"
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func session(s *Session) string {
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

func branch(b *Branch) string {
	if b == nil {
		return "-"
	}
	return "+" + cell(b.Ahead, func(n int) string { return fmt.Sprint(n) })
}

func cl(c *github.CLState) string {
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
