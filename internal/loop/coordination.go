package loop

import (
	"fmt"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/coord"
	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/worker"
)

// マシンを跨いだ分担 (system.md §4・§7、formats.md §12): coordination board の着手宣言と生存報告の読み方・持ち主と生死の判定・
// push と削除は coord.Board が持つ。loop は Board を呼び出す順 (tick の手順) と、Board が返した結果から行を書くことと、乗っ取られた
// claim を遷移させることを受け持つ。

// Coordination は loop が coordination board について呼ぶ口 (system.md §13)。本番は coord.Board、coordination board を指さない
// project は何もしない noCoordination。method ごとの振る舞いは coord.Board の同名の method の doc に書く (ここに写さない)。
type Coordination interface {
	LoopName() string
	Observe(clock func() time.Time, orphans map[target.Ref]bool) ([]coord.Outcome, error)
	Newer() bool
	Held(ref target.Ref) bool
	Claim(ref target.Ref) coord.Outcome
	Release(ref target.Ref) []coord.Outcome
	ReleaseInherited(orphans map[target.Ref]bool) []coord.Outcome
	Tidy(w coord.Working) []coord.Outcome
	Leave(w coord.Working) []coord.Outcome
	StartBeating(after func(time.Duration) <-chan time.Time, deliver func(error) bool) (stop func())
	Beat(err error) []coord.Outcome
	Dropped() []coord.Outcome
	Attention() coord.Attention
}

var _ Coordination = (*coord.Board)(nil)

// noCoordination は coordination board を指さない project の Coordination。誰も着手宣言を持たず、着手宣言の push は常に通り、
// 掃除と生存報告は無い。
type noCoordination struct{}

func (noCoordination) LoopName() string { return "" }
func (noCoordination) Observe(func() time.Time, map[target.Ref]bool) ([]coord.Outcome, error) {
	return nil, nil
}
func (noCoordination) Newer() bool                                          { return false }
func (noCoordination) Held(target.Ref) bool                                 { return false }
func (noCoordination) Claim(target.Ref) coord.Outcome                       { return coord.ClaimPlaced{} }
func (noCoordination) Release(target.Ref) []coord.Outcome                   { return nil }
func (noCoordination) ReleaseInherited(map[target.Ref]bool) []coord.Outcome { return nil }
func (noCoordination) Tidy(coord.Working) []coord.Outcome                   { return nil }
func (noCoordination) Leave(coord.Working) []coord.Outcome                  { return nil }
func (noCoordination) StartBeating(func(time.Duration) <-chan time.Time, func(error) bool) func() {
	return func() {}
}
func (noCoordination) Beat(error) []coord.Outcome { return nil }
func (noCoordination) Dropped() []coord.Outcome   { return nil }
func (noCoordination) Attention() coord.Attention { return coord.Attention{} }

// deliverBeat は生存報告の置き直しの結果を、loop が読めるよう beats へ渡す。溜まって渡せなければ false (捨てて数える)。
func (l *loop) deliverBeat(err error) bool {
	select {
	case l.beats <- err:
		return true
	default:
		return false
	}
}

// working は、claude がまだ作業を続けうる作業対象: 直近の tick に生きていた orphan と、止められない claude がまだ生きている
// claim。
func (l *loop) working() coord.Working {
	unstopped := map[target.Ref]bool{}
	for ref, c := range l.claims {
		if worker.UnstoppedAlive(c.exited) {
			unstopped[ref] = true
		}
	}
	return coord.Working{Orphans: l.orphans, Unstopped: unstopped}
}

// holderOf は乗っ取った着手宣言の今の持ち主 (status.TakenOver.By の綴り)。
func holderOf(t coord.TakenOver) string {
	if t.Unreadable {
		return status.UnknownHolder
	}
	return t.By
}

// report は Board が返した結果から、log と画面の行を書く。乗っ取られた作業対象の claim は遷移させる。
func (l *loop) report(outcomes []coord.Outcome) {
	for _, outcome := range outcomes {
		switch o := outcome.(type) {
		case coord.Unread:
			l.rec.error(target.Ref{}, oneLine(o.Err))
		case coord.OwnerUnreadable:
			l.rec.error(o.Ref, "持ち主の loop を読めない着手宣言がある (この作業対象は起動しない): "+o.Name)
		case coord.IntervalUnreadable:
			l.rec.error(target.Ref{}, fmt.Sprintf("間隔を読めない生存報告がある (loop %s の死を判定しない): %s", o.Loop, o.Name))
		case coord.TakenOver:
			if c, claimed := l.claims[o.Ref]; claimed {
				l.apply(o.Ref, c, c.takenOverBy(o.Ref, holderOf(o)))
			}
		case coord.InheritReplaced:
			l.rec.error(o.Ref, "orphan のために引き継いだ着手宣言が置き換わった (orphan の claude と二重に動きうる)")
		case coord.StaleDeleteFailed:
			l.rec.error(o.Ref, "消し損ねた自分の着手宣言を消せない: "+oneLine(o.Err))
		case coord.Inherited:
			l.rec.event("inherit", map[string]any{"target": o.Ref.String(), "from": o.From},
				LineNote, "引き継ぎ", " %s: orphan の claude が動いているので、死んだ loop %s の着手宣言を引き継いだ", o.Ref, o.From)
		case coord.InheritFailed:
			l.rec.error(o.Ref, "orphan の作業対象の着手宣言を引き継げない (次の tick で試み直す): "+oneLine(o.Err))
		case coord.DeadRefDeleteFailed:
			l.rec.error(o.Ref, fmt.Sprintf("死んだ loop %s の ref を消せない: %s", o.Loop, oneLine(o.Err)))
		case coord.DeadLoopCleaned:
			l.rec.event("dead_loop", map[string]any{"loop": o.Loop, "refs": o.Refs},
				LineNote, "死んだ loop", " %s: %s", o.Loop, deletedScreen(o.Heartbeat, o.Claims))
		case coord.InheritedReleaseFailed:
			l.rec.error(o.Ref, "引き継いだ着手宣言を消せない (次の tick で消し直す): "+oneLine(o.Err))
		case coord.ClaimReleaseFailed:
			l.rec.error(o.Ref, "着手宣言を消せない (次の tick で消し直す): "+oneLine(o.Err))
		case coord.ClaimReleaseLost:
			// 他の loop が先に消したか乗っ取った。何も書かずに読み飛ばす
		case coord.InheritKept:
			reason := "orphan は終わったが消せなかった"
			if o.OrphanAlive {
				reason = "orphan の claude が作業を続けうる"
			}
			l.rec.error(o.Ref, "orphan のために引き継いだ着手宣言を残して止まる ("+reason+")")
		case coord.UnsureInheritKept:
			reason := "orphan は終わっていた"
			if o.OrphanAlive {
				reason = "orphan の claude が作業を続けうる"
			}
			l.rec.error(o.Ref, "orphan のために引き継いだかもしれない着手宣言を残して止まる (引き継ぎの結果が分からず、"+reason+")")
		case coord.LeaveFailed:
			l.rec.error(target.Ref{}, "生存報告を消せない: "+oneLine(o.Err))
		case coord.BeatFailed:
			l.rec.error(target.Ref{}, "生存報告を置けない: "+oneLine(o.Err))
		case coord.BeatsDropped:
			l.rec.error(target.Ref{}, fmt.Sprintf("生存報告の置き直しの結果 %d 件を読まずに捨てた", o.N))
		default:
			// 行の無い結果を黙って捨てない。無人で回る loop を止めないよう、panic ではなく error の行に残す
			l.rec.error(target.Ref{}, fmt.Sprintf("行に写せない coordination board の結果 %T", o))
		}
	}
}

// claimRemote は候補の着手宣言を置く。持ち主の死んだ着手宣言は旧 sha を期待値にして乗っ取る。負けたら claim_lost の行を、それ以外の
// 失敗は error の行を残して false を返す。
func (l *loop) claimRemote(ref target.Ref, triggerName string) bool {
	switch o := l.coordination.Claim(ref).(type) {
	case coord.ClaimLost:
		l.rec.event("claim_lost", map[string]any{"target": ref.String(), "trigger": triggerName},
			LineNote, "着手宣言に負けた", " %s (%s): 他の loop が先に着手した", ref, triggerName)
		return false
	case coord.ClaimFailed:
		l.rec.error(ref, "着手宣言を置けない (次の tick で試み直す): "+oneLine(o.Err))
		return false
	case coord.ClaimTookOver:
		l.rec.event("takeover", map[string]any{"target": ref.String(), "trigger": triggerName, "from": o.From},
			LineNote, "乗っ取り", " %s (%s): 死んだ loop %s の着手宣言を乗っ取った", ref, triggerName, o.From)
	case coord.ClaimPlaced:
	default:
		// 置けたかを決められないので、起動しない側に倒す
		l.rec.error(ref, fmt.Sprintf("着手宣言の結果として知らない値 %T (起動しない)", o))
		return false
	}
	return true
}

// deletedScreen は死んだ loop の掃除で消した ref の、人が読む文 (formats.md §6)。
func deletedScreen(heartbeat bool, claims int) string {
	switch {
	case heartbeat && claims > 0:
		return fmt.Sprintf("生存報告と着手宣言 %d 本を消した", claims)
	case heartbeat:
		return "生存報告を消した"
	}
	return fmt.Sprintf("着手宣言 %d 本を消した", claims)
}

// leaveCoordination は loop が止まるときに、生存報告の置き直しを止め、残っている自分の着手宣言と生存報告を消す。
func (l *loop) leaveCoordination() {
	l.stopBeating()
	// 止めた後に届いていた置き直しの結果も数える
	for drained := false; !drained; {
		select {
		case err := <-l.beats:
			l.report(l.coordination.Beat(err))
		default:
			drained = true
		}
	}
	l.report(l.coordination.Dropped())
	l.report(l.coordination.Leave(l.working()))
}
