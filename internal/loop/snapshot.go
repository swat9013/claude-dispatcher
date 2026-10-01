package loop

import (
	"cmp"
	"slices"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
)

// board は状態 file にだけ使う状態: loop を始めた時刻・次の tick の予定・直近の tick と、その tick の曖昧な CL。
type board struct {
	startedAt  time.Time
	nextTickAt time.Time
	lastTick   *status.Tick
	ambiguous  []trigger.AmbiguousHead
	// publishError は直近の状態 file の書き出しの失敗。同じ失敗の行を周期ごとに重ねないために持つ
	publishError string
}

// tickFailed は tick の失敗を tick の行に書き、状態 file の直近の tick に残す。失敗した tick は曖昧な CL を数えていない。
func (l *loop) tickFailed(message string) {
	l.board.lastTick = &status.Tick{At: l.o.Now(), Result: status.TickError, Error: message}
	l.board.ambiguous = nil
	l.rec.tickError(message)
}

// publish は今の状態を状態 file に書き出す。書けなければ error の行を残して続ける (状態 file は表示のためだけの痕跡)。
// 同じ失敗が続く間は、行を 1 つだけ残す。
func (l *loop) publish() {
	message := ""
	if err := l.o.Publish(l.snapshot()); err != nil {
		message = oneLine(err)
	}
	if message != "" && message != l.board.publishError {
		l.rec.error(target.Ref{}, message)
	}
	l.board.publishError = message
}

// phases は claim の段階を状態 file の綴りに写す
var phases = map[phase]status.Phase{
	phaseRunning: status.Running, phaseStopping: status.Stopping, phaseAwaitingVerification: status.Verifying, phaseWaitingRetry: status.WaitingRetry,
}

// snapshot は状態 file の中身 (formats.md §7.1)。作業対象は種類と番号の順に並べる。
func (l *loop) snapshot() status.Snapshot {
	s := status.Snapshot{
		Scope: l.o.ScopeKey, Workflow: l.o.Workflow, StartedAt: l.board.startedAt, UpdatedAt: l.o.Now(),
		Stopping: l.stopping > 0, LastTick: l.board.lastTick,
		Workers: []status.Worker{}, Abandoned: []status.Abandoned{}, Ambiguous: []status.Ambiguous{},
	}
	if l.stopping == 0 && !l.board.nextTickAt.IsZero() {
		next := l.board.nextTickAt
		s.NextTickAt = &next
	}
	for _, ref := range sortedRefs(l.claims) {
		c := l.claims[ref]
		w := status.Worker{
			Target: ref.String(), Trigger: c.trigger.Name, Attempt: c.attempt, SessionID: c.sessionID,
			Phase: phases[c.phase], StartedAt: c.startedAt, Activity: c.activity,
		}
		if c.phase == phaseWaitingRetry {
			// 停止要求の後に失敗した claim は再起動を予定しない
			if !c.retryAt.IsZero() {
				retryAt := c.retryAt
				w.RetryAt = &retryAt
			}
		}
		s.Workers = append(s.Workers, w)
	}
	for _, ref := range sortedRefs(l.abandoned) {
		s.Abandoned = append(s.Abandoned, status.Abandoned{Target: ref.String(), Trigger: l.abandoned[ref]})
	}
	for _, a := range l.board.ambiguous {
		s.Ambiguous = append(s.Ambiguous, status.Ambiguous{Head: a.Head, Targets: refNames(a.Targets)})
	}
	return s
}

func sortedRefs[V any](m map[target.Ref]V) []target.Ref {
	refs := make([]target.Ref, 0, len(m))
	for ref := range m {
		refs = append(refs, ref)
	}
	slices.SortFunc(refs, func(a, b target.Ref) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Number, b.Number))
	})
	return refs
}

// refresh は走っている worker の活動を読み、変わっていれば活動の行を出す。終わった worker (run が nil) は読まない。
func (l *loop) refresh() {
	for _, ref := range sortedRefs(l.claims) {
		c := l.claims[ref]
		if c.run == nil {
			continue
		}
		a := c.run.Activity()
		if a.At.IsZero() || (c.activity != nil && c.activity.At.Equal(a.At)) {
			continue
		}
		c.activity = &a
		l.rec.human("活動 %s: %s", ref, a.Summary)
	}
}
