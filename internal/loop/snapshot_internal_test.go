package loop

import (
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

func TestEveryClaimPhaseIsWrittenToTheStatusFile(t *testing.T) {
	for p := phaseRunning; p <= phaseWaitingRetry; p++ {
		if _, ok := phases[p]; !ok {
			t.Errorf("段階 %d を状態 file の綴りに写さない", p)
		}
	}
}

// lines は人が読む行を受けたまま持つ。
type lines []Line

func (l *lines) Show(line Line) { *l = append(*l, line) }

// activeWorker は活動を 1 つ持つ走っている worker。
type activeWorker struct{ activity status.Activity }

func (activeWorker) Stop()                       {}
func (w activeWorker) Activity() status.Activity { return w.activity }

func TestActivityIsShownAsAnActivityLine(t *testing.T) {
	// 端末の画面は活動の行を種類で見分けてログから外す (formats.md §6)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var out lines
	l := &loop{
		claims: map[target.Ref]*claim{{Kind: target.KindIssue, Number: 42}: {run: activeWorker{status.Activity{At: now, Summary: "thinking"}}}},
		rec:    recorder{output: &out, now: func() time.Time { return now }},
	}

	l.refresh()

	if len(out) != 1 || out[0].Kind != LineActivity || out[0].Label+out[0].Rest != "活動 issue#42: thinking" {
		t.Fatalf("行 = %+v, want 活動の行 1 つ", out)
	}
}
