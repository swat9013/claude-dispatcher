package loop

import (
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/coord"
	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/worker"
)

// マシンを跨いだ分担 (system.md §4・§7、formats.md §12): coordination board の着手宣言と生存報告を読み、自分の着手宣言の乗っ取りを
// 見つけ、他の生きている loop の着手宣言のある作業対象を候補から外し、死んだ loop の ref を片付ける。死んだ loop の判定は、
// sha が変わったのを最後に見た時刻からの経過を loop 自身の時計で数える。

// Coordination は coordination board の口。本番は coord.Party、テストは fake。
type Coordination interface {
	LoopName() string
	ClaimRef(ref target.Ref) string
	HeartbeatRef(loop string) string
	// Observe は ref と読めた commit message を返す。ref を読めなければ error
	Observe() (coord.View, error)
	// Claim は着手宣言を expect を条件に置き、置いた sha を返す。負けたら coord.ErrLost を包む
	Claim(ref target.Ref, expect coord.Expect) (string, error)
	// Delete は ref を sha を指しているときだけ消す。負けたら coord.ErrLost を包む
	Delete(name, sha string) error
	// Heartbeat は生存報告を置き直す
	Heartbeat(interval time.Duration) error
	// Leave は自分の生存報告を消す
	Leave() error
}

// sighting は ref の sha と、その sha を初めて見た時刻。
type sighting struct {
	sha   string
	since time.Time
	// checked は、この sha の commit message を読んで、読めない項目を確かめたか
	checked bool
}

// holder は他の loop が持つ着手宣言。
type holder struct {
	loop string
	sha  string
	dead bool
}

// mediation は coordination board を指した loop が、coordination board について持つ記憶。
type mediation struct {
	c Coordination
	// seen は ref の名前ごとの直近の sha と、その sha を初めて見た時刻
	seen map[string]sighting
	// holders は直近の tick で見た、他の loop の着手宣言 (作業対象ごと)
	holders map[target.Ref]*holder
	// deadLoops は直近の tick で死んだとみなした loop の、生存報告の sha (生存報告の無い loop は "")
	deadLoops map[string]string
	// newer は直近の tick で見た、新しい版の ref。無ければ nil
	newer *status.NewerFormat
	// inherited は、生きている orphan の作業対象のために死んだ loop から引き継いだ自分の着手宣言の sha (claim は立てない。
	// ADR 0027)
	inherited map[target.Ref]string
	// unsureInherits は、直近の tick で引き継ぎの push が結果の分からない失敗をした作業対象。remote に届いていたかは次の
	// tick の deleteStaleClaims が確かめる。それより前に止まるときは inherited と同じに扱う
	unsureInherits map[target.Ref]bool
	// takenOver は乗っ取られた作業対象。loop を起動し直すまで残す
	takenOver []status.TakenOver
	// failures は続けて落ちた生存報告の置き直しの回数と直近の理由
	failures heartbeatFailures
	// interval は生存報告の間隔。起動時の workflow 定義から決め、走っている間は変えない (system.md §9)
	interval time.Duration
	// results は生存報告の置き直しの結果。dropped は results の満杯で捨てた結果の数。stopBeating は置き直しの goroutine を
	// 止めて終わりを待つ
	results     chan error
	dropped     atomic.Int64
	stopBeating func()
}

// heartbeatFailures は生存報告の置き直しが続けて落ちた回数と、直近の理由 (状態 file の heartbeat_failures の元)。
type heartbeatFailures struct {
	count int
	last  error
}

// note は置き直しの結果を数える。落ちていれば続けて落ちた回数を足し、通っていれば 0 に戻す。
func (f *heartbeatFailures) note(err error) {
	if err != nil {
		f.count, f.last = f.count+1, err
		return
	}
	f.count, f.last = 0, nil
}

// startBeating は、生存報告を tick と別の goroutine で間隔ごとに置き直し始める。tick が長引いても置き直しを遅らせない
// (system.md §9)。結果は results に送り、loop が読めないほど溜まれば捨てて数える (置き直し自体は止めない)。
func (m *mediation) startBeating(after func(time.Duration) <-chan time.Time) {
	m.results = make(chan error, 16)
	m.stopBeating = coord.KeepBeating(m.c.Heartbeat, m.interval, after, func(err error) {
		select {
		case m.results <- err:
		default:
			m.dropped.Add(1)
		}
	})
}

// beats は生存報告の置き直しの結果が届く channel。coordination board を指さない loop では nil (届かない)。
func (l *loop) beats() <-chan error {
	if l.mediation == nil {
		return nil
	}
	return l.mediation.results
}

// beat は生存報告の置き直しの結果を受ける。落ちたら error の行を残して数え、続けて 2 回以上落ちていれば要対処に出す。
func (l *loop) beat(err error) {
	l.noteDropped()
	if err != nil {
		l.mediation.failures.note(err)
		l.rec.error(target.Ref{}, "生存報告を置けない: "+oneLine(err))
		return
	}
	l.mediation.failures.note(nil)
}

// noteDropped は、results の満杯で捨てた置き直しの結果があれば、その数を error の行に残す。
func (l *loop) noteDropped() {
	if n := l.mediation.dropped.Swap(0); n > 0 {
		l.rec.error(target.Ref{}, fmt.Sprintf("生存報告の置き直しの結果 %d 件を読まずに捨てた", n))
	}
}

// coordinate は coordination board の突き合わせ (formats.md §12 の 1–5)。ref を読めなければ error を返す (その tick は掃除も起動も
// しない)。
func (l *loop) coordinate() error {
	m := l.mediation
	v, err := m.c.Observe()
	if err != nil {
		return err
	}
	if v.Unread != nil {
		l.rec.error(target.Ref{}, oneLine(v.Unread))
	}
	now := l.o.Now()
	l.noteSightings(v, now)
	l.detectTakeovers(v)
	l.deleteStaleClaims(v)
	m.newer = newerFormatIn(v)
	m.holders = l.othersClaims(v)
	m.deadLoops = map[string]string{}
	if m.newer != nil {
		// 読めない形の生存報告を死んだとみなさない
		return nil
	}
	l.judgeDeadLoops(v, now)
	return nil
}

// noteSightings は ref ごとの sha と、その sha を初めて見た時刻を覚え直す。読めない項目の error の行は、sha の commit message を
// 初めて読めた tick に 1 回だけ書く (fetch が落ちた tick の後でも)。
func (l *loop) noteSightings(v coord.View, now time.Time) {
	m := l.mediation
	current := map[string]sighting{}
	note := func(name, sha string) (coord.Message, bool) {
		s, ok := m.seen[name]
		if !ok || s.sha != sha {
			s = sighting{sha: sha, since: now}
		}
		msg, read := v.Messages[sha]
		fresh := read && !s.checked
		s.checked = s.checked || read
		current[name] = s
		return msg, fresh
	}
	for _, ref := range sortedRefs(v.Claims) {
		msg, fresh := note(m.c.ClaimRef(ref), v.Claims[ref])
		if fresh && msg.Loop == "" {
			l.rec.error(ref, "持ち主の loop を読めない着手宣言がある (この作業対象は起動しない): "+m.c.ClaimRef(ref))
		}
	}
	for loop, sha := range v.Loops {
		msg, fresh := note(m.c.HeartbeatRef(loop), sha)
		if fresh && !msg.Newer() && msg.Interval <= 0 && loop != m.c.LoopName() {
			l.rec.error(target.Ref{}, fmt.Sprintf("間隔を読めない生存報告がある (loop %s の死を判定しない): %s", loop, m.c.HeartbeatRef(loop)))
		}
	}
	m.seen = current
}

// newerFormatIn は、自分の知る版より新しい版の ref を見たなら要対処に出す形で返す。無ければ nil。
func newerFormatIn(v coord.View) *status.NewerFormat {
	msg, ok := v.Newer()
	if !ok {
		return nil
	}
	found := &status.NewerFormat{}
	if msg.Format > 0 {
		format := msg.Format
		found.Format = &format
	}
	return found
}

// othersClaims は、他の loop の着手宣言を作業対象ごとに返す。持ち主を読めない着手宣言も、生きている loop のものとして含める
// (起動しない・片付けない)。死んだかはまだ決めない。
func (l *loop) othersClaims(v coord.View) map[target.Ref]*holder {
	holders := map[target.Ref]*holder{}
	for ref, sha := range v.Claims {
		msg, read := v.Messages[sha]
		if read && msg.Loop == l.mediation.c.LoopName() {
			continue
		}
		holders[ref] = &holder{loop: msg.Loop, sha: sha}
	}
	return holders
}

// judgeDeadLoops は、生存報告が間隔の 3 倍新しくならない loop と、持ち主の生存報告が無いまま自分の間隔の 3 倍変わらない着手宣言の
// 持ち主を死んだとみなす (formats.md §12 の 5)。
func (l *loop) judgeDeadLoops(v coord.View, now time.Time) {
	m := l.mediation
	for loop, sha := range v.Loops {
		msg, read := v.Messages[sha]
		if loop == m.c.LoopName() || !read || msg.Interval <= 0 {
			continue
		}
		if now.Sub(m.seen[m.c.HeartbeatRef(loop)].since) >= 3*msg.Interval {
			m.deadLoops[loop] = sha
		}
	}
	for ref, h := range m.holders {
		if h.loop == "" {
			continue
		}
		if _, alive := v.Loops[h.loop]; alive {
			_, h.dead = m.deadLoops[h.loop]
			continue
		}
		if now.Sub(m.seen[m.c.ClaimRef(ref)].since) >= 3*m.interval {
			h.dead = true
			if _, known := m.deadLoops[h.loop]; !known {
				m.deadLoops[h.loop] = ""
			}
		}
	}
}

// newerFormatSeen は、coordination board に自分の知る版より新しい版の ref を直近の tick で見たか (その間は起動しない。formats.md §12)。
func (l *loop) newerFormatSeen() bool {
	return l.mediation != nil && l.mediation.newer != nil
}

// detectTakeovers は、自分の着手宣言の sha が変わったか消えた claim に乗っ取られたことを渡し、要対処に足す。
func (l *loop) detectTakeovers(v coord.View) {
	for _, ref := range sortedRefs(l.claims) {
		c := l.claims[ref]
		if c.claimSHA == "" {
			continue
		}
		sha, ok := v.Claims[ref]
		if ok && sha == c.claimSHA {
			continue
		}
		by := ""
		if ok {
			by = status.UnknownHolder
			if m, read := v.Messages[sha]; read && m.Loop != "" {
				by = m.Loop
			}
		}
		l.mediation.takenOver = append(l.mediation.takenOver, status.TakenOver{Target: ref.String(), By: by})
		l.apply(ref, c, c.takenOverBy(ref, by))
	}
}

// deleteStaleClaims は、自分の loop 名の着手宣言のうち claim の無い作業対象のもの (消し損ねた着手宣言) を消す。
func (l *loop) deleteStaleClaims(v coord.View) {
	r := l.mediation
	// 結果の分からなかった引き継ぎは、この View で決着したものだけ忘れる (届いていれば下で inherited に覚え直す)。着手宣言の
	// commit message を読めなかったものは、届いたかを決められないので覚えたままにする
	for ref := range r.unsureInherits {
		if sha, held := v.Claims[ref]; !held {
			delete(r.unsureInherits, ref)
		} else if _, read := v.Messages[sha]; read {
			delete(r.unsureInherits, ref)
		}
	}
	for ref, sha := range r.inherited {
		if v.Claims[ref] != sha {
			// 引き継いだ着手宣言が置き換わったか消えた。claim が無いので止める worker は無く、忘れる。orphan はまだ動きうるので、
			// 置き換わったなら二重実行になりうることを残す
			if _, held := v.Claims[ref]; held {
				l.rec.error(ref, "orphan のために引き継いだ着手宣言が置き換わった (orphan の claude と二重に動きうる)")
			}
			delete(r.inherited, ref)
		}
	}
	for _, ref := range sortedRefs(v.Claims) {
		sha := v.Claims[ref]
		if m, read := v.Messages[sha]; !read || m.Loop != r.c.LoopName() {
			continue
		}
		if c, claimed := l.claims[ref]; claimed && c.claimSHA == sha {
			continue
		}
		if r.inherited[ref] == sha {
			continue
		}
		if l.orphans[ref] {
			// 引き継ぎの push が結果の分からない失敗をしたが、remote には届いていた。orphan が生きているので引き継いだものとして覚える
			if r.inherited == nil {
				r.inherited = map[target.Ref]string{}
			}
			r.inherited[ref] = sha
			continue
		}
		if err := r.c.Delete(r.c.ClaimRef(ref), sha); err != nil && !errors.Is(err, coord.ErrLost) {
			l.rec.error(ref, "消し損ねた自分の着手宣言を消せない: "+oneLine(err))
		}
	}
}

// heldByOther は、作業対象に他の生きている loop の着手宣言があるか (候補の定義。system.md §4)。
func (l *loop) heldByOther(ref target.Ref) bool {
	if l.mediation == nil {
		return false
	}
	if _, inherited := l.mediation.inherited[ref]; inherited {
		// 消し損ねた引き継いだ着手宣言が残っている。消し直すまで起動しない (自分の着手宣言に負けて claim_lost と書かないため)
		return true
	}
	h, ok := l.mediation.holders[ref]
	return ok && !h.dead
}

// claimRemote は候補の着手宣言を置き、置いた sha を返す。持ち主の死んだ着手宣言は旧 sha を期待値にして乗っ取る。負けたら
// claim_lost の行を、それ以外の失敗は error の行を残して false を返す。
func (l *loop) claimRemote(ref target.Ref, triggerName string) (string, bool) {
	if l.mediation == nil {
		return "", true
	}
	expect, from := coord.Absent(), ""
	h, held := l.mediation.holders[ref]
	if held {
		expect, from = coord.Holding(h.sha), h.loop
	}
	sha, err := l.mediation.c.Claim(ref, expect)
	switch {
	case errors.Is(err, coord.ErrLost):
		l.rec.event("claim_lost", map[string]any{"target": ref.String(), "trigger": triggerName},
			LineNote, "着手宣言に負けた", " %s (%s): 他の loop が先に着手した", ref, triggerName)
		return "", false
	case err != nil:
		l.rec.error(ref, "着手宣言を置けない (次の tick で試み直す): "+oneLine(err))
		return "", false
	}
	if held {
		l.rec.event("takeover", map[string]any{"target": ref.String(), "trigger": triggerName, "from": from},
			LineNote, "乗っ取り", " %s (%s): 死んだ loop %s の着手宣言を乗っ取った", ref, triggerName, from)
	}
	return sha, true
}

// releaseInherited は、orphan が終わった作業対象の引き継いだ着手宣言を消す (formats.md §12 の 8)。orphan を確かめた後、候補の
// 起動の前に撃つ。消せなければ error の行を残して覚えたままにする (次の tick で消し直す)。
func (l *loop) releaseInherited() {
	m := l.mediation
	for _, ref := range sortedRefs(m.inherited) {
		if l.orphans[ref] {
			continue
		}
		if err := m.c.Delete(m.c.ClaimRef(ref), m.inherited[ref]); err != nil && !errors.Is(err, coord.ErrLost) {
			l.rec.error(ref, "引き継いだ着手宣言を消せない (次の tick で消し直す): "+oneLine(err))
			continue
		}
		delete(m.inherited, ref)
	}
}

// inherit は、生きている orphan の作業対象の死んだ loop の着手宣言を、旧 sha を期待値にした置き換えで自分の名前に引き継ぐ
// (formats.md §12 の 8)。負けは読み飛ばす。
func (l *loop) inherit(ref target.Ref, h *holder) {
	m := l.mediation
	sha, err := m.c.Claim(ref, coord.Holding(h.sha))
	switch {
	case errors.Is(err, coord.ErrLost):
		return
	case err != nil:
		l.rec.error(ref, "orphan の作業対象の着手宣言を引き継げない (次の tick で試み直す): "+oneLine(err))
		// remote に届いていたかもしれない。次の tick で確かめる前に止まるときのために覚えておく
		if m.unsureInherits == nil {
			m.unsureInherits = map[target.Ref]bool{}
		}
		m.unsureInherits[ref] = true
		return
	}
	if m.inherited == nil {
		m.inherited = map[target.Ref]string{}
	}
	m.inherited[ref] = sha
	l.rec.event("inherit", map[string]any{"target": ref.String(), "from": h.loop},
		LineNote, "引き継ぎ", " %s: orphan の claude が動いているので、死んだ loop %s の着手宣言を引き継いだ", ref, h.loop)
}

// inheritOrphanClaims は、生きている orphan の作業対象に残った、死んだ loop の着手宣言を引き継ぐ (formats.md §12 の 8)。
// 持ち主が同じマシンの別の loop なら、死の判定 (間隔の 3 倍) を待たずに死んだとみなす。同じ scope の loop は 1 台に 1 つしか
// 立たない (scope key の lock) ので、同じマシンの別の loop 名は落ちた前の loop で、待つと別のマシンの loop に先に乗っ取られる。
// 新しい版の ref がある間は撃たない。
func (l *loop) inheritOrphanClaims() {
	m := l.mediation
	if m.newer != nil {
		return
	}
	for _, ref := range sortedRefs(m.holders) {
		h := m.holders[ref]
		if _, claimed := l.claims[ref]; claimed || !l.orphans[ref] || h.loop == "" {
			continue
		}
		if h.dead || sameMachine(h.loop, m.c.LoopName()) {
			l.inherit(ref, h)
		}
	}
}

// sameMachine は、2 つの loop 名の hostname の部分 (coord.LoopHost) が同じか。
func sameMachine(a, b string) bool {
	return coord.LoopHost(a) == coord.LoopHost(b)
}

// cleanupDeadLoops は、死んだ loop の着手宣言のうち乗っ取らなかったものと、死んだ loop の生存報告を消す (formats.md §12 の 7)。
// 生きている orphan の作業対象の着手宣言は消さずに引き継ぐ。消すときの負けは読み飛ばす (他の loop が先に消したか乗っ取った)。
func (l *loop) cleanupDeadLoops() {
	r := l.mediation
	loops := make([]string, 0, len(r.deadLoops))
	for loop := range r.deadLoops {
		loops = append(loops, loop)
	}
	slices.Sort(loops)
	for _, loop := range loops {
		deleted := []string{}
		remove := func(ref target.Ref, name, sha string) bool {
			err := r.c.Delete(name, sha)
			switch {
			case err == nil:
				deleted = append(deleted, name)
				return true
			case !errors.Is(err, coord.ErrLost):
				l.rec.error(ref, fmt.Sprintf("死んだ loop %s の ref を消せない: %s", loop, oneLine(err)))
			}
			return false
		}
		claims := 0
		for _, ref := range sortedRefs(r.holders) {
			// 自分が着手宣言を置いて claim している (この tick の候補の起動で乗っ取った) 作業対象と、自分の claude が作業を続けている
			// 作業対象と、生きている orphan の作業対象 (inheritOrphanClaims が引き継ぐ) の着手宣言は消さない
			h := r.holders[ref]
			c, claimed := l.claims[ref]
			if mine := claimed && (c.claimSHA != "" || worker.UnstoppedAlive(c.exited)); h.loop != loop || !h.dead || mine || l.orphans[ref] {
				continue
			}
			if remove(ref, r.c.ClaimRef(ref), h.sha) {
				claims++
			}
		}
		heartbeat := false
		if sha := r.deadLoops[loop]; sha != "" {
			heartbeat = remove(target.Ref{}, r.c.HeartbeatRef(loop), sha)
		}
		if len(deleted) > 0 {
			l.rec.event("dead_loop", map[string]any{"loop": loop, "refs": deleted},
				LineNote, "死んだ loop", " %s: %s", loop, deletedScreen(heartbeat, claims))
		}
	}
	r.deadLoops = map[string]string{}
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

// releaseRemote は claim を解いた作業対象の自分の着手宣言を消す。消せなければ error の行を残す (次の tick で消し直す)。
func (l *loop) releaseRemote(ref target.Ref, c *claim) {
	if l.mediation == nil || c.claimSHA == "" {
		return
	}
	if err := l.mediation.c.Delete(l.mediation.c.ClaimRef(ref), c.claimSHA); err != nil && !errors.Is(err, coord.ErrLost) {
		l.rec.error(ref, "着手宣言を消せない (次の tick で消し直す): "+oneLine(err))
	}
	c.claimSHA = ""
}

// leaveRemote は loop が止まるときに、残っている自分の着手宣言と生存報告を消す。
func (l *loop) leaveRemote() {
	if l.mediation == nil {
		return
	}
	l.mediation.stopBeating()
	// 止めた後に届いていた置き直しの結果も数える
	for drained := false; !drained; {
		select {
		case err := <-l.mediation.results:
			l.beat(err)
		default:
			drained = true
		}
	}
	l.noteDropped()
	claudeLeft := false
	for _, ref := range sortedRefs(l.claims) {
		// 止められない claude が生きている claim の着手宣言は残す。claude が作業を続けうるので、他のマシンの loop には、
		// 生存報告が途絶えてから死を判定するまで起動させない (system.md §9)
		if c := l.claims[ref]; worker.UnstoppedAlive(c.exited) {
			claudeLeft = true
		} else {
			l.releaseRemote(ref, c)
		}
	}
	// orphan のために引き継いだ着手宣言は、直近の tick で orphan が終わっていたものを消し、生きていたものは残す (orphan が
	// 作業を続けうる)
	l.releaseInherited()
	// 結果の分からなかった引き継ぎは、届いていたものとして引き継いだ着手宣言と同じに扱う (sha を知らないので消せない)
	for _, ref := range sortedRefs(l.mediation.unsureInherits) {
		if l.orphans[ref] {
			claudeLeft = true
			l.rec.error(ref, "orphan のために引き継いだかもしれない着手宣言を残して止まる (引き継ぎの結果が分からず、orphan の claude が作業を続けうる)")
		}
	}
	for _, ref := range sortedRefs(l.mediation.inherited) {
		reason := "orphan は終わったが消せなかった"
		if l.orphans[ref] {
			// orphan が作業を続けうる間だけ、生存報告も残す
			claudeLeft, reason = true, "orphan の claude が作業を続けうる"
		}
		l.rec.error(ref, "orphan のために引き継いだ着手宣言を残して止まる ("+reason+")")
	}
	if claudeLeft {
		// 生存報告も消さずに途絶えさせる。消すと、持ち主の生存報告の無い着手宣言としてすぐに乗っ取られうる
		return
	}
	if err := l.mediation.c.Leave(); err != nil {
		l.rec.error(target.Ref{}, "生存報告を消せない: "+oneLine(err))
	}
}
