package loop

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/tick"
	"github.com/swat9013/claude-dispatcher/internal/ticklog"
)

// phase は loop の状態 (formats.md §13.1 の 1 行目の状態欄)。
type phase int

const (
	waiting phase = iota
	ticking
	// stopping は停止を求められた後。tick の実行中なら終わるのを待ち、終わっていれば止まる
	stopping
)

// clearScreen は端末の画面を消して左上へ戻す
const clearScreen = "\033[H\033[2J"

// view は画面 1 枚分の材料。
type view struct {
	project  string
	interval string
	phase    phase
	now      time.Time
	// next は次の tick の時刻 (待機のとき)
	next time.Time
	// tickStarted は実行中の tick を始めた時刻。tick を実行していなければ零値
	tickStarted time.Time
	// orchestratorStarted は実行中の tick が orchestrator を起動した時刻。起動していなければ零値
	orchestratorStarted time.Time
	// last は loop が回した直近の tick。まだ 1 回も終えていなければ nil
	last   *tick.Outcome
	report status.Report
}

// lines は画面の行。withGuide が false なら操作案内を除く (stdout が端末でないときと、止まった後の画面)。
func (v view) lines(withGuide bool) []string {
	lines := []string{
		fmt.Sprintf("%s  loop %s  %s", v.project, v.interval, v.state()),
		v.lastTick(),
	}
	if v.last != nil && v.last.Result != tick.ResultOK {
		lines = append(lines, "  ! "+v.last.Error)
	}
	lines = append(lines, v.report.NoteLines()...)
	lines = append(lines, v.report.TableLines()...)
	if withGuide {
		lines = append(lines, "", v.guide())
	}
	return lines
}

func (v view) state() string {
	switch v.phase {
	case waiting:
		return fmt.Sprintf("待機 · 次の tick %s (あと %s)", v.next.UTC().Format(timeLayout), status.FormatElapsed(max(v.next.Sub(v.now), 0)))
	case ticking:
		return "tick 実行中 · " + v.progress()
	}
	if v.tickStarted.IsZero() {
		return "停止待ち"
	}
	return "停止待ち · " + v.progress()
}

// progress は実行中の tick の経過。orchestrator を待つ間はその経過と上限を出す。
func (v view) progress() string {
	if !v.orchestratorStarted.IsZero() {
		return fmt.Sprintf("orchestrator %s (上限 %s)", status.FormatElapsed(v.now.Sub(v.orchestratorStarted)), status.FormatElapsed(tick.OrchestratorTimeout))
	}
	return status.FormatElapsed(v.now.Sub(v.tickStarted))
}

func (v view) lastTick() string {
	if v.last == nil {
		return "最終 tick なし"
	}
	parts := []string{fmt.Sprintf("最終 tick %s %s", ticklog.ShortTS(v.last.TS), v.last.Result.Name), instructionsPart(v.last.Instructions)}
	if len(v.last.Spawned) > 0 {
		issues := make([]string, 0, len(v.last.Spawned))
		for _, issue := range v.last.Spawned {
			issues = append(issues, fmt.Sprintf("#%d", issue))
		}
		parts = append(parts, "起動 "+strings.Join(issues, " "))
	}
	return strings.Join(parts, " · ")
}

// instructionsPart は指示の種別と件数 (`指示 start 1`)。0 件なら `指示 0`。
func instructionsPart(counts map[string]int) string {
	kinds := make([]string, 0, len(counts))
	for kind, n := range counts {
		if n > 0 {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) == 0 {
		return "指示 0"
	}
	slices.Sort(kinds)
	pairs := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		pairs = append(pairs, fmt.Sprintf("%s %d", kind, counts[kind]))
	}
	return "指示 " + strings.Join(pairs, ", ")
}

func (v view) guide() string {
	switch v.phase {
	case waiting:
		return "Ctrl+C で停止"
	case ticking:
		return "Ctrl+C: この tick を終えてから停止"
	}
	return "停止待ち: この tick を終えたら止まる。もう一度 Ctrl+C で orchestrator を止めて止まる (付いた wip は残りうる)"
}

// timeLayout は画面と終了行の時刻 (UTC、秒まで)
const timeLayout = "2006-01-02T15:04:05Z"
