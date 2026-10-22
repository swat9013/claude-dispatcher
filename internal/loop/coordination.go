package loop

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/coord"
	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

// マシンを跨いだ分担 (system.md §4・§7、formats.md §12): 調停 remote の着手宣言と生存報告を読み、自分の着手宣言の乗っ取りを
// 見つけ、他の生きている loop の着手宣言のある作業対象を候補から外し、死んだ loop の ref を片付ける。死んだ loop の判定は、
// sha が変わったのを最後に見た時刻からの経過を loop 自身の時計で数える。

// Coordination は調停 remote の口。本番は coord.Party、テストは fake。
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
}

// holder は他の loop が持つ着手宣言。
type holder struct {
	loop string
	sha  string
	dead bool
	// takenByMe は、この tick の候補の起動で乗っ取ったか
	takenByMe bool
}

// remote は調停 remote を指した loop の、調停の記憶。
type remote struct {
	c Coordination
	// seen は ref の名前ごとの直近の sha と、その sha を初めて見た時刻
	seen map[string]sighting
	// holders は直近の tick で見た、他の loop の着手宣言 (作業対象ごと)
	holders map[target.Ref]*holder
	// deadLoops は直近の tick で死んだとみなした loop の、生存報告の sha (生存報告の無い loop は "")
	deadLoops map[string]string
	// newer は直近の tick で見た、新しい版の ref。無ければ nil
	newer *status.NewerFormat
	// takenOver は乗っ取られた作業対象。loop を起動し直すまで残す
	takenOver []status.TakenOver
	// heartbeatFailures と heartbeatError は続けて落ちた生存報告の置き直しの回数と直近の理由
	heartbeatFailures int
	heartbeatError    string
}

// heartbeatInterval は今の生存報告の間隔。
func (l *loop) heartbeatInterval() time.Duration { return coord.HeartbeatInterval(l.def.Interval) }

// heartbeat は生存報告を置き直す。落ちたら error の行を残して数え、続けて 2 回以上落ちていれば要対処に出す。
func (l *loop) heartbeat() {
	if err := l.remote.c.Heartbeat(l.heartbeatInterval()); err != nil {
		l.remote.heartbeatFailures++
		l.remote.heartbeatError = oneLine(err)
		l.rec.error(target.Ref{}, "生存報告を置けない: "+l.remote.heartbeatError)
		return
	}
	l.remote.heartbeatFailures, l.remote.heartbeatError = 0, ""
}

// coordinate は調停 remote の突き合わせ (formats.md §12 の 1–5)。ref を読めなければ error を返す (その tick は掃除も起動も
// しない)。
func (l *loop) coordinate() error {
	r := l.remote
	v, err := r.c.Observe()
	if err != nil {
		return err
	}
	if v.Unread != nil {
		l.rec.error(target.Ref{}, oneLine(v.Unread))
	}
	now := l.o.Now()
	current := map[string]sighting{}
	note := func(name, sha string) {
		if s, ok := r.seen[name]; ok && s.sha == sha {
			current[name] = s
			return
		}
		current[name] = sighting{sha: sha, since: now}
	}
	for ref, sha := range v.Claims {
		note(r.c.ClaimRef(ref), sha)
	}
	for loop, sha := range v.Loops {
		note(r.c.HeartbeatRef(loop), sha)
	}
	r.seen = current

	l.detectTakeovers(v)
	l.deleteStaleClaims(v)

	r.newer = nil
	for _, m := range v.Messages {
		if m.Newer() {
			found := &status.NewerFormat{}
			if m.Format > 0 {
				format := m.Format
				found.Format = &format
			}
			r.newer = found
			break
		}
	}
	r.holders, r.deadLoops = map[target.Ref]*holder{}, map[string]string{}
	for ref, sha := range v.Claims {
		m, read := v.Messages[sha]
		if read && m.Loop == r.c.LoopName() {
			continue
		}
		// 持ち主を読めない着手宣言は、生きている loop のものとして扱う (起動しない・片付けない)
		r.holders[ref] = &holder{loop: m.Loop, sha: sha}
	}
	if r.newer != nil {
		// 読めない形の生存報告を死んだとみなさない
		return nil
	}
	for loop, sha := range v.Loops {
		m, read := v.Messages[sha]
		if loop == r.c.LoopName() || !read || m.Interval <= 0 {
			continue
		}
		if now.Sub(r.seen[r.c.HeartbeatRef(loop)].since) >= 3*m.Interval {
			r.deadLoops[loop] = sha
		}
	}
	for ref, h := range r.holders {
		if h.loop == "" {
			continue
		}
		if _, alive := v.Loops[h.loop]; alive {
			_, h.dead = r.deadLoops[h.loop]
			continue
		}
		// 持ち主の生存報告が無い着手宣言は、自分の生存報告の間隔の 3 倍変わらなければ持ち主を死んだとみなす
		if now.Sub(r.seen[r.c.ClaimRef(ref)].since) >= 3*l.heartbeatInterval() {
			h.dead = true
			if _, known := r.deadLoops[h.loop]; !known {
				r.deadLoops[h.loop] = ""
			}
		}
	}
	return nil
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
			by = v.Messages[sha].Loop
		}
		l.remote.takenOver = append(l.remote.takenOver, status.TakenOver{Target: ref.String(), By: by})
		l.apply(ref, c, c.takenOverBy(ref, by))
	}
}

// deleteStaleClaims は、自分の loop 名の着手宣言のうち claim の無い作業対象のもの (消し損ねた着手宣言) を消す。
func (l *loop) deleteStaleClaims(v coord.View) {
	r := l.remote
	for _, ref := range sortedRefs(v.Claims) {
		sha := v.Claims[ref]
		if m, read := v.Messages[sha]; !read || m.Loop != r.c.LoopName() {
			continue
		}
		if c, claimed := l.claims[ref]; claimed && c.claimSHA == sha {
			continue
		}
		if err := r.c.Delete(r.c.ClaimRef(ref), sha); err != nil && !errors.Is(err, coord.ErrLost) {
			l.rec.error(ref, "消し損ねた自分の着手宣言を消せない: "+oneLine(err))
		}
	}
}

// heldByOther は、作業対象に他の生きている loop の着手宣言があるか (候補の定義。system.md §4)。
func (l *loop) heldByOther(ref target.Ref) bool {
	if l.remote == nil {
		return false
	}
	h, ok := l.remote.holders[ref]
	return ok && !h.dead
}

// claimRemote は候補の着手宣言を置き、置いた sha を返す。持ち主の死んだ着手宣言は旧 sha を期待値にして乗っ取る。負けたら
// claim_lost の行を、それ以外の失敗は error の行を残して false を返す。
func (l *loop) claimRemote(ref target.Ref, triggerName string) (string, bool) {
	if l.remote == nil {
		return "", true
	}
	expect, from := coord.Absent(), ""
	h, held := l.remote.holders[ref]
	if held {
		expect, from = coord.Holding(h.sha), h.loop
	}
	sha, err := l.remote.c.Claim(ref, expect)
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
		h.takenByMe = true
		l.rec.event("takeover", map[string]any{"target": ref.String(), "trigger": triggerName, "from": from},
			LineNote, "乗っ取り", " %s (%s): 死んだ loop %s の着手宣言を乗っ取った", ref, triggerName, from)
	}
	return sha, true
}

// cleanupDeadLoops は、死んだ loop の着手宣言のうち乗っ取らなかったものと、死んだ loop の生存報告を消す (formats.md §12 の 7)。
// 消すときの負けは読み飛ばす (他の loop が先に消したか乗っ取った)。
func (l *loop) cleanupDeadLoops() {
	r := l.remote
	loops := make([]string, 0, len(r.deadLoops))
	for loop := range r.deadLoops {
		loops = append(loops, loop)
	}
	slices.Sort(loops)
	for _, loop := range loops {
		deleted := []string{}
		remove := func(ref target.Ref, name, sha string) {
			err := r.c.Delete(name, sha)
			switch {
			case err == nil:
				deleted = append(deleted, name)
			case !errors.Is(err, coord.ErrLost):
				l.rec.error(ref, fmt.Sprintf("死んだ loop %s の ref を消せない: %s", loop, oneLine(err)))
			}
		}
		for _, ref := range sortedRefs(r.holders) {
			if h := r.holders[ref]; h.loop == loop && h.dead && !h.takenByMe {
				remove(ref, r.c.ClaimRef(ref), h.sha)
			}
		}
		if sha := r.deadLoops[loop]; sha != "" {
			remove(target.Ref{}, r.c.HeartbeatRef(loop), sha)
		}
		if len(deleted) > 0 {
			l.rec.event("dead_loop", map[string]any{"loop": loop, "refs": deleted},
				LineNote, "死んだ loop", " %s: ref %d 本を消した", loop, len(deleted))
		}
	}
	r.deadLoops = map[string]string{}
}

// releaseRemote は claim を解いた作業対象の自分の着手宣言を消す。消せなければ error の行を残す (次の tick で消し直す)。
func (l *loop) releaseRemote(ref target.Ref, c *claim) {
	if l.remote == nil || c.claimSHA == "" {
		return
	}
	if err := l.remote.c.Delete(l.remote.c.ClaimRef(ref), c.claimSHA); err != nil && !errors.Is(err, coord.ErrLost) {
		l.rec.error(ref, "着手宣言を消せない (次の tick で消し直す): "+oneLine(err))
	}
	c.claimSHA = ""
}

// leaveRemote は loop が止まるときに、残っている自分の着手宣言と生存報告を消す。
func (l *loop) leaveRemote() {
	if l.remote == nil {
		return
	}
	for _, ref := range sortedRefs(l.claims) {
		l.releaseRemote(ref, l.claims[ref])
	}
	if err := l.remote.c.Leave(); err != nil {
		l.rec.error(target.Ref{}, "生存報告を消せない: "+oneLine(err))
	}
}
