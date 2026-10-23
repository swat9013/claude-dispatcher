package coord

import (
	"errors"
	"maps"
	"slices"
	"sync/atomic"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
)

// Board は 1 つの loop (か単発起動) が coordination board について持つ記憶と判定 (system.md §4 の「マシンを跨いだ分担」、§13)。
// 着手宣言の持ち主 (自分・引き継いだもの・他の生きている loop・死んだ loop) と生死の判定を集め、push と削除まで撃って、何が
// 起きたかを Outcome で返す。時計は持たず、経過は呼び出し側が渡す時刻で数える。呼び出し側の loop が持つ事実 (orphan など) は
// method の引数で受ける。loop の 1 本の goroutine から呼び、生存報告の置き直しの goroutine とは dropped だけを共有する。
type Board struct {
	refs Refs
	// interval は自分の生存報告の間隔。起動時の workflow 定義から決め、走っている間は変えない (system.md §9)
	interval time.Duration

	// claimSeen と loopSeen は ref ごとの直近の sha と、その sha を初めて見た時刻
	claimSeen map[target.Ref]sighting
	loopSeen  map[string]sighting
	// holders は直近の Observe で見た、他の loop の着手宣言 (作業対象ごと)
	holders map[target.Ref]*holder
	// deadLoops は直近の Observe で死んだとみなした loop の、生存報告の sha (生存報告の無い loop は "")
	deadLoops map[string]string
	// newer は直近の Observe で見た、新しい版の ref。無ければ nil
	newer *NewerFormat
	// mine は自分が置いた着手宣言の sha (作業対象ごと)。乗っ取られた後と、解いた後は持たない
	mine map[target.Ref]string
	// inherited は、生きている orphan の作業対象のために死んだ loop から引き継いだ自分の着手宣言の sha (claim は立てない。
	// ADR 0027)
	inherited map[target.Ref]string
	// unsure は、引き継ぎの push が結果の分からない失敗をした作業対象。remote に届いていたかは次の Observe が確かめる。それより
	// 前に止まるときは inherited と同じに扱う
	unsure map[target.Ref]bool
	// takenOver は乗っ取られた作業対象。loop を起動し直すまで残す
	takenOver []TakenOver
	// failures は続けて落ちた生存報告の置き直しの回数と直近の理由
	failures HeartbeatFailures
	// dropped は、受け取り手が読めないほど溜まって捨てた置き直しの結果の数
	dropped atomic.Int64
}

// missedBeats は、生存報告の間隔の何倍新しくならなければ死んだとみなすか (system.md §4)。
const missedBeats = 3

// NewBoard は refs を通して coordination board を読み書きする Board を作る。interval は自分の生存報告の間隔 (HeartbeatInterval)。
func NewBoard(refs Refs, interval time.Duration) *Board {
	return &Board{
		refs: refs, interval: interval, claimSeen: map[target.Ref]sighting{}, loopSeen: map[string]sighting{},
		mine: map[target.Ref]string{}, inherited: map[target.Ref]string{}, unsure: map[target.Ref]bool{},
	}
}

// sighting は ref の sha と、その sha を初めて見た時刻。
type sighting struct {
	sha   string
	since time.Time
	// name は ref の名前
	name string
	// checked は、この sha の commit message を読んで、読めない項目を確かめたか
	checked bool
}

// holder は他の loop が持つ着手宣言。
type holder struct {
	loop string
	sha  string
	dead bool
}

// NewerFormat は coordination board に見た、自分の知る版より新しい版の ref。Format は読んだ版で、版を読めなければ 0。
type NewerFormat struct{ Format int }

// HeartbeatFailures は生存報告の置き直しが続けて落ちた回数と、直近の理由。
type HeartbeatFailures struct {
	Count int
	Last  error
}

// note は置き直しの結果を数える。落ちていれば続けて落ちた回数を足し、通っていれば 0 に戻す。
func (f *HeartbeatFailures) note(err error) {
	if err != nil {
		f.Count, f.Last = f.Count+1, err
		return
	}
	f.Count, f.Last = 0, nil
}

// Attention は、人に見せる要対処の元 (状態 file に載る。formats.md §7.1)。
type Attention struct {
	// TakenOver は着手宣言を乗っ取られた作業対象 (乗っ取られた順)
	TakenOver []TakenOver
	// NewerFormat は直近の Observe で新しい版の ref を見た間だけ持つ
	NewerFormat *NewerFormat
	// HeartbeatFailures は生存報告の置き直しが 2 回以上続けて落ちている間だけ持つ
	HeartbeatFailures *HeartbeatFailures
}

// LoopName は自分の loop 名。
func (b *Board) LoopName() string { return b.refs.LoopName() }

// Observes は、tick ごとに coordination board を突き合わせるか。突き合わせるなら、tick の外では再起動しない (乗っ取られたかを
// 確かめるまで待つ。system.md §7)。
func (b *Board) Observes() bool { return true }

// Attention は今の要対処の元。
func (b *Board) Attention() Attention {
	a := Attention{TakenOver: b.takenOver, NewerFormat: b.newer}
	if f := b.failures; f.Count >= 2 {
		a.HeartbeatFailures = &f
	}
	return a
}

// Newer は、自分の知る版より新しい版の ref を直近の Observe で見たか (その間は起動しない。formats.md §12)。
func (b *Board) Newer() bool { return b.newer != nil }

// Held は、作業対象に他の生きている loop の着手宣言があるか (候補の定義。system.md §4)。引き継いだ着手宣言が残っていても true
// (消し直すまで起動しない)。
func (b *Board) Held(ref target.Ref) bool {
	if _, inherited := b.inherited[ref]; inherited {
		return true
	}
	h, ok := b.holders[ref]
	return ok && !h.dead
}

// Holder は、直近の Observe で見た作業対象の着手宣言の持ち主の loop 名。持ち主を読めないか、他の loop の着手宣言が無ければ ""。
func (b *Board) Holder(ref target.Ref) string {
	if h, ok := b.holders[ref]; ok {
		return h.loop
	}
	return ""
}

// Observe は coordination board の突き合わせ (formats.md §12 の 1–5)。ref を読み、新しく見た sha の commit message を読んで、
// 見た時刻を覚え直し、自分の着手宣言の乗っ取りを見つけ、消し損ねた自分の着手宣言を消し、他の loop の着手宣言と死んだ loop を
// 判定する。ref を読めなければ error を返し、何も変えない。
//
// now は読んだ時刻。orphans は前の tick に生きていた orphan の作業対象で、消し損ねた着手宣言の掃除が、引き継いだ着手宣言との
// 見分けに使う (この tick の orphan の確かめより前に撃つため)。返す Outcome は log に残す順に並ぶ。
func (b *Board) Observe(now time.Time, orphans map[target.Ref]bool) ([]Outcome, error) {
	v, err := b.refs.Observe()
	if err != nil {
		return nil, err
	}
	var out []Outcome
	if v.Unread != nil {
		out = append(out, Unread{v.Unread})
	}
	out = append(out, b.noteSightings(v, now)...)
	out = append(out, b.detectTakeovers(v)...)
	out = append(out, b.deleteStaleClaims(v, orphans)...)
	b.newer = newerFormatIn(v)
	b.holders = b.othersClaims(v)
	b.deadLoops = map[string]string{}
	if b.newer == nil {
		// 読めない形の生存報告を死んだとみなさない
		b.judgeDeadLoops(v, now)
	}
	return out, nil
}

// see は ref が今指す sha を、前に見た記録に重ねる。sha が変わっていれば見た時刻を now に改める。fresh は、この sha の commit message
// を初めて読めたか。
func see(prev sighting, known bool, ref RefState, now time.Time, v View) (s sighting, msg Message, fresh bool) {
	if !known || prev.sha != ref.SHA {
		prev = sighting{sha: ref.SHA, since: now}
	}
	msg, read := v.Messages[ref.SHA]
	fresh = read && !prev.checked
	prev.checked = prev.checked || read
	prev.name = ref.Name
	return prev, msg, fresh
}

// noteSightings は ref ごとの sha と、その sha を初めて見た時刻を覚え直す。読めない項目は、sha の commit message を初めて読めた
// tick に 1 回だけ返す (fetch が落ちた tick の後でも)。
func (b *Board) noteSightings(v View, now time.Time) []Outcome {
	var out []Outcome
	claims := map[target.Ref]sighting{}
	for _, ref := range target.SortedRefs(v.Claims) {
		prev, known := b.claimSeen[ref]
		s, msg, fresh := see(prev, known, v.Claims[ref], now, v)
		claims[ref] = s
		if fresh && msg.Loop == "" {
			out = append(out, OwnerUnreadable{Ref: ref, Name: s.name})
		}
	}
	loops := map[string]sighting{}
	for _, loop := range slices.Sorted(maps.Keys(v.Loops)) {
		prev, known := b.loopSeen[loop]
		s, msg, fresh := see(prev, known, v.Loops[loop], now, v)
		loops[loop] = s
		if fresh && !msg.Newer() && msg.Interval <= 0 && loop != b.refs.LoopName() {
			out = append(out, IntervalUnreadable{Loop: loop, Name: s.name})
		}
	}
	b.claimSeen, b.loopSeen = claims, loops
	return out
}

// newerFormatIn は、自分の知る版より新しい版の ref を見たなら返す。無ければ nil。
func newerFormatIn(v View) *NewerFormat {
	msg, ok := v.Newer()
	if !ok {
		return nil
	}
	return &NewerFormat{Format: max(msg.Format, 0)}
}

// othersClaims は、他の loop の着手宣言を作業対象ごとに返す。持ち主を読めない着手宣言も、生きている loop のものとして含める
// (起動しない・片付けない)。死んだかはまだ決めない。
func (b *Board) othersClaims(v View) map[target.Ref]*holder {
	holders := map[target.Ref]*holder{}
	for ref, state := range v.Claims {
		msg, read := v.Messages[state.SHA]
		if read && msg.Loop == b.refs.LoopName() {
			continue
		}
		holders[ref] = &holder{loop: msg.Loop, sha: state.SHA}
	}
	return holders
}

// judgeDeadLoops は、生存報告が間隔の 3 倍新しくならない loop と、持ち主の生存報告が無いまま自分の間隔の 3 倍変わらない着手宣言の
// 持ち主を死んだとみなす (formats.md §12 の 5)。
func (b *Board) judgeDeadLoops(v View, now time.Time) {
	for loop, state := range v.Loops {
		msg, read := v.Messages[state.SHA]
		if loop == b.refs.LoopName() || !read || msg.Interval <= 0 {
			continue
		}
		if now.Sub(b.loopSeen[loop].since) >= missedBeats*msg.Interval {
			b.deadLoops[loop] = state.SHA
		}
	}
	for ref, h := range b.holders {
		if h.loop == "" {
			continue
		}
		if _, alive := v.Loops[h.loop]; alive {
			_, h.dead = b.deadLoops[h.loop]
			continue
		}
		if now.Sub(b.claimSeen[ref].since) >= missedBeats*b.interval {
			h.dead = true
			if _, known := b.deadLoops[h.loop]; !known {
				b.deadLoops[h.loop] = ""
			}
		}
	}
}

// detectTakeovers は、自分の着手宣言の sha が変わったか消えた作業対象を、乗っ取られたものとして返し、要対処に足す。
func (b *Board) detectTakeovers(v View) []Outcome {
	var out []Outcome
	for _, ref := range target.SortedRefs(b.mine) {
		state, ok := v.Claims[ref]
		if ok && state.SHA == b.mine[ref] {
			continue
		}
		t := TakenOver{Ref: ref}
		if ok {
			t.Unreadable = true
			if m, read := v.Messages[state.SHA]; read && m.Loop != "" {
				t.By, t.Unreadable = m.Loop, false
			}
		}
		delete(b.mine, ref)
		b.takenOver = append(b.takenOver, t)
		out = append(out, t)
	}
	return out
}

// deleteStaleClaims は、自分の loop 名の着手宣言のうち自分の着手宣言でも引き継いだものでもない作業対象のもの (消し損ねた着手宣言)
// を消す。
func (b *Board) deleteStaleClaims(v View, orphans map[target.Ref]bool) []Outcome {
	var out []Outcome
	// 結果の分からなかった引き継ぎは、この View で決着したものだけ忘れる (届いていれば下で inherited に覚え直す)。着手宣言の
	// commit message を読めなかったものは、届いたかを決められないので覚えたままにする
	for ref := range b.unsure {
		if state, held := v.Claims[ref]; !held {
			delete(b.unsure, ref)
		} else if _, read := v.Messages[state.SHA]; read {
			delete(b.unsure, ref)
		}
	}
	for _, ref := range target.SortedRefs(b.inherited) {
		if v.Claims[ref].SHA != b.inherited[ref] {
			// 引き継いだ着手宣言が置き換わったか消えた。claim が無いので止める worker は無く、忘れる。orphan はまだ動きうるので、
			// 置き換わったなら二重実行になりうることを残す
			if _, held := v.Claims[ref]; held {
				out = append(out, InheritReplaced{ref})
			}
			delete(b.inherited, ref)
		}
	}
	for _, ref := range target.SortedRefs(v.Claims) {
		sha := v.Claims[ref].SHA
		if m, read := v.Messages[sha]; !read || m.Loop != b.refs.LoopName() {
			continue
		}
		if b.mine[ref] == sha || b.inherited[ref] == sha {
			continue
		}
		if orphans[ref] {
			// 引き継ぎの push が結果の分からない失敗をしたが、remote には届いていた。orphan が生きているので引き継いだものとして覚える
			b.inherited[ref] = sha
			continue
		}
		if err := b.refs.DeleteClaim(ref, sha); err != nil && !errors.Is(err, ErrLost) {
			out = append(out, StaleDeleteFailed{ref, err})
		}
	}
	return out
}

// Claim は候補の着手宣言を置く。持ち主の死んだ着手宣言は旧 sha を期待値にして乗っ取り、無ければ「無いときだけ作る」で置く。置けたら
// 自分の着手宣言として覚える。ClaimPlaced・ClaimTookOver・ClaimLost・ClaimFailed のどれかを返す。
func (b *Board) Claim(ref target.Ref) Outcome {
	expect, from := Absent(), ""
	h, held := b.holders[ref]
	if held {
		expect, from = Holding(h.sha), h.loop
	}
	sha, err := b.refs.Claim(ref, expect)
	switch {
	case errors.Is(err, ErrLost):
		return ClaimLost{}
	case err != nil:
		return ClaimFailed{err}
	}
	b.mine[ref] = sha
	if held {
		return ClaimTookOver{From: from}
	}
	return ClaimPlaced{}
}

// Release は claim を解いた作業対象の自分の着手宣言を消す。消せなければ Outcome を返す (消し損ねた着手宣言として、次の Observe が
// 消し直す)。自分の着手宣言を持たない作業対象 (乗っ取られた後を含む) では何もしない。
func (b *Board) Release(ref target.Ref) []Outcome {
	sha, ok := b.mine[ref]
	if !ok {
		return nil
	}
	delete(b.mine, ref)
	if err := b.refs.DeleteClaim(ref, sha); err != nil && !errors.Is(err, ErrLost) {
		return []Outcome{ClaimReleaseFailed{ref, err}}
	}
	return nil
}

// ReleaseInherited は、orphan が終わった作業対象の引き継いだ着手宣言を消す (formats.md §12 の 8)。orphan を確かめた後、候補の
// 起動の前に撃つ。orphans はこの tick に生きていた orphan。消せなければ Outcome を返し、覚えたままにする (次の tick で消し直す)。
func (b *Board) ReleaseInherited(orphans map[target.Ref]bool) []Outcome {
	var out []Outcome
	for _, ref := range target.SortedRefs(b.inherited) {
		if orphans[ref] {
			continue
		}
		if err := b.refs.DeleteClaim(ref, b.inherited[ref]); err != nil && !errors.Is(err, ErrLost) {
			out = append(out, InheritedReleaseFailed{ref, err})
			continue
		}
		delete(b.inherited, ref)
	}
	return out
}

// Tidy は、候補の起動の後に、生きている orphan の作業対象に残った死んだ loop の着手宣言を引き継ぎ (formats.md §12 の 8)、
// 引き継がなかった死んだ loop の着手宣言と生存報告を消す (同 7)。orphans はこの tick に生きていた orphan、unstopped は止められない
// claude がまだ生きている claim の作業対象。
func (b *Board) Tidy(orphans, unstopped map[target.Ref]bool) []Outcome {
	return append(b.inheritOrphanClaims(orphans), b.cleanupDeadLoops(orphans, unstopped)...)
}

// inheritOrphanClaims は、生きている orphan の作業対象に残った、死んだ loop の着手宣言を引き継ぐ。持ち主が同じマシンの別の
// loop なら、死の判定 (間隔の 3 倍) を待たずに死んだとみなす。同じ scope の loop は 1 台に 1 つしか立たない (scope key の lock)
// ので、同じマシンの別の loop 名は落ちた前の loop で、待つと別のマシンの loop に先に乗っ取られる。新しい版の ref がある間は撃たない。
func (b *Board) inheritOrphanClaims(orphans map[target.Ref]bool) []Outcome {
	if b.newer != nil {
		return nil
	}
	var out []Outcome
	for _, ref := range target.SortedRefs(b.holders) {
		h := b.holders[ref]
		if !orphans[ref] || h.loop == "" {
			continue
		}
		if h.dead || sameMachine(h.loop, b.refs.LoopName()) {
			out = append(out, b.inherit(ref, h)...)
		}
	}
	return out
}

// inherit は作業対象の死んだ loop の着手宣言を、旧 sha を期待値にした置き換えで自分の名前に引き継ぐ。負けは読み飛ばす。
func (b *Board) inherit(ref target.Ref, h *holder) []Outcome {
	sha, err := b.refs.Claim(ref, Holding(h.sha))
	switch {
	case errors.Is(err, ErrLost):
		return nil
	case err != nil:
		// remote に届いていたかもしれない。次の Observe で確かめる前に止まるときのために覚えておく
		b.unsure[ref] = true
		return []Outcome{InheritFailed{ref, err}}
	}
	b.inherited[ref] = sha
	return []Outcome{Inherited{Ref: ref, From: h.loop}}
}

// sameMachine は、2 つの loop 名の hostname の部分 (LoopHost) が同じか。
func sameMachine(a, b string) bool { return LoopHost(a) == LoopHost(b) }

// cleanupDeadLoops は、死んだ loop の着手宣言のうち乗っ取らなかったものと、死んだ loop の生存報告を消す。生きている orphan の
// 作業対象の着手宣言は消さずに引き継ぐ (inheritOrphanClaims)。消すときの負けは読み飛ばす (他の loop が先に消したか乗っ取った)。
func (b *Board) cleanupDeadLoops(orphans, unstopped map[target.Ref]bool) []Outcome {
	var out []Outcome
	for _, loop := range slices.Sorted(maps.Keys(b.deadLoops)) {
		var refs []string
		claims := 0
		for _, ref := range target.SortedRefs(b.holders) {
			// 自分が着手宣言を置いている (この tick の候補の起動で乗っ取った) 作業対象と、自分の claude が作業を続けている
			// 作業対象と、生きている orphan の作業対象 (inheritOrphanClaims が引き継ぐ) の着手宣言は消さない
			h := b.holders[ref]
			_, mine := b.mine[ref]
			if h.loop != loop || !h.dead || mine || unstopped[ref] || orphans[ref] {
				continue
			}
			switch err := b.refs.DeleteClaim(ref, h.sha); {
			case err == nil:
				refs = append(refs, b.claimSeen[ref].name)
				claims++
			case !errors.Is(err, ErrLost):
				out = append(out, DeadRefDeleteFailed{Ref: ref, Loop: loop, Err: err})
			}
		}
		heartbeat := false
		if sha := b.deadLoops[loop]; sha != "" {
			switch err := b.refs.DeleteHeartbeat(loop, sha); {
			case err == nil:
				refs = append(refs, b.loopSeen[loop].name)
				heartbeat = true
			case !errors.Is(err, ErrLost):
				out = append(out, DeadRefDeleteFailed{Loop: loop, Err: err})
			}
		}
		if len(refs) > 0 {
			out = append(out, DeadLoopCleaned{Loop: loop, Refs: refs, Heartbeat: heartbeat, Claims: claims})
		}
	}
	b.deadLoops = map[string]string{}
	return out
}

// Leave は loop (か単発起動) が止まるときに、残っている自分の着手宣言と生存報告を消す。orphans は直近の tick に生きていた orphan、
// unstopped は止められない claude がまだ生きている claim の作業対象。
//
// 止められない claude の作業対象の着手宣言は残す。claude が作業を続けうるので、他のマシンの loop には、生存報告が途絶えてから死を
// 判定するまで起動させない (system.md §9)。引き継いだ着手宣言も、orphan が作業を続けうる間は残す。着手宣言を残すときは生存報告も
// 消さずに途絶えさせる。消すと、持ち主の生存報告の無い着手宣言としてすぐに乗っ取られうる。
func (b *Board) Leave(orphans, unstopped map[target.Ref]bool) []Outcome {
	var out []Outcome
	claudeLeft := len(unstopped) > 0
	for _, ref := range target.SortedRefs(b.mine) {
		if !unstopped[ref] {
			out = append(out, b.Release(ref)...)
		}
	}
	// orphan のために引き継いだ着手宣言は、直近の tick で orphan が終わっていたものを消し、生きていたものは残す
	out = append(out, b.ReleaseInherited(orphans)...)
	// 結果の分からなかった引き継ぎは、届いていたものとして引き継いだ着手宣言と同じに扱う (sha を知らないので消せない)
	for _, ref := range target.SortedRefs(b.unsure) {
		if orphans[ref] {
			claudeLeft = true
			out = append(out, UnsureInheritKept{ref})
		}
	}
	for _, ref := range target.SortedRefs(b.inherited) {
		// orphan が作業を続けうる間だけ、生存報告も残す
		claudeLeft = claudeLeft || orphans[ref]
		out = append(out, InheritKept{Ref: ref, OrphanAlive: orphans[ref]})
	}
	if claudeLeft {
		return out
	}
	if err := b.refs.Leave(); err != nil {
		out = append(out, LeaveFailed{err})
	}
	return out
}

// StartBeating は、生存報告を tick と別の goroutine で間隔ごとに置き直し始める。tick が長引いても置き直しを遅らせない
// (system.md §9)。置き直しの結果は deliver に渡し、受け取り手が読めないほど溜まって deliver が false を返したら、捨てて数える
// (置き直し自体は止めない)。返した関数で止め、止まるまで待つ。
func (b *Board) StartBeating(after func(time.Duration) <-chan time.Time, deliver func(error) bool) (stop func()) {
	return KeepBeating(b.refs.Heartbeat, b.interval, after, func(err error) {
		if !deliver(err) {
			b.dropped.Add(1)
		}
	})
}

// Beat は受け取った置き直しの結果を数える。落ちていれば続けて落ちた回数を足し、通っていれば 0 に戻す。捨てた結果があれば、その
// 数を先に返す。
func (b *Board) Beat(err error) []Outcome {
	out := b.Dropped()
	b.failures.note(err)
	if err != nil {
		out = append(out, BeatFailed{err})
	}
	return out
}

// Dropped は、受け取り手が読めないほど溜まって捨てた置き直しの結果があれば、その数を返す。
func (b *Board) Dropped() []Outcome {
	if n := b.dropped.Swap(0); n > 0 {
		return []Outcome{BeatsDropped{int(n)}}
	}
	return nil
}
