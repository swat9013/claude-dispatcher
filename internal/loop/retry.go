package loop

import (
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// 失敗の扱い (system.md §7、formats.md §6 の再起動): 失敗した worker の次の attempt を backoff の後に起動し、attempt の上限で
// 打ち切る。再起動と打ち切りは loop の memory だけに持つ。

// baseBackoff は 1 回目の失敗の後の backoff
const baseBackoff = 10 * time.Second

// Backoff は attempt 回目が失敗した後に待つ時間: min(10s × 2^(attempt−1), limit)。
func Backoff(attempt int, limit time.Duration) time.Duration {
	d := baseBackoff
	for i := 1; i < attempt && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}

// fail は失敗で終わった attempt の後始末をする。上限に達していれば打ち切り、達していなければ再起動を予定する。
func (l *loop) fail(n int, c *claim) {
	name := target.Name(n)
	if c.attempt >= l.def.MaxAttempts {
		delete(l.claims, n)
		l.abandoned[n] = c.trigger.Name
		l.rec.event("abandon", map[string]any{"target": name, "trigger": c.trigger.Name, "attempts": c.attempt, "session_id": c.sessionID},
			"打ち切り %s (%s, attempt %d 回): trigger から外すと解ける (外してから 1 周期待つ)", name, c.trigger.Name, c.attempt)
		return
	}
	backoff := Backoff(c.attempt, l.def.MaxRetryBackoff)
	c.phase, c.ended, c.retryAt = phaseWaitingRetry, nil, l.o.Now().Add(backoff)
	l.rec.event("retry", map[string]any{
		"target": name, "trigger": c.trigger.Name, "attempt": c.attempt + 1, "session_id": c.sessionID, "backoff": int(backoff / time.Second),
	}, "再起動を予定 %s (%s, attempt %d, %s 後)", name, c.trigger.Name, c.attempt+1, backoff)
	l.schedule(c.retryAt)
}

// schedule は再起動の予定 at に届く wake を置く。もっと早い予定の wake が既にあれば置き直さない。
func (l *loop) schedule(at time.Time) {
	if l.wake != nil && !at.Before(l.wakeAt) {
		return
	}
	l.wake, l.wakeAt = l.o.After(max(at.Sub(l.o.Now()), 0)), at
}

// retry は backoff の明けた再起動待ちの claim を起動する。空きが無いか、作業対象を読み直せなければ attempt を進めずに待ち直す
// (空きは worker が終わったとき、読み直しは次の tick で試み直す)。作業対象が終端か trigger から外れていれば、起動せずに claim を解く。
func (l *loop) retry(issues Issues) {
	now := l.o.Now()
	for n, c := range l.claims {
		if c.phase != phaseWaitingRetry {
			continue
		}
		if now.Before(c.retryAt) {
			l.schedule(c.retryAt)
			continue
		}
		if l.running() >= l.def.MaxConcurrent {
			continue
		}
		issue, err := issues.Issue(n)
		if err != nil {
			l.rec.error(n, "再起動を待つ作業対象を読み直せない (次の tick で読み直す): "+oneLine(err))
			continue
		}
		switch {
		case issue.Closed:
			l.remove(l.o.Workspaces(c.def), n)
			l.releaseWaiting(n, c, reasonTerminal)
		case !c.trigger.When.Matches(issue):
			l.releaseWaiting(n, c, reasonLeftTrigger)
		default:
			c.issue = issue
			c.attempt++
			l.start(c)
		}
	}
}

// releaseWaiting は再起動を待つ claim を、起動せずに解く。
func (l *loop) releaseWaiting(n int, c *claim, reason string) {
	delete(l.claims, n)
	name := target.Name(n)
	l.rec.event("release", map[string]any{"target": name, "trigger": c.trigger.Name, "attempt": c.attempt, "session_id": c.sessionID, "reason": reason},
		"再起動せずに解いた %s (%s): %s", name, c.trigger.Name, reason)
}

// waitingRetry は再起動待ちの claim の数。
func (l *loop) waitingRetry() int {
	n := 0
	for _, c := range l.claims {
		if c.phase == phaseWaitingRetry {
			n++
		}
	}
	return n
}

// clearAbandoned は、打ち切った作業対象のうち、打ち切ったときの trigger の述語に当たらなくなったものの打ち切りを解く。
// open な issue の一覧に無いもの (終端) と、trigger が workflow 定義から消えたものも解く。
func (l *loop) clearAbandoned(def workflow.Definition, open []target.Issue) {
	for n, name := range l.abandoned {
		if l.stillMatches(def, open, n, name) {
			continue
		}
		delete(l.abandoned, n)
		l.rec.event("unabandon", map[string]any{"target": target.Name(n), "trigger": name},
			"打ち切りを解いた %s (%s)", target.Name(n), name)
	}
}

func (l *loop) stillMatches(def workflow.Definition, open []target.Issue, n int, triggerName string) bool {
	var when *trigger.IssuePredicate
	for i := range def.Triggers {
		if def.Triggers[i].Name == triggerName {
			when = &def.Triggers[i].When
		}
	}
	if when == nil {
		return false
	}
	for _, issue := range open {
		if issue.Number == n {
			return when.Matches(issue)
		}
	}
	return false
}
