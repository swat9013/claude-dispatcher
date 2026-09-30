package loop_test

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// 周期を待つ振る舞い (tick ごとの読み直し・周期の選び方・停止)。周期を分の単位で選ぶ振る舞いは black-box テストが待てないので、ここで見る。

const startScope = "scope-a"

// tickPlan は 1 回の tick が読むもの。
type tickPlan struct {
	load func() (workflow.Definition, error)
	// scope はその版から組み立てた issue 置き場の scope key (空なら起動時と同じ)
	scope string
	// fail が nil でなければ、置き場の観測がこの失敗を返す
	fail error
	// stopDuring が nil でなければ、この tick の観測の途中に停止要求を送る
	stopDuring os.Signal
}

// memoryIssues は in-memory の issue 置き場。
type memoryIssues struct {
	scopeKey string
	observe  func() ([]target.Issue, error)
}

func (m memoryIssues) ScopeKey() string                    { return m.scopeKey }
func (m memoryIssues) OpenIssues() ([]target.Issue, error) { return m.observe() }

// harness は plans を順に 1 tick ずつ回し、読み切ったところで stop の停止要求を送る。
type harness struct {
	plans []tickPlan
	stop  os.Signal
	// 観測の結果
	reads  int
	waits  []time.Duration
	stdout bytes.Buffer
}

func definition(interval time.Duration) workflow.Definition {
	return workflow.Definition{Interval: interval, Triggers: []trigger.Trigger{{Name: "implement", On: trigger.Issue}}}
}

func good(interval time.Duration) func() (workflow.Definition, error) {
	return func() (workflow.Definition, error) { return definition(interval), nil }
}

func (h *harness) run(t *testing.T) []string {
	t.Helper()
	if h.stop == nil {
		h.stop = syscall.SIGINT
	}
	signals := make(chan os.Signal, 1)
	tick := 0
	exit := loop.Run(loop.Options{
		Load: func() (workflow.Definition, error) {
			tick++
			return h.plans[tick-1].load()
		},
		Open: func(workflow.Definition) loop.Issues {
			plan := h.plans[tick-1]
			scope := startScope
			if plan.scope != "" {
				scope = plan.scope
			}
			return memoryIssues{scopeKey: scope, observe: func() ([]target.Issue, error) {
				h.reads++
				if plan.stopDuring != nil {
					signals <- plan.stopDuring
				}
				if plan.fail != nil {
					return nil, plan.fail
				}
				return []target.Issue{{Number: 1}}, nil
			}}
		},
		Interval: time.Minute,
		ScopeKey: startScope,
		Stdout:   &h.stdout,
		Signals:  signals,
		Now:      func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
		After: func(d time.Duration) <-chan time.Time {
			h.waits = append(h.waits, d)
			ch := make(chan time.Time, 1)
			if tick < len(h.plans) {
				ch <- time.Time{}
			} else {
				signals <- h.stop
			}
			return ch
		},
	})
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
	return strings.Split(strings.TrimSpace(h.stdout.String()), "\n")
}

var brokenDefinition = func() (workflow.Definition, error) {
	return workflow.Definition{}, errors.New("x.md: triggers[0].on (5 行目): 未知の値\nx.md: tracker (2 行目): y")
}

func TestTickWithABrokenWorkflowDefinitionNamesTheErrorsOnOneLine(t *testing.T) {
	h := &harness{plans: []tickPlan{{load: brokenDefinition}}}

	lines := h.run(t)

	if !strings.HasSuffix(lines[0], "tick error · workflow 定義の誤り: x.md: triggers[0].on (5 行目): 未知の値 / x.md: tracker (2 行目): y") {
		t.Fatalf("出力:\n%s", h.stdout.String())
	}
}

func TestTickWithABrokenWorkflowDefinitionDoesNotObserve(t *testing.T) {
	h := &harness{plans: []tickPlan{{load: good(time.Minute)}, {load: brokenDefinition}}}

	h.run(t)

	if h.reads != 1 {
		t.Fatalf("置き場を読んだ回数 = %d, want 1 (workflow 定義が誤っている tick は読まない)", h.reads)
	}
}

func TestWaitAfterABrokenWorkflowDefinitionUsesTheLastGoodInterval(t *testing.T) {
	h := &harness{plans: []tickPlan{{load: good(3 * time.Minute)}, {load: brokenDefinition}, {load: good(7 * time.Minute)}}}

	h.run(t)

	if want := []time.Duration{3 * time.Minute, 3 * time.Minute, 7 * time.Minute}; !slices.Equal(h.waits, want) {
		t.Fatalf("待った周期 = %v, want %v", h.waits, want)
	}
}

func TestTickWhoseScopeKeyDiffersFromTheStartNamesBothKeys(t *testing.T) {
	h := &harness{plans: []tickPlan{{load: good(time.Minute), scope: "scope-b"}}}

	lines := h.run(t)

	if !strings.HasSuffix(lines[0], "tick error · workflow 定義の scope key scope-b が起動時の scope-a と違う (loop を起動し直す)") {
		t.Fatalf("出力:\n%s", h.stdout.String())
	}
}

func TestTickWhoseScopeKeyDiffersFromTheStartDoesNotObserve(t *testing.T) {
	h := &harness{plans: []tickPlan{{load: good(time.Minute), scope: "scope-b"}}}

	h.run(t)

	if h.reads != 0 {
		t.Fatalf("scope key が違うのに置き場を読んだ (%d 回)", h.reads)
	}
}

var rateLimited = &target.Failure{Kind: target.RateLimit, Place: "acme/widgets", Err: errors.New("API rate limit exceeded")}

func TestFailedObservationIsReportedWithItsKind(t *testing.T) {
	h := &harness{plans: []tickPlan{{load: good(time.Minute), fail: rateLimited}}}

	lines := h.run(t)

	if !strings.HasSuffix(lines[0], "tick error · 置き場 acme/widgets を観測できない (rate limit): API rate limit exceeded") {
		t.Fatalf("出力:\n%s", h.stdout.String())
	}
}

func TestLoopGoesOnToTheNextPeriodAfterAFailedObservation(t *testing.T) {
	h := &harness{plans: []tickPlan{{load: good(2 * time.Minute), fail: rateLimited}, {load: good(2 * time.Minute)}}}

	lines := h.run(t)

	if !strings.HasSuffix(lines[1], "tick ok · 候補 1: implement issue #1") || !slices.Equal(h.waits, []time.Duration{2 * time.Minute, 2 * time.Minute}) {
		t.Fatalf("待った周期 %v, 出力:\n%s", h.waits, h.stdout.String())
	}
}

func TestStopRequestDuringATickIsTakenAfterTheTickEnds(t *testing.T) {
	h := &harness{plans: []tickPlan{{load: good(time.Minute), stopDuring: syscall.SIGTERM}, {load: good(time.Minute)}}}

	lines := h.run(t)

	if len(lines) != 2 || !strings.HasSuffix(lines[0], "tick ok · 候補 1: implement issue #1") ||
		!strings.HasSuffix(lines[1], "loop を止めた (停止要求 SIGTERM)") || len(h.waits) != 0 {
		t.Fatalf("待った周期 %v, 出力:\n%s", h.waits, h.stdout.String())
	}
}

func TestStopLineNamesTheSignal(t *testing.T) {
	// 3 種の signal を同じに扱うことは、signal の配線ごと black-box テストが見る
	h := &harness{plans: []tickPlan{{load: good(time.Minute)}}, stop: syscall.SIGHUP}

	lines := h.run(t)

	if !strings.HasSuffix(lines[len(lines)-1], "loop を止めた (停止要求 SIGHUP)") {
		t.Fatalf("出力:\n%s", h.stdout.String())
	}
}
