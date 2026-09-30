package loop_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/worker"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// 周期を待つ振る舞い (tick ごとの読み直し・周期の選び方・停止)。周期を分の単位で選ぶ振る舞いは black-box テストが待てないので、ここで見る。

const startScope = "scope-a"

// tickPlan は 1 回の tick が読むもの。
type tickPlan struct {
	load func(maxConcurrent int) (workflow.Definition, error)
	// scope はその版から組み立てた issue 置き場の scope key (空なら起動時と同じ)
	scope string
	// fail が nil でなければ、置き場の観測がこの失敗を返す
	fail error
	// stopDuring が nil でなければ、この tick の観測の途中に停止要求を送る
	stopDuring os.Signal
}

// memoryStore は in-memory の issue 置き場。
type memoryStore struct {
	scopeKey func() string
	observe  func() ([]target.Item, error)
	reread   func(ref target.Ref) (target.Item, error)
}

func (m memoryStore) ScopeKey() string                         { return m.scopeKey() }
func (m memoryStore) Open(target.Kind) ([]target.Item, error)  { return m.observe() }
func (m memoryStore) Read(ref target.Ref) (target.Item, error) { return m.reread(ref) }

// noWorkspaces は workspace を持たない掃除の口。
type noWorkspaces struct{}

func (noWorkspaces) Existing() ([]target.Ref, error)   { return nil, nil }
func (noWorkspaces) Remove(target.Ref) error           { return nil }
func (noWorkspaces) Branch(target.Ref) (string, error) { return "", nil }

// endedWorker は起動するとすぐに正常に終わる worker。
type endedWorker struct{}

func (endedWorker) Stop() {}

func (endedWorker) Activity() status.Activity { return status.Activity{} }

// harness は plans を順に 1 tick ずつ回し、読み切ったところで stop の停止要求を送る。
type harness struct {
	plans []tickPlan
	stop  os.Signal
	// maxConcurrent は並列上限。0 なら worker を起動しない
	maxConcurrent int
	// open は置き場の open な issue の番号 (空なら 1 だけ)
	open []int
	// maxAttempts は attempt の上限。0 なら 1 (1 回目の失敗で打ち切る)
	maxAttempts int
	// rereadFailures は、終わった worker の作業対象の読み直しを最初に何回失敗させるか
	rereadFailures int
	// inflight は、起動した worker のうち event を loop が受け取り終えていない数。周期と再起動の予定はそれを待ってから進める
	inflight int
	settled  *sync.Cond
	stopOnce sync.Once
	// clock は loop の時計。周期と再起動の予定が届くたびに、待った分だけ進める
	mu    sync.Mutex
	clock time.Time
	// 観測の結果
	reads   int
	rereads int
	jobs    []worker.Job
	waits   []time.Duration
	stdout  bytes.Buffer
	// published は書き出した状態 file の中身
	published []status.Snapshot
}

func definition(interval time.Duration, maxConcurrent int) workflow.Definition {
	return workflow.Definition{Interval: interval, MaxConcurrent: maxConcurrent, MaxAttempts: 1, MaxRetryBackoff: 5 * time.Minute,
		Triggers: []trigger.Trigger{{Name: "implement", On: target.KindIssue}}}
}

func (h *harness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

// begin と done は起動した worker の event の受け渡しを数え、waitSettled は受け渡しが無くなるまで待つ。
func (h *harness) begin() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inflight++
}

func (h *harness) done() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inflight--
	h.settled.Broadcast()
}

func (h *harness) waitSettled() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for h.inflight > 0 {
		h.settled.Wait()
	}
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clock = h.clock.Add(d)
}

// withAttempts は harness の attempt の上限を定義に写す。
func (h *harness) withAttempts(def workflow.Definition) workflow.Definition {
	def.MaxAttempts = max(h.maxAttempts, 1)
	return def
}

func (h *harness) run(t *testing.T) []string {
	t.Helper()
	if h.stop == nil {
		h.stop = syscall.SIGINT
	}
	signals := make(chan os.Signal, 4)
	h.clock = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.settled = sync.NewCond(&h.mu)
	tick := 0
	exit := loop.Run(loop.Options{
		Load: func() (workflow.Definition, error) {
			tick++
			def, err := h.plans[tick-1].load(h.maxConcurrent)
			return h.withAttempts(def), err
		},
		Definition: h.withAttempts(definition(time.Minute, h.maxConcurrent)),
		Store: func(workflow.Definition) loop.Store {
			// scope key と観測は、その tick が読み直した workflow 定義の plan で答える
			return memoryStore{scopeKey: func() string {
				if scope := h.plans[tick-1].scope; scope != "" {
					return scope
				}
				return startScope
			}, observe: func() ([]target.Item, error) {
				plan := h.plans[tick-1]
				h.reads++
				if plan.stopDuring != nil {
					signals <- plan.stopDuring
				}
				if plan.fail != nil {
					return nil, plan.fail
				}
				numbers := h.open
				if len(numbers) == 0 {
					numbers = []int{1}
				}
				issues := make([]target.Item, len(numbers))
				for i, n := range numbers {
					issues[i] = target.Issue{Number: n}
				}
				return issues, nil
			}, reread: func(ref target.Ref) (target.Item, error) {
				h.rereads++
				if h.rereads <= h.rereadFailures {
					return nil, errors.New("gh の失敗")
				}
				return target.Issue{Number: ref.Number}, nil
			}}
		},
		Workspaces: func(workflow.Definition) loop.Workspaces { return noWorkspaces{} },
		Launch: func(_ workflow.Definition, job worker.Job, events chan<- worker.Event) loop.Worker {
			h.jobs = append(h.jobs, job)
			h.begin()
			go func() {
				defer h.done()
				events <- worker.Started{Target: job.Item.Ref(), PID: 1}
				code := 0
				events <- worker.Ended{Target: job.Item.Ref(), Result: worker.Result{ExitCode: &code}}
			}()
			return endedWorker{}
		},
		NewSessionID: func() (string, error) { return "session-1", nil },
		ScopeKey:     startScope,
		Log:          io.Discard,
		Stdout:       &h.stdout,
		Publish: func(s status.Snapshot) error {
			h.published = append(h.published, s)
			return nil
		},
		Signals: signals,
		Now:     h.now,
		After: func(d time.Duration) <-chan time.Time {
			h.waits = append(h.waits, d)
			ch := make(chan time.Time, 1)
			last := tick >= len(h.plans)
			go func() {
				h.waitSettled()
				if last {
					h.stopOnce.Do(func() { signals <- h.stop })
					return
				}
				h.advance(d)
				ch <- time.Time{}
			}()
			return ch
		},
	})
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
	return strings.Split(strings.TrimSpace(h.stdout.String()), "\n")
}

func good(interval time.Duration) func(int) (workflow.Definition, error) {
	return func(maxConcurrent int) (workflow.Definition, error) { return definition(interval, maxConcurrent), nil }
}

var brokenDefinition = func(int) (workflow.Definition, error) {
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

func rereadFailsOnce() *harness {
	return &harness{plans: []tickPlan{{load: good(time.Minute)}, {load: good(time.Minute)}, {load: good(time.Minute)}}, maxConcurrent: 1, rereadFailures: 1}
}

func TestEndedWorkerWhoseIssueCannotBeReadLeavesAnErrorLine(t *testing.T) {
	h := rereadFailsOnce()

	h.run(t)

	if !strings.Contains(h.stdout.String(), "error issue#1: 終わった worker の作業対象を読み直せない (次の tick で読み直す)") {
		t.Fatalf("出力:\n%s", h.stdout.String())
	}
}

func TestEndedWorkerWhoseIssueCannotBeReadIsCheckedAgainOnTheNextTick(t *testing.T) {
	h := rereadFailsOnce()

	lines := h.run(t)

	var ends []string
	for _, line := range lines {
		if strings.Contains(line, " 終了 issue#1") {
			ends = append(ends, line)
		}
	}
	if len(ends) == 0 || !strings.HasSuffix(ends[0], "終了 issue#1 (implement): failed — trigger に当たったまま") {
		t.Fatalf("出力:\n%s", h.stdout.String())
	}
}

// rereadAlwaysFails は、読み直しを毎回失敗させる rereadFailures
const rereadAlwaysFails = 1 << 30

func TestWorkerWaitingToBeCheckedDoesNotTakeASlot(t *testing.T) {
	h := &harness{plans: []tickPlan{{load: good(time.Minute)}, {load: good(time.Minute)}}, maxConcurrent: 1, open: []int{1, 2}, rereadFailures: rereadAlwaysFails}

	h.run(t)

	if len(h.jobs) != 2 {
		t.Fatalf("起動した数 = %d, want 2 (確かめ待ちの issue#1 が並列の枠を塞いだ)", len(h.jobs))
	}
}

// failingWriter は書き込みを必ず失敗させる log.jsonl の代役。
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestLineThatCannotBeWrittenToTheLogIsReportedOnStdout(t *testing.T) {
	var stdout bytes.Buffer
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGINT

	loop.Run(loop.Options{
		Load:       func() (workflow.Definition, error) { return definition(time.Minute, 0), nil },
		Definition: definition(time.Minute, 0),
		Store: func(workflow.Definition) loop.Store {
			return memoryStore{scopeKey: func() string { return startScope }, observe: func() ([]target.Item, error) { return nil, nil }}
		},
		Workspaces:   func(workflow.Definition) loop.Workspaces { return noWorkspaces{} },
		NewSessionID: func() (string, error) { return "", nil },
		ScopeKey:     startScope,
		Log:          failingWriter{},
		Stdout:       &stdout,
		Publish:      func(status.Snapshot) error { return nil },
		Signals:      signals,
		Now:          time.Now,
		After:        func(time.Duration) <-chan time.Time { return nil },
	})

	if !strings.Contains(stdout.String(), "log.jsonl に tick の行を書けない: disk full") {
		t.Fatalf("出力:\n%s", stdout.String())
	}
}

func TestFailedAttemptIsRetriedAfterTenSecondsInTheSameSession(t *testing.T) {
	h := &harness{plans: []tickPlan{{load: good(time.Minute)}, {load: good(time.Minute)}, {load: good(time.Minute)}}, maxConcurrent: 1, maxAttempts: 2}

	h.run(t)

	if len(h.jobs) != 2 || h.jobs[1].Attempt != 2 || !h.jobs[1].Resume || h.jobs[1].SessionID != h.jobs[0].SessionID {
		t.Fatalf("起動 = %+v, want 同じ session を続ける attempt 2", h.jobs)
	}
	if !strings.Contains(h.stdout.String(), "再起動を予定 issue#1 (implement, attempt 2, 10s 後)") {
		t.Fatalf("出力:\n%s", h.stdout.String())
	}
}

func TestAttemptThatFailsAtTheLimitIsAbandoned(t *testing.T) {
	h := &harness{plans: []tickPlan{{load: good(time.Minute)}, {load: good(time.Minute)}, {load: good(time.Minute)}}, maxConcurrent: 1, maxAttempts: 2}

	h.run(t)

	if !strings.Contains(h.stdout.String(), "打ち切り issue#1 (implement, attempt 2 回)") {
		t.Fatalf("出力:\n%s", h.stdout.String())
	}
}

func TestWorkerThatFailsAfterAStopRequestIsNotScheduledForRetry(t *testing.T) {
	h := &harness{plans: []tickPlan{{load: good(time.Minute), stopDuring: syscall.SIGINT}}, maxConcurrent: 1, maxAttempts: 2}

	h.run(t)

	if out := h.stdout.String(); strings.Contains(out, "再起動を予定") || !strings.Contains(out, "error issue#1: 再起動を待ったまま止まる") {
		t.Fatalf("出力:\n%s", out)
	}
}

func TestStatusAfterAStopRequestHasNoNextTickAndNoRestart(t *testing.T) {
	h := &harness{plans: []tickPlan{{load: good(time.Minute), stopDuring: syscall.SIGINT}}, maxConcurrent: 1, maxAttempts: 2}

	h.run(t)

	last := h.published[len(h.published)-1]
	if !last.Stopping || last.NextTickAt != nil || len(last.Workers) != 1 {
		t.Fatalf("状態 = %+v, want 停止待ちで次の tick の無い 1 件", last)
	}
	if w := last.Workers[0]; w.Phase != status.WaitingRetry || w.RetryAt != nil || w.StartedAt != nil {
		t.Fatalf("worker = %+v, want 再起動の予定も起動の時刻も無い再起動待ち", w)
	}
}

func TestBackoffDoublesAfterEachFailedAttempt(t *testing.T) {
	h := &harness{plans: []tickPlan{{load: good(time.Minute)}, {load: good(time.Minute)}, {load: good(time.Minute)}, {load: good(time.Minute)}}, maxConcurrent: 1, maxAttempts: 3}

	h.run(t)

	if !strings.Contains(h.stdout.String(), "再起動を予定 issue#1 (implement, attempt 3, 20s 後)") {
		t.Fatalf("出力:\n%s", h.stdout.String())
	}
}
