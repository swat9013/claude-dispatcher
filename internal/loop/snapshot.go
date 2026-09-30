package loop

import (
	"cmp"
	"slices"

	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

// tickFailed は tick の失敗を tick の行に書き、状態 file の直近の tick に残す。
func (l *loop) tickFailed(message string) {
	l.lastTick = &status.Tick{At: l.o.Now(), Result: "error", Error: message}
	l.rec.tickError(message)
}

// publish は今の状態を状態 file に書き出す。書けなければ error の行を残して続ける (状態 file は表示のためだけの痕跡)。
func (l *loop) publish() {
	if l.o.Publish == nil {
		return
	}
	if err := l.o.Publish(l.snapshot()); err != nil {
		l.rec.error(target.Ref{}, oneLine(err))
	}
}

// phases は claim の段階を状態 file の綴りに写す
var phases = map[phase]status.Phase{
	phaseRunning: status.Running, phaseStopping: status.Stopping, phaseAwaitingVerification: status.Verifying, phaseWaitingRetry: status.WaitingRetry,
}

// snapshot は状態 file の中身 (formats.md §7.1)。作業対象は種類と番号の順に並べる。
func (l *loop) snapshot() status.Snapshot {
	s := status.Snapshot{
		Scope: l.o.ScopeKey, Workflow: l.o.Workflow, StartedAt: l.startedAt, UpdatedAt: l.o.Now(),
		Stopping: l.stopping > 0, LastTick: l.lastTick,
		Workers: []status.Worker{}, Abandoned: []status.Abandoned{}, Ambiguous: []status.Ambiguous{},
	}
	if l.stopping == 0 && !l.nextTickAt.IsZero() {
		next := l.nextTickAt
		s.NextTickAt = &next
	}
	for _, ref := range sortedRefs(l.claims) {
		c := l.claims[ref]
		w := status.Worker{
			Target: ref.String(), Trigger: c.trigger.Name, Attempt: c.attempt, SessionID: c.sessionID,
			Phase: phases[c.phase], StartedAt: c.startedAt,
		}
		if c.phase == phaseWaitingRetry {
			retryAt := c.retryAt
			w.RetryAt, w.StartedAt = &retryAt, nil
		}
		s.Workers = append(s.Workers, w)
	}
	for _, ref := range sortedRefs(l.abandoned) {
		s.Abandoned = append(s.Abandoned, status.Abandoned{Target: ref.String(), Trigger: l.abandoned[ref]})
	}
	for _, a := range l.ambiguous {
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
