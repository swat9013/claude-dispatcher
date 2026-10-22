package loop

import (
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// 失敗の扱い (system.md §7、formats.md §6 の再起動): 失敗した worker の次の attempt を backoff の後に起動し、attempt の上限で
// 打ち切る。再起動と打ち切りは loop の memory だけに持つ。attempt の上限・backoff の上限・並列上限は、そのときの workflow
// 定義から読む (並列上限は workflow 定義に書けず、max-concurrent で渡した値、無ければ loop の引数 --max-concurrent の値。
// system.md §8)。

// baseBackoff は 1 回目の失敗の後の backoff
const baseBackoff = 10 * time.Second

// backoff は attempt 回目が失敗した後に待つ時間: min(10s × 2^(attempt−1), limit)。
func backoff(attempt int, limit time.Duration) time.Duration {
	d := baseBackoff
	for i := 1; i < attempt && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}

// arm は、最も早い再起動の予定に届く wake を置く。もっと早い予定の wake が既にあれば置き直さない。停止要求の後は置かない。
// retry が直近に見渡した時刻までに予定の明けた claim には置かない。その時に試みて起動できなかった claim は、tick と worker
// の終わりに試み直す (置き直すと、tick の外では起動できない CL の claim などで wake が回り続ける)。
func (l *loop) arm() {
	if l.stop.requested() {
		return
	}
	var earliest time.Time
	for _, c := range l.claims {
		if l.restartable(c) && c.retryAt.After(l.retriedAt) && (earliest.IsZero() || c.retryAt.Before(earliest)) {
			earliest = c.retryAt
		}
	}
	if earliest.IsZero() || (l.wake != nil && !earliest.Before(l.wakeAt)) {
		return
	}
	l.wake, l.wakeAt = l.o.After(max(earliest.Sub(l.o.Now()), 0)), earliest
}

// retry は backoff の明けた再起動待ちの claim を起動する。v は tick が読んだ view (tick の外からなら outsideTick) で、
// CL の曖昧さと branch を確かめるのに使う。作業対象は tick の中でも一覧を使わず、置き場から 1 件ずつ読み直す (一覧の
// 検索は古い結果を返しうる。formats.md §6 の再起動)。空きが
// 無いか、作業対象を読み直せないか、当たるかをまだ決められないか、CL が tick の一覧で曖昧に見えるか、CL の head branch
// を別の claim が checkout していれば、attempt を進めずに待ち直す (空きは worker が終わったとき、それ以外は次の tick で
// 試み直す)。曖昧に見えても claim は解かない (理由は formats.md §6 の再起動)。作業対象が終端か、trigger から外れて
// いれば、起動せずに claim を解く (release の行を残す)。宣言順で先の trigger に当たるようになったかは確かめない (再起動
// は同じ trigger で続ける。system.md §7)。CL の再起動は、曖昧さと branch を確かめられる tick の中でだけ試みる。
func (l *loop) retry(store Store, v *view) {
	now := l.o.Now()
	l.retriedAt = now
	if l.orphansUnknown {
		// どの workspace の claude が生きているかを決められない間は再起動しない (formats.md §6 の orphan)。見渡した時刻は
		// 進めるので、明けた予定で wake を置き直し続けない
		return
	}
	for ref, c := range l.claims {
		if c.phase != phaseWaitingRetry || now.Before(c.retryAt) {
			continue
		}
		if l.slotsInUse() >= l.def.MaxConcurrent {
			l.apply(ref, c, c.backoffElapsed(ref, hindranceNoSlot, nil, l.env()))
			continue
		}
		current, ok := triggerNamed(l.def, c.trigger.Name)
		if !ok {
			l.apply(ref, c, c.backoffElapsed(ref, hindranceTriggerRemoved, nil, l.env()))
			continue
		}
		if l.remote != nil && v == outsideTick {
			// 調停 remote を指した loop は、乗っ取られたかを tick の突き合わせで確かめてから再起動する (system.md §7)
			continue
		}
		if ref.Kind == target.KindCL && v == outsideTick {
			// CL の曖昧さと branch を確かめられないので、backoff の明けを tick の中まで持ち越す
			continue
		}
		o, item := l.read(store, ref, current.Observe)
		o.current = &current
		l.apply(ref, c, c.observed(ref, o, l.env()))
		if o.readErr != nil || o.observed != trigger.ObservedStillMatches {
			// 当たったままのときだけ backoff の明けへ進む (ほかは観測の入口が待ち直すか claim を解いた)
			continue
		}
		h := hindranceNone
		if cl, isCL := item.(target.CL); isCL && v.ambiguous[ref] {
			h = hindranceAmbiguousCL
		} else if isCL && v.branchHeld(cl, ref) {
			h = hindranceBranchHeld
		} else if !l.restartable(c) {
			// 事前検査に落ちている trigger は、直るまで attempt を進めずに待ち直す (tick が確かめ直す)
			h = hindrancePrecheck
		} else if l.remote != nil && l.remote.newer != nil {
			// 調停 remote に新しい版の ref がある間は、attempt を進めずに待ち直す (formats.md §12)
			h = hindranceNewerFormat
		}
		l.apply(ref, c, c.backoffElapsed(ref, h, item, l.env()))
	}
}

// findItem は open な一覧から作業対象を探す。
func findItem(open []target.Item, ref target.Ref) (target.Item, bool) {
	for _, item := range open {
		if item.Ref() == ref {
			return item, true
		}
	}
	return nil, false
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

// restartable は、再起動待ちの claim のうち、起動できるもの (trigger が事前検査に落ちていない)。落ちている trigger の
// claim は、直るまで並列の枠にも再起動の予定にも数えない (枠を塞いで他の trigger を止めず、明けた予定で回り続けない)。
// retry は、落ちている間も終端や trigger から外れたことを確かめて claim を解くので、この述語で絞らずに読み直す。
func (l *loop) restartable(c *claim) bool {
	return c.phase == phaseWaitingRetry && !l.isBlocked(c.trigger.Name)
}

// waitingRetry は起動できる再起動待ちの claim の数。
func (l *loop) waitingRetry() int {
	n := 0
	for _, c := range l.claims {
		if l.restartable(c) {
			n++
		}
	}
	return n
}

// clearAbandoned は、打ち切った作業対象のうち、終端になったか、打ち切ったときの trigger の述語に当たらなくなったものの
// 打ち切りを解く。trigger が workflow 定義から消えたものも解く。曖昧な CL になったことでは解かない (formats.md §6 の
// tick の手順 5)。
func (l *loop) clearAbandoned(store Store, def workflow.Definition, v *view) {
	for ref, name := range l.abandoned {
		if l.stillAbandoned(store, def, v, ref, name) {
			continue
		}
		delete(l.abandoned, ref)
		l.rec.event("unabandon", map[string]any{"target": ref.String(), "trigger": name},
			LineNote, "打ち切りを解いた", " %s (%s)", ref, name)
	}
}

// stillAbandoned は打ち切りを保つか。open な一覧で打ち切ったときの trigger に当たったまま (か当たるかをまだ決められない)
// なら保つ。一覧に無いか一覧で外れて見えれば、一覧は古い結果を返しうるので置き場から 1 件読み直し、終端か外れたのを確かめて
// から解く。読み直せないか、当たるかをまだ決められなければ保つ (外れたのを観測するまで解かない。次の tick で確かめ直す)。
func (l *loop) stillAbandoned(store Store, def workflow.Definition, v *view, ref target.Ref, triggerName string) bool {
	t, ok := triggerNamed(def, triggerName)
	if !ok {
		return false
	}
	if item, listed := findItem(v.open, ref); listed && matchesAbandoned(t, item) {
		return true
	}
	item, err := store.Read(ref)
	if err != nil {
		l.rec.error(ref, "打ち切った作業対象を読み直せない (打ち切りを保ち、次の tick で確かめ直す): "+oneLine(err))
		return true
	}
	return matchesAbandoned(t, item)
}

// matchesAbandoned は、作業対象が打ち切ったときの trigger t に当たったままか。終端なら当たっていない (merge した CL は
// conflict を計算しないので、当たるかを決められないままになりうる)。終端でなく、当たるかをまだ決められなければ当たった
// ままとする。
func matchesAbandoned(t trigger.Trigger, item target.Item) bool {
	observed := t.Observe(item)
	return observed == trigger.ObservedStillMatches || observed == trigger.ObservedUndecided
}
