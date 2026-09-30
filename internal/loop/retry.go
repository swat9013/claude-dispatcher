package loop

import (
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// 失敗の扱い (system.md §7、formats.md §6 の再起動): 失敗した worker の次の attempt を backoff の後に起動し、attempt の上限で
// 打ち切る。再起動と打ち切りは loop の memory だけに持つ。attempt の上限・backoff の上限・並列上限は、そのときの workflow
// 定義から読む。

// baseBackoff は 1 回目の失敗の後の backoff
const baseBackoff = 10 * time.Second

// reasonTriggerRemoved は、再起動を待つ間に起動した trigger が workflow 定義から消えたときの理由
const reasonTriggerRemoved = "trigger が workflow 定義から消えた"

// backoff は attempt 回目が失敗した後に待つ時間: min(10s × 2^(attempt−1), limit)。
func backoff(attempt int, limit time.Duration) time.Duration {
	d := baseBackoff
	for i := 1; i < attempt && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}

// fail は失敗で終わった attempt の後始末をする。上限に達していれば打ち切り、達していなければ再起動を予定する。停止要求を
// 受けた後は再起動を予定しない (loop は再起動待ちを error の行に残して止まる)。
func (l *loop) fail(n int, c *claim) {
	name := target.Name(n)
	if c.attempt >= l.def.MaxAttempts {
		delete(l.claims, n)
		l.abandoned[n] = c.trigger.Name
		l.rec.event("abandon", map[string]any{"target": name, "trigger": c.trigger.Name, "attempt": c.attempt, "session_id": c.sessionID},
			"打ち切り %s (%s, attempt %d 回): label を外して trigger から外すと解ける (外してから 1 周期待つ)", name, c.trigger.Name, c.attempt)
		return
	}
	c.phase, c.ended = phaseWaitingRetry, nil
	if l.stopping > 0 {
		return
	}
	wait := backoff(c.attempt, l.def.MaxRetryBackoff)
	c.retryAt = l.o.Now().Add(wait)
	l.rec.event("retry", map[string]any{
		"target": name, "trigger": c.trigger.Name, "attempt": c.attempt, "next_attempt": c.attempt + 1,
		"session_id": c.sessionID, "backoff": wait.Seconds(),
	}, "再起動を予定 %s (%s, attempt %d, %s 後)", name, c.trigger.Name, c.attempt+1, wait)
}

// arm は、最も早い再起動の予定に届く wake を置く。もっと早い予定の wake が既にあれば置き直さない。停止要求の後は置かない。
func (l *loop) arm() {
	if l.stopping > 0 {
		return
	}
	var earliest time.Time
	for _, c := range l.claims {
		if c.phase == phaseWaitingRetry && (earliest.IsZero() || c.retryAt.Before(earliest)) {
			earliest = c.retryAt
		}
	}
	if earliest.IsZero() || (l.wake != nil && !earliest.Before(l.wakeAt)) {
		return
	}
	l.wake, l.wakeAt = l.o.After(max(earliest.Sub(l.o.Now()), 0)), earliest
}

// retry は backoff の明けた再起動待ちの claim を起動する。open は tick が読んだ open な issue の一覧 (tick の外からなら nil)
// で、そこに無い作業対象だけを 1 件ずつ読み直す。空きが無いか、作業対象を読み直せなければ attempt を進めずに待ち直す
// (空きは worker が終わったとき、読み直しは次の tick で試み直す)。作業対象が終端か trigger から外れていれば、起動せずに
// claim を解く。
func (l *loop) retry(issues Issues, open []target.Issue) {
	now := l.o.Now()
	for n, c := range l.claims {
		if c.phase != phaseWaitingRetry || now.Before(c.retryAt) {
			continue
		}
		if l.running() >= l.def.MaxConcurrent {
			if !c.waitingSlot {
				c.waitingSlot = true
				l.rec.human("再起動を待つ %s (%s, attempt %d): 並列上限 %d に空きが出るまで待つ", target.Name(n), c.trigger.Name, c.attempt+1, l.def.MaxConcurrent)
			}
			continue
		}
		current, ok := triggerNamed(l.def, c.trigger.Name)
		if !ok {
			l.releaseWaiting(n, c, reasonTriggerRemoved)
			continue
		}
		issue, err := reread(issues, open, n)
		if err != nil {
			l.rec.error(n, "再起動を待つ作業対象を読み直せない (次の tick で読み直す): "+oneLine(err))
			continue
		}
		c.trigger = current
		if settled := l.settle(n, c, issue); settled != "" {
			l.releaseWaiting(n, c, settled)
			continue
		}
		c.issue, c.waitingSlot = issue, false
		c.attempt++
		l.start(c)
	}
}

// reread は作業対象を読み直す。open な issue の一覧にあればそれを使い、無ければ置き場から 1 件読む。
func reread(issues Issues, open []target.Issue, n int) (target.Issue, error) {
	for _, issue := range open {
		if issue.Number == n {
			return issue, nil
		}
	}
	return issues.Issue(n)
}

// triggerNamed は workflow 定義から名前の trigger を引く。
func triggerNamed(def workflow.Definition, name string) (trigger.Trigger, bool) {
	for _, t := range def.Triggers {
		if t.Name == name {
			return t, true
		}
	}
	return trigger.Trigger{}, false
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
		if stillMatches(def, open, n, name) {
			continue
		}
		delete(l.abandoned, n)
		l.rec.event("unabandon", map[string]any{"target": target.Name(n), "trigger": name},
			"打ち切りを解いた %s (%s)", target.Name(n), name)
	}
}

func stillMatches(def workflow.Definition, open []target.Issue, n int, triggerName string) bool {
	t, ok := triggerNamed(def, triggerName)
	if !ok {
		return false
	}
	for _, issue := range open {
		if issue.Number == n {
			return t.When.Matches(issue)
		}
	}
	return false
}
