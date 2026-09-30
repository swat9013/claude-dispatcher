package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/status"
)

// screenLines は端末の画面に残す直近の事象の行の数
const screenLines = 10

// screen は端末の画面 (formats.md §6)。状態を受けるたびと事象の行を受けるたびに、`status` と同じ見出しと表・空行・
// 直近の事象の行を描き直す。
type screen struct {
	out   io.Writer
	scope string
	mu    sync.Mutex
	snap  *status.Snapshot
	lines []string
	// partial は改行をまだ受けていない書きかけの行
	partial []byte
}

// isTerminal は f が端末か。
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// Write は事象の行を受けて描き直す。
func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			break
		}
		s.lines = append(s.lines, string(s.partial[:i]))
		s.partial = s.partial[i+1:]
	}
	if len(s.lines) > screenLines {
		s.lines = s.lines[len(s.lines)-screenLines:]
	}
	return len(p), s.draw()
}

// show は状態を受けて描き直す。
func (s *screen) show(snap status.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = &snap
	_ = s.draw()
}

func (s *screen) draw() error {
	var b strings.Builder
	b.WriteString("\033[H\033[2J")
	if s.snap != nil {
		b.WriteString(status.Render(s.snap, s.scope, true, time.Now(), time.Local))
	}
	b.WriteString("\n")
	for _, line := range s.lines {
		b.WriteString(line + "\n")
	}
	_, err := io.WriteString(s.out, b.String())
	return err
}
