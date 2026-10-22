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
	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/worker"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// マシンを跨いだ分担 (system.md §4・§7、formats.md §12)。死んだ loop の判定は分の単位の経過を数えるので、black-box テストは
// 待てない。ここでは fake の時計と in-memory の調停 remote で見る。

const me = "me-00000001"

// memoryRemote は in-memory の調停 remote。条件付きの push を server と同じく照合する。
type memoryRemote struct {
	claims   map[target.Ref]string
	loops    map[string]string
	messages map[string]coord.Message
	n        int
	// onObserve は Observe の回数ごとに、読ませる前に remote を書き換える (他のマシンの loop の書き込みの代役)
	onObserve func(r *memoryRemote, times int)
	observed  int
	// claimed と deleted は Claim と Delete の呼び出し (`<ref> <期待値>`)
	claimed, deleted []string
	// heartbeatErr が nil でなければ、生存報告の置き直しをこの失敗で落とす
	heartbeatErr error
	// deleteErr が nil でなければ、ref の削除をこの失敗で落とす (負けではない失敗)
	deleteErr error
}

func newMemoryRemote() *memoryRemote {
	return &memoryRemote{claims: map[target.Ref]string{}, loops: map[string]string{}, messages: map[string]coord.Message{}}
}

func (m *memoryRemote) LoopName() string                { return me }
func (m *memoryRemote) ClaimRef(ref target.Ref) string  { return "claims/" + ref.FileName() }
func (m *memoryRemote) HeartbeatRef(loop string) string { return "loops/" + loop }

// put は他の loop の着手宣言か生存報告を置く。
func (m *memoryRemote) put(ref *target.Ref, msg coord.Message) string {
	m.n++
	sha := msg.Loop + "-" + string(rune('a'+m.n))
	m.messages[sha] = msg
	if ref != nil {
		m.claims[*ref] = sha
	} else {
		m.loops[msg.Loop] = sha
	}
	return sha
}

func (m *memoryRemote) Observe() (coord.View, error) {
	m.observed++
	if m.onObserve != nil {
		m.onObserve(m, m.observed)
	}
	v := coord.View{Claims: map[target.Ref]string{}, Loops: map[string]string{}, Messages: map[string]coord.Message{}}
	for ref, sha := range m.claims {
		v.Claims[ref], v.Messages[sha] = sha, m.messages[sha]
	}
	for loop, sha := range m.loops {
		v.Loops[loop], v.Messages[sha] = sha, m.messages[sha]
	}
	return v, nil
}

func (m *memoryRemote) Claim(ref target.Ref, expect coord.Expect) (string, error) {
	m.claimed = append(m.claimed, ref.String()+" "+expect.String())
	if expect.String() != expected(m.claims[ref]) {
		return "", coord.ErrLost
	}
	m.put(&ref, coord.Message{Kind: coord.KindClaim, Format: coord.Format, Loop: me, Target: ref.String()})
	return m.claims[ref], nil
}

func (m *memoryRemote) Delete(name, sha string) error {
	m.deleted = append(m.deleted, name+" "+sha)
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if ref, ok := target.ParseFileName(strings.TrimPrefix(name, "claims/")); ok && m.claims[ref] == sha {
		delete(m.claims, ref)
		return nil
	}
	if loop := strings.TrimPrefix(name, "loops/"); m.loops[loop] == sha && sha != "" {
		delete(m.loops, loop)
		return nil
	}
	return coord.ErrLost
}

func (m *memoryRemote) Heartbeat(interval time.Duration) error {
	if m.heartbeatErr != nil {
		return m.heartbeatErr
	}
	m.put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format, Loop: me, Interval: interval})
	return nil
}

func (m *memoryRemote) Leave() error {
	delete(m.loops, me)
	return nil
}

// expected は remote の今の sha に通る期待値の綴り。
func expected(current string) string {
	if current == "" {
		return "absent"
	}
	return "holding " + current
}

var issue42 = target.Ref{Kind: target.KindIssue, Number: 42}

// coordinatedRun は、issue 42 が当たり続ける置き場で、remote を調停 remote にして周期 1 分の loop を ticks 回の tick
// だけ回す。worker は script のとおりに振る舞う。
func coordinatedRun(t *testing.T, remote *memoryRemote, ticks int, script func(worker.Job) workerScript) *rig {
	t.Helper()
	r := newRig()
	r.script = script
	n := 0
	r.timer = func(kind timerKind, _ time.Duration) timing {
		if kind != periodTimer {
			return timing{fate: neverFires}
		}
		n++
		if n >= ticks {
			return timing{fate: stopsTheLoop}
		}
		return timing{fate: fires}
	}
	item := target.Issue{Number: 42}
	r.run(t, loop.Options{
		Definition:     definition(time.Minute, 1),
		Coordination:   remote,
		HeartbeatAfter: func(time.Duration) <-chan time.Time { return nil },
		Store: func(workflow.Definition) loop.Store {
			return memoryStore{
				observe: func() ([]target.Item, error) { return []target.Item{item}, nil },
				reread:  func(target.Ref) (target.Item, error) { return item, nil },
			}.store()
		},
	})
	return r
}

func TestAClaimOfALiveLoopKeepsTheTargetFromBeingLaunched(t *testing.T) {
	remote := newMemoryRemote()
	remote.onObserve = func(m *memoryRemote, _ int) {
		// 他の loop は周期ごとに生存報告を置き直す
		m.put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format, Loop: "other-00000002", Interval: time.Minute})
	}
	remote.put(&issue42, coord.Message{Kind: coord.KindClaim, Format: coord.Format, Loop: "other-00000002", Target: "issue#42"})

	r := coordinatedRun(t, remote, 6, nil)

	if len(r.jobs) != 0 || len(remote.claimed) != 0 {
		t.Fatalf("起動 %d 回・着手宣言 %v, want 生きている loop の着手宣言の作業対象を起動しない", len(r.jobs), remote.claimed)
	}
}

func TestADeadLoopsClaimIsTakenOverFromItsOldSHAAndItsHeartbeatIsDeleted(t *testing.T) {
	remote := newMemoryRemote()
	heartbeat := remote.put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format, Loop: "dead-00000002", Interval: time.Minute})
	old := remote.put(&issue42, coord.Message{Kind: coord.KindClaim, Format: coord.Format, Loop: "dead-00000002", Target: "issue#42"})

	r := coordinatedRun(t, remote, 5, nil)

	// 生存報告は 0 分に見てから変わらない。3 分 (間隔の 3 倍) の tick で死んだとみなす
	if len(r.jobs) != 1 || !slices.Equal(remote.claimed, []string{"issue#42 holding " + old}) {
		t.Fatalf("起動 %d 回・着手宣言 %v, want 旧 sha %s を期待値にした乗っ取りで 1 回", len(r.jobs), remote.claimed, old)
	}
	takeovers := r.logEvents(t, "takeover")
	if len(takeovers) != 1 || takeovers[0]["from"] != "dead-00000002" {
		t.Fatalf("takeover の行 = %v", takeovers)
	}
	if !slices.Contains(remote.deleted, "loops/dead-00000002 "+heartbeat) {
		t.Fatalf("消した ref = %v, want 死んだ loop の生存報告", remote.deleted)
	}
	if dead := r.logEvents(t, "dead_loop"); len(dead) != 1 || dead[0]["loop"] != "dead-00000002" {
		t.Fatalf("dead_loop の行 = %v", dead)
	}
}

func TestAClaimWithoutItsOwnersHeartbeatIsKeptForLessThanThreeOwnIntervals(t *testing.T) {
	remote := newMemoryRemote()
	remote.put(&issue42, coord.Message{Kind: coord.KindClaim, Format: coord.Format, Loop: "gone-00000002", Target: "issue#42"})

	r := coordinatedRun(t, remote, 3, nil)

	if len(r.jobs) != 0 || len(remote.claimed) != 0 {
		t.Fatalf("2 分の時点で起動 %d 回・着手宣言 %v, want 自分の生存報告の間隔 (1 分) の 3 倍まで待つ", len(r.jobs), remote.claimed)
	}
}

func TestAClaimWithoutItsOwnersHeartbeatIsTakenOverAfterThreeOwnIntervals(t *testing.T) {
	remote := newMemoryRemote()
	old := remote.put(&issue42, coord.Message{Kind: coord.KindClaim, Format: coord.Format, Loop: "gone-00000002", Target: "issue#42"})

	coordinatedRun(t, remote, 5, nil)

	if !slices.Equal(remote.claimed, []string{"issue#42 holding " + old}) {
		t.Fatalf("着手宣言 = %v, want 旧 sha からの乗っ取り", remote.claimed)
	}
}

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
func matchingStore(workflow.Definition) loop.Store {
	item := target.Issue{Number: 42}
	return memoryStore{
		observe: func() ([]target.Item, error) { return []target.Item{item}, nil },
		reread:  func(target.Ref) (target.Item, error) { return item, nil },
	}.store()
}

func TestADeadLoopsClaimIsKeptWhileAnOrphanWorksOnTheTarget(t *testing.T) {
	remote := newMemoryRemote()
	old := remote.put(&issue42, coord.Message{Kind: coord.KindClaim, Format: coord.Format, Loop: "dead-00000002", Target: "issue#42"})
	r := tickRig(6)

	r.run(t, loop.Options{
		Definition: definition(time.Minute, 1), Coordination: remote, Store: matchingStore,
		HeartbeatAfter: func(time.Duration) <-chan time.Time { return nil },
		Workspaces:     func(workflow.Definition) loop.Workspaces { return listedWorkspaces{issue42} },
		Orphaned:       func(target.Ref) (bool, error) { return true, nil },
	})

	if remote.claims[issue42] != old || len(r.jobs) != 0 {
		t.Fatalf("着手宣言 %q (起動 %d 回), want orphan が作業している間は死んだ loop の着手宣言 %s を消さない", remote.claims[issue42], len(r.jobs), old)
	}
}

// listedWorkspaces は refs の workspace が在る掃除の口。
type listedWorkspaces []target.Ref

func (w listedWorkspaces) Existing() ([]target.Ref, error) { return w, nil }
func (listedWorkspaces) Remove(target.Ref) error           { return nil }
func (listedWorkspaces) Branch(target.Ref) (string, error) { return "", nil }

func TestALoopWhoseCoordinationRemoteChangedLaunchesNothing(t *testing.T) {
	start := definition(time.Minute, 1)
	start.CoordinationRemote = "origin"
	changed := start
	changed.CoordinationRemote = "upstream"
	r := tickRig(2)

	r.run(t, loop.Options{
		Definition: start, Coordination: newMemoryRemote(), Store: matchingStore,
		Load:           func() (workflow.Definition, error) { return changed, nil },
		HeartbeatAfter: func(time.Duration) <-chan time.Time { return nil },
	})

	ticks := r.logEvents(t, "tick")
	if len(r.jobs) != 0 || len(ticks) == 0 || ticks[0]["result"] != "error" || !strings.Contains(ticks[0]["error"].(string), "coordination.remote") {
		t.Fatalf("起動 %d 回・tick の行 %v, want coordination.remote の違いで何も起動しない", len(r.jobs), ticks)
	}
}

func TestATakenOverWorkerIsStoppedAndItsClaimIsNotDeleted(t *testing.T) {
	remote := newMemoryRemote()
	remote.onObserve = func(m *memoryRemote, times int) {
		if times == 2 {
			// 1 回目の tick で置いた着手宣言を、他の loop が乗っ取った
			m.put(&issue42, coord.Message{Kind: coord.KindClaim, Format: coord.Format, Loop: "other-00000002", Target: "issue#42"})
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
	for _, d := range remote.deleted {
		if strings.HasPrefix(d, "claims/") {
			t.Fatalf("乗っ取られた着手宣言を消した: %v", remote.deleted)
		}
	}
	last := r.published[len(r.published)-1]
	if len(last.TakenOver) != 1 || last.TakenOver[0].Target != "issue#42" || last.TakenOver[0].By != "other-00000002" {
		t.Fatalf("状態 file の taken_over = %+v", last.TakenOver)
	}
}

func TestARefOfANewerFormatStopsLaunchingInTheScope(t *testing.T) {
	remote := newMemoryRemote()
	remote.put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format + 1, Loop: "newer-00000002", Interval: time.Minute})

	r := coordinatedRun(t, remote, 2, nil)

	if len(r.jobs) != 0 || len(remote.claimed) != 0 {
		t.Fatalf("起動 %d 回・着手宣言 %v, want 新しい版の ref がある間は起動しない", len(r.jobs), remote.claimed)
	}
	last := r.published[len(r.published)-1]
	if last.NewerFormat == nil || last.NewerFormat.Format == nil || *last.NewerFormat.Format != coord.Format+1 {
		t.Fatalf("状態 file の newer_format = %+v", last.NewerFormat)
	}
}

func TestAReleasedClaimAndTheHeartbeatAreDeletedWhenTheLoopStops(t *testing.T) {
	remote := newMemoryRemote()

	coordinatedRun(t, remote, 2, nil)

	// worker はすぐ当たったまま終わり、attempt の上限 1 で打ち切る (claim を解く)
	if len(remote.claims) != 0 || len(remote.loops) != 0 {
		t.Fatalf("残った着手宣言 %v・生存報告 %v, want claim を解いたときと loop を止めたときに消す", remote.claims, remote.loops)
	}
}

func TestHeartbeatFailingTwiceInARowNeedsAttention(t *testing.T) {
	remote := newMemoryRemote()
	remote.heartbeatErr = errors.New("push が落ちた")
	r := newRig()
	r.timer = func(timerKind, time.Duration) timing { return timing{fate: neverFires} }
	beats := 0
	r.run(t, loop.Options{
		Definition:   definition(time.Minute, 0),
		Coordination: remote,
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
}

func TestATakeoverByAClaimWithoutItsOwnerIsShownAsUnreadable(t *testing.T) {
	remote := newMemoryRemote()
	remote.onObserve = func(m *memoryRemote, times int) {
		if times == 2 {
			m.put(&issue42, coord.Message{Kind: coord.KindClaim, Format: coord.Format, Target: "issue#42"})
		}
	}

	r := coordinatedRun(t, remote, 3, func(worker.Job) workerScript { return workerScript{untilStopped: true} })

	if taken := r.logEvents(t, "taken_over"); len(taken) != 1 || taken[0]["by"] != "?" {
		t.Fatalf("taken_over の行 = %v, want 持ち主を読めない ?", taken)
	}
	if errs := r.logEvents(t, "error"); len(errs) != 1 || !strings.Contains(errs[0]["error"].(string), "持ち主の loop を読めない着手宣言") {
		t.Fatalf("error の行 = %v, want 持ち主を読めない着手宣言の行を 1 回", errs)
	}
}

// unreadableOnce は、最初の Observe でだけ sha の commit message を読ませない調停 remote (fetch が一度落ちた)。
type unreadableOnce struct {
	*memoryRemote
	sha string
}

func (u unreadableOnce) Observe() (coord.View, error) {
	v, err := u.memoryRemote.Observe()
	if u.observed == 1 {
		delete(v.Messages, u.sha)
		v.Unread = errors.New("fetch が落ちた")
	}
	return v, err
}

func TestAHeartbeatWithoutAnIntervalIsReportedOnceAfterItsMessageIsRead(t *testing.T) {
	remote := newMemoryRemote()
	sha := remote.put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format, Loop: "odd-00000002"})
	r := newRig()
	n := 0
	r.timer = func(kind timerKind, _ time.Duration) timing {
		if kind != periodTimer {
			return timing{fate: neverFires}
		}
		if n++; n >= 4 {
			return timing{fate: stopsTheLoop}
		}
		return timing{fate: fires}
	}

	r.run(t, loop.Options{
		Definition: definition(time.Minute, 0), Coordination: unreadableOnce{remote, sha},
		HeartbeatAfter: func(time.Duration) <-chan time.Time { return nil },
	})

	reported := 0
	for _, e := range r.logEvents(t, "error") {
		if strings.Contains(e["error"].(string), "間隔を読めない生存報告") {
			reported++
		}
	}
	if reported != 1 {
		t.Fatalf("間隔を読めない生存報告の error の行 = %d 回, want fetch が落ちた tick の後に 1 回", reported)
	}
}

func TestALoopLeavingAnUnstoppedClaudeKeepsItsClaimAndHeartbeat(t *testing.T) {
	remote := newMemoryRemote()
	remote.put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format, Loop: me, Interval: time.Minute})

	coordinatedRun(t, remote, 2, func(worker.Job) workerScript { return workerScript{unstopped: "EPERM", neverEnds: true} })

	if _, held := remote.claims[issue42]; !held {
		t.Fatalf("着手宣言 = %v, want 止められない claude の作業対象の着手宣言を残す", remote.claims)
	}
	if _, beating := remote.loops[me]; !beating {
		t.Fatalf("生存報告 = %v, want 消さずに途絶えさせる", remote.loops)
	}
}

var issue7 = target.Ref{Kind: target.KindIssue, Number: 7}

func TestALeftoverClaimRefOfMyOwnIsDeleted(t *testing.T) {
	remote := newMemoryRemote()
	stale := remote.put(&issue7, coord.Message{Kind: coord.KindClaim, Format: coord.Format, Loop: me, Target: "issue#7"})

	tickRig(2).run(t, loop.Options{
		Definition: definition(time.Minute, 1), Coordination: remote,
		HeartbeatAfter: func(time.Duration) <-chan time.Time { return nil },
	})

	if !slices.Contains(remote.deleted, "claims/issue-7 "+stale) {
		t.Fatalf("消した ref = %v, want 消し損ねた自分の着手宣言 %s", remote.deleted, stale)
	}
}

func TestALeftoverClaimRefOfMyOwnThatCannotBeDeletedLeavesAnErrorLine(t *testing.T) {
	remote := newMemoryRemote()
	remote.put(&issue7, coord.Message{Kind: coord.KindClaim, Format: coord.Format, Loop: me, Target: "issue#7"})
	remote.deleteErr = errors.New("push が落ちた")
	r := tickRig(2)

	r.run(t, loop.Options{
		Definition: definition(time.Minute, 1), Coordination: remote,
		HeartbeatAfter: func(time.Duration) <-chan time.Time { return nil },
	})

	if !hasErrorLine(t, r, "issue#7", "消し損ねた自分の着手宣言を消せない: push が落ちた") {
		t.Fatalf("error の行 = %v", r.logEvents(t, "error"))
	}
}

func TestAReleasedClaimThatCannotBeDeletedLeavesAnErrorLine(t *testing.T) {
	remote := newMemoryRemote()
	remote.deleteErr = errors.New("push が落ちた")

	r := coordinatedRun(t, remote, 2, nil)

	if !hasErrorLine(t, r, "issue#42", "着手宣言を消せない (次の tick で消し直す): push が落ちた") {
		t.Fatalf("error の行 = %v", r.logEvents(t, "error"))
	}
}

// hasErrorLine は、作業対象 ref の error の行に want を含むものがあるか。
func hasErrorLine(t *testing.T, r *rig, ref, want string) bool {
	t.Helper()
	return slices.ContainsFunc(r.logEvents(t, "error"), func(e map[string]any) bool {
		msg, _ := e["error"].(string)
		return e["target"] == ref && strings.Contains(msg, want)
	})
}

func TestTheDeadLoopLineNamesWhatWasDeleted(t *testing.T) {
	const dead = "dead-00000002"
	cases := []struct {
		name             string
		heartbeat, claim bool
		want             string
	}{
		{"生存報告と着手宣言", true, true, dead + ": 生存報告と着手宣言 1 本を消した"},
		{"生存報告だけ", true, false, dead + ": 生存報告を消した"},
		{"着手宣言だけ", false, true, dead + ": 着手宣言 1 本を消した"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			remote := newMemoryRemote()
			if c.heartbeat {
				remote.put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format, Loop: dead, Interval: time.Minute})
			}
			if c.claim {
				remote.put(&issue7, coord.Message{Kind: coord.KindClaim, Format: coord.Format, Loop: dead, Target: "issue#7"})
			}
			r := tickRig(5)

			// 置き場は issue 7 を返さないので、着手宣言は乗っ取らずに掃除で消す
			r.run(t, loop.Options{
				Definition: definition(time.Minute, 1), Coordination: remote,
				HeartbeatAfter: func(time.Duration) <-chan time.Time { return nil },
			})

			if !strings.Contains(r.stdout.String(), c.want) {
				t.Fatalf("出力に %q が無い:\n%s", c.want, r.stdout.String())
			}
		})
	}
}

func TestALoopWithACoordinationRemoteRelaunchesOnlyInATick(t *testing.T) {
	remote := newMemoryRemote()
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
		Definition: def, Coordination: remote, Store: matchingStore,
		Load: func() (workflow.Definition, error) {
			launchesAtTick = append(launchesAtTick, len(r.jobs))
			return def, nil
		},
		HeartbeatAfter: func(time.Duration) <-chan time.Time { return nil },
	})

	if wakes == 0 {
		t.Fatal("backoff の wake を置いていない")
	}
	if len(launchesAtTick) < 2 || launchesAtTick[1] != 1 || len(r.jobs) != 2 {
		t.Fatalf("tick ごとの起動の数 %v・起動 %d 回, want wake では再起動せず (2 回目の tick の前は 1 回)、2 回目の tick で再起動する", launchesAtTick, len(r.jobs))
	}
}

// beatSignal は、生存報告の置き直しを beats へ知らせる調停 remote (remote の表は書き換えない)。
type beatSignal struct {
	*memoryRemote
	beats chan struct{}
}

func (b beatSignal) Heartbeat(time.Duration) error {
	select {
	case b.beats <- struct{}{}:
	default:
	}
	return nil
}

func TestHeartbeatIsRenewedWhileATickIsStillRunning(t *testing.T) {
	remote := beatSignal{newMemoryRemote(), make(chan struct{}, 1)}
	var first atomic.Bool
	// inTick は tick が一覧の読み出しに入ったときに閉じる。置き直しの時計はそれまで明けない
	inTick := make(chan struct{})
	renewedDuringTick := false
	r := tickRig(1)

	r.run(t, loop.Options{
		Definition: definition(time.Minute, 1), Coordination: remote,
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
		Store: func(workflow.Definition) loop.Store {
			return memoryStore{observe: func() ([]target.Item, error) {
				close(inTick)
				// 一覧の読み出しが長引いている間に、置き直しが届くか (届かなければ 5 秒で諦めて落とす)
				select {
				case <-remote.beats:
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
