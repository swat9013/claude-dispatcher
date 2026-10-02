// Package loop は tick を周期ごとに回し、trigger に当たった作業対象へ worker を起動して、終わり方を確かめる
// (system.md §7 / §9、formats.md §4 / §6)。claim は loop の memory だけに持ち、tracker には書かない。
package loop

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/precheck"
	"github.com/swat9013/claude-dispatcher/internal/status"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/worker"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// Store は issue 置き場と CL 置き場の部品 (system.md §13)。本番は gh の adapter、テストは in-memory。失敗は *target.Failure。
type Store interface {
	ScopeKey() string
	// Open は種類の open な作業対象を全件読む
	Open(kind target.Kind) ([]target.Item, error)
	// Read は作業対象 1 件を読み直す。終端になっているか消えていれば Terminal
	Read(ref target.Ref) (target.Item, error)
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
	Now      func() time.Time
	// After は d 後に届く channel を返す (time.After。テストは差し替える)
	After func(d time.Duration) <-chan time.Time
	// Refresh は走っている worker の活動を確かめる周期 (1s) に届く。nil なら確かめない
	Refresh <-chan time.Time
}

// phase は claim の段階。
type phase int

const (
	// phaseRunning は worker が走っている
	phaseRunning phase = iota
	// phaseStopping は loop が worker を止めている
	phaseStopping
	// phaseAwaitingVerification は worker が終わったが、作業対象を読み直せず終わり方を確かめ待ち
	phaseAwaitingVerification
	// phaseWaitingRetry は失敗した worker の次の attempt を、backoff が明けるか空きが出るまで待っている
	phaseWaitingRetry
)

// stopReason は loop が worker を止めた理由。
type stopReason string

const (
	stoppedAtTerminal stopReason = "終端"
	stoppedByRequest  stopReason = "停止要求"
)

// 終わった worker の作業対象を読み直したときの理由
const (
	reasonTerminal     = "終端"
	reasonLeftTrigger  = "trigger から外れた"
	reasonStillMatches = "trigger に当たったまま"
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
}

type loop struct {
	o      Options
	def    workflow.Definition
	claims map[target.Ref]*claim
	// abandoned は打ち切った作業対象と、打ち切ったときの trigger の名前
	abandoned map[target.Ref]string
	events    chan worker.Event
	rec       recorder
	// stopping は受けた停止要求の数
	stopping int
	lastStop os.Signal
	// wake は最も早い再起動の予定に届く channel、wakeAt はその時刻。予定が無ければ nil
	wake   <-chan time.Time
	wakeAt time.Time
	// blocked は、採っている workflow 定義で事前検査に落ちた trigger。起動も再起動もしない
	blocked []precheck.Problem
	// board は状態 file にだけ使う状態。loop の判断には使わない
	board board
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
		if l.stopping > 0 && l.running() == 0 {
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
			if l.stopping == 0 {
				l.tick()
			} else {
				l.reconcile(l.o.Store(l.def))
			}
		case <-l.wake:
			l.wake = nil
			if l.stopping == 0 {
				l.retry(l.o.Store(l.def), outsideTick)
			}
		case <-o.Refresh:
			l.refresh()
		}
	}
}

// requestStop は停止要求を受ける。1 回目は新しい起動をやめて worker の終了を待ち、2 回目は worker を止める。
func (l *loop) requestStop(sig os.Signal) {
	l.stopping++
	l.lastStop = sig
	running := l.running()
	if running == 0 {
		return
	}
	if l.stopping == 1 {
		l.rec.human(LineStopping, "停止待ち", ": 走っている worker %d 本の終了を待つ (もう一度で止める)", running)
		return
	}
	for _, c := range l.claims {
		if c.phase == phaseRunning {
			l.stop(c, stoppedByRequest)
		}
	}
}

func (l *loop) exit() int {
	for ref, c := range l.claims {
		if c.phase == phaseWaitingRetry {
			l.rec.error(ref, fmt.Sprintf("再起動を待ったまま止まる (%s, attempt %d まで)", c.trigger.Name, c.attempt))
			continue
		}
		l.rec.error(ref, fmt.Sprintf("終わり方を確かめられないまま止まる (%s)", c.trigger.Name))
	}
	// 止まる時点の claim を状態 file に残す (loop が止まった後の status は worker を出さないが、file は読み返せる)
	l.publish()
	l.rec.human(LineNote, "loop を止めた", " (停止要求 %s)", SignalName(l.lastStop))
	return 0
}

// running は走っている worker (止めている途中を含む) の数。
func (l *loop) running() int {
	n := 0
	for _, c := range l.claims {
		if c.phase == phaseRunning || c.phase == phaseStopping {
			n++
		}
	}
	return n
}

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
	store := l.o.Store(def)
	if key := store.ScopeKey(); key != l.o.ScopeKey {
		// 別の置き場を指す版は採らない。この tick は掃除も起動もせず、次の tick の突き合わせは採っていた版の置き場を読む
		l.tickFailed(fmt.Sprintf("workflow 定義の scope key %s が起動時の %s と違う (loop を起動し直す)", key, l.o.ScopeKey))
		return
	}
	l.def, l.blocked = def, l.o.Precheck(def)
	open, err := OpenItems(store, def)
	if err != nil {
		l.tickFailed(oneLine(err))
		return
	}
	// 事前検査に落ちた trigger は評価から外す。その trigger に当たる作業対象は、宣言順で後ろの trigger に当たれば起動する
	candidates, ambiguous := trigger.Evaluate(l.evaluable(def), open)
	v := &view{open: open, ambiguous: trigger.AmbiguousRefs(ambiguous), branches: l.claimedBranches()}
	l.sweep(store, open)
	l.clearAbandoned(def, v)
	l.retry(store, v)
	// 起動を始める前の数を tick の行にも載せる。起動した worker は走っている worker に加わるので、launched の数を足して判定する
	running, waitingRetry := l.running(), l.waitingRetry()
	var launched []string
	for _, c := range candidates {
		if running+waitingRetry+len(launched) >= def.MaxConcurrent {
			break
		}
		if cl, ok := c.Item.(target.CL); ok && v.branchHeld(cl, target.Ref{}) {
			// claim している作業対象の branch に、CL の worker を重ねない
			continue
		}
		ref := c.Item.Ref()
		if _, claimed := l.claims[ref]; claimed {
			continue
		}
		if _, abandoned := l.abandoned[ref]; abandoned {
			continue
		}
		if l.launch(def, c) {
			launched = append(launched, ref.String())
		}
	}
	l.board.lastTick = &status.Tick{At: l.o.Now(), Result: status.TickOK, Candidates: len(candidates)}
	l.board.ambiguous = ambiguous
	l.rec.event("tick", map[string]any{"result": status.TickOK, "candidates": len(candidates), "launched": nonNil(launched), "ambiguous": ambiguousFields(ambiguous), "blocked": blockedFields(l.blocked),
		"running": running, "waiting_retry": waitingRetry, "max_concurrent": def.MaxConcurrent},
		LineTickOK, "tick ok", " · %s%s%s", Summary(candidates), ambiguousSummary(ambiguous), blockedSummary(l.blocked))
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
		for ref, c := range l.claims {
			branch, err := l.o.Workspaces(l.definitionOf(c)).Branch(ref)
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

// OpenItems は、workflow 定義の trigger に現れる種類の open な作業対象を置き場から全件読む。
func OpenItems(store Store, def workflow.Definition) ([]target.Item, error) {
	items := []target.Item{}
	for _, kind := range def.Kinds() {
		open, err := store.Open(kind)
		if err != nil {
			return nil, err
		}
		items = append(items, open...)
	}
	return items, nil
}

// reconcile は走っている worker の作業対象を読み直し、終端になっていれば worker を止める。trigger から外れただけでは止めない。
func (l *loop) reconcile(store Store) {
	for ref, c := range l.claims {
		if c.phase != phaseRunning {
			continue
		}
		item, err := store.Read(ref)
		if err != nil {
			l.rec.error(ref, "走っている worker の作業対象を読み直せない: "+oneLine(err))
			continue
		}
		if item.Terminal() {
			l.stop(c, stoppedAtTerminal)
		}
	}
}

func (l *loop) stop(c *claim, reason stopReason) {
	c.phase, c.stopReason = phaseStopping, reason
	c.run.Stop()
}

// recheck は、終わったが作業対象を読み直せなかった worker を確かめ直す。
func (l *loop) recheck(store Store) {
	for ref, c := range l.claims {
		if c.phase == phaseAwaitingVerification {
			l.verify(store, ref, c)
		}
	}
}

// sweep は、claim の無い workspace のうち作業対象が終端になったものを消す。open な一覧に無いものだけを読み直す。
// loop の起動の直後の tick が起動時の掃除を兼ね、以後の tick が claim を解いた後に終端になったものと、消し損ねたものを拾う。
func (l *loop) sweep(store Store, open []target.Item) {
	ws := l.o.Workspaces(l.def)
	refs, err := ws.Existing()
	if err != nil {
		l.rec.error(target.Ref{}, oneLine(err))
		return
	}
	isOpen := map[target.Ref]bool{}
	for _, i := range open {
		isOpen[i.Ref()] = true
	}
	for _, ref := range refs {
		if _, claimed := l.claims[ref]; claimed || isOpen[ref] {
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

func (l *loop) launch(def workflow.Definition, c trigger.Candidate) bool {
	ref := c.Item.Ref()
	sessionID, err := l.o.NewSessionID()
	if err != nil {
		l.rec.error(ref, err.Error())
		return false
	}
	claimed := &claim{item: c.Item, trigger: *c.Trigger, attempt: 1, sessionID: sessionID, workspaceRoot: def.WorkspaceRoot}
	l.claims[ref] = claimed
	l.start(claimed)
	return true
}

// start は claim の今の attempt の worker を、そのときの workflow 定義で起動する。
func (l *loop) start(c *claim) {
	def := l.definitionOf(c)
	c.phase, c.startedAt, c.activity = phaseRunning, nil, nil
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

// handle は worker の起動か終わりを受ける。
func (l *loop) handle(ev worker.Event) {
	switch ev := ev.(type) {
	case worker.Started:
		c, ok := l.claimOf(ev.Target)
		if !ok {
			return
		}
		c.sessionStarted = true
		now := l.o.Now()
		c.startedAt = &now
		name := ev.Target.String()
		l.rec.event("start", map[string]any{
			"target": name, "trigger": c.trigger.Name, "attempt": c.attempt,
			"session_id": c.sessionID, "workspace": ev.Workspace, "pid": ev.PID,
		}, LineStart, "起動", " %s (%s, attempt %d, session %s)", name, c.trigger.Name, c.attempt, c.sessionID)
	case worker.Ended:
		c, ok := l.claimOf(ev.Target)
		if !ok {
			return
		}
		result := ev.Result
		c.run = nil
		for _, message := range []string{result.AfterRunError, result.StopError} {
			if message != "" {
				l.rec.error(ev.Target, message)
			}
		}
		if result.Stopped {
			if c.stopReason == stoppedAtTerminal {
				l.remove(l.o.Workspaces(l.definitionOf(c)), ev.Target)
			}
			l.end(ev.Target, c, stopped, string(c.stopReason), result)
			delete(l.claims, ev.Target)
			return
		}
		c.phase, c.ended = phaseAwaitingVerification, &result
		store := l.o.Store(l.def)
		l.verify(store, ev.Target, c)
		if l.stopping == 0 {
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

// verify は終わった worker の作業対象を読み直し、終わり方を決める。完了なら claim を解き、失敗なら再起動に回す。読み直せなければ
// claim を持ったまま次の tick で確かめ直す (完了とも失敗とも数えない)。作業対象が終端か trigger から外れていれば、worker 自身の
// 失敗に関わらず完了とする。
func (l *loop) verify(store Store, ref target.Ref, c *claim) {
	item, err := store.Read(ref)
	if err != nil {
		l.rec.error(ref, "終わった worker の作業対象を読み直せない (次の tick で読み直す): "+oneLine(err))
		return
	}
	result := *c.ended
	if c.trigger.Undecided(item) {
		l.rec.error(ref, "終わった worker の作業対象が trigger に当たるかをまだ決められない (CL の conflict を計算中。次の tick で確かめ直す)")
		return
	}
	if settled := l.settle(ref, c, item); settled != "" {
		l.end(ref, c, completed, withFailure(settled, result), result)
		delete(l.claims, ref)
		return
	}
	reason := reasonStillMatches
	if result.Failure != "" {
		reason = result.Failure
	}
	l.end(ref, c, failed, reason, result)
	l.fail(ref, c)
}

// settle は読み直した作業対象が claim を解く状態かを見て、解く理由を返す。終端なら workspace も消す。当たったままなら ""。
func (l *loop) settle(ref target.Ref, c *claim, item target.Item) string {
	switch {
	case item.Terminal():
		l.remove(l.o.Workspaces(l.definitionOf(c)), ref)
		return reasonTerminal
	case !c.trigger.Matches(item):
		return reasonLeftTrigger
	}
	return ""
}

// withFailure は完了の理由に、worker 自身の失敗を添える。
func withFailure(reason string, result worker.Result) string {
	if result.Failure == "" {
		return reason
	}
	return reason + " (worker は " + result.Failure + ")"
}

// remove は workspace を消す。消せなければ error の行を残し、次の tick の掃除で消し直す。
func (l *loop) remove(ws Workspaces, ref target.Ref) bool {
	if err := ws.Remove(ref); err != nil {
		l.rec.error(ref, oneLine(err))
		return false
	}
	return true
}

// end は worker 1 回分の終わり方の行を書く。claim を解くかは呼び出し側が決める。
func (l *loop) end(ref target.Ref, c *claim, o outcome, reason string, result worker.Result) {
	name := ref.String()
	fields := map[string]any{
		"target": name, "trigger": c.trigger.Name, "attempt": c.attempt, "session_id": c.sessionID,
		"outcome": string(o), "reason": reason,
	}
	if result.ExitCode != nil {
		fields["exit_code"] = *result.ExitCode
	}
	l.rec.event("end", fields, endKinds[o], "終了", " %s (%s): %s — %s", name, c.trigger.Name, o, reason)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Summary は tick の行の候補の欄。
func Summary(candidates []trigger.Candidate) string {
	if len(candidates) == 0 {
		return "候補 0"
	}
	names := make([]string, len(candidates))
	for i, c := range candidates {
		names[i] = fmt.Sprintf("%s %s #%d", c.Trigger.Name, c.Trigger.On, c.Item.Ref().Number)
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
