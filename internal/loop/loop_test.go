package loop_test

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// 周期を待つ振る舞い (tick ごとの読み直し・周期の選び方)。black-box テストは周期の下限 (1m) を待てないので、ここで見る。

// memoryIssues は in-memory の issue 置き場。
type memoryIssues struct {
	scopeKey string
	issues   []target.Issue
	reads    *int
}

func (m memoryIssues) ScopeKey() string { return m.scopeKey }

func (m memoryIssues) OpenIssues() ([]target.Issue, error) {
	*m.reads++
	return m.issues, nil
}

// harness は loads を読み切ったところで停止要求を送る loop の入力を組む。loads[i] は i 回目の tick が読む workflow 定義で、
// scopes[i] はその版から組み立てた issue 置き場の scope key (空なら起動時と同じ)。
type harness struct {
	loads   []func() (workflow.Definition, error)
	scopes  []string
	waits   []time.Duration
	reads   int
	stdout  bytes.Buffer
	signals chan os.Signal
}

const startScope = "scope-a"

func definition(interval time.Duration) workflow.Definition {
	return workflow.Definition{
		Interval: interval,
		Triggers: []trigger.Trigger{{Name: "implement", On: "issue", Action: "/implement"}},
	}
}

func (h *harness) run(t *testing.T) string {
	t.Helper()
	h.signals = make(chan os.Signal, 1)
	tick := 0
	exit := loop.Run(loop.Options{
		Load: func() (workflow.Definition, error) {
			load := h.loads[tick]
			tick++
			return load()
		},
		Open: func(workflow.Definition) (loop.Issues, error) {
			scope := startScope
			if tick-1 < len(h.scopes) && h.scopes[tick-1] != "" {
				scope = h.scopes[tick-1]
			}
			return memoryIssues{scopeKey: scope, issues: []target.Issue{{Number: 1}}, reads: &h.reads}, nil
		},
		Interval: time.Minute,
		ScopeKey: startScope,
		Stdout:   &h.stdout,
		Signals:  h.signals,
		Now:      func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
		After: func(d time.Duration) <-chan time.Time {
			h.waits = append(h.waits, d)
			ch := make(chan time.Time, 1)
			if tick < len(h.loads) {
				ch <- time.Time{}
			} else {
				h.signals <- syscall.SIGINT
			}
			return ch
		},
	})
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
	return h.stdout.String()
}

func TestTickWithABrokenWorkflowDefinitionObservesNothingAndKeepsTheLastGoodInterval(t *testing.T) {
	h := &harness{loads: []func() (workflow.Definition, error){
		func() (workflow.Definition, error) { return definition(3 * time.Minute), nil },
		func() (workflow.Definition, error) {
			return workflow.Definition{}, errors.New("x.md: triggers[0].on (5 行目): 未知の値\nx.md: tracker (2 行目): y")
		},
		func() (workflow.Definition, error) { return definition(7 * time.Minute), nil },
	}}

	out := h.run(t)

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !strings.HasSuffix(lines[0], "tick ok · 候補 1: implement issue #1") ||
		!strings.HasSuffix(lines[1], "tick error · workflow 定義の誤り: x.md: triggers[0].on (5 行目): 未知の値 / x.md: tracker (2 行目): y") ||
		!strings.HasSuffix(lines[2], "tick ok · 候補 1: implement issue #1") ||
		!strings.HasSuffix(lines[3], "loop を止めた (停止要求 SIGINT)") {
		t.Fatalf("出力:\n%s", out)
	}
	if h.reads != 2 {
		t.Fatalf("置き場を読んだ回数 = %d, want 2 (workflow 定義が誤っている tick は読まない)", h.reads)
	}
	if want := []time.Duration{3 * time.Minute, 3 * time.Minute, 7 * time.Minute}; !equal(h.waits, want) {
		t.Fatalf("待った周期 = %v, want %v", h.waits, want)
	}
}

func TestTickWhoseScopeKeyDiffersFromTheStartObservesNothing(t *testing.T) {
	h := &harness{loads: []func() (workflow.Definition, error){
		func() (workflow.Definition, error) { return definition(time.Minute), nil },
	}, scopes: []string{"scope-b"}}

	out := h.run(t)

	if !strings.Contains(out, "tick error · workflow 定義の scope key scope-b が起動時の scope-a と違う") {
		t.Fatalf("出力:\n%s", out)
	}
	if h.reads != 0 {
		t.Fatalf("scope key が違うのに置き場を読んだ (%d 回)", h.reads)
	}
}

func equal(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
