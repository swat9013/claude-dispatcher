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
	d := status.Display{Palette: terminalPalette(getenv)}
	if width, _, err := term.GetSize(int(f.Fd())); err == nil {
		d.Width = width
	}
	return d
}

// terminalPalette は端末へ描くときの色の付け方。NO_COLOR は空でない値のときだけ効く (https://no-color.org)。
func terminalPalette(getenv func(string) string) status.Palette {
	if getenv("NO_COLOR") != "" {
		return status.Monochrome
	}
	return status.ANSI
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

// showStatus は loop の Publish から呼ばれ、状態 file と同じ中身で描き直す (1s ごとの描き直しはこの呼び出しで起きる)。
func (s *screen) showStatus(snap status.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = &snap
	s.draw()
}

// headTones はログの行の頭の語の色、wholeTones は行全体 (時刻を含む) の色
var (
	headTones = map[loop.LineKind]status.Tone{
		loop.LineStart: status.Blue, loop.LineCompleted: status.Green, loop.LineFailed: status.Red, loop.LineError: status.Red,
		loop.LineRetry: status.Yellow, loop.LineStopping: status.Yellow,
	}
	wholeTones = map[loop.LineKind]status.Tone{loop.LineTickOK: status.Faint, loop.LineTickError: status.Red}
)

func (s *screen) draw() {
	d := s.display()
	var b strings.Builder
	b.WriteString("\033[H\033[2J")
	if snap := s.snap; snap != nil {
		b.WriteString(status.Heading(*snap, status.LoopAlive, s.loc, d))
		// 2 回目の停止要求の後は、worker が running でなくなる (止めている) ので案内を消す
		if n := running(snap.Workers); snap.Stopping && n > 0 {
			b.WriteString(d.Line(status.Span{Tone: status.Yellow, Text: fmt.Sprintf("worker の終了を待っている。もう一度 Ctrl+C で走っている worker %d 本を止めて終える", n)}) + "\n")
		}
		b.WriteString("\n" + status.Sections(*snap, s.now(), s.loc, d) + "\n")
	}
	b.WriteString(d.Section(status.Span{Tone: status.Bold, Text: "ログ"}) + "\n")
	for _, l := range s.lines {
		at := l.At.In(s.loc).Format("15:04:05")
		if tone, ok := wholeTones[l.Kind]; ok {
			b.WriteString(d.Line(status.Span{Tone: tone, Text: at + " " + l.Label + l.Rest}) + "\n")
			continue
		}
		b.WriteString(d.Line(status.Span{Tone: status.Gray, Text: at}, status.Span{Text: " "}, status.Span{Tone: headTones[l.Kind], Text: l.Label}, status.Span{Text: l.Rest}) + "\n")
	}
	_, _ = io.WriteString(s.out, b.String())
}

// running は段階が running の worker の数 (2 回目の停止要求が止める worker)。
func running(workers []status.Worker) int {
	n := 0
	for _, w := range workers {
		if w.Phase == status.Running {
			n++
		}
	}
	return n
}
