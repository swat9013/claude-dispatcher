package loop_test

import (
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/coord"
	"github.com/swat9013/claude-dispatcher/internal/coord/coordtest"
	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/places"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/worker"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// マシンを跨いだ分担 (system.md §4・§7、formats.md §12)。持ち主と生死の判定は coord.Board の表の test (internal/coord/board_test.go) が
// 確かめる。ここでは、本物の coord.Board と in-memory の coordination board (coordtest.Memory) を loop に繋ぎ、Board を呼ぶ順と、
// 返った結果から書く行と、乗っ取られた claim の遷移を通しで見る。死んだ loop の判定は分の単位の経過を数えるので、black-box テストは
// 待てない。ここでは fake の時計で見る。

const me = "me-00000001"

var (
	issue42 = target.Ref{Kind: target.KindIssue, Number: 42}
	issue7  = target.Ref{Kind: target.KindIssue, Number: 7}
)

// flaky は coordtest.Memory に、他のマシンの loop の書き込みと生存報告の置き直しの知らせを足した coordination board。
// 失敗の注入は coordtest.Memory が持つ。
type flaky struct {
	*coordtest.Memory
	// onObserve は Observe の回数ごとに、読ませる前に呼ぶ (他のマシンの loop の書き込みの代役)
	onObserve func(times int)
	observed  int
	// unlisted が nil でなければ、Observe は ref の一覧を読めずにこれで落ちる (onObserve の後に見る)
	unlisted error
	// beforeClaim は着手宣言の push の直前に呼ぶ (他のマシンの loop に先を越される代役)
	beforeClaim func()
	// undelivered が nil でなければ、着手宣言の push を remote に届けずにこれを返す (届いたかの分からない失敗)
	undelivered error
	// renewed は、生存報告の置き直しがあったことを知らせる
	renewed chan struct{}
}

func newFlaky() *flaky { return &flaky{Memory: coordtest.NewMemory(me)} }

func (f *flaky) Observe() (coord.View, error) {
	f.observed++
	if f.onObserve != nil {
		f.onObserve(f.observed)
	}
	if f.unlisted != nil {
		return coord.View{}, f.unlisted
	}
	return f.Memory.Observe()
}

func (f *flaky) Claim(ref target.Ref, expect coord.Expect) (string, error) {
	if f.beforeClaim != nil {
		f.beforeClaim()
	}
	if f.undelivered != nil {
		return "", f.undelivered
	}
	return f.Memory.Claim(ref, expect)
}

func (f *flaky) Heartbeat(interval time.Duration) error {
	if f.renewed != nil {
		select {
		case f.renewed <- struct{}{}:
		default:
		}
	}
	return f.Memory.Heartbeat(interval)
}

// beating は自分の生存報告を置く (起動時に呼び出し側が置く最初の生存報告の代役)。
func (f *flaky) beating(t *testing.T) {
	t.Helper()
	if err := f.Memory.Heartbeat(time.Minute); err != nil {
		t.Fatal(err)
	}
}

func claimBy(loop string, ref target.Ref) coord.Message {
	return coord.Message{Kind: coord.KindClaim, Format: coord.Format, Loop: loop, Target: ref.String()}
}

func heartbeatOf(loop string) coord.Message {
	return coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format, Loop: loop, Interval: time.Minute}
}

// boardOf は周期 1 分の loop の Board。
func boardOf(refs coord.Refs) *coord.Board {
	return coord.NewBoard(refs, coord.HeartbeatInterval(time.Minute))
}

// noRenewal は、生存報告を置き直す時計が明けない HeartbeatAfter。
func noRenewal(time.Duration) <-chan time.Time { return nil }

// tickRig は、周期の timer を ticks 回目で停止要求に替える rig。
func tickRig(ticks int) *rig {
	r := newRig()
	n := 0
	r.timer = func(kind timerKind, _ time.Duration) timing {
		if kind != periodTimer {
			return timing{fate: neverFires}
		}
		if n++; n >= ticks {
			return timing{fate: stopsTheLoop}
		}
		return timing{fate: fires}
	}
	return r
}

// matchingStore は issue 42 が当たり続ける置き場。
func matchingStore(workflow.Definition) places.Store {
	item := target.Issue{Number: 42}
	return memoryStore{
		observe: func() ([]target.Item, error) { return []target.Item{item}, nil },
		reread:  func(target.Ref) (target.Item, error) { return item, nil },
	}.store()
}

// coordinatedRun は、issue 42 が当たり続ける置き場で、refs を coordination board にして周期 1 分の loop を ticks 回の tick だけ
// 回す。worker は script のとおりに振る舞う。
func coordinatedRun(t *testing.T, refs coord.Refs, ticks int, script func(worker.Job) workerScript) *rig {
	t.Helper()
	r := tickRig(ticks)
	r.script = script
	r.run(t, loop.Options{
		Definition: definition(time.Minute, 1), Coordination: boardOf(refs), HeartbeatAfter: noRenewal, Store: matchingStore,
	})
	return r
}

// orphanedRun は、issue 42 の workspace に orphan が生きている状態で、refs を coordination board にして loop を ticks 回の tick
// だけ回す。orphan は alive が true を返す間だけ生きている。
func orphanedRun(t *testing.T, refs coord.Refs, ticks int, alive func() bool) *rig {
	t.Helper()
	r := tickRig(ticks)
	r.run(t, loop.Options{
		Definition: definition(time.Minute, 1), Coordination: boardOf(refs), HeartbeatAfter: noRenewal, Store: matchingStore,
		Workspaces: func(workflow.Definition) loop.Workspaces { return listedWorkspaces{issue42} },
		Orphaned:   func(target.Ref) (bool, error) { return alive(), nil },
	})
	return r
}

func always() bool { return true }

// listedWorkspaces は refs の workspace が在る掃除の口。
type listedWorkspaces []target.Ref

func (w listedWorkspaces) Existing() ([]target.Ref, error) { return w, nil }
func (listedWorkspaces) Remove(target.Ref) error           { return nil }
func (listedWorkspaces) Branch(target.Ref) (string, error) { return "", nil }

func TestAClaimOfALiveLoopKeepsTheTargetFromBeingLaunched(t *testing.T) {
	remote := newFlaky()
	remote.onObserve = func(int) {
		// 他の loop は周期ごとに生存報告を置き直す
		remote.Put(nil, heartbeatOf("other-00000002"))
	}
	theirs := remote.Put(&issue42, claimBy("other-00000002", issue42))

	r := coordinatedRun(t, remote, 6, nil)

	if len(r.jobs) != 0 || remote.Claims()[issue42] != theirs {
		t.Fatalf("起動 %d 回・着手宣言 %v, want 生きている loop の着手宣言の作業対象を起動せず、着手宣言を触らない", len(r.jobs), remote.Claims())
	}
}

func TestADeadLoopsClaimIsTakenOverAndItsHeartbeatIsDeleted(t *testing.T) {
	remote := newFlaky()
	remote.Put(nil, heartbeatOf("dead-00000002"))
	remote.Put(&issue42, claimBy("dead-00000002", issue42))

	r := coordinatedRun(t, remote, 5, nil)

	// 生存報告は 0 分に見てから変わらない。3 分 (間隔の 3 倍) の tick で死んだとみなす
	takeovers := r.logEvents(t, "takeover")
	if len(r.jobs) != 1 || len(takeovers) != 1 || takeovers[0]["from"] != "dead-00000002" {
		t.Fatalf("起動 %d 回・takeover の行 %v, want 死んだ loop の着手宣言の乗っ取りで 1 回起動", len(r.jobs), takeovers)
	}
	if _, beating := remote.Loops()["dead-00000002"]; beating {
		t.Fatalf("生存報告 = %v, want 死んだ loop の生存報告を消す", remote.Loops())
	}
	dead := r.logEvents(t, "dead_loop")
	if len(dead) != 1 || dead[0]["loop"] != "dead-00000002" || !slices.Equal(toStrings(dead[0]["refs"]), []string{coordtest.HeartbeatName("dead-00000002")}) {
		t.Fatalf("dead_loop の行 = %v", dead)
	}
	if !strings.Contains(r.stdout.String(), "死んだ loop dead-00000002: 生存報告を消した") {
		t.Fatalf("画面 = %q, want 消した ref の文", r.stdout.String())
	}
}

func toStrings(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}

func TestASlowReadOfTheRefsDoesNotMakeALiveLoopLookDead(t *testing.T) {
	remote := newFlaky()
	remote.Put(nil, heartbeatOf("other-00000002"))
	remote.Put(&issue42, claimBy("other-00000002", issue42))
	r := tickRig(3)
	// 1 tick 目の ref の読みが 2 分 59 秒かかる。sha を見た時刻は読み終えた後なので、次の tick (その 1 分後) までは 3 分に満たない
	remote.onObserve = func(times int) {
		if times == 1 {
			r.setClock(2*time.Minute + 59*time.Second)
		}
	}

	r.run(t, loop.Options{
		Definition: definition(time.Minute, 1), Coordination: boardOf(remote), HeartbeatAfter: noRenewal, Store: matchingStore,
	})

	if takeovers := r.logEvents(t, "takeover"); len(r.jobs) != 0 || len(takeovers) != 0 {
		t.Fatalf("起動 %d 回・takeover の行 %v, want 読みが遅くても生きている loop を死んだとみなさない", len(r.jobs), takeovers)
	}
}

func TestADeadLoopsClaimIsInheritedWhileAnOrphanWorksOnTheTarget(t *testing.T) {
	remote := newFlaky()
	remote.Put(&issue42, claimBy("dead-00000002", issue42))

	r := orphanedRun(t, remote, 6, always)

	// 止まるときも orphan が生きているので、引き継いだ着手宣言を残す
	if owner := remote.Message(remote.Claims()[issue42]).Loop; owner != me || len(r.jobs) != 0 {
		t.Fatalf("着手宣言の持ち主 %q・起動 %d 回, want 自分の名前に引き継ぎ、起動しない", owner, len(r.jobs))
	}
	if !strings.Contains(r.stdout.String(), "引き継ぎ issue#42") {
		t.Fatalf("画面 = %q, want 引き継ぎの行", r.stdout.String())
	}
	if inherits := r.logEvents(t, "inherit"); len(inherits) != 1 || inherits[0]["from"] != "dead-00000002" {
		t.Fatalf("inherit の行 = %v", inherits)
	}
}

func TestAnInheritedClaimIsDeletedWhenTheOrphanEndsAndTheTargetIsLaunched(t *testing.T) {
	remote := newFlaky()
	remote.Put(&issue42, claimBy("dead-00000002", issue42))

	// orphan は、死んだ loop の判定 (間隔の 3 倍) より後の 7 tick 目の確かめで終わっている
	r := orphanedRun(t, remote, 10, func() bool { return remote.observed < 7 })

	if len(r.logEvents(t, "inherit")) != 1 || len(r.logEvents(t, "orphan_end")) != 1 {
		t.Fatalf("inherit の行 %v・orphan_end の行 %v", r.logEvents(t, "inherit"), r.logEvents(t, "orphan_end"))
	}
	if len(r.jobs) == 0 {
		t.Fatal("orphan が終わって引き継いだ着手宣言を消した後も、起動しなかった")
	}
}

func TestAnInheritWithAnUnknownResultKeepsTheHeartbeatWhenTheLoopStopsBeforeTheNextTick(t *testing.T) {
	remote := newFlaky()
	remote.beating(t)
	remote.Put(nil, heartbeatOf("me-00000009"))
	remote.Put(&issue42, claimBy("me-00000009", issue42))
	// push は remote に届いたが、結果の分からない失敗を返す
	remote.FailClaim(errors.New("connection reset"))

	r := orphanedRun(t, remote, 1, always)

	// 次の tick で覚え直す前に止まっても、引き継いだものと同じに扱い、生存報告を消さない
	if _, beating := remote.Loops()[me]; !beating {
		t.Fatalf("生存報告 = %v, want 生存報告を消さずに止まる", remote.Loops())
	}
	for _, want := range []string{
		"orphan の作業対象の着手宣言を引き継げない (次の tick で試み直す): connection reset",
		"orphan のために引き継いだかもしれない着手宣言を残して止まる (引き継ぎの結果が分からず、orphan の claude が作業を続けうる)",
	} {
		if !hasErrorLine(t, r, "issue#42", want) {
			t.Fatalf("error の行 = %v, want %q", r.logEvents(t, "error"), want)
		}
	}
}

func TestAnInheritWithAnUnknownResultIsKeptAsInheritedByTheNextTick(t *testing.T) {
	remote := newFlaky()
	remote.beating(t)
	remote.Put(nil, heartbeatOf("me-00000009"))
	remote.Put(&issue42, claimBy("me-00000009", issue42))
	// push は remote に届いたが、結果の分からない失敗を返す
	remote.FailClaim(errors.New("connection reset"))

	r := orphanedRun(t, remote, 2, always)

	// 2 tick 目の突き合わせが、前の tick の orphan を使って、届いていた着手宣言を消し損ねたものでなく引き継いだものと見分ける
	if owner := remote.Message(remote.Claims()[issue42]).Loop; owner != me {
		t.Fatalf("着手宣言の持ち主 %q, want 引き継いだ着手宣言を消さずに残す", owner)
	}
	if _, beating := remote.Loops()[me]; !beating || len(r.jobs) != 0 {
		t.Fatalf("生存報告 = %v・起動 %d 回, want 生存報告を消さずに止まり、起動しない", remote.Loops(), len(r.jobs))
	}
	if !hasErrorLine(t, r, "issue#42", "orphan のために引き継いだ着手宣言を残して止まる (orphan の claude が作業を続けうる)") {
		t.Fatalf("error の行 = %v, want 引き継いだものとして残して止まる行", r.logEvents(t, "error"))
	}
}

func TestAnInheritThatFailedBeforePushDeletesTheHeartbeatWhenTheLoopStops(t *testing.T) {
	remote := newFlaky()
	remote.beating(t)
	remote.Put(nil, heartbeatOf("me-00000009"))
	theirs := remote.Put(&issue42, claimBy("me-00000009", issue42))
	// 着手宣言の commit を作れず、remote には届いていない
	remote.FailClaimBeforePush(errors.New("disk full"))

	r := orphanedRun(t, remote, 1, always)

	// 引き継いだかもしれない着手宣言は無いので、orphan が生きていても生存報告を消して止まる
	if _, beating := remote.Loops()[me]; beating || remote.Claims()[issue42] != theirs {
		t.Fatalf("生存報告 = %v・着手宣言 %v, want 着手宣言を触らずに生存報告を消す", remote.Loops(), remote.Claims())
	}
	if !hasErrorLine(t, r, "issue#42", "orphan の作業対象の着手宣言を引き継げない (次の tick で試み直す): ") ||
		!hasErrorLine(t, r, "issue#42", "disk full") {
		t.Fatalf("error の行 = %v, want 引き継げない行", r.logEvents(t, "error"))
	}
	if hasErrorLine(t, r, "issue#42", "引き継いだかもしれない") {
		t.Fatalf("error の行 = %v, want 引き継いだかもしれない着手宣言を残す行を書かない", r.logEvents(t, "error"))
	}
}

// 結果の分からない引き継ぎを覚えるのは orphan が生きている tick だけで、orphan の生死は次の tick の突き合わせで覚えたものを
// 決着させた後に確かめ直す (formats.md §12 の 8)。突き合わせが落ちた tick は orphan を確かめず、直近の生死を残す。この順を
// 変えて orphan を先に確かめると、止まるときに orphan が終わったとみなし、届いていた着手宣言を生存報告の無いまま残す。
func TestOrphansAreCheckedOnlyAfterTheBoardSettlesAnInheritWithAnUnknownResult(t *testing.T) {
	remote := newFlaky()
	remote.beating(t)
	remote.Put(nil, heartbeatOf("me-00000009"))
	remote.Put(&issue42, claimBy("me-00000009", issue42))
	// 1 tick 目の引き継ぎの push は remote に届いたが、結果の分からない失敗を返す
	remote.FailClaim(errors.New("connection reset"))
	// 2 tick 目の突き合わせは ref を読めない
	remote.onObserve = func(times int) {
		if times == 2 {
			remote.unlisted = errors.New("ls-remote が落ちた")
		}
	}
	// orphan は 1 回目の確かめでだけ生きている (2 回目に確かめれば終わっている)
	checks := 0
	alive := func() bool { checks++; return checks == 1 }

	r := orphanedRun(t, remote, 2, alive)

	if owner := remote.Message(remote.Claims()[issue42]).Loop; owner != me {
		t.Fatalf("着手宣言の持ち主 %q, want 届いていた着手宣言が残る", owner)
	}
	if _, beating := remote.Loops()[me]; !beating {
		t.Fatalf("生存報告 = %v, want 届いていたかもしれない着手宣言の持ち主として生存報告を残す", remote.Loops())
	}
	if !hasErrorLine(t, r, "issue#42", "orphan のために引き継いだかもしれない着手宣言を残して止まる (引き継ぎの結果が分からず、orphan の claude が作業を続けうる)") {
		t.Fatalf("error の行 = %v, want orphan が作業を続けうるとして引き継いだかもしれない着手宣言を残して止まる行", r.logEvents(t, "error"))
	}
}

// 着手宣言の commit message を読めないまま orphan が終わってから止まると、届いていた着手宣言が生存報告の無いまま残る
// (formats.md §12 の 8 が受け入れた経路)。その作業対象がしばらく起動されない理由を log から読めるよう、error の行を書く。
func TestAnInheritWithAnUnknownResultLeavesAnErrorLineWhenTheOrphanHasEndedBeforeTheLoopStops(t *testing.T) {
	remote := newFlaky()
	remote.beating(t)
	remote.Put(nil, heartbeatOf("me-00000009"))
	remote.Put(&issue42, claimBy("me-00000009", issue42))
	// 1 tick 目の引き継ぎの push は remote に届いたが、結果の分からない失敗を返す
	remote.FailClaim(errors.New("connection reset"))
	// 2 tick 目の突き合わせは、届いていた着手宣言の commit message を読めない
	remote.onObserve = func(times int) {
		if times == 2 {
			remote.Unreadable(remote.Claims()[issue42], errors.New("fetch が落ちた"))
		}
	}
	// orphan は 1 回目の確かめでだけ生きている
	checks := 0
	alive := func() bool { checks++; return checks == 1 }

	r := orphanedRun(t, remote, 2, alive)

	if owner := remote.Message(remote.Claims()[issue42]).Loop; owner != me {
		t.Fatalf("着手宣言の持ち主 %q, want 届いていた着手宣言が残る", owner)
	}
	if _, beating := remote.Loops()[me]; beating {
		t.Fatalf("生存報告 = %v, want orphan は終わっているので生存報告を消す", remote.Loops())
	}
	if !hasErrorLine(t, r, "issue#42", "orphan のために引き継いだかもしれない着手宣言を残して止まる (引き継ぎの結果が分からず、orphan は終わっていた)") {
		t.Fatalf("error の行 = %v, want orphan は終わっていたとして引き継いだかもしれない着手宣言を残して止まる行", r.logEvents(t, "error"))
	}
}

// 引き継ぎの push が remote に届かずに結果の分からない失敗を返し、remote が死んだ持ち主の着手宣言のままなら、その commit message を
// 読めなくても、次の tick で旧 sha と比べて届いていないと決着させる (formats.md §12 の 8・ADR 0031)。止まるときに、引き継いだ
// かもしれない着手宣言を残したとは書かない。
func TestAnInheritWithAnUnknownResultThatDidNotReachTheRemoteIsSettledByTheOldSHA(t *testing.T) {
	cases := []struct {
		name  string
		alive func() func() bool
	}{
		{"orphan が生きていて止まる", func() func() bool { return always }},
		{"orphan が終わっていて止まる", func() func() bool {
			// orphan は 1 回目の確かめでだけ生きている
			checks := 0
			return func() bool { checks++; return checks == 1 }
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			remote := newFlaky()
			remote.beating(t)
			remote.Put(nil, heartbeatOf("me-00000009"))
			theirs := remote.Put(&issue42, claimBy("me-00000009", issue42))
			// 1 tick 目の引き継ぎの push は remote に届かず、結果の分からない失敗を返す
			remote.undelivered = errors.New("connection reset")
			// 2 tick 目の突き合わせは、remote に残った元の着手宣言の commit message を読めない
			remote.onObserve = func(times int) {
				if times == 2 {
					remote.undelivered = nil
					remote.Unreadable(theirs, errors.New("fetch が落ちた"))
				}
			}

			r := orphanedRun(t, remote, 2, c.alive())

			if got := remote.Claims()[issue42]; got != theirs {
				t.Fatalf("着手宣言 = %q, want 元の着手宣言 %q のまま", got, theirs)
			}
			if hasErrorLine(t, r, "issue#42", "引き継いだかもしれない") {
				t.Fatalf("error の行 = %v, want 届いていないと決着させ、引き継いだかもしれない着手宣言を残す行を書かない", r.logEvents(t, "error"))
			}
			if _, beating := remote.Loops()[me]; beating {
				t.Fatalf("生存報告 = %v, want 引き継いだかもしれない着手宣言は無いので生存報告を消す", remote.Loops())
			}
		})
	}
}

func TestALostClaimWritesAClaimLostLineAndDoesNotLaunch(t *testing.T) {
	remote := newFlaky()
	// 他のマシンの loop が、着手宣言を置く直前に先に置く
	remote.beforeClaim = func() { remote.Put(&issue42, claimBy("other-00000002", issue42)) }

	r := coordinatedRun(t, remote, 1, nil)

	lost := r.logEvents(t, "claim_lost")
	if len(r.jobs) != 0 || len(lost) != 1 || lost[0]["target"] != "issue#42" || lost[0]["trigger"] != "implement" {
		t.Fatalf("起動 %d 回・claim_lost の行 %v", len(r.jobs), lost)
	}
	if !strings.Contains(r.stdout.String(), "着手宣言に負けた issue#42 (implement): 他の loop が先に着手した") {
		t.Fatalf("画面 = %q", r.stdout.String())
	}
}

func TestAClaimThatCannotBePlacedWritesAnErrorLineAndDoesNotLaunch(t *testing.T) {
	remote := newFlaky()
	remote.FailClaim(errors.New("push が落ちた"))

	r := coordinatedRun(t, remote, 1, nil)

	if len(r.jobs) != 0 || !hasErrorLine(t, r, "issue#42", "着手宣言を置けない (次の tick で試み直す): push が落ちた") {
		t.Fatalf("起動 %d 回・error の行 %v", len(r.jobs), r.logEvents(t, "error"))
	}
}

func TestALoopWhoseCoordinationRemoteChangedLaunchesNothing(t *testing.T) {
	start := definition(time.Minute, 1)
	start.CoordinationRemote = "origin"
	changed := start
	changed.CoordinationRemote = "upstream"
	r := tickRig(2)

	r.run(t, loop.Options{
		Definition: start, Coordination: boardOf(newFlaky()), Store: matchingStore,
		Load:           func() (workflow.Definition, error) { return changed, nil },
		HeartbeatAfter: noRenewal,
	})

	ticks := r.logEvents(t, "tick")
	if len(r.jobs) != 0 || len(ticks) == 0 || ticks[0]["result"] != "error" || !strings.Contains(ticks[0]["error"].(string), "coordination.remote") {
		t.Fatalf("起動 %d 回・tick の行 %v, want coordination.remote の違いで何も起動しない", len(r.jobs), ticks)
	}
}

func TestATakenOverWorkerIsStoppedAndItsClaimIsNotDeleted(t *testing.T) {
	remote := newFlaky()
	var taker string
	remote.onObserve = func(times int) {
		if times == 2 {
			// 1 回目の tick で置いた着手宣言を、他の loop が乗っ取った
			taker = remote.Put(&issue42, claimBy("other-00000002", issue42))
		}
	}

	r := coordinatedRun(t, remote, 3, func(worker.Job) workerScript { return workerScript{untilStopped: true} })

	taken := r.logEvents(t, "taken_over")
	if len(taken) != 1 || taken[0]["by"] != "other-00000002" {
		t.Fatalf("taken_over の行 = %v", taken)
	}
	ends := r.logEvents(t, "end")
	if len(ends) != 1 || ends[0]["outcome"] != "stopped" || ends[0]["reason"] != "taken_over" {
		t.Fatalf("end の行 = %v, want 乗っ取られて止めた", ends)
	}
	if got := remote.Claims()[issue42]; got != taker {
		t.Fatalf("着手宣言 = %q, want 乗っ取った loop の着手宣言 %q を消さない", got, taker)
	}
	last := r.published[len(r.published)-1]
	if len(last.TakenOver) != 1 || last.TakenOver[0].Target != "issue#42" || last.TakenOver[0].By != "other-00000002" {
		t.Fatalf("状態 file の taken_over = %+v", last.TakenOver)
	}
}

func TestATakeoverByAClaimWithoutItsOwnerIsShownAsUnreadable(t *testing.T) {
	remote := newFlaky()
	remote.onObserve = func(times int) {
		if times == 2 {
			remote.Put(&issue42, claimBy("", issue42))
		}
	}

	r := coordinatedRun(t, remote, 3, func(worker.Job) workerScript { return workerScript{untilStopped: true} })

	if taken := r.logEvents(t, "taken_over"); len(taken) != 1 || taken[0]["by"] != "?" {
		t.Fatalf("taken_over の行 = %v, want 持ち主を読めない ?", taken)
	}
	if !hasErrorLine(t, r, "issue#42", "持ち主の loop を読めない着手宣言がある (この作業対象は起動しない): "+coordtest.ClaimName(issue42)) || len(r.logEvents(t, "error")) != 1 {
		t.Fatalf("error の行 = %v, want 持ち主を読めない着手宣言の行を 1 回", r.logEvents(t, "error"))
	}
}

func TestARefOfANewerFormatStopsLaunchingInTheScope(t *testing.T) {
	remote := newFlaky()
	remote.Put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format + 1, Loop: "newer-00000002", Interval: time.Minute})

	r := coordinatedRun(t, remote, 2, nil)

	if len(r.jobs) != 0 || len(remote.Claims()) != 0 {
		t.Fatalf("起動 %d 回・着手宣言 %v, want 新しい版の ref がある間は起動しない", len(r.jobs), remote.Claims())
	}
	last := r.published[len(r.published)-1]
	if last.NewerFormat == nil || last.NewerFormat.Format == nil || *last.NewerFormat.Format != coord.Format+1 {
		t.Fatalf("状態 file の newer_format = %+v", last.NewerFormat)
	}
}

func TestAReleasedClaimAndTheHeartbeatAreDeletedWhenTheLoopStops(t *testing.T) {
	remote := newFlaky()
	remote.beating(t)

	coordinatedRun(t, remote, 2, nil)

	// worker はすぐ当たったまま終わり、attempt の上限 1 で打ち切る (claim を解く)
	if len(remote.Claims()) != 0 || len(remote.Loops()) != 0 {
		t.Fatalf("残った着手宣言 %v・生存報告 %v, want claim を解いたときと loop を止めたときに消す", remote.Claims(), remote.Loops())
	}
}

func TestALoopLeavingAnUnstoppedClaudeKeepsItsClaimAndHeartbeat(t *testing.T) {
	remote := newFlaky()
	remote.beating(t)

	coordinatedRun(t, remote, 2, func(worker.Job) workerScript { return workerScript{unstopped: "EPERM", neverEnds: true} })

	if _, held := remote.Claims()[issue42]; !held {
		t.Fatalf("着手宣言 = %v, want 止められない claude の作業対象の着手宣言を残す", remote.Claims())
	}
	if _, beating := remote.Loops()[me]; !beating {
		t.Fatalf("生存報告 = %v, want 消さずに途絶えさせる", remote.Loops())
	}
}

func TestALeftoverClaimRefOfMyOwnIsDeleted(t *testing.T) {
	remote := newFlaky()
	remote.Put(&issue7, claimBy(me, issue7))

	tickRig(2).run(t, loop.Options{Definition: definition(time.Minute, 1), Coordination: boardOf(remote), HeartbeatAfter: noRenewal})

	if _, left := remote.Claims()[issue7]; left {
		t.Fatalf("着手宣言 = %v, want 消し損ねた自分の着手宣言を消す", remote.Claims())
	}
}

func TestALeftoverClaimRefOfMyOwnThatCannotBeDeletedLeavesAnErrorLine(t *testing.T) {
	remote := newFlaky()
	remote.Put(&issue7, claimBy(me, issue7))
	remote.FailDelete(errors.New("push が落ちた"))
	r := tickRig(2)

	r.run(t, loop.Options{Definition: definition(time.Minute, 1), Coordination: boardOf(remote), HeartbeatAfter: noRenewal})

	if !hasErrorLine(t, r, "issue#7", "消し損ねた自分の着手宣言を消せない: push が落ちた") {
		t.Fatalf("error の行 = %v", r.logEvents(t, "error"))
	}
}

func TestAReleasedClaimThatCannotBeDeletedLeavesAnErrorLine(t *testing.T) {
	remote := newFlaky()
	remote.FailDelete(errors.New("push が落ちた"))

	r := coordinatedRun(t, remote, 2, nil)

	if !hasErrorLine(t, r, "issue#42", "着手宣言を消せない (次の tick で消し直す): push が落ちた") {
		t.Fatalf("error の行 = %v", r.logEvents(t, "error"))
	}
}

// hasErrorLine は、作業対象 ref (作業対象を持たない行なら "") の error の行に want を含むものがあるか。
func hasErrorLine(t *testing.T, r *rig, ref, want string) bool {
	t.Helper()
	return slices.ContainsFunc(r.logEvents(t, "error"), func(e map[string]any) bool {
		msg, _ := e["error"].(string)
		got, _ := e["target"].(string)
		return got == ref && strings.Contains(msg, want)
	})
}

func TestHeartbeatFailingTwiceInARowNeedsAttention(t *testing.T) {
	remote := newFlaky()
	remote.FailHeartbeat(errors.New("push が落ちた"))
	r := newRig()
	r.timer = func(timerKind, time.Duration) timing { return timing{fate: neverFires} }
	beats := 0
	r.run(t, loop.Options{
		Definition:   definition(time.Minute, 0),
		Coordination: boardOf(remote),
		HeartbeatAfter: func(time.Duration) <-chan time.Time {
			beats++
			if beats > 2 {
				r.signals <- syscall.SIGINT
				return nil
			}
			fired := make(chan time.Time, 1)
			// 置き直しの goroutine から呼ばれるので、rig の時計は読まない
			fired <- rigStart
			return fired
		},
	})

	last := r.published[len(r.published)-1]
	if last.HeartbeatFailures == nil || last.HeartbeatFailures.Count != 2 || !strings.Contains(last.HeartbeatFailures.Error, "push が落ちた") {
		t.Fatalf("状態 file の heartbeat_failures = %+v", last.HeartbeatFailures)
	}
	if !hasErrorLine(t, r, "", "生存報告を置けない: push が落ちた") {
		t.Fatalf("error の行 = %v", r.logEvents(t, "error"))
	}
}

func TestHeartbeatIsRenewedWhileATickIsStillRunning(t *testing.T) {
	remote := newFlaky()
	remote.renewed = make(chan struct{}, 1)
	var first atomic.Bool
	// inTick は tick が一覧の読み出しに入ったときに閉じる。置き直しの時計はそれまで明けない
	inTick := make(chan struct{})
	renewedDuringTick := false
	r := tickRig(1)

	r.run(t, loop.Options{
		Definition: definition(time.Minute, 1), Coordination: boardOf(remote),
		HeartbeatAfter: func(time.Duration) <-chan time.Time {
			if first.Swap(true) {
				return nil
			}
			fired := make(chan time.Time, 1)
			go func() {
				<-inTick
				fired <- rigStart
			}()
			return fired
		},
		Store: func(workflow.Definition) places.Store {
			return memoryStore{observe: func() ([]target.Item, error) {
				close(inTick)
				// 一覧の読み出しが長引いている間に、置き直しが届くか (届かなければ 5 秒で諦めて落とす)
				select {
				case <-remote.renewed:
					renewedDuringTick = true
				case <-time.After(5 * time.Second):
				}
				return nil, nil
			}}.store()
		},
	})

	if !renewedDuringTick {
		t.Fatal("tick が終わるまで生存報告を置き直さなかった")
	}
}

func TestARestartWaitsWhileARefOfANewerFormatExists(t *testing.T) {
	remote := newFlaky()
	remote.onObserve = func(times int) {
		if times == 2 {
			remote.Put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format + 1, Loop: "newer-00000002", Interval: time.Minute})
		}
	}
	def := definition(time.Minute, 1)
	def.MaxAttempts = 2
	r := newRig()
	r.script = func(worker.Job) workerScript { return exits(1) }
	periods := 0
	r.timer = func(kind timerKind, _ time.Duration) timing {
		if kind != periodTimer {
			return timing{fate: neverFires}
		}
		if periods++; periods >= 2 {
			return timing{fate: stopsTheLoop}
		}
		return timing{fate: fires}
	}

	r.run(t, loop.Options{Definition: def, Coordination: boardOf(remote), Store: matchingStore, HeartbeatAfter: noRenewal})

	// 2 回目の tick が新しい版の ref を見たので、再起動待ちの claim は attempt を進めずに待ち直す
	if len(r.jobs) != 1 {
		t.Fatalf("起動 %d 回, want 新しい版の ref がある間は再起動しない", len(r.jobs))
	}
}

func TestALoopWithACoordinationRemoteRelaunchesOnlyInATick(t *testing.T) {
	remote := newFlaky()
	def := definition(time.Minute, 1)
	def.MaxAttempts = 2
	r := newRig()
	r.script = func(worker.Job) workerScript { return exits(1) }
	wakes, periods := 0, 0
	r.timer = func(kind timerKind, _ time.Duration) timing {
		if kind == periodTimer {
			// 周期 (1 分) より先に backoff (10 秒) の wake が明ける。2 回目の tick の後で止める
			if periods++; periods >= 2 {
				return timing{fate: stopsTheLoop}
			}
			return timing{fate: fires}
		}
		if wakes++; wakes > 3 {
			return timing{fate: neverFires}
		}
		return timing{fate: fires}
	}
	// launchesAtTick は tick ごとの、その tick が workflow 定義を読んだ時点 (再起動を試みる前) の起動の数
	var launchesAtTick []int

	r.run(t, loop.Options{
		Definition: def, Coordination: boardOf(remote), Store: matchingStore,
		Load: func() (workflow.Definition, error) {
			launchesAtTick = append(launchesAtTick, len(r.jobs))
			return def, nil
		},
		HeartbeatAfter: noRenewal,
	})

	if wakes == 0 {
		t.Fatal("backoff の wake を置いていない")
	}
	if len(launchesAtTick) < 2 || launchesAtTick[1] != 1 || len(r.jobs) != 2 {
		t.Fatalf("tick ごとの起動の数 %v・起動 %d 回, want wake では再起動せず (2 回目の tick の前は 1 回)、2 回目の tick で再起動する", launchesAtTick, len(r.jobs))
	}
}
