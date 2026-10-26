package coord_test

import (
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/coord"
	"github.com/swat9013/claude-dispatcher/internal/coord/coordtest"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

// coordination board の持ち主と生死の判定 (system.md §4・§7・§13、formats.md §12)。死んだ loop の判定は分の単位の経過を数えるので、
// 時刻は Observe の引数で渡し、coordination board は in-memory の adapter (coordtest.Memory) に置く。

const (
	me    = "me-00000001"
	dead  = "dead-00000002"
	other = "other-00000003"
	// beat は自分の生存報告の間隔
	beat = time.Minute
)

var (
	start   = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	issue42 = target.Ref{Kind: target.KindIssue, Number: 42}
	issue7  = target.Ref{Kind: target.KindIssue, Number: 7}
	cl3     = target.Ref{Kind: target.KindCL, Number: 3}

	errPush  = errors.New("push が落ちた")
	errFetch = errors.New("fetch が落ちた")
)

func claimBy(loop string, ref target.Ref) coord.Message {
	return coord.Message{Kind: coord.KindClaim, Format: coord.Format, Loop: loop, Target: ref.String()}
}

func beatOf(loop string, interval time.Duration) coord.Message {
	return coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format, Loop: loop, Interval: interval}
}

// recorded は coordtest.Memory に、撃たれた push と削除と Leave の記録を足した adapter。失敗の注入は coordtest.Memory が持つ。
type recorded struct {
	*coordtest.Memory
	// claimed と deleted は Claim と DeleteClaim・DeleteHeartbeat の呼び出し (`<作業対象> <期待値>`・`<ref の名前> <sha>`)
	claimed, deleted []string
	// left は Leave の呼び出しの回数
	left int
}

func (r *recorded) Claim(ref target.Ref, expect coord.Expect) (string, error) {
	r.claimed = append(r.claimed, ref.String()+" "+expect.String())
	return r.Memory.Claim(ref, expect)
}

func (r *recorded) DeleteClaim(ref target.Ref, sha string) error {
	r.deleted = append(r.deleted, coordtest.ClaimName(ref)+" "+sha)
	return r.Memory.DeleteClaim(ref, sha)
}

func (r *recorded) DeleteHeartbeat(loop, sha string) error {
	r.deleted = append(r.deleted, coordtest.HeartbeatName(loop)+" "+sha)
	return r.Memory.DeleteHeartbeat(loop, sha)
}

func (r *recorded) Leave() error {
	r.left++
	return r.Memory.Leave()
}

// world は Board と、それが読み書きする coordination board。
type world struct {
	*recorded
	t     *testing.T
	board *coord.Board
}

func newWorld(t *testing.T) *world { return newWorldOf(t, me) }

func newWorldOf(t *testing.T, loop string) *world {
	r := &recorded{Memory: coordtest.NewMemory(loop)}
	return &world{recorded: r, t: t, board: coord.NewBoard(r, beat)}
}

func set(refs []target.Ref) map[target.Ref]bool {
	m := map[target.Ref]bool{}
	for _, ref := range refs {
		m[ref] = true
	}
	return m
}

// observe は、起動から at 経った時刻に coordination board を突き合わせる。orphans は前の tick に生きていた orphan。
func (w *world) observe(at time.Duration, orphans ...target.Ref) []coord.Outcome {
	w.t.Helper()
	out, err := w.board.Observe(func() time.Time { return start.Add(at) }, set(orphans))
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// claim は ref の着手宣言を置き、置けたことを確かめる。
func (w *world) claim(ref target.Ref) {
	w.t.Helper()
	if out := w.board.Claim(ref); !reflect.DeepEqual(out, coord.ClaimPlaced{}) {
		w.t.Fatalf("Claim(%v) = %#v, want ClaimPlaced", ref, out)
	}
}

// beating は自分の生存報告を置く (Board が置き直した後の状態)。
func (w *world) beating() {
	w.t.Helper()
	if err := w.Heartbeat(beat); err != nil {
		w.t.Fatal(err)
	}
}

// ownClaim は、自分が置いた ref の着手宣言の sha。
func (w *world) ownClaim(ref target.Ref) string { return w.Claims()[ref] }

func wantOutcomes(t *testing.T, got []coord.Outcome, want ...coord.Outcome) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outcomes = %#v\nwant       %#v", got, want)
	}
}

func wantDeleted(t *testing.T, w *world, want ...string) {
	t.Helper()
	if !slices.Equal(w.deleted, want) {
		t.Fatalf("消した ref = %v, want %v", w.deleted, want)
	}
}

func TestAClaimIsHeldUntilItsOwnerIsJudgedDead(t *testing.T) {
	type obs struct {
		at  time.Duration
		put func(w *world)
	}
	renew := func(loop string) func(*world) {
		return func(w *world) { w.Put(nil, beatOf(loop, beat)) }
	}
	deadHeartbeat := func(interval time.Duration) func(*world) {
		return func(w *world) {
			w.Put(nil, beatOf(dead, interval))
			w.Put(&issue42, claimBy(dead, issue42))
		}
	}
	claimOnly := func(w *world) { w.Put(&issue42, claimBy(dead, issue42)) }
	cases := []struct {
		name  string
		setup func(w *world)
		obs   []obs
		held  bool
	}{
		{"生存報告を置き直し続ける loop の着手宣言は死んだとみなさない", deadHeartbeat(beat),
			[]obs{{0, nil}, {beat, renew(dead)}, {2 * beat, renew(dead)}, {3 * beat, renew(dead)}, {4 * beat, renew(dead)}}, true},
		{"生存報告が間隔の 3 倍に満たないうちは生きている", deadHeartbeat(beat), []obs{{0, nil}, {3*beat - time.Second, nil}}, true},
		{"生存報告が間隔の 3 倍変わらなければ死んだとみなす", deadHeartbeat(beat), []obs{{0, nil}, {3 * beat, nil}}, false},
		{"死の判定は報告に書かれた間隔で数える (2 分なら 6 分に満たないうちは生きている)", deadHeartbeat(2 * beat), []obs{{0, nil}, {6*beat - time.Second, nil}}, true},
		{"死の判定は報告に書かれた間隔で数える (2 分なら 6 分で死んだ)", deadHeartbeat(2 * beat), []obs{{0, nil}, {6 * beat, nil}}, false},
		{"持ち主の生存報告が無い着手宣言は、自分の間隔の 3 倍に満たないうちは生きている", claimOnly, []obs{{0, nil}, {3*beat - time.Second, nil}}, true},
		{"持ち主の生存報告が無い着手宣言は、自分の間隔の 3 倍変わらなければ死んだ", claimOnly, []obs{{0, nil}, {3 * beat, nil}}, false},
		{"経過は sha を初めて見た時刻から数える", claimOnly, []obs{{10 * beat, nil}, {13*beat - time.Second, nil}}, true},
		{"着手宣言の sha が変われば数え直す", claimOnly,
			[]obs{{0, nil}, {2 * beat, claimOnly}, {5*beat - time.Second, nil}}, true},
		{"着手宣言の sha が変わっても、数え直した 3 倍が過ぎれば死んだ", claimOnly,
			[]obs{{0, nil}, {2 * beat, claimOnly}, {5 * beat, nil}}, false},
		{"持ち主を読めない着手宣言は死んだとみなさない", func(w *world) { w.Put(&issue42, claimBy("", issue42)) }, []obs{{0, nil}, {60 * beat, nil}}, true},
		{"間隔を読めない生存報告の loop は死んだとみなさない", deadHeartbeat(0), []obs{{0, nil}, {60 * beat, nil}}, true},
		{"新しい版の ref がある間は、死んだとみなさない", func(w *world) {
			claimOnly(w)
			w.Put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format + 1, Loop: "newer-00000004", Interval: beat})
		}, []obs{{0, nil}, {60 * beat, nil}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			c.setup(w)
			for _, o := range c.obs {
				if o.put != nil {
					o.put(w)
				}
				w.observe(o.at)
			}

			if got := w.board.Held(issue42); got != c.held {
				t.Fatalf("Held = %v, want %v", got, c.held)
			}
		})
	}
}

func TestOnlyTheClaimsOfOtherLoopsAreHeld(t *testing.T) {
	w := newWorld(t)
	w.Put(&issue42, claimBy(other, issue42))
	w.Put(&issue7, claimBy(me, issue7))

	w.observe(0)

	if !w.board.Held(issue42) || w.board.Held(issue7) || w.board.Held(cl3) {
		t.Fatalf("Held(42, 7, cl3) = %v, %v, %v, want 他の loop の着手宣言だけ", w.board.Held(issue42), w.board.Held(issue7), w.board.Held(cl3))
	}
	if got := w.board.Holder(issue42); got != other {
		t.Fatalf("Holder = %q, want %q", got, other)
	}
}

func TestClaimPushesWithTheExpectationThatMatchesTheHolder(t *testing.T) {
	t.Run("誰も持っていない作業対象は無いときだけ作る", func(t *testing.T) {
		w := newWorld(t)
		w.observe(0)

		out := w.board.Claim(issue42)

		if !reflect.DeepEqual(out, coord.ClaimPlaced{}) || !slices.Equal(w.claimed, []string{"issue#42 absent"}) {
			t.Fatalf("Claim = %#v・push %v", out, w.claimed)
		}
		if got := w.Message(w.ownClaim(issue42)).Loop; got != me {
			t.Fatalf("置いた着手宣言の持ち主 = %q", got)
		}
	})
	t.Run("死んだ loop の着手宣言は旧 sha を期待値にして乗っ取る", func(t *testing.T) {
		w := newWorld(t)
		old := w.Put(&issue42, claimBy(dead, issue42))
		w.observe(0)
		w.observe(3 * beat)

		out := w.board.Claim(issue42)

		if !reflect.DeepEqual(out, coord.ClaimTookOver{From: dead}) || !slices.Equal(w.claimed, []string{"issue#42 holding " + old}) {
			t.Fatalf("Claim = %#v・push %v", out, w.claimed)
		}
	})
	t.Run("他の生きている loop の着手宣言は、push せずに負けとする", func(t *testing.T) {
		for name, holder := range map[string]string{"生きている loop": other, "持ち主を読めない着手宣言": ""} {
			w := newWorld(t)
			w.Put(&issue42, claimBy(holder, issue42))
			w.observe(0)

			out := w.board.Claim(issue42)

			if !reflect.DeepEqual(out, coord.ClaimLost{}) || len(w.claimed) != 0 {
				t.Fatalf("%s: Claim = %#v・push %v, want push せずに ClaimLost", name, out, w.claimed)
			}
		}
	})
	t.Run("引き継いだ着手宣言がある作業対象は、push せずに負けとする", func(t *testing.T) {
		w, _ := inherited(t)

		out := w.board.Claim(issue42)

		if !reflect.DeepEqual(out, coord.ClaimLost{}) || len(w.claimed) != 0 {
			t.Fatalf("Claim = %#v・push %v, want push せずに ClaimLost", out, w.claimed)
		}
	})
	t.Run("先に他の loop が置いたら負ける", func(t *testing.T) {
		w := newWorld(t)
		w.observe(0)
		w.Put(&issue42, claimBy(other, issue42))

		out := w.board.Claim(issue42)

		if !reflect.DeepEqual(out, coord.ClaimLost{}) {
			t.Fatalf("Claim = %#v, want ClaimLost", out)
		}
		w.board.Release(issue42)
		wantDeleted(t, w)
	})
	t.Run("負け以外の失敗は着手宣言を置いたことにしない", func(t *testing.T) {
		w := newWorld(t)
		w.observe(0)
		w.FailClaim(errPush)

		out := w.board.Claim(issue42)

		if !reflect.DeepEqual(out, coord.ClaimFailed{Err: errPush}) {
			t.Fatalf("Claim = %#v, want ClaimFailed", out)
		}
		w.board.Release(issue42)
		wantDeleted(t, w)
	})
}

func TestReleaseDeletesOnlyTheClaimItPlaced(t *testing.T) {
	w := newWorld(t)
	w.observe(0)
	w.claim(issue42)
	sha := w.ownClaim(issue42)

	wantOutcomes(t, w.board.Release(issue42))
	wantOutcomes(t, w.board.Release(issue42))

	wantDeleted(t, w, coordtest.ClaimName(issue42)+" "+sha)
	if len(w.Claims()) != 0 {
		t.Fatalf("着手宣言 = %v, want 消えている", w.Claims())
	}
}

func TestAFailedDeleteIsReturnedAsAFailedReleaseWhenReleasing(t *testing.T) {
	w := newWorld(t)
	w.observe(0)
	w.claim(issue42)
	w.FailDelete(errPush)

	wantOutcomes(t, w.board.Release(issue42), coord.ClaimReleaseFailed{Ref: issue42, Err: errPush})
}

func TestALostDeleteIsReturnedAsALostReleaseWhenReleasing(t *testing.T) {
	w := newWorld(t)
	w.observe(0)
	w.claim(issue42)
	w.Put(&issue42, claimBy(other, issue42))

	wantOutcomes(t, w.board.Release(issue42), coord.ClaimReleaseLost{Ref: issue42, Err: coord.ErrLost})
}

func TestAClaimThatWasTakenOverIsReportedOnceAndNeverDeleted(t *testing.T) {
	unreadable := coord.TakenOver{Ref: issue42, Unreadable: true}
	cases := []struct {
		name   string
		change func(w *world, mine string)
		want   []coord.Outcome
	}{
		{"他の loop の着手宣言に置き換わった", func(w *world, _ string) { w.Put(&issue42, claimBy(other, issue42)) },
			[]coord.Outcome{coord.TakenOver{Ref: issue42, By: other}}},
		{"着手宣言が消えていた", func(w *world, mine string) { _ = w.Memory.DeleteClaim(issue42, mine) },
			[]coord.Outcome{coord.TakenOver{Ref: issue42}}},
		{"持ち主を読めない着手宣言に置き換わった", func(w *world, _ string) { w.Put(&issue42, claimBy("", issue42)) },
			[]coord.Outcome{coord.OwnerUnreadable{Ref: issue42, Name: coordtest.ClaimName(issue42)}, unreadable}},
		{"commit message を読めない着手宣言に置き換わった", func(w *world, _ string) {
			w.Unreadable(errFetch, w.Put(&issue42, claimBy(other, issue42)))
		}, []coord.Outcome{coord.Unread{Err: errFetch}, unreadable}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.observe(0)
			w.claim(issue42)
			c.change(w, w.ownClaim(issue42))

			got := w.observe(beat)

			wantOutcomes(t, got, c.want...)
			taken := c.want[len(c.want)-1].(coord.TakenOver)
			if a := w.board.Attention(); !slices.Equal(a.TakenOver, []coord.TakenOver{taken}) {
				t.Fatalf("要対処の乗っ取られた作業対象 = %v, want %v", a.TakenOver, taken)
			}
			wantOutcomes(t, w.board.Release(issue42))
			w.Unreadable(nil)
			wantOutcomes(t, w.observe(2*beat))
			wantDeleted(t, w)
		})
	}
}

func TestALeftoverClaimOfMyOwnIsDeletedUnlessItIsInUse(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(w *world) (sha string)
		orphans []target.Ref
		err     error
		want    func(sha string) []coord.Outcome
		deleted bool
	}{
		{"claim の無い作業対象の自分の着手宣言は消す", func(w *world) string { return w.Put(&issue7, claimBy(me, issue7)) }, nil, nil,
			func(string) []coord.Outcome { return nil }, true},
		{"消せなければ Outcome を返す", func(w *world) string { return w.Put(&issue7, claimBy(me, issue7)) }, nil, errPush,
			func(string) []coord.Outcome {
				return []coord.Outcome{coord.StaleDeleteFailed{Ref: issue7, Err: errPush}}
			}, true},
		{"負けは読み飛ばす", func(w *world) string { return w.Put(&issue7, claimBy(me, issue7)) }, nil, coord.ErrLost,
			func(string) []coord.Outcome { return nil }, true},
		{"他の loop の着手宣言は消さない", func(w *world) string { return w.Put(&issue7, claimBy(other, issue7)) }, nil, nil,
			func(string) []coord.Outcome { return nil }, false},
		{"持ち主を読めない着手宣言は消さない", func(w *world) string { return w.Put(&issue7, claimBy("", issue7)) }, nil, nil,
			func(string) []coord.Outcome {
				return []coord.Outcome{coord.OwnerUnreadable{Ref: issue7, Name: coordtest.ClaimName(issue7)}}
			}, false},
		{"前の tick に生きていた orphan の作業対象のものは、引き継いだものとして覚えて消さない", func(w *world) string { return w.Put(&issue7, claimBy(me, issue7)) },
			[]target.Ref{issue7}, nil, func(string) []coord.Outcome { return nil }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			sha := c.setup(w)
			w.FailDelete(c.err)

			got := w.observe(0, c.orphans...)

			wantOutcomes(t, got, c.want(sha)...)
			if c.deleted {
				wantDeleted(t, w, coordtest.ClaimName(issue7)+" "+sha)
			} else {
				wantDeleted(t, w)
			}
		})
	}
}

func TestAClaimIPlacedIsNotAStaleClaim(t *testing.T) {
	w := newWorld(t)
	w.observe(0)
	w.claim(issue42)

	wantOutcomes(t, w.observe(beat))

	wantDeleted(t, w)
}

func TestAnOrphansDeadHoldersClaimIsInheritedOnlyWhereTheOrphanWorks(t *testing.T) {
	const hyphenated = "s-watanabe-mbp-1a2b3c4d"
	orphan42 := []target.Ref{issue42}
	deadAt := []time.Duration{0, 3 * beat}
	cases := []struct {
		name string
		// me は自分の loop 名。holder は着手宣言の持ち主の loop 名で、heartbeat なら、その生存報告も置く
		me        string
		holder    string
		heartbeat bool
		// newer なら、新しい版の生存報告も置く
		newer bool
		// at は突き合わせの時刻 (最後の突き合わせの後に Tidy を撃つ)。orphans はその間生きている orphan
		at      []time.Duration
		orphans []target.Ref
		// race は最後の突き合わせの後、Tidy の前に remote を書き換える (他のマシンの loop の書き込み)
		race func(w *world)
		// from は引き継いだ着手宣言の持ち主。"" なら引き継がず、Tidy が返すのは others だけ
		from   string
		others []coord.Outcome
	}{
		{name: "死んだ loop の着手宣言を引き継ぐ", me: me, holder: dead, at: deadAt, orphans: orphan42, from: dead},
		{name: "死の判定を待たずに、同じマシンの前の loop の着手宣言を引き継ぐ", me: me, holder: "me-00000009", heartbeat: true, at: []time.Duration{0}, orphans: orphan42, from: "me-00000009"},
		{name: "hostname に - を含むマシンでも、hostname の部分で同じマシンと見分ける", me: hyphenated, holder: "s-watanabe-mbp-0000ffff", heartbeat: true, at: []time.Duration{0}, orphans: orphan42, from: "s-watanabe-mbp-0000ffff"},
		{name: "hostname の前半だけが同じ別のマシンの生きている loop の着手宣言は引き継がない", me: hyphenated, holder: "s-watanabe-0000ffff", heartbeat: true, at: []time.Duration{0}, orphans: orphan42},
		{name: "別のマシンの生きている loop の着手宣言は引き継がない", me: me, holder: other, heartbeat: true, at: []time.Duration{0}, orphans: orphan42},
		{name: "orphan が生きていない作業対象の着手宣言は引き継がず、死んだ loop のものとして消す", me: me, holder: dead, at: deadAt,
			others: []coord.Outcome{coord.DeadLoopCleaned{Loop: dead, Refs: []string{coordtest.ClaimName(issue42)}, Claims: 1}}},
		{name: "新しい版の ref がある間は引き継がない", me: me, holder: dead, newer: true, at: deadAt, orphans: orphan42},
		{name: "新しい版の ref がある間は、同じマシンの前の loop の着手宣言も引き継がない", me: me, holder: "me-00000009", heartbeat: true, newer: true, at: []time.Duration{0}, orphans: orphan42},
		{name: "持ち主を読めない着手宣言は引き継がない", me: me, holder: "", at: deadAt, orphans: orphan42},
		{name: "引き継ぎの push に負けたら読み飛ばす", me: me, holder: dead, at: deadAt, orphans: orphan42,
			race: func(w *world) { w.Put(&issue42, claimBy(other, issue42)) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorldOf(t, c.me)
			if c.heartbeat {
				w.Put(nil, beatOf(c.holder, beat))
			}
			if c.newer {
				w.Put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format + 1, Loop: "newer-00000004", Interval: beat})
			}
			old := w.Put(&issue42, claimBy(c.holder, issue42))
			for _, at := range c.at {
				w.observe(at, c.orphans...)
			}
			if c.race != nil {
				c.race(w)
			}

			got := w.board.Tidy(set(c.orphans), nil)

			if c.from == "" {
				wantOutcomes(t, got, c.others...)
				if w.Message(w.ownClaim(issue42)).Loop == c.me {
					t.Fatal("引き継がないはずの着手宣言を、自分の名前に置き換えた")
				}
				return
			}
			wantOutcomes(t, got, coord.Inherited{Ref: issue42, From: c.from})
			if !slices.Equal(w.claimed, []string{"issue#42 holding " + old}) || w.Message(w.ownClaim(issue42)).Loop != c.me {
				t.Fatalf("着手宣言の push = %v (持ち主 %q), want 旧 sha を期待値にした置き換えで自分の名前に引き継ぐ", w.claimed, w.Message(w.ownClaim(issue42)).Loop)
			}
			if !w.board.Held(issue42) {
				t.Fatal("引き継いだ着手宣言が残る間は、起動しない")
			}
		})
	}
}

func TestAClaimIPlacedOnAnOrphansTargetIsNotInherited(t *testing.T) {
	w := newWorld(t)
	w.Put(&issue42, claimBy(dead, issue42))
	w.observe(0, issue42)
	w.observe(3*beat, issue42)
	if out := w.board.Claim(issue42); !reflect.DeepEqual(out, coord.ClaimTookOver{From: dead}) {
		t.Fatalf("Claim = %#v", out)
	}
	w.claimed = nil

	wantOutcomes(t, w.board.Tidy(set([]target.Ref{issue42}), nil))

	if len(w.claimed) != 0 {
		t.Fatalf("着手宣言の push = %v, want 自分の着手宣言の作業対象は引き継がない", w.claimed)
	}
}

func TestAnInheritWithAnUnknownResultIsRememberedAndSettledByTheNextObservation(t *testing.T) {
	inheritUnsure := func(t *testing.T) (*world, string) {
		w := newWorldOf(t, me)
		w.Put(nil, beatOf("me-00000009", beat))
		old := w.Put(&issue42, claimBy("me-00000009", issue42))
		w.observe(0, issue42)
		// push は remote に届いたが、結果の分からない失敗を返す
		w.FailClaim(errPush)
		wantOutcomes(t, w.board.Tidy(set([]target.Ref{issue42}), nil), coord.InheritFailed{Ref: issue42, Err: errPush})
		w.FailClaim(nil)
		if !slices.Equal(w.claimed, []string{"issue#42 holding " + old}) {
			t.Fatalf("着手宣言の push = %v", w.claimed)
		}
		return w, old
	}
	t.Run("次の突き合わせの前に止まっても、引き継いだものと同じに扱って生存報告を消さない", func(t *testing.T) {
		w, _ := inheritUnsure(t)

		wantOutcomes(t, w.board.Leave(set([]target.Ref{issue42}), nil), coord.UnsureInheritKept{Ref: issue42})

		if w.left != 0 {
			t.Fatalf("Leave %d 回, want 生存報告を消さずに止まる", w.left)
		}
	})
	t.Run("次の突き合わせで着手宣言の commit message を読めない間は覚えたまま", func(t *testing.T) {
		w, _ := inheritUnsure(t)
		w.Unreadable(errFetch, w.ownClaim(issue42))

		wantOutcomes(t, w.observe(beat, issue42), coord.Unread{Err: errFetch})

		wantOutcomes(t, w.board.Leave(set([]target.Ref{issue42}), nil), coord.UnsureInheritKept{Ref: issue42})
		if w.left != 0 {
			t.Fatalf("Leave %d 回, want 生存報告を消さずに止まる", w.left)
		}
	})
	t.Run("次の突き合わせで届いていた着手宣言を読めたら、引き継いだものとして覚え直す", func(t *testing.T) {
		w, _ := inheritUnsure(t)

		wantOutcomes(t, w.observe(beat, issue42))

		if !w.board.Held(issue42) {
			t.Fatal("引き継いだものとして覚え直した着手宣言が残る間は、起動しない")
		}
		wantDeleted(t, w)
		wantOutcomes(t, w.board.Leave(set([]target.Ref{issue42}), nil), coord.InheritKept{Ref: issue42, OrphanAlive: true})
	})
	t.Run("次の突き合わせで着手宣言が無ければ忘れる", func(t *testing.T) {
		w, _ := inheritUnsure(t)
		_ = w.Memory.DeleteClaim(issue42, w.ownClaim(issue42))

		wantOutcomes(t, w.observe(beat, issue42))

		wantOutcomes(t, w.board.Leave(set([]target.Ref{issue42}), nil))
		if w.left != 1 {
			t.Fatalf("Leave %d 回, want 生存報告を消す", w.left)
		}
	})
	t.Run("止まるときに orphan が終わっていれば、覚えていても生存報告を消す", func(t *testing.T) {
		w, _ := inheritUnsure(t)

		wantOutcomes(t, w.board.Leave(nil, nil))

		if w.left != 1 {
			t.Fatalf("Leave %d 回, want 生存報告を消す", w.left)
		}
	})
}

// inherited は、生きている orphan の作業対象のために dead の着手宣言を引き継いだ world と、引き継いだ着手宣言の sha を返す。
func inherited(t *testing.T) (*world, string) {
	t.Helper()
	w := newWorld(t)
	w.Put(&issue42, claimBy(dead, issue42))
	w.observe(0, issue42)
	w.observe(3*beat, issue42)
	wantOutcomes(t, w.board.Tidy(set([]target.Ref{issue42}), nil), coord.Inherited{Ref: issue42, From: dead})
	w.claimed, w.deleted = nil, nil
	return w, w.ownClaim(issue42)
}

func TestAnInheritedClaimIsDeletedWhenTheOrphanEnds(t *testing.T) {
	w, sha := inherited(t)

	wantOutcomes(t, w.board.ReleaseInherited(set([]target.Ref{issue42})))
	wantDeleted(t, w)
	if !w.board.Held(issue42) {
		t.Fatal("orphan が生きている間は、引き継いだ着手宣言を残して起動しない")
	}

	wantOutcomes(t, w.board.ReleaseInherited(nil))

	wantDeleted(t, w, coordtest.ClaimName(issue42)+" "+sha)
	if w.board.Held(issue42) {
		t.Fatal("orphan が終わって消したら、起動できる")
	}
}

func TestAnInheritedClaimIsNotDeletedAsAStaleClaimEvenAfterTheOrphanEnded(t *testing.T) {
	w, _ := inherited(t)

	// 前の tick で orphan が終わっていても、引き継いだ着手宣言は ReleaseInherited が消すので、突き合わせでは消さない
	wantOutcomes(t, w.observe(4*beat))

	wantDeleted(t, w)
	if !w.board.Held(issue42) {
		t.Fatal("引き継いだ着手宣言が残る間は、起動しない")
	}
}

func TestAnInheritedClaimThatCannotBeDeletedKeepsTheTargetFromLaunching(t *testing.T) {
	w, _ := inherited(t)
	w.FailDelete(errPush)

	wantOutcomes(t, w.board.ReleaseInherited(nil), coord.InheritedReleaseFailed{Ref: issue42, Err: errPush})

	if !w.board.Held(issue42) {
		t.Fatal("消せなかった引き継いだ着手宣言が残る間は、起動しない")
	}
	w.FailDelete(nil)
	wantOutcomes(t, w.board.ReleaseInherited(nil))
	if w.board.Held(issue42) {
		t.Fatal("消し直したら、起動できる")
	}
}

func TestAnInheritedClaimIsForgottenWhenItsSHAChanges(t *testing.T) {
	t.Run("他の loop の着手宣言に置き換わったら、二重に動きうることを返して忘れる", func(t *testing.T) {
		w, _ := inherited(t)
		w.Put(&issue42, claimBy(other, issue42))

		wantOutcomes(t, w.observe(4*beat, issue42), coord.InheritReplaced{Ref: issue42})

		wantOutcomes(t, w.board.Leave(set([]target.Ref{issue42}), nil))
		wantDeleted(t, w)
	})
	t.Run("消えていたら何も返さずに忘れる", func(t *testing.T) {
		w, sha := inherited(t)
		_ = w.Memory.DeleteClaim(issue42, sha)

		wantOutcomes(t, w.observe(4*beat, issue42))

		wantOutcomes(t, w.board.Leave(set([]target.Ref{issue42}), nil))
		wantDeleted(t, w)
	})
}

func TestTheDeadLoopsRefsAreDeletedExceptTheOnesInUse(t *testing.T) {
	setup := func(w *world) (heartbeat, claim7, claim42, claimCL string) {
		heartbeat = w.Put(nil, beatOf(dead, beat))
		claim7 = w.Put(&issue7, claimBy(dead, issue7))
		claim42 = w.Put(&issue42, claimBy(dead, issue42))
		claimCL = w.Put(&cl3, claimBy(dead, cl3))
		w.observe(0)
		w.observe(3 * beat)
		return heartbeat, claim7, claim42, claimCL
	}
	t.Run("着手宣言と生存報告を消して、消した ref の名前を返す", func(t *testing.T) {
		w := newWorld(t)
		heartbeat, claim7, claim42, claimCL := setup(w)

		got := w.board.Tidy(nil, nil)

		wantOutcomes(t, got, coord.DeadLoopCleaned{Loop: dead, Refs: []string{coordtest.ClaimName(cl3), coordtest.ClaimName(issue7), coordtest.ClaimName(issue42), coordtest.HeartbeatName(dead)}, Heartbeat: true, Claims: 3})
		wantDeleted(t, w, coordtest.ClaimName(cl3)+" "+claimCL, coordtest.ClaimName(issue7)+" "+claim7, coordtest.ClaimName(issue42)+" "+claim42, coordtest.HeartbeatName(dead)+" "+heartbeat)
		wantOutcomes(t, w.board.Tidy(nil, nil))
	})
	t.Run("自分が着手宣言を置いた作業対象と、claude が作業を続けている作業対象と、orphan の作業対象は消さない", func(t *testing.T) {
		w := newWorld(t)
		heartbeat, _, _, _ := setup(w)
		if out := w.board.Claim(issue7); !reflect.DeepEqual(out, coord.ClaimTookOver{From: dead}) {
			t.Fatalf("Claim = %#v", out)
		}
		w.claimed = nil

		got := w.board.Tidy(set([]target.Ref{issue42}), set([]target.Ref{cl3}))

		// orphan の作業対象の着手宣言は、消さずに引き継ぐ
		wantOutcomes(t, got, coord.Inherited{Ref: issue42, From: dead}, coord.DeadLoopCleaned{Loop: dead, Refs: []string{coordtest.HeartbeatName(dead)}, Heartbeat: true})
		wantDeleted(t, w, coordtest.HeartbeatName(dead)+" "+heartbeat)
	})
	t.Run("生存報告の無い loop の着手宣言だけを消す", func(t *testing.T) {
		w := newWorld(t)
		claim := w.Put(&issue7, claimBy(dead, issue7))
		w.observe(0)
		w.observe(3 * beat)

		got := w.board.Tidy(nil, nil)

		wantOutcomes(t, got, coord.DeadLoopCleaned{Loop: dead, Refs: []string{coordtest.ClaimName(issue7)}, Claims: 1})
		wantDeleted(t, w, coordtest.ClaimName(issue7)+" "+claim)
	})
	t.Run("消せなければ Outcome を返し、消した ref が無ければ dead_loop にしない", func(t *testing.T) {
		w := newWorld(t)
		setup(w)
		w.FailDelete(errPush)

		got := w.board.Tidy(nil, nil)

		wantOutcomes(t, got,
			coord.DeadRefDeleteFailed{Ref: cl3, Loop: dead, Err: errPush},
			coord.DeadRefDeleteFailed{Ref: issue7, Loop: dead, Err: errPush},
			coord.DeadRefDeleteFailed{Ref: issue42, Loop: dead, Err: errPush},
			coord.DeadRefDeleteFailed{Loop: dead, Err: errPush})
	})
	t.Run("消すときの負けは読み飛ばす (他の loop が先に消したか乗っ取った)", func(t *testing.T) {
		w := newWorld(t)
		setup(w)
		w.Put(&issue7, claimBy(other, issue7))
		w.Put(&issue42, claimBy(other, issue42))
		w.Put(nil, beatOf(dead, beat))

		got := w.board.Tidy(nil, nil)

		wantOutcomes(t, got, coord.DeadLoopCleaned{Loop: dead, Refs: []string{coordtest.ClaimName(cl3)}, Claims: 1})
	})
	t.Run("新しい版の ref がある間は、死んだ loop を判定せず消さない", func(t *testing.T) {
		w := newWorld(t)
		w.Put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format + 1, Loop: "newer-00000004", Interval: beat})
		setup(w)

		wantOutcomes(t, w.board.Tidy(nil, nil))
		wantDeleted(t, w)
	})
}

func TestLeaveDeletesTheClaimsAndTheHeartbeatExceptWhereAClaudeMayStillWork(t *testing.T) {
	t.Run("claim の着手宣言と生存報告を消す", func(t *testing.T) {
		w := newWorld(t)
		w.observe(0)
		w.claim(issue42)
		w.claim(cl3)
		w.beating()
		a, b := w.ownClaim(issue42), w.ownClaim(cl3)

		wantOutcomes(t, w.board.Leave(nil, nil))

		wantDeleted(t, w, coordtest.ClaimName(cl3)+" "+b, coordtest.ClaimName(issue42)+" "+a)
		if len(w.Claims()) != 0 || len(w.Loops()) != 0 || w.left != 1 {
			t.Fatalf("残った着手宣言 %v・生存報告 %v (Leave %d 回)", w.Claims(), w.Loops(), w.left)
		}
	})
	t.Run("止められない claude の作業対象の着手宣言と生存報告は残す", func(t *testing.T) {
		w := newWorld(t)
		w.observe(0)
		w.claim(issue42)
		w.claim(cl3)
		w.beating()

		wantOutcomes(t, w.board.Leave(nil, set([]target.Ref{issue42})))

		if _, kept := w.Claims()[issue42]; !kept || len(w.Claims()) != 1 {
			t.Fatalf("着手宣言 = %v, want 止められない claude の作業対象だけ残す", w.Claims())
		}
		if len(w.Loops()) != 1 || w.left != 0 {
			t.Fatalf("生存報告 = %v (Leave %d 回), want 消さずに途絶えさせる", w.Loops(), w.left)
		}
	})
	t.Run("着手宣言を消せなければ Outcome を返し、生存報告は消す", func(t *testing.T) {
		w := newWorld(t)
		w.observe(0)
		w.claim(issue42)
		w.FailDelete(errPush)

		wantOutcomes(t, w.board.Leave(nil, nil), coord.ClaimReleaseFailed{Ref: issue42, Err: errPush})

		if w.left != 1 {
			t.Fatalf("Leave %d 回, want 1", w.left)
		}
	})
	t.Run("生存報告を他の loop に置き換えられていたら LeaveFailed を返す", func(t *testing.T) {
		w := newWorld(t)
		w.beating()
		w.Put(nil, beatOf(me, beat))

		wantOutcomes(t, w.board.Leave(nil, nil), coord.LeaveFailed{Err: coord.ErrLost})
	})
	t.Run("生存報告を消せなければ Outcome を返す", func(t *testing.T) {
		w := newWorld(t)
		w.FailLeave(errPush)

		wantOutcomes(t, w.board.Leave(nil, nil), coord.LeaveFailed{Err: errPush})
	})
	t.Run("orphan が生きている引き継いだ着手宣言は残し、生存報告も残す", func(t *testing.T) {
		w, _ := inherited(t)

		wantOutcomes(t, w.board.Leave(set([]target.Ref{issue42}), nil), coord.InheritKept{Ref: issue42, OrphanAlive: true})

		if len(w.Claims()) != 1 || w.left != 0 {
			t.Fatalf("着手宣言 %v・Leave %d 回, want 引き継いだ着手宣言も生存報告も残す", w.Claims(), w.left)
		}
	})
	t.Run("orphan は終わったが消せなかった引き継いだ着手宣言は、生存報告を消して止まる", func(t *testing.T) {
		w, _ := inherited(t)
		w.FailDelete(errPush)

		got := w.board.Leave(nil, nil)

		wantOutcomes(t, got, coord.InheritedReleaseFailed{Ref: issue42, Err: errPush}, coord.InheritKept{Ref: issue42})
		if w.left != 1 {
			t.Fatalf("Leave %d 回, want orphan が終わっていれば生存報告を消す", w.left)
		}
	})
	t.Run("orphan が終わっていれば引き継いだ着手宣言を消して止まる", func(t *testing.T) {
		w, sha := inherited(t)

		wantOutcomes(t, w.board.Leave(nil, nil))

		wantDeleted(t, w, coordtest.ClaimName(issue42)+" "+sha)
		if w.left != 1 {
			t.Fatalf("Leave %d 回, want 1", w.left)
		}
	})
}

func TestARefThatCannotBeReadIsReportedOnce(t *testing.T) {
	t.Run("持ち主の loop を読めない着手宣言", func(t *testing.T) {
		w := newWorld(t)
		sha := w.Put(&issue42, claimBy("", issue42))

		wantOutcomes(t, w.observe(0), coord.OwnerUnreadable{Ref: issue42, Name: coordtest.ClaimName(issue42)})
		wantOutcomes(t, w.observe(beat))
		w.Put(&issue42, claimBy("", issue42))
		if got := w.Claims()[issue42]; got == sha {
			t.Fatal("sha が変わっていない")
		}
		wantOutcomes(t, w.observe(2*beat), coord.OwnerUnreadable{Ref: issue42, Name: coordtest.ClaimName(issue42)})
	})
	t.Run("間隔を読めない生存報告", func(t *testing.T) {
		w := newWorld(t)
		w.Put(nil, beatOf("odd-00000004", 0))

		wantOutcomes(t, w.observe(0), coord.IntervalUnreadable{Loop: "odd-00000004", Name: coordtest.HeartbeatName("odd-00000004")})
		wantOutcomes(t, w.observe(beat))
	})
	t.Run("自分の生存報告と新しい版の生存報告は、間隔を読めなくても返さない", func(t *testing.T) {
		w := newWorld(t)
		w.Put(nil, beatOf(me, 0))
		w.Put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format + 1, Loop: "newer-00000004"})

		wantOutcomes(t, w.observe(0))
	})
	t.Run("commit message を読めなかった tick の後、初めて読めた tick に 1 回だけ返す", func(t *testing.T) {
		w := newWorld(t)
		sha := w.Put(nil, beatOf("odd-00000004", 0))
		w.Unreadable(errFetch, sha)

		wantOutcomes(t, w.observe(0), coord.Unread{Err: errFetch})
		wantOutcomes(t, w.observe(beat), coord.Unread{Err: errFetch})
		w.Unreadable(nil)
		wantOutcomes(t, w.observe(2*beat), coord.IntervalUnreadable{Loop: "odd-00000004", Name: coordtest.HeartbeatName("odd-00000004")})
		wantOutcomes(t, w.observe(3*beat))
	})
}

func TestARefOfANewerFormatIsReportedAsAttention(t *testing.T) {
	cases := []struct {
		name   string
		format int
		want   int
	}{
		{"知る版より新しい版", coord.Format + 1, coord.Format + 1},
		{"版を読めない", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			if got := w.board.Attention().NewerFormat; got != nil || w.board.Newer() {
				t.Fatalf("突き合わせる前から新しい版を見ている: %v", got)
			}
			w.Put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: c.format, Loop: "newer-00000004", Interval: beat})

			w.observe(0)

			if got := w.board.Attention().NewerFormat; !w.board.Newer() || got == nil || got.Format != c.want {
				t.Fatalf("要対処の新しい版 = %v (Newer %v), want 版 %d", got, w.board.Newer(), c.want)
			}
		})
	}
}

func TestObservingFailsWhenTheRefsCannotBeReadAndChangesNothing(t *testing.T) {
	r := &unreadableRefs{Memory: coordtest.NewMemory(me)}
	board := coord.NewBoard(r, beat)
	r.Put(&issue42, claimBy(other, issue42))

	out, err := board.Observe(func() time.Time { return start }, nil)

	if !errors.Is(err, errFetch) || out != nil {
		t.Fatalf("Observe = %v, %v, want 読めなかった error", out, err)
	}
	if board.Held(issue42) {
		t.Fatal("読めなかった突き合わせが、持ち主を決めた")
	}
}

func TestTheClockIsReadAfterTheRefsAreRead(t *testing.T) {
	r := &countingRefs{Memory: coordtest.NewMemory(me)}
	board := coord.NewBoard(r, beat)
	r.Put(&issue42, claimBy(other, issue42))

	_, err := board.Observe(func() time.Time {
		r.readsWhenClockWasRead = append(r.readsWhenClockWasRead, r.reads)
		return start
	}, nil)

	if err != nil || !slices.Equal(r.readsWhenClockWasRead, []int{1}) {
		t.Fatalf("時計を読んだときの ref の読み出しの回数 = %v (%v), want ref を読み終えた後に 1 回", r.readsWhenClockWasRead, err)
	}
}

// countingRefs は ref を読んだ回数を数える coordination board。
type countingRefs struct {
	*coordtest.Memory
	reads                 int
	readsWhenClockWasRead []int
}

func (r *countingRefs) Observe() (coord.View, error) {
	r.reads++
	return r.Memory.Observe()
}

// unreadableRefs は ref の一覧を読めない coordination board。
type unreadableRefs struct{ *coordtest.Memory }

func (*unreadableRefs) Observe() (coord.View, error) { return coord.View{}, errFetch }

func TestHeartbeatIsRenewedWithTheIntervalOfTheBoard(t *testing.T) {
	w := newWorld(t)
	clock := make(chan time.Time)
	renewed := make(chan struct{})
	r := &renewals{Memory: w.Memory, renewed: renewed}
	board := coord.NewBoard(r, 90*time.Second)
	var waits []time.Duration
	var mu sync.Mutex
	after := func(d time.Duration) <-chan time.Time {
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, d)
		return clock
	}
	stop := board.StartBeating(after, func(error) bool { return true })

	for range 2 {
		clock <- time.Time{}
		<-renewed
	}
	stop()

	if got := w.Message(w.Loops()[me]).Interval; got != 90*time.Second {
		t.Fatalf("生存報告に書いた間隔 = %v, want 1m30s", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, d := range waits {
		if d != 90*time.Second {
			t.Fatalf("待ち = %v, want 1m30s", waits)
		}
	}
}

// renewals は生存報告の置き直しを renewed へ知らせる。
type renewals struct {
	*coordtest.Memory
	renewed chan struct{}
}

func (r *renewals) Heartbeat(interval time.Duration) error {
	err := r.Memory.Heartbeat(interval)
	r.renewed <- struct{}{}
	return err
}

func TestResultsThatCannotBeDeliveredAreDroppedAndCounted(t *testing.T) {
	w := newWorld(t)
	clock := make(chan time.Time)
	renewed := make(chan struct{})
	board := coord.NewBoard(&renewals{Memory: w.Memory, renewed: renewed}, beat)
	stop := board.StartBeating(func(time.Duration) <-chan time.Time { return clock }, func(error) bool { return false })
	for range 3 {
		clock <- time.Time{}
		<-renewed
	}
	stop()

	wantOutcomes(t, board.Dropped(), coord.BeatsDropped{N: 3})
	wantOutcomes(t, board.Dropped())
}

func TestHeartbeatFailuresAreCountedWhileTheyContinue(t *testing.T) {
	w := newWorld(t)

	wantOutcomes(t, w.board.Beat(errPush), coord.BeatFailed{Err: errPush})
	if got := w.board.Attention().HeartbeatFailures; got != nil {
		t.Fatalf("1 回落ちただけで要対処: %+v", got)
	}
	wantOutcomes(t, w.board.Beat(errFetch), coord.BeatFailed{Err: errFetch})
	got := w.board.Attention().HeartbeatFailures
	if got == nil || got.Count != 2 || got.Last != errFetch {
		t.Fatalf("要対処の生存報告 = %+v, want 2 回続けて落ちた直近の理由", got)
	}

	wantOutcomes(t, w.board.Beat(nil))

	if got := w.board.Attention().HeartbeatFailures; got != nil {
		t.Fatalf("置き直せたのに要対処: %+v", got)
	}
}

func TestDroppedResultsAreReportedBeforeTheNextResult(t *testing.T) {
	w := newWorld(t)
	clock := make(chan time.Time)
	renewed := make(chan struct{})
	board := coord.NewBoard(&renewals{Memory: w.Memory, renewed: renewed}, beat)
	stop := board.StartBeating(func(time.Duration) <-chan time.Time { return clock }, func(error) bool { return false })
	clock <- time.Time{}
	<-renewed
	stop()

	wantOutcomes(t, board.Beat(errPush), coord.BeatsDropped{N: 1}, coord.BeatFailed{Err: errPush})
}
