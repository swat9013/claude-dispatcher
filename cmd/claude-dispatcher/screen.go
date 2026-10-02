package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/status"
)

// screenLines は端末の画面に残す直近のログの行の数
const screenLines = 10

// screen は端末の画面 (formats.md §6)。状態を受けるたびとログの行を受けるたびに、`status` と同じ見出し・停止待ちの案内・
// `status` と同じセクション・直近のログを描き直す。画面に書けなくても loop は止めない (ログの行と同じく捨てる)。
type screen struct {
	out io.Writer
	now func() time.Time
	// loc は画面の時刻の time zone (端末の local time)
	loc *time.Location
	// display は描くたびに端末の性質を読む (端末の幅は描いている間にも変わる)
	display func() status.Display
	mu      sync.Mutex
	snap    *status.Snapshot
	lines   []loop.Line
}

// isTerminal は f が端末か。pipe や file へ流しているときは描き直さず、ログの行を追記する。
func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// display は f へ描くときの端末の性質 (formats.md §7.2 の色と幅)。端末でなければ色も幅も無い。
func display(f *os.File, getenv func(string) string) status.Display {
	if !isTerminal(f) {
		return status.Display{Palette: status.Monochrome}
	}
	d := status.Display{Palette: status.ANSI}
	// NO_COLOR は空でない値のときだけ効く (https://no-color.org)
	if getenv("NO_COLOR") != "" {
		d.Palette = status.Monochrome
	}
	if width, _, err := term.GetSize(int(f.Fd())); err == nil {
		d.Width = width
	}
	return d
}

// Show は loop のログの行の出し先。活動の行は表の列で見るので、ログには残さない。
func (s *screen) Show(l loop.Line) {
	if l.Kind == loop.LineActivity {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, l)
	if len(s.lines) > screenLines {
		s.lines = s.lines[len(s.lines)-screenLines:]
	}
	s.draw()
}

// show は loop の Publish から呼ばれ、状態 file と同じ中身で描き直す (1s ごとの描き直しはこの呼び出しで起きる)。
func (s *screen) show(snap status.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = &snap
	s.draw()
}

// lineTones はログの行の頭の語の色
var lineTones = map[loop.LineKind]status.Tone{
	loop.LineStart: status.Blue, loop.LineCompleted: status.Green, loop.LineFailed: status.Red, loop.LineError: status.Red,
	loop.LineRetry: status.Yellow, loop.LineStopping: status.Yellow,
}

func (s *screen) draw() {
	d := s.display()
	var b strings.Builder
	b.WriteString("\033[H\033[2J")
	if snap := s.snap; snap != nil {
		b.WriteString(status.Heading(*snap, status.LoopAlive, s.loc, d))
		if snap.Stopping {
			b.WriteString(d.Line(status.Span{Tone: status.Yellow, Text: fmt.Sprintf("走っている worker %d 本の終了を待っている。もう一度 Ctrl+C で worker を止めて終える", running(snap.Workers))}) + "\n")
		}
		b.WriteString("\n" + status.Sections(*snap, s.now(), s.loc, d) + "\n")
	}
	b.WriteString(d.Section(status.Span{Tone: status.Bold, Text: "ログ"}) + "\n")
	for _, l := range s.lines {
		at := l.At.In(s.loc).Format("15:04:05")
		if l.Kind == loop.LineTickOK {
			b.WriteString(d.Line(status.Span{Tone: status.Faint, Text: at + " " + l.Label + l.Rest}) + "\n")
			continue
		}
		b.WriteString(d.Line(status.Span{Tone: status.Gray, Text: at}, status.Span{Text: " "}, status.Span{Tone: lineTones[l.Kind], Text: l.Label}, status.Span{Text: l.Rest}) + "\n")
	}
	_, _ = io.WriteString(s.out, b.String())
}

// running は走っている worker (止めている途中を含む) の数。
func running(workers []status.Worker) int {
	n := 0
	for _, w := range workers {
		if w.Phase == status.Running || w.Phase == status.Stopping {
			n++
		}
	}
	return n
}
