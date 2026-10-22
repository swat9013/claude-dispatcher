package loop

import (
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
)

// apply は claim の遷移の入口 (claim.go) が返した effect を実行する。claim の記録 (l.claims) から claim を消すのはここだけ。
// readItem は呼び出し側が受ける (読み直しに使う置き場を呼び出し側が持つため)。
func (l *loop) apply(ref target.Ref, c *claim, e effect) {
	if e.removeWorkspace {
		l.remove(l.o.Workspaces(l.definitionOf(c)), ref)
	}
	for _, line := range e.lines {
		if line.errorMessage != "" {
			l.rec.error(ref, line.errorMessage)
			continue
		}
		l.rec.event(line.event, line.fields, line.kind, line.label, line.format, line.args...)
	}
	if e.stopWorker {
		c.run.Stop()
	}
	if e.startWorker {
		l.start(c)
	}
	if e.exit == stay {
		return
	}
	l.releaseRemote(ref, c)
	delete(l.claims, ref)
	if e.exit == abandon {
		l.abandoned[ref] = c.trigger.Name
	}
}

// read は作業対象を置き場から読み直し、observe で観測する。読み直せなければ読み直しの失敗の観測で、item は nil。
func (l *loop) read(store Store, ref target.Ref, observe func(target.Item) trigger.Observation) (observation, target.Item) {
	item, err := store.Read(ref)
	if err != nil {
		return observation{readErr: err}, nil
	}
	return observation{observed: observe(item)}, item
}

// terminalOnly は走っている worker の作業対象の観測。走っている間の読み直しは終端かどうかだけを見る (ADR 0009)。
func terminalOnly(item target.Item) trigger.Observation {
	if item.Terminal() {
		return trigger.ObservedTerminal
	}
	return trigger.ObservedStillMatches
}

// env は claim の遷移の入口に渡す、loop のそのときの値。
func (l *loop) env() claimEnv {
	return claimEnv{now: l.o.Now(), stop: l.stop, maxAttempts: l.def.MaxAttempts, maxRetryBackoff: l.def.MaxRetryBackoff, maxConcurrent: l.def.MaxConcurrent}
}
