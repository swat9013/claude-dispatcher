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
	"github.com/swat9013/claude-dispatcher/internal/precheck"
	"github.com/swat9013/claude-dispatcher/internal/provider"
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
	// nonFatal が nil でなければ、置き場の観測が作業対象とともにこの処理を続けられる失敗を返す
	nonFatal error
	// stopDuring が nil でなければ、この tick の観測の途中に停止要求を送る
	stopDuring os.Signal
	// blocked はこの tick の事前検査に落ちる trigger の名前
	blocked []string
}

// memoryStore は in-memory の置き場。observe は作業対象の種類に依らず同じものを返す。
type memoryStore struct {
	scopeKey func() string
	observe  func() ([]target.Item, error)
	reread   func(ref target.Ref) (target.Item, error)
}

func (m memoryStore) ScopeKey() string                   { return m.scopeKey() }
func (m memoryStore) OpenIssues() ([]target.Item, error) { return m.observe() }
func (m memoryStore) OpenCLs() ([]target.Item, error)    { return m.observe() }
func (m memoryStore) ReadIssue(n int) (target.Item, error) {
	return m.reread(target.Ref{Kind: target.KindIssue, Number: n})
}
func (m memoryStore) ReadCL(n int) (target.Item, error) {
	return m.reread(target.Ref{Kind: target.KindCL, Number: n})
}

// store は m を issue 置き場と CL 置き場の両方の部品にした置き場。
func (m memoryStore) store() loop.Store { return loop.Store{Issues: m, CLs: m} }

// noWorkspaces は workspace を持たない掃除の口。
type noWorkspaces struct{}

func (noWorkspaces) Existing() ([]target.Ref, error)   { return nil, nil }
func (noWorkspaces) Remove(target.Ref) error           { return nil }
func (noWorkspaces) Branch(target.Ref) (string, error) { return "", nil }

// noProblems は、どの trigger も事前検査に通す。
func noProblems(workflow.Definition) []precheck.Problem { return nil }

// endedWorker は起動するとすぐに正常に終わる worker。
type endedWorker struct{}

func (endedWorker) Stop() {}

func (endedWorker) Activity() status.Activity { return status.Activity{} }

// harness は plans を順に 1 tick ずつ回し、読み切ったところで stop の停止要求を送る。
type harness struct {
	plans []tickPlan
	// start は起動時の workflow 定義の版。nil なら definition
	start func(maxConcurrent int) (workflow.Definition, error)
	stop  os.Signal
	// maxConcurrent は並列上限。0 なら worker を起動しない
	maxConcurrent int
	// open は置き場の open な issue の番号 (空なら 1 だけ)
	open []int
	// maxAttempts は attempt の上限。0 なら 1 (1 回目の失敗で打ち切る)
	maxAttempts int
	// rereadFailures は、起動した worker の作業対象の読み直しを最初に何回失敗させるか。起動の直前の読み直しを数えないよう、
	// まだ起動していない作業対象の読み直しを数えない (近似。同じ作業対象を claim を解いてから起動し直す plan では、2 回目の
	// 起動の直前の読み直しも数える)
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
	return workflow.Definition{Tracker: githubTracker, Interval: interval, MaxConcurrent: maxConcurrent, MaxAttempts: 1, MaxRetryBackoff: 5 * time.Minute,
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
	start := definition(time.Minute, h.maxConcurrent)
	if h.start != nil {
		start, _ = h.start(h.maxConcurrent)
	}
	exit := loop.Run(loop.Options{
		Load: func() (workflow.Definition, error) {
			tick++
			def, err := h.plans[tick-1].load(h.maxConcurrent)
			return h.withAttempts(def), err
		},
		Definition: h.withAttempts(start),
		Precheck: func(workflow.Definition) []precheck.Problem {
			var problems []precheck.Problem
			for _, name := range h.plans[tick-1].blocked {
				problems = append(problems, precheck.Problem{Trigger: name, Error: "見つからない"})
			}
			return problems
		},
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
				return issues, plan.nonFatal
			}, reread: func(ref target.Ref) (target.Item, error) {
				if !slices.ContainsFunc(h.jobs, func(j worker.Job) bool { return j.Item.Ref() == ref }) {
					return target.Issue{Number: ref.Number}, nil
				}
				h.rereads++
				if h.rereads <= h.rereadFailures {
					return nil, errors.New("gh の失敗")
				}
				return target.Issue{Number: ref.Number}, nil
			}}.store()
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
		Output:       loop.Appender{W: &h.stdout},
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

// jiraWithCL は cl: に GitLab の path の CL 置き場を宣言した jira の定義の版。
func jiraWithCL(path string) func(int) (workflow.Definition, error) {
	return func(maxConcurrent int) (workflow.Definition, error) {
		def := definition(time.Minute, maxConcurrent)
		jira, _ := provider.Of(provider.Jira)
		gitlab, _ := provider.Of(provider.GitLab)
		def.Tracker = workflow.Tracker{Kind: provider.Jira, Place: jira.NewPlace("acme.atlassian.net", "WIDGETS")}
		def.CL = &workflow.CL{Kind: provider.GitLab, Place: gitlab.NewPlace("gitlab.com", path)}
		return def, nil
	}
}

func TestTickWhoseCLPlaceDiffersFromTheStartDoesNotObserve(t *testing.T) {
	// scope key は plan が決めるので、どの版も起動時と同じ scope key を返す
	for name, start := range map[string]func(int) (workflow.Definition, error){
		"CL 置き場の種類が変わる (github の issue 置き場と同じ場所 → jira の cl: の gitlab)": good(time.Minute),
		"cl: を別の置き場にする": jiraWithCL("acme/other"),
	} {
		t.Run(name, func(t *testing.T) {
			h := &harness{start: start, plans: []tickPlan{{load: jiraWithCL("acme/widgets")}}}

			lines := h.run(t)

			if !strings.Contains(lines[0], "tick error · workflow 定義の CL 置き場") || h.reads != 0 {
				t.Fatalf("CL 置き場が違う版を採った (読んだ回数 %d):\n%s", h.reads, h.stdout.String())
			}
		})
	}
}

func TestTickWhoseCLDeclarationIsUnchangedObserves(t *testing.T) {
	h := &harness{start: jiraWithCL("acme/widgets"), plans: []tickPlan{{load: jiraWithCL("acme/widgets")}}}

	h.run(t)

	if h.reads == 0 {
		t.Fatalf("CL 置き場が同じ版を採らなかった:\n%s", h.stdout.String())
	}
}

// githubAt は tracker.repo に repo を書いた github の定義の版。
func githubAt(repo string) func(int) (workflow.Definition, error) {
	return func(maxConcurrent int) (workflow.Definition, error) {
		def := definition(time.Minute, maxConcurrent)
		github, _ := provider.Of(provider.GitHub)
		def.Tracker = workflow.Tracker{Kind: provider.GitHub, Place: github.NewPlace("", repo)}
		return def, nil
	}
}

// 大文字と小文字だけの書き換えは scope key と同じく同じ置き場とみなす (formats.md §6 の tick)。
func TestTickWhoseCLPlaceDiffersOnlyInCaseObserves(t *testing.T) {
	for name, c := range map[string]struct {
		start, reloaded func(int) (workflow.Definition, error)
	}{
		"github の tracker.repo": {githubAt("acme/widgets"), githubAt("Acme/Widgets")},
		"jira の cl.repo":        {jiraWithCL("acme/widgets"), jiraWithCL("Acme/Widgets")},
	} {
		t.Run(name, func(t *testing.T) {
			h := &harness{start: c.start, plans: []tickPlan{{load: c.reloaded}}}

			h.run(t)

			if h.reads == 0 {
				t.Fatalf("大文字と小文字だけが違う CL 置き場の版を採らなかった:\n%s", h.stdout.String())
			}
		})
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

func TestNonFatalObservationFailureLeavesAnErrorLineAndTheTickGoesOn(t *testing.T) {
	recheckFailed := &target.NonFatal{Place: "acme/widgets", Err: errors.New("HTTP 502")}
	h := &harness{plans: []tickPlan{{load: good(time.Minute), nonFatal: recheckFailed}}}

	h.run(t)

	out := h.stdout.String()
	if !strings.Contains(out, "error: 置き場 acme/widgets の補助の要求が落ちた: HTTP 502") || !strings.Contains(out, "tick ok · 候補 1: implement issue #1") {
		t.Fatalf("出力:\n%s", out)
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
		Precheck:   noProblems,
		Store: func(workflow.Definition) loop.Store {
			return memoryStore{scopeKey: func() string { return startScope }, observe: func() ([]target.Item, error) { return nil, nil }}.store()
		},
		Workspaces:   func(workflow.Definition) loop.Workspaces { return noWorkspaces{} },
		NewSessionID: func() (string, error) { return "", nil },
		ScopeKey:     startScope,
		Log:          failingWriter{},
		Output:       loop.Appender{W: &stdout},
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

// lastPublishedAfterAStopRequest は、tick の途中で停止要求を受け、走っていた worker が失敗して止まった loop の、
// 最後に書き出した状態。
func lastPublishedAfterAStopRequest(t *testing.T) status.Snapshot {
	t.Helper()
	h := &harness{plans: []tickPlan{{load: good(time.Minute), stopDuring: syscall.SIGINT}}, maxConcurrent: 1, maxAttempts: 2}
	h.run(t)
	return h.published[len(h.published)-1]
}

func TestStatusAfterAStopRequestHasNoNextTick(t *testing.T) {
	last := lastPublishedAfterAStopRequest(t)

	if !last.Stopping || last.NextTickAt != nil {
		t.Fatalf("状態 = %+v, want 停止待ちで次の tick が無い", last)
	}
}

func TestWorkerThatFailsAfterAStopRequestHasNoRestartInTheStatus(t *testing.T) {
	last := lastPublishedAfterAStopRequest(t)

	if len(last.Workers) != 1 || last.Workers[0].Phase != status.WaitingRetry || last.Workers[0].RetryAt != nil || last.Workers[0].StartedAt != nil {
		t.Fatalf("worker = %+v, want 再起動の予定も起動の時刻も無い再起動待ち", last.Workers)
	}
}

// blockedWhileRetryIsDue は、周期 5s・backoff 10s で、2 回目と 3 回目の tick (5s・10s) で implement が事前検査に落ち、
// 4 回目で直る harness。再起動の予定 (10s) は落ちている間に明ける。
func blockedWhileRetryIsDue() *harness {
	return &harness{plans: []tickPlan{
		{load: good(5 * time.Second)}, {load: good(5 * time.Second), blocked: []string{"implement"}},
		{load: good(5 * time.Second), blocked: []string{"implement"}}, {load: good(5 * time.Second)},
	}, maxConcurrent: 1, maxAttempts: 2}
}

func TestRetryOfATriggerThatFailsThePrecheckRestartsWhenItPasses(t *testing.T) {
	h := blockedWhileRetryIsDue()

	h.run(t)

	if len(h.jobs) != 2 || h.jobs[1].Attempt != 2 {
		t.Fatalf("起動 = %+v, want 直った後に attempt 2 を 1 回", h.jobs)
	}
}

func TestRetryOfATriggerThatFailsThePrecheckIsNotScheduledWhileItFails(t *testing.T) {
	h := blockedWhileRetryIsDue()

	h.run(t)

	if slices.Contains(h.waits, 0) {
		t.Fatalf("待ち = %v, 事前検査に落ちている間に明けた予定で回り直した", h.waits)
	}
}

func TestBackoffDoublesAfterEachFailedAttempt(t *testing.T) {
	h := &harness{plans: []tickPlan{{load: good(time.Minute)}, {load: good(time.Minute)}, {load: good(time.Minute)}, {load: good(time.Minute)}}, maxConcurrent: 1, maxAttempts: 3}

	h.run(t)

	if !strings.Contains(h.stdout.String(), "再起動を予定 issue#1 (implement, attempt 3, 20s 後)") {
		t.Fatalf("出力:\n%s", h.stdout.String())
	}
}

func TestTickLineNamesAMergeRequestWithItsGitLabReference(t *testing.T) {
	candidates := []trigger.Candidate{
		{Trigger: &trigger.Trigger{Name: "implement", On: target.KindIssue}, Item: target.Issue{Number: 42}},
		{Trigger: &trigger.Trigger{Name: "fix-ci", On: target.KindCL}, Item: target.CL{Number: 7}},
	}

	for kind, want := range map[provider.Kind]string{
		provider.GitHub: "候補 2: implement issue #42, fix-ci cl #7",
		provider.GitLab: "候補 2: implement issue #42, fix-ci cl !7",
	} {
		if got := loop.Summary(candidates, workflow.Definition{Tracker: workflow.Tracker{Kind: kind}}); got != want {
			t.Errorf("%s: Summary = %q, want %q", kind, got, want)
		}
	}
}

// githubTracker は、テストの workflow 定義の tracker (作業対象を人が読む行に出す呼び方を決める)。
var githubTracker = workflow.Tracker{Kind: provider.GitHub}
