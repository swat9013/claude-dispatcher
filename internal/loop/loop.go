// Package loop は tick を周期ごとに回し、trigger に当たった作業対象へ worker を起動して、終わり方を確かめる
// (system.md §7 / §9、formats.md §4 / §6)。claim は loop の memory だけに持ち、tracker には書かない。
package loop

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/coord"
	"github.com/swat9013/claude-dispatcher/internal/precheck"
	"github.com/swat9013/claude-dispatcher/internal/provider"
	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/version"
	"github.com/swat9013/claude-dispatcher/internal/worker"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// Store は loop が読む置き場: issue 置き場の部品と CL 置き場の部品 (system.md §13)。本番は provider が組み立てた adapter、
// テストは in-memory。失敗は *target.Failure。一覧は *target.NonFatal を一覧とともに返しうる (OpenItems)。
type Store struct {
	Issues provider.IssueStore
	// CLs は CL 置き場の部品。検査に通った workflow 定義は CL 置き場をちょうど 1 つ持つ (ADR 0015) ので、いつも組まれる
	CLs provider.CLStore
	// Slow は一覧の読み出しが遅いときの知らせ方。ゼロ値なら知らせない
	Slow SlowNotice
}

// SlowListing は 1 つの置き場の一覧を 1 回読む時間の目標。超えたら、読み終わる前に 1 行出す (formats.md §6 の「遅い一覧」)。
const SlowListing = 15 * time.Second

// SlowNotice は、一覧の読み出しが SlowListing を超えたことを、読み終わる前に 1 回だけ知らせる口。After か Notify が nil
// なら知らせない。Notify は見張りの goroutine で呼び、Watch (Open も Watch を通る) は Notify が終わるまで返らない。
type SlowNotice struct {
	// After は d 後に届く channel を返す (time.After。テストは差し替える)
	After  func(d time.Duration) <-chan time.Time
	Notify func(kind target.Kind)
}

// PlaceLabel は kind の作業対象の置き場を人が読む行で指す綴り (`issue 置き場 <置き場>` か `CL 置き場 <置き場>`)。doctor の
// 行と遅い一覧の行が使う (formats.md §6・§7.4)。
func PlaceLabel(def workflow.Definition, kind target.Kind) string {
	name := "issue 置き場 "
	if kind == target.KindCL {
		name = "CL 置き場 "
	}
	return name + placeOf(def, kind)
}

// placeOf は kind の作業対象の置き場の綴り。
func placeOf(def workflow.Definition, kind target.Kind) string {
	if kind == target.KindCL {
		_, place := def.CLHost()
		return place.String()
	}
	return def.Tracker.Place.String()
}

// SlowToOutput は、遅い一覧の行を人が読む行 out に出す口 (formats.md §6 の「遅い一覧」)。
func SlowToOutput(def workflow.Definition, out Output, after func(time.Duration) <-chan time.Time, now func() time.Time) SlowNotice {
	return SlowNotice{After: after, Notify: func(kind target.Kind) {
		out.Show(Line{At: now(), Kind: LineNote, Label: SlowListingLine(def, kind)})
	}}
}

// SlowListingLine は、kind の置き場の一覧の読み出しが SlowListing を超えたときに出す 1 行。
func SlowListingLine(def workflow.Definition, kind target.Kind) string {
	return fmt.Sprintf("%s の一覧の読み出しが %d 秒を超えた", PlaceLabel(def, kind), int(SlowListing/time.Second))
}

// slowListingFields は log.jsonl の slow_listing の行の中身 (formats.md §4)。
func slowListingFields(def workflow.Definition, kind target.Kind) map[string]any {
	return map[string]any{"kind": string(kind), "place": placeOf(def, kind)}
}

// Open は種類の open な作業対象を、その種類の置き場の部品で全件読む。読み出しが SlowListing を超えたら、読み終わる前に
// s.Slow で知らせる。知らせても読み出しは止めず、結果も変えない。
func (s Store) Open(kind target.Kind) ([]target.Item, error) {
	var read func() ([]target.Item, error)
	switch kind {
	case target.KindIssue:
		read = s.Issues.OpenIssues
	case target.KindCL:
		read = s.CLs.OpenCLs
	default:
		return nil, fmt.Errorf("未知の作業対象の種類 %q", kind)
	}
	var items []target.Item
	var err error
	s.Slow.Watch(kind, func() { items, err = read() })
	return items, err
}

// Watch は read を呼び、read が SlowListing を超えたら、終わる前に kind の置き場の知らせを 1 回出す。After か Notify が nil
// なら read を呼ぶだけ。知らせても read は止めない。
func (n SlowNotice) Watch(kind target.Kind, read func()) {
	if n.After == nil || n.Notify == nil {
		read()
		return
	}
	// 読み出しは呼んだ goroutine のまま撃ち (panic も呼んだ側で起きる)、見張りだけを別の goroutine に置く。見張りが
	// 終わるまで返らないので、知らせの行は Watch の後に呼んだ側が出す行と並ばない
	finished, watching := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watching)
		select {
		case <-n.After(SlowListing):
			n.Notify(kind)
		case <-finished:
		}
	}()
	defer func() {
		close(finished)
		<-watching
	}()
	read()
}

// Read は作業対象 1 件を、その種類の置き場の部品で読み直す。終端になっているか消えていれば Terminal。
func (s Store) Read(ref target.Ref) (target.Item, error) {
	switch ref.Kind {
	case target.KindIssue:
		return s.Issues.ReadIssue(ref.Number)
	case target.KindCL:
		return s.CLs.ReadCL(ref.Number)
	}
	return nil, fmt.Errorf("未知の作業対象の種類 %q", ref.Kind)
}

// Workspaces は workspace の掃除の口。
type Workspaces interface {
	Existing() ([]target.Ref, error)
	// Remove は before_remove を撃ってから workspace を消す
	Remove(ref target.Ref) error
	// Branch は workspace で checkout されている branch の名前。workspace が無いか branch を指していなければ ""
	Branch(ref target.Ref) (string, error)
}

// Worker は走っている worker 1 回分。
type Worker interface {
	Stop()
	// Activity は最新の活動 (formats.md §6)。まだ無ければ At がゼロ値
	Activity() status.Activity
}

// Options は loop の入力。
type Options struct {
	// Load は workflow 定義を読み直す
	Load func() (workflow.Definition, error)
	// MaxConcurrent は max-concurrent で渡した並列上限を読む。渡されていなければ ok が false。nil なら渡されていないとみなす
	MaxConcurrent func() (n int, ok bool, err error)
	// Definition は起動時に検査に通った workflow 定義
	Definition workflow.Definition
	// Precheck は workflow 定義の事前検査 (formats.md §2.9)。落ちた trigger は起動しない
	Precheck func(workflow.Definition) []precheck.Problem
	// Store は workflow 定義から置き場の部品を組み立てる
	Store func(workflow.Definition) Store
	// Workspaces は workflow 定義から workspace の掃除の口を組み立てる
	Workspaces func(workflow.Definition) Workspaces
	// Launch は worker を起動し、起動と終わりを events に送らせる
	Launch func(def workflow.Definition, job worker.Job, events chan<- worker.Event) Worker
	// NewSessionID は worker に渡す session id を発行する
	NewSessionID func() (string, error)
	// Orphaned は、claim の無い作業対象の worker log を、前の loop が起動した claude (orphan) がまだ持っているかを返す
	// (system.md §7 の orphan)。本番は worker log の flock を試し、テストは fake
	Orphaned func(ref target.Ref) (bool, error)
	// ScopeKeyOf は workflow 定義の issue 置き場の scope key を、起動時と同じ経路 (provider が Place から返すもの。system.md §13) で返す
	ScopeKeyOf func(workflow.Definition) string
	// ScopeKey は起動時の scope key (lock を取った鍵)
	ScopeKey string
	// Log は log.jsonl (formats.md §4)、Output は人が読む行の出し先
	Log     io.Writer
	Output  Output
	Signals <-chan os.Signal
	// Publish は状態 file (formats.md §7.1) を書き出す
	Publish func(status.Snapshot) error
	// Workflow は workflow 定義の path (状態 file に載せる)
	Workflow string
	// Build は loop の binary の版と commit (`--version` と同じ値)。log.jsonl の loop_start 行と状態 file に載せる
	Build version.Build
	Now   func() time.Time
	// After は d 後に届く channel を返す (time.After。テストは差し替える)
	After func(d time.Duration) <-chan time.Time
	// Refresh は走っている worker の活動を確かめる周期 (1s) に届く。nil なら確かめない
	Refresh <-chan time.Time
	// SlowAfter は tick の一覧の読み出しが遅いかを測る (time.After。テストは差し替える)。After と分けるのは、After が
	// 周期と再起動の wake だけを置くため。nil なら測らない
	SlowAfter func(d time.Duration) <-chan time.Time
	// StartupSlowKinds は、起動時の確認で一覧の読み出しが SlowListing を超えた置き場の種類 (読んだ順)。確認は log.jsonl を
	// 開く前に読むので、loop_start の後に slow_listing の行として書く。人が読む行は確認のときに出ているので出さない
	StartupSlowKinds []target.Kind
	// Coordination は調停 remote の口 (system.md §4 のマシンを跨いだ分担)。調停 remote を指さない project では nil。
	// 最初の生存報告は呼び出し側が起動時の検査として置いてから渡す
	Coordination Coordination
	// HeartbeatAfter は d 後に届く、次の生存報告の時計 (time.After。テストは差し替える)。Coordination があるときだけ使う
	HeartbeatAfter func(d time.Duration) <-chan time.Time
}

// phase は claim の段階。
type phase int

const (
	// phaseRunning は worker が走っている
	phaseRunning phase = iota
	// phaseStopping は loop が worker を止めている
	phaseStopping
	// phaseAwaitingVerification は worker が終わったが、作業対象を読み直せないか trigger に当たるかをまだ決められず、終わり方を確かめ待ち
	phaseAwaitingVerification
	// phaseWaitingRetry は失敗した worker の次の attempt を、backoff が明けるか空きが出るまで待っている
	phaseWaitingRetry
)

// stopReason は loop が worker を止めた理由。止めた worker の end の行の reason に書く。
type stopReason Reason

const (
	stoppedAtTerminal = stopReason(reasonTerminal)
	stoppedByRequest  = stopReason(reasonStopRequested)
	stoppedByTakeover = stopReason(reasonTakenOver)
)

// outcome は claim を解いたときの終わり方 (formats.md §4)。
type outcome string

const (
	completed outcome = "completed"
	failed    outcome = "failed"
	stopped   outcome = "stopped"
)

// endKinds は終わり方ごとの終了の行の種類
var endKinds = map[outcome]LineKind{completed: LineCompleted, failed: LineFailed, stopped: LineNote}

// claim は loop が worker を起動中か、終わり方を確かめ待ちか、再起動待ちの作業対象。
type claim struct {
	item      target.Item
	trigger   trigger.Trigger
	attempt   int
	sessionID string
	// workspaceRoot は最初に起動したときの workspace.root。session は workspace の path ごとに保存されるので、途中で
	// workspace.root が変わっても、再起動と workspace の削除はこの root で行う。ほかの項目は、そのときの workflow 定義から読む
	workspaceRoot string
	// sessionStarted は、sessionID の session を始めた (claude を起動できた) attempt があったか
	sessionStarted bool
	phase          phase
	// run は phaseRunning と phaseStopping の worker
	run Worker
	// stopReason は phaseStopping の理由
	stopReason stopReason
	// ended は phaseAwaitingVerification の worker の終わり方
	ended *worker.Result
	// retryAt は phaseWaitingRetry の backoff が明ける時刻
	retryAt time.Time
	// waitingSlot は、backoff が明けたが並列上限で起動できず、そのことを log に書いたか
	waitingSlot bool
	// startedAt は今の attempt の claude を起動した時刻 (状態 file の経過に使う)。起動の前は nil
	startedAt *time.Time
	// activity は今の attempt の worker の最新の活動。まだ無ければ nil
	activity *status.Activity
	// pid と workspace は今の attempt で起動した claude の pid と workspace の path (止められない claude を人が探すため)
	pid       int
	workspace string
	// claimSHA は調停 remote に置いた自分の着手宣言の sha。調停 remote を指さない project と、乗っ取られた後は ""
	claimSHA string
	// takenOver は着手宣言を乗っ取られたか。乗っ取られた worker は、終われば終わりを確かめずに claim を解く (system.md §7)
	takenOver bool
	// exited は、SIGKILL を送れず claude が終わっていなかった worker の知らせ (worker.Unstopped) が渡した、claude が終わると
	// 閉じる channel。知らせを受けてから worker の終わりが届くまで nil でない。その間、段階は phaseStopping のまま残し、loop の
	// 停止は claude が終わるまでこの worker を待たない (system.md §7)
	exited <-chan struct{}
}

type loop struct {
	o      Options
	def    workflow.Definition
	claims map[target.Ref]*claim
	// abandoned は打ち切った作業対象と、打ち切ったときの trigger の名前
	abandoned map[target.Ref]string
	events    chan worker.Event
	rec       recorder
	// stop は受けた停止要求
	stop     stopRequests
	lastStop os.Signal
	// wake は最も早い再起動の予定に届く channel、wakeAt はその時刻。予定が無ければ nil
	wake   <-chan time.Time
	wakeAt time.Time
	// retriedAt は retry が直近に再起動待ちの claim を見渡した時刻。予定がこれ以前の claim はその時に試みている
	retriedAt time.Time
	// blocked は、採っている workflow 定義で事前検査に落ちた trigger。起動も再起動もしない
	blocked []precheck.Problem
	// orphans は直近に確かめられた tick で生きていた orphan の作業対象。tick ごとに確かめ直して置き換える。前の tick との
	// 違いで、見つけた行と終わった行を 1 回ずつ書く
	orphans map[target.Ref]bool
	// orphansUnknown は、直近の tick で orphan を確かめられなかったか。次に確かめられる tick まで、tick の外の再起動もしない
	orphansUnknown bool
	// board は状態 file にだけ使う状態。loop の判断には使わない
	board board
	// mediation は調停 remote を指した loop の調停の記憶。指さなければ nil
	mediation *mediation
}

// fixedAtStartChanged は、起動時に固定する項目 (formats.md §6) のうち def で起動時と変わったものがあれば、その tick の error の
// 文を返す。変わらなければ ""。項目ごとの固定する理由は formats.md §6。
func (l *loop) fixedAtStartChanged(def workflow.Definition) string {
	start := l.o.Definition
	key, place, startPlace := l.o.ScopeKeyOf(def), def.CLPlace(), start.CLPlace()
	for _, f := range []struct {
		item, now, start string
		same             bool
	}{
		{"scope key", key, l.o.ScopeKey, key == l.o.ScopeKey},
		{"CL 置き場", strconv.Quote(place), strconv.Quote(startPlace), strings.EqualFold(place, startPlace)},
		{"coordination.remote", strconv.Quote(def.CoordinationRemote), strconv.Quote(start.CoordinationRemote), def.CoordinationRemote == start.CoordinationRemote},
	} {
		if !f.same {
			return fmt.Sprintf("workflow 定義の %s %s が起動時の %s と違う (loop を起動し直す)", f.item, f.now, f.start)
		}
	}
	return ""
}

// Run は停止要求で止まるまで tick を回す。起動の直後に 1 回 tick を撃ち、以後は tick の終了から周期だけ待つ。
// 再起動の backoff が明けたら、tick を待たずに再起動を試みる。停止待ちの間は起動せず、周期ごとに走っている worker の
// 作業対象だけを読み直す。
func Run(o Options) int {
	l := &loop{
		o: o, def: o.Definition, claims: map[target.Ref]*claim{}, abandoned: map[target.Ref]string{}, events: make(chan worker.Event),
		rec: recorder{log: o.Log, output: o.Output, now: o.Now, scope: o.ScopeKey}, board: board{startedAt: o.Now()},
	}
	var next <-chan time.Time
	start := map[string]any{"version": o.Build.Version, "commit": o.Build.Commit}
	if o.Coordination != nil {
		l.mediation = &mediation{c: o.Coordination, seen: map[string]sighting{}, interval: coord.HeartbeatInterval(o.Definition.Interval)}
		start["loop"] = o.Coordination.LoopName()
		l.mediation.startBeating(o.HeartbeatAfter)
	}
	// どの版の binary が書いた log かを後から読めるよう、起動を 1 行残す。人が読む起動の行は呼び出し側が出している
	l.rec.write("loop_start", start)
	for _, kind := range o.StartupSlowKinds {
		l.rec.write("slow_listing", slowListingFields(o.Definition, kind))
	}
	// 前の loop が残した状態 file を、最初の tick の前に今の loop の状態で書き換える
	l.publish()
	l.tick()
	for {
		// tick の間に届いた停止要求は、tick を終えてから受ける
		select {
		case sig := <-o.Signals:
			l.requestStop(sig)
		default:
		}
		if l.stop.requested() && l.awaited() == 0 {
			return l.exit()
		}
		if next == nil {
			next = o.After(l.def.Interval)
			l.board.nextTickAt = o.Now().Add(l.def.Interval)
		}
		l.arm()
		l.publish()
		select {
		case sig := <-o.Signals:
			l.requestStop(sig)
		case ev := <-l.events:
			l.handle(ev)
		case <-next:
			next = nil
			if !l.stop.requested() {
				l.tick()
			} else {
				l.reconcile(l.o.Store(l.def))
			}
		case <-l.wake:
			l.wake = nil
			if !l.stop.requested() {
				l.retry(l.o.Store(l.def), outsideTick)
			}
		case <-o.Refresh:
			l.refresh()
		case err := <-l.beats():
			l.beat(err)
		}
	}
}

// requestStop は停止要求を受ける。1 回目は新しい起動をやめて worker の終了を待ち、2 回目は worker を止める。
func (l *loop) requestStop(sig os.Signal) {
	l.stop = l.stop.next()
	l.lastStop = sig
	awaited := l.awaited()
	for ref, c := range l.claims {
		l.apply(ref, c, c.stopRequested(l.stop))
	}
	if awaited > 0 && l.stop == stopFirst {
		l.rec.human(LineStopping, "停止待ち", ": 走っている worker %d 本の終了を待つ (もう一度で止める)", awaited)
	}
}

func (l *loop) exit() int {
	for ref, c := range l.claims {
		l.apply(ref, c, c.loopEnded())
	}
	l.leaveRemote()
	// 止まる時点の claim を状態 file に残す (loop が止まった後の status は worker を出さないが、file は読み返せる)
	l.publish()
	l.rec.human(LineNote, "loop を止めた", " (停止要求 %s)", SignalName(l.lastStop))
	return 0
}

// awaited は loop の停止が終わりを待つ worker の数。止められない worker は、claude がいつ終わるか分からないので、claude が
// 終わるまで数えない。
func (l *loop) awaited() int {
	n := 0
	for _, c := range l.claims {
		if c.holdsWorker() && !c.claudeAlive() {
			n++
		}
	}
	return n
}

// running は走っている worker (止めている途中と、止められない worker を含む) の数。並列の枠を数えるのに使う。
func (l *loop) running() int {
	n := 0
	for _, c := range l.claims {
		if c.holdsWorker() {
			n++
		}
	}
	return n
}

// slotsInUse は再起動の空きの判定に使う、並列の枠を使っているものの数: 走っている worker と生きている orphan (claude が
// 走り続けうる)。候補の起動は、tick の初めの走っている数に、再起動待ち・今の orphan・起動した数を足して比べる。
func (l *loop) slotsInUse() int { return l.running() + len(l.orphans) }

// holdsWorker は claim が worker (止めている途中と、止められない worker を含む) を持つ段階か。
func (c *claim) holdsWorker() bool { return c.phase == phaseRunning || c.phase == phaseStopping }

// tick は 1 周期分の仕事をする (formats.md §6 の tick の手順)。
func (l *loop) tick() {
	current := l.o.Store(l.def)
	l.reconcile(current)
	l.recheck(current)
	def, err := l.o.Load()
	if err != nil {
		l.tickFailed("workflow 定義の誤り: " + oneLine(err))
		return
	}
	if l.o.MaxConcurrent != nil {
		// max-concurrent で渡した値は --max-concurrent より優先する。読めなければ、workflow 定義を採れない tick と同じく
		// 何も起動しない (system.md §7 の手順 2)
		n, ok, err := l.o.MaxConcurrent()
		if err != nil {
			l.tickFailed(oneLine(err))
			return
		}
		if ok {
			def.MaxConcurrent = n
		}
	}
	if changed := l.fixedAtStartChanged(def); changed != "" {
		// 起動時に固定する項目が変わった版は採らない。この tick は掃除も起動もせず、次の tick の突き合わせは採っていた版を読む
		l.tickFailed(changed)
		return
	}
	if def.MaxConcurrent != l.def.MaxConcurrent {
		// max-concurrent で渡した値を採った。採った値は tick の行と状態 file にも出る (formats.md §6)
		l.rec.human(LineNote, "並列上限を変えた", ": %d → %d", l.def.MaxConcurrent, def.MaxConcurrent)
	}
	l.def, l.blocked = def, l.o.Precheck(def)
	if l.mediation != nil {
		if err := l.coordinate(); err != nil {
			// 誰が着手しているかを決められないので、掃除も起動 (再起動を含む) もしない
			l.tickFailed(oneLine(err))
			return
		}
	}
	store := l.o.Store(def)
	// 遅い一覧は目標を超えた読み出しなので、画面の行に加えて log.jsonl にも残す (formats.md §4 の冒頭の規則)
	store.Slow = SlowNotice{After: l.o.SlowAfter, Notify: func(kind target.Kind) {
		l.rec.event("slow_listing", slowListingFields(def, kind), LineNote, SlowListingLine(def, kind), "")
	}}
	open, err := OpenItems(store, def)
	var nonFatal *target.NonFatal
	switch {
	case errors.As(err, &nonFatal):
		// 作業対象は読めた。補助の要求の失敗だけを残して tick を続ける (formats.md §3)
		l.rec.error(target.Ref{}, oneLine(err))
	case err != nil:
		l.tickFailed(oneLine(err))
		return
	}
	// 事前検査に落ちた trigger は評価から外す。その trigger に当たる作業対象は、宣言順で後ろの trigger に当たれば起動する
	triggers := l.evaluable(def)
	candidates, ambiguous := trigger.Evaluate(triggers, open)
	v := &view{open: open, ambiguous: trigger.AmbiguousRefs(ambiguous), branches: l.claimedBranches()}
	// 打ち切りを解く確かめは orphan と関わらないので、orphan を確かめられない tick でも済ませる
	l.clearAbandoned(store, def, v)
	ws := l.o.Workspaces(l.def)
	unclaimed, checked, err := l.observeOrphans(ws)
	l.orphansUnknown = err != nil
	if err != nil {
		// どの workspace の claude が生きているかを決められないので、掃除も起動 (再起動を含む) もしない
		l.tickFailed(oneLine(err))
		return
	}
	l.sweep(store, ws, open, unclaimed)
	l.retry(store, v)
	// 起動を始める前の数を tick の行にも載せる。起動した worker は走っている worker に加わるので、launched の数を足して判定する。
	// orphan も claude が走り続けうるので並列の枠を使う
	running, waitingRetry, orphans := l.running(), l.waitingRetry(), len(l.orphans)
	var launched []string
	for _, c := range candidates {
		// orphan は起動の直前の確かめでも増えるので、今の数で比べる
		if running+waitingRetry+len(l.orphans)+len(launched) >= def.MaxConcurrent {
			break
		}
		ref := c.Item.Ref()
		if _, claimed := l.claims[ref]; claimed {
			continue
		}
		if _, abandoned := l.abandoned[ref]; abandoned {
			continue
		}
		if l.orphans[ref] {
			continue
		}
		if l.newerFormatSeen() {
			// 新しい版の ref がある間は、その scope で worker を起動しない (formats.md §12)
			break
		}
		if l.heldByOther(ref) {
			continue
		}
		// claim している作業対象の branch に、CL の worker を重ねない。一覧の CL で外せるものは読み直さずに外し、読み直した
		// CL でも確かめ直す
		if cl, isCL := c.Item.(target.CL); isCL && v.branchHeld(cl, target.Ref{}) {
			continue
		}
		// claim の無い workspace の一覧に無かった候補 (別の clone・workspace.root の変更・人が消した workspace) も、
		// 起動の前に orphan を確かめる。置き場を読み直す前に確かめ、orphan の候補に読み直しを払わない
		if !checked[ref] && l.blockedByOrphan(ref) {
			continue
		}
		item, ok := l.recheckCandidate(store, triggers, c)
		if !ok {
			continue
		}
		if cl, isCL := item.(target.CL); isCL && v.branchHeld(cl, target.Ref{}) {
			continue
		}
		c.Item = item
		if l.launch(def, c) {
			launched = append(launched, ref.String())
		}
	}
	if l.mediation != nil {
		l.cleanupDeadLoops()
	}
	l.board.lastTick = &status.Tick{At: l.o.Now(), Result: status.TickOK, Candidates: len(candidates)}
	l.board.ambiguous = ambiguous
	l.rec.event("tick", map[string]any{"result": status.TickOK, "candidates": len(candidates), "launched": nonNil(launched), "ambiguous": ambiguousFields(ambiguous), "blocked": blockedFields(l.blocked),
		"running": running, "waiting_retry": waitingRetry, "orphans": orphans, "max_concurrent": def.MaxConcurrent},
		LineTickOK, "tick ok", " · %s%s%s", Summary(candidates, def), ambiguousSummary(ambiguous), blockedSummary(l.blocked))
}

// view は tick が読んだ open な一覧と、そこから導いた CL の除外 (曖昧な CL と、claim している作業対象の branch)。
// tick の外から再起動を試みるときは outsideTick (nil) で、CL の除外を確かめられない。
type view struct {
	open      []target.Item
	ambiguous map[target.Ref]bool
	// branches は claim している作業対象の workspace の branch と、それを checkout している claim の作業対象を読む。ok が
	// false なら読めない branch があった
	branches func() (owners map[string][]target.Ref, ok bool)
}

// outsideTick は tick の外から retry を呼ぶときの view (無い)
var outsideTick *view

// branchHeld は、cl の head branch が self 以外の claim の workspace で checkout されているか。branch を読めなければ
// checkout されているものとして扱う (同じ branch に worker を重ねないため)。
func (v *view) branchHeld(cl target.CL, self target.Ref) bool {
	owners, ok := v.branches()
	if !ok {
		return true
	}
	return cl.SameRepo && slices.ContainsFunc(owners[cl.Head], func(owner target.Ref) bool { return owner != self })
}

// claimedBranches は、claim している作業対象の workspace で checkout されている branch を読む関数を返す。読むのは最初に
// 呼ばれたとき (CL を起動しようとしたとき) の 1 回だけ。読めない branch があれば error の行を残し、ok を false で返す
// (その tick は CL の worker を起動しない)。
func (l *loop) claimedBranches() func() (map[string][]target.Ref, bool) {
	var owners map[string][]target.Ref
	var ok bool
	return func() (map[string][]target.Ref, bool) {
		if owners != nil {
			return owners, ok
		}
		owners, ok = map[string][]target.Ref{}, true
		// orphan の claude もその workspace の branch で作業を続けうるので、claim と同じく数える
		holders := map[target.Ref]Workspaces{}
		for ref, c := range l.claims {
			holders[ref] = l.o.Workspaces(l.definitionOf(c))
		}
		for ref := range l.orphans {
			holders[ref] = l.o.Workspaces(l.def)
		}
		for ref, ws := range holders {
			branch, err := ws.Branch(ref)
			if err != nil {
				l.rec.error(ref, "この tick は CL の worker を起動しない: "+oneLine(err))
				ok = false
				continue
			}
			if branch != "" {
				owners[branch] = append(owners[branch], ref)
			}
		}
		return owners, ok
	}
}

// isBlocked は trigger が事前検査に落ちているか。
func (l *loop) isBlocked(name string) bool {
	return slices.ContainsFunc(l.blocked, func(p precheck.Problem) bool { return p.Trigger == name })
}

// evaluable は workflow 定義の trigger のうち、事前検査に落ちていないもの (宣言順のまま)。
func (l *loop) evaluable(def workflow.Definition) []trigger.Trigger {
	var triggers []trigger.Trigger
	for _, t := range def.Triggers {
		if !l.isBlocked(t.Name) {
			triggers = append(triggers, t)
		}
	}
	return triggers
}

// blockedFields は tick の行の blocked の値。
func blockedFields(blocked []precheck.Problem) []map[string]any {
	fields := []map[string]any{}
	for _, p := range blocked {
		fields = append(fields, map[string]any{"trigger": p.Trigger, "error": p.Error})
	}
	return fields
}

// blockedSummary は人が読む tick の行の、起動しない trigger の欄。無ければ ""。
func blockedSummary(blocked []precheck.Problem) string {
	if len(blocked) == 0 {
		return ""
	}
	parts := make([]string, len(blocked))
	for i, p := range blocked {
		parts[i] = fmt.Sprintf("%s (%s)", p.Trigger, p.Error)
	}
	return " · 起動しない trigger: " + strings.Join(parts, ", ")
}

// ambiguousFields は tick の行の ambiguous の値。
func ambiguousFields(ambiguous []trigger.AmbiguousHead) []map[string]any {
	fields := []map[string]any{}
	for _, a := range ambiguous {
		fields = append(fields, map[string]any{"head": a.Head, "targets": refNames(a.Targets)})
	}
	return fields
}

// ambiguousSummary は tick の人が読む行に足す、曖昧な CL の欄。無ければ ""。
func ambiguousSummary(ambiguous []trigger.AmbiguousHead) string {
	if len(ambiguous) == 0 {
		return ""
	}
	heads := make([]string, len(ambiguous))
	for i, a := range ambiguous {
		heads[i] = fmt.Sprintf("%s (%s)", a.Head, strings.Join(refNames(a.Targets), ", "))
	}
	return " · 曖昧な CL: " + strings.Join(heads, ", ")
}

func refNames(refs []target.Ref) []string {
	names := make([]string, len(refs))
	for i, ref := range refs {
		names[i] = ref.String()
	}
	return names
}

// OpenItems は、workflow 定義の trigger に現れる種類の open な作業対象を置き場から全件読む。置き場の部品が作業対象とともに
// 処理を続けられる失敗 (*target.NonFatal) を返したら、作業対象を読み進め、その失敗をまとめて作業対象とともに返す。
func OpenItems(store Store, def workflow.Definition) ([]target.Item, error) {
	items := []target.Item{}
	var nonFatal []error
	for _, kind := range def.Kinds() {
		open, err := store.Open(kind)
		var failure *target.NonFatal
		switch {
		case errors.As(err, &failure):
			nonFatal = append(nonFatal, err)
		case err != nil:
			return nil, err
		}
		items = append(items, open...)
	}
	return items, errors.Join(nonFatal...)
}

// reconcile は走っている worker の作業対象を読み直し、終端になっていれば worker を止める。trigger から外れただけでは止めない。
func (l *loop) reconcile(store Store) {
	for ref, c := range l.claims {
		if c.phase != phaseRunning {
			continue
		}
		o, _ := l.read(store, ref, terminalOnly)
		l.apply(ref, c, c.observed(ref, o, l.env()))
	}
}

// recheck は、終わり方を確かめ待ちの worker (phaseAwaitingVerification) を確かめ直す。
func (l *loop) recheck(store Store) {
	for ref, c := range l.claims {
		if c.phase == phaseAwaitingVerification {
			l.verify(store, ref, c)
		}
	}
}

// observeOrphans は、claim の無い workspace と前の tick まで生きていた orphan の作業対象ごとに orphan が生きているかを
// 確かめ、l.orphans を置き換える。claim の無い workspace の作業対象 (unclaimed) と、確かめた作業対象 (checked) を返す。
// 見つけた orphan と、前の tick まで生きていて終わった orphan を 1 行ずつ書く。workspace の一覧を読めないか、どれか 1 つでも
// 確かめられなければ error を返し、l.orphans を前の tick のまま残す (formats.md §6 の orphan)。前の tick の orphan も
// 確かめ直すのは、workspace の一覧から消えても (workspace.root を変えた・人が消した) claude が生きていれば終わったと
// 書かないため。
func (l *loop) observeOrphans(ws Workspaces) (unclaimed []target.Ref, checked map[target.Ref]bool, err error) {
	refs, err := ws.Existing()
	if err != nil {
		return nil, nil, fmt.Errorf("orphan を確かめられない (workspace の一覧を読めない): %w", err)
	}
	checked = map[target.Ref]bool{}
	alive := map[target.Ref]bool{}
	probe := func(ref target.Ref) error {
		if _, claimed := l.claims[ref]; claimed || checked[ref] {
			return nil
		}
		checked[ref] = true
		held, err := l.o.Orphaned(ref)
		if err != nil {
			return fmt.Errorf("%s の orphan を確かめられない: %w", ref, err)
		}
		if held {
			alive[ref] = true
		}
		return nil
	}
	for _, ref := range refs {
		if _, claimed := l.claims[ref]; !claimed {
			unclaimed = append(unclaimed, ref)
		}
		if err := probe(ref); err != nil {
			return nil, nil, err
		}
	}
	for _, ref := range sortedRefs(l.orphans) {
		if err := probe(ref); err != nil {
			return nil, nil, err
		}
	}
	for _, ref := range sortedRefs(alive) {
		if !l.orphans[ref] {
			l.logOrphan(ref)
		}
	}
	for _, ref := range sortedRefs(l.orphans) {
		if !alive[ref] {
			l.rec.event("orphan_end", map[string]any{"target": ref.String()}, LineNote, "orphan", " %s が終わった", ref)
		}
	}
	l.orphans = alive
	return unclaimed, checked, nil
}

// blockedByOrphan は、tick の初めに確かめなかった作業対象の orphan を確かめ、生きているか確かめられなければ true を返す。生きて
// いれば l.orphans に足して見つけた行を書き、確かめられなければ error の行を残す (次の tick で試み直す)。
func (l *loop) blockedByOrphan(ref target.Ref) bool {
	held, err := l.o.Orphaned(ref)
	if err != nil {
		l.rec.error(ref, "起動しようとした作業対象の orphan を確かめられない (次の tick で試み直す): "+oneLine(err))
		return true
	}
	if held {
		if l.orphans == nil {
			l.orphans = map[target.Ref]bool{}
		}
		l.orphans[ref] = true
		l.logOrphan(ref)
	}
	return held
}

// logOrphan は orphan を見つけた行を書く。
func (l *loop) logOrphan(ref target.Ref) {
	l.rec.event("orphan", map[string]any{"target": ref.String()}, LineNote, "orphan", " %s: 前の loop の claude がまだ動いている", ref)
}

// sweep は、claim の無い workspace (unclaimed) のうち、orphan が生きておらず作業対象が終端になったものを消す。open な一覧に
// 無いものだけを読み直す。loop の起動の直後の tick が起動時の掃除を兼ね、以後の tick が claim を解いた後に終端になったものと、
// 消し損ねたものと、orphan が終わったものを拾う。
func (l *loop) sweep(store Store, ws Workspaces, open []target.Item, unclaimed []target.Ref) {
	isOpen := map[target.Ref]bool{}
	for _, i := range open {
		isOpen[i.Ref()] = true
	}
	for _, ref := range unclaimed {
		if isOpen[ref] || l.orphans[ref] {
			continue
		}
		item, err := store.Read(ref)
		if err != nil {
			l.rec.error(ref, "workspace を掃除できない (読み直せない): "+oneLine(err))
			continue
		}
		if item.Terminal() && l.remove(ws, ref) {
			l.rec.human(LineNote, "掃除", " %s: 終端の workspace を消した", ref)
		}
	}
}

// recheckCandidate は、起動しようとする候補を RecheckCandidate で読み直し、起動してよければ読み直した作業対象を返す。
// 起動しない理由があれば recheck_skip の行を、読み直せなければ error の行を残して起動しない。どれも次の tick で候補になれば
// 試み直す。
func (l *loop) recheckCandidate(store Store, triggers []trigger.Trigger, c trigger.Candidate) (target.Item, bool) {
	ref := c.Item.Ref()
	item, reason, err := RecheckCandidate(store, triggers, c)
	if err != nil {
		l.rec.error(ref, "起動しようとした作業対象を読み直せない (次の tick で試み直す): "+oneLine(err))
		return nil, false
	}
	if reason == "" {
		return item, true
	}
	l.rec.event("recheck_skip", map[string]any{"target": ref.String(), "trigger": c.Trigger.Name, "reason": reason},
		LineNote, "読み直しで起動せず", " %s (%s): %s", ref, c.Trigger.Name, reason.screen())
	return nil, false
}

// RecheckCandidate は、候補を置き場から読み直して起動してよいかを決める (formats.md §6 の tick の手順 7)。open な一覧の
// 検索は書き込みの直後に古い結果を返しうる (Jira の JQL 検索など) ので、完了した直後の作業対象を起動し直さないために読み
// 直す。loop の起動と試運転 (formats.md §5) が同じ判定を使う。起動してよければ読み直した作業対象と "" を、起動しなければその理由 (当たるかを
// まだ決められない・終端・起動しようとした trigger から外れた・triggers (宣言順) のうち先のものに当たる) を返す。読み直せ
// なければ error。再起動はこの判定を使わない (宣言順で先の trigger を確かめないため。retry を見る)。
func RecheckCandidate(store Store, triggers []trigger.Trigger, c trigger.Candidate) (target.Item, Reason, error) {
	item, err := store.Read(c.Item.Ref())
	if err != nil {
		return nil, "", err
	}
	if observed := c.Trigger.Observe(item); observed != trigger.ObservedStillMatches {
		return nil, reasonOf(observed), nil
	}
	if matchesEarlier(triggers, *c.Trigger, item) {
		return nil, reasonEarlierTrigger, nil
	}
	return item, "", nil
}

// matchesEarlier は、作業対象が triggers (宣言順) のうち t より先の trigger に当たるか。
func matchesEarlier(triggers []trigger.Trigger, t trigger.Trigger, item target.Item) bool {
	for _, earlier := range triggers {
		if earlier.Name == t.Name {
			return false
		}
		if earlier.Matches(item) {
			return true
		}
	}
	return false
}

func (l *loop) launch(def workflow.Definition, c trigger.Candidate) bool {
	ref := c.Item.Ref()
	sessionID, err := l.o.NewSessionID()
	if err != nil {
		l.rec.error(ref, err.Error())
		return false
	}
	// 着手宣言は claim を立てる直前に置く。負けたら claim を立てない (system.md §7 の調停 remote の突き合わせ)
	claimSHA, ok := l.claimRemote(ref, c.Trigger.Name)
	if !ok {
		return false
	}
	claimed := newClaim(c.Item, *c.Trigger, sessionID, def.WorkspaceRoot, claimSHA)
	l.claims[ref] = claimed
	l.start(claimed)
	return true
}

// start は claim の今の attempt の worker を、そのときの workflow 定義で起動する。
func (l *loop) start(c *claim) {
	def := l.definitionOf(c)
	job := worker.Job{Item: c.item, Trigger: c.trigger, Attempt: c.attempt, SessionID: c.sessionID, Resume: c.sessionStarted, Prompt: def.Prompt}
	c.run = l.o.Launch(def, job, l.events)
}

// definitionOf は claim の worker と workspace に使う workflow 定義。そのときの定義の workspace.root だけを、最初に起動した
// ときの root に差し替える。
func (l *loop) definitionOf(c *claim) workflow.Definition {
	def := l.def
	def.WorkspaceRoot = c.workspaceRoot
	return def
}

// handle は worker の起動・止められない知らせ・終わりを受ける。
func (l *loop) handle(ev worker.Event) {
	switch ev := ev.(type) {
	case worker.Started:
		c, ok := l.claimOf(ev.Target)
		if !ok {
			return
		}
		c.sessionStarted, c.pid, c.workspace = true, ev.PID, ev.Workspace
		now := l.o.Now()
		c.startedAt = &now
		name := ev.Target.String()
		l.rec.event("start", map[string]any{
			"target": name, "trigger": c.trigger.Name, "attempt": c.attempt,
			"session_id": c.sessionID, "workspace": ev.Workspace, "pid": ev.PID,
		}, LineStart, "起動", " %s (%s, attempt %d, session %s)", name, c.trigger.Name, c.attempt, c.sessionID)
	case worker.Unstopped:
		c, ok := l.claimOf(ev.Target)
		if !ok {
			return
		}
		l.apply(ev.Target, c, c.workerUnstopped(ev.StopError, ev.Exited))
	case worker.Ended:
		c, ok := l.claimOf(ev.Target)
		if !ok {
			return
		}
		result := ev.Result
		for _, message := range []string{result.AfterRunError, result.StopError, result.ResultSummaryError} {
			if message != "" {
				l.rec.error(ev.Target, message)
			}
		}
		store := l.o.Store(l.def)
		e := c.workerEnded(ev.Target, result)
		l.apply(ev.Target, c, e)
		if e.readItem {
			l.verify(store, ev.Target, c)
		}
		if !l.stop.requested() {
			// 空いた枠で、空きを待っていた再起動を試みる
			l.retry(store, outsideTick)
		}
	}
}

// claimOf は作業対象の claim を返す。無ければ運用者に残す (claim の無い worker は起動しないので、起きれば loop の誤り)。
func (l *loop) claimOf(ref target.Ref) (*claim, bool) {
	c, ok := l.claims[ref]
	if !ok {
		l.rec.error(ref, "claim の無い作業対象の worker の event を受けた")
	}
	return c, ok
}

// verify は終わった worker の作業対象を読み直し、観測を claim の入口に渡す (終わり方の規則は claim.verified)。
func (l *loop) verify(store Store, ref target.Ref, c *claim) {
	o, _ := l.read(store, ref, c.trigger.Observe)
	l.apply(ref, c, c.observed(ref, o, l.env()))
}

// reasonOf は読み直した作業対象の観測を、loop の理由に写す。
func reasonOf(observed trigger.Observation) Reason {
	switch observed {
	case trigger.ObservedStillMatches:
		return reasonStillMatches
	case trigger.ObservedTerminal:
		return reasonTerminal
	case trigger.ObservedLeftTrigger:
		return reasonLeftTrigger
	case trigger.ObservedUndecided:
		return reasonUndecided
	}
	panic(fmt.Sprintf("trigger.Observation の値 %d を写せない", observed))
}

// endScreen は終了の行に出す、終わり方の理由の文。worker 自身が失敗していれば、completed では理由に失敗を添え、failed
// では理由 (当たったまま) の代わりに失敗を出す。
func endScreen(o outcome, reason Reason, failure string) string {
	switch {
	case failure == "":
		return reason.screen()
	case o == failed:
		return failure
	}
	return reason.screen() + " (worker は " + failure + ")"
}

// remove は workspace を消す。消せなければ error の行を残し、次の tick の掃除で消し直す。
func (l *loop) remove(ws Workspaces, ref target.Ref) bool {
	if err := ws.Remove(ref); err != nil {
		l.rec.error(ref, oneLine(err))
		return false
	}
	return true
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Summary は tick の行の候補の欄。番号は作業対象の置き場の綴りの参照で出す。
func Summary(candidates []trigger.Candidate, def workflow.Definition) string {
	if len(candidates) == 0 {
		return "候補 0"
	}
	names := make([]string, len(candidates))
	for i, c := range candidates {
		names[i] = fmt.Sprintf("%s %s %s", c.Trigger.Name, c.Trigger.On, def.Reference(c.Item.Ref()))
	}
	return fmt.Sprintf("候補 %d: %s", len(candidates), strings.Join(names, ", "))
}

// oneLine は複数行の error を ` / ` で 1 行に畳む。
func oneLine(err error) string {
	return strings.ReplaceAll(err.Error(), "\n", " / ")
}

// SignalName は停止要求の signal の名前 (SIGINT など)。
func SignalName(sig os.Signal) string {
	switch sig {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGHUP:
		return "SIGHUP"
	}
	return sig.String()
}
