package status

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/github"
)

var columns = []string{"ISSUE", "KIND", "STATE", "ELAPSED", "SESSION", "BRANCH", "WIP", "CL", "TICK"}

// RenderTable は project ごとの表を空行で区切って並べる (formats.md §10)。
func RenderTable(reports []Report) string {
	blocks := make([]string, 0, len(reports))
	for _, r := range reports {
		blocks = append(blocks, strings.Join(renderProject(r), "\n"))
	}
	return strings.Join(blocks, "\n\n")
}

// RenderJSON は `status ps --json` の 1 文書。
func RenderJSON(reports []Report) ([]byte, error) {
	return json.MarshalIndent(map[string][]Report{"projects": reports}, "", "  ")
}

func renderProject(r Report) []string {
	running := "待機"
	switch r.Tick.Running {
	case true:
		running = "tick 実行中"
	case Unknown:
		running = "tick ?"
	}
	last := "なし"
	if r.Tick.LastTS != nil {
		last = shortTS(*r.Tick.LastTS) + " " + *r.Tick.LastResult
	}
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
			fmt.Sprintf("#%d", w.Issue), w.Kind, w.State, formatElapsed(w.ElapsedSec), cellSession(w.Session),
			cellBranch(w.Branch), cellWIP(w.WIP), cellCL(w.CL), shortTS(w.TickTS),
		})
	}
	widths := make([]int, len(columns))
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], len([]rune(cell)))
		}
	}
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			cells[i] = cell + strings.Repeat(" ", widths[i]-len([]rune(cell)))
		}
		lines = append(lines, strings.TrimRight(strings.Join(cells, "  "), " "))
	}
	return lines
}

// formatElapsed は経過秒を `45s` / `12m` / `3h05m` / `2d04h` にする。
func formatElapsed(seconds int) string {
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

// shortTS は log の ts (マイクロ秒まで) を秒までにする。読めなければそのまま。
func shortTS(ts string) string {
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return at.UTC().Format("2006-01-02T15:04:05Z")
}

func cellSession(v any) string {
	s, ok := v.(Session)
	if !ok {
		return orDash(v)
	}
	var progress []string
	for _, part := range []string{s.Status, s.State} {
		if part != "" {
			progress = append(progress, part)
		}
	}
	return orDash(s.ID) + " " + orDash(strings.Join(progress, "/"))
}

func cellBranch(v any) string {
	b, ok := v.(Branch)
	if !ok {
		return orDash(v)
	}
	return fmt.Sprintf("+%v", b.Ahead)
}

func cellWIP(v any) string {
	switch v {
	case true:
		return "yes"
	case false:
		return "no"
	}
	return orDash(v)
}

func cellCL(v any) string {
	cl, ok := v.(github.CLState)
	if !ok {
		return orDash(v)
	}
	return fmt.Sprintf("#%d %s", cl.Number, cl.State)
}

// orDash は nil と空文字を `-` に、それ以外 (Unknown 等) を文字列にする。
func orDash(v any) string {
	if v == nil || v == "" {
		return "-"
	}
	return fmt.Sprint(v)
}
