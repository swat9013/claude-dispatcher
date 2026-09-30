// Package loop は tick を周期ごとに回し、trigger に当たった作業対象へ worker を起動して、終わり方を確かめる
// (system.md §7 / §9、formats.md §4 / §6)。claim は loop の memory だけに持ち、tracker には書かない。
package loop

import (
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/worker"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// Issues は issue 置き場の部品 (system.md §13)。本番は gh の adapter、テストは in-memory。失敗は *target.Failure。
type Issues interface {
	ScopeKey() string
	OpenIssues() ([]target.Issue, error)
	// Issue は 1 件を読み直す。close されているか消えていれば Closed
	Issue(number int) (target.Issue, error)
}

// Workspaces は workspace の掃除の口。
type Workspaces interface {
	Existing() ([]int, error)
	// Remove は before_remove を撃ってから workspace を消す
	Remove(number int) error
}

// Worker は走っている worker 1 回分。
type Worker interface{ Stop() }

// Options は loop の入力。
type Options struct {
	// Load は workflow 定義を読み直す
	Load func() (workflow.Definition, error)
	// Definition は起動時に検査に通った workflow 定義
	Definition workflow.Definition
	// Open は workflow 定義から issue 置き場の部品を組み立てる
	Open func(workflow.Definition) Issues
	// Workspaces は workflow 定義から workspace の掃除の口を組み立てる
	Workspaces func(workflow.Definition) Workspaces
	// Launch は worker を起動し、起動と終わりを events に送らせる
	Launch func(def workflow.Definition, job worker.Job, events chan<- worker.Event) Worker
	// NewSessionID は worker に渡す session id を発行する
	NewSessionID func() (string, error)
	// ScopeKey は起動時の scope key (lock を取った鍵)
	ScopeKey string
	// Log は log.jsonl (formats.md §4)、Stdout は人が読む行の出し先
	Log     io.Writer
	Stdout  io.Writer
	Signals <-chan os.Signal
	Now     func() time.Time
	// After は d 後に届く channel を返す (time.After。テストは差し替える)
	After func(d time.Duration) <-chan time.Time
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
)

// stopReason は loop が worker を止めた理由。
type stopReason string

const (
	stoppedAtTerminal stopReason = "終端"
	stoppedByRequest  stopReason = "停止要求"
)

// 終わった worker の作業対象を読み直したときの理由
const (
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

// claim は loop が worker を起動中か、終わり方を確かめ待ちの作業対象。
type claim struct {
	issue     target.Issue
	trigger   trigger.Trigger
	attempt   int
	sessionID string
	// workspaces は起動したときの workflow 定義の workspace の口。途中で定義が変わっても、同じ workspace を消す
	workspaces Workspaces
	phase      phase
	// run は phaseRunning と phaseStopping の worker
	run Worker
	// stopReason は phaseStopping の理由
	stopReason stopReason
	// ended は phaseAwaitingVerification の worker の終わり方
	ended *worker.Result
}

type loop struct {
	o      Options
	def    workflow.Definition
	claims map[int]*claim
	// held は失敗で終わった作業対象と、起動した trigger の名前。その trigger から外れるまで起動し直さない
	// (retry と backoff は #79 が置き換える)
	held   map[int]string
	events chan worker.Event
	rec    recorder
	// stopping は受けた停止要求の数
	stopping int
	lastStop os.Signal
}

// Run は停止要求で止まるまで tick を回す。起動の直後に 1 回 tick を撃ち、以後は tick の終了から周期だけ待つ。
// 停止待ちの間は起動せず、周期ごとに走っている worker の作業対象だけを読み直す。
func Run(o Options) int {
	l := &loop{
		o: o, def: o.Definition, claims: map[int]*claim{}, held: map[int]string{}, events: make(chan worker.Event),
		rec: recorder{log: o.Log, stdout: o.Stdout, now: o.Now, scope: o.ScopeKey},
	}
	var next <-chan time.Time
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
		}
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
				l.reconcile(l.o.Open(l.def))
			}
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
		l.rec.human("停止待ち: 走っている worker %d 本の終了を待つ (もう一度で止める)", running)
		return
	}
	for _, c := range l.claims {
		if c.phase == phaseRunning {
			l.stop(c, stoppedByRequest)
		}
	}
}

func (l *loop) exit() int {
	for n, c := range l.claims {
		l.rec.error(n, fmt.Sprintf("終わり方を確かめられないまま止まる (%s)", c.trigger.Name))
	}
	l.rec.human("loop を止めた (停止要求 %s)", SignalName(l.lastStop))
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
	current := l.o.Open(l.def)
	l.reconcile(current)
	l.recheck(current)
	def, err := l.o.Load()
	if err != nil {
		l.rec.tickError("workflow 定義の誤り: " + oneLine(err))
		return
	}
	issues := l.o.Open(def)
	if key := issues.ScopeKey(); key != l.o.ScopeKey {
		// 別の置き場を指す版は採らない。走っている worker の読み直しと掃除は、起動時の置き場のまま続ける
		l.rec.tickError(fmt.Sprintf("workflow 定義の scope key %s が起動時の %s と違う (loop を起動し直す)", key, l.o.ScopeKey))
		return
	}
	l.def = def
	open, err := issues.OpenIssues()
	if err != nil {
		l.rec.tickError(oneLine(err))
		return
	}
	l.sweep(issues, open)
	candidates := trigger.Evaluate(def.Triggers, open)
	l.release(candidates)
	var launched []string
	for _, c := range candidates {
		if l.running() >= def.MaxConcurrent {
			break
		}
		if _, claimed := l.claims[c.Issue.Number]; claimed {
			continue
		}
		if _, held := l.held[c.Issue.Number]; held {
			continue
		}
		if l.launch(def, c) {
			launched = append(launched, target.Name(c.Issue.Number))
		}
	}
	l.rec.event("tick", map[string]any{"result": "ok", "candidates": len(candidates), "launched": nonNil(launched)},
		"tick ok · %s", Summary(candidates))
}

// reconcile は走っている worker の作業対象を読み直し、終端になっていれば worker を止める。trigger から外れただけでは止めない。
func (l *loop) reconcile(issues Issues) {
	for n, c := range l.claims {
		if c.phase != phaseRunning {
			continue
		}
		issue, err := issues.Issue(n)
		if err != nil {
			l.rec.error(n, "走っている worker の作業対象を読み直せない: "+oneLine(err))
			continue
		}
		if issue.Closed {
			l.stop(c, stoppedAtTerminal)
		}
	}
}

func (l *loop) stop(c *claim, reason stopReason) {
	c.phase, c.stopReason = phaseStopping, reason
	c.run.Stop()
}

// recheck は、終わったが作業対象を読み直せなかった worker を確かめ直す。
func (l *loop) recheck(issues Issues) {
	for n, c := range l.claims {
		if c.phase == phaseAwaitingVerification {
			l.verify(issues, n, c)
		}
	}
}

// sweep は、claim の無い workspace のうち作業対象が終端になったものを消す。open な issue の一覧に無いものだけを読み直す。
// loop の起動の直後の tick が起動時の掃除を兼ね、以後の tick が claim を解いた後に終端になったものと、消し損ねたものを拾う。
func (l *loop) sweep(issues Issues, open []target.Issue) {
	ws := l.o.Workspaces(l.def)
	numbers, err := ws.Existing()
	if err != nil {
		l.rec.error(0, oneLine(err))
		return
	}
	isOpen := map[int]bool{}
	for _, i := range open {
		isOpen[i.Number] = true
	}
	for _, n := range numbers {
		if _, claimed := l.claims[n]; claimed || isOpen[n] {
			continue
		}
		issue, err := issues.Issue(n)
		if err != nil {
			l.rec.error(n, "workspace を掃除できない (読み直せない): "+oneLine(err))
			continue
		}
		if issue.Closed && l.remove(ws, n) {
			l.rec.human("掃除 %s: 終端の workspace を消した", target.Name(n))
		}
	}
}

// release は、失敗で終わった作業対象のうち、起動した trigger から外れたものを再び候補にする。
func (l *loop) release(candidates []trigger.Candidate) {
	for n, name := range l.held {
		matched := false
		for _, c := range candidates {
			if c.Issue.Number == n && c.Trigger.Name == name {
				matched = true
				break
			}
		}
		if !matched {
			delete(l.held, n)
		}
	}
}

func (l *loop) launch(def workflow.Definition, c trigger.Candidate) bool {
	n := c.Issue.Number
	sessionID, err := l.o.NewSessionID()
	if err != nil {
		l.rec.error(n, err.Error())
		return false
	}
	cl := &claim{issue: c.Issue, trigger: *c.Trigger, attempt: 1, sessionID: sessionID, workspaces: l.o.Workspaces(def), phase: phaseRunning}
	cl.run = l.o.Launch(def, worker.Job{Issue: c.Issue, Trigger: *c.Trigger, Attempt: cl.attempt, SessionID: sessionID, Prompt: def.Prompt}, l.events)
	l.claims[n] = cl
	return true
}

// handle は worker の起動か終わりを受ける。
func (l *loop) handle(ev worker.Event) {
	switch ev := ev.(type) {
	case worker.Started:
		c, ok := l.claimOf(ev.Number)
		if !ok {
			return
		}
		name := target.Name(ev.Number)
		l.rec.event("start", map[string]any{
			"target": name, "trigger": c.trigger.Name, "attempt": c.attempt,
			"session_id": c.sessionID, "workspace": ev.Workspace, "pid": ev.PID,
		}, "起動 %s (%s, attempt %d, session %s)", name, c.trigger.Name, c.attempt, c.sessionID)
	case worker.Ended:
		c, ok := l.claimOf(ev.Number)
		if !ok {
			return
		}
		result := ev.Result
		c.run = nil
		for _, message := range []string{result.AfterRunError, result.StopError} {
			if message != "" {
				l.rec.error(ev.Number, message)
			}
		}
		if result.Stopped {
			if c.stopReason == stoppedAtTerminal {
				l.remove(c.workspaces, ev.Number)
			}
			l.end(ev.Number, c, stopped, string(c.stopReason), result)
			return
		}
		c.phase, c.ended = phaseAwaitingVerification, &result
		l.verify(l.o.Open(l.def), ev.Number, c)
	}
}

// claimOf は作業対象の claim を返す。無ければ運用者に残す (claim の無い worker は起動しないので、起きれば loop の誤り)。
func (l *loop) claimOf(n int) (*claim, bool) {
	c, ok := l.claims[n]
	if !ok {
		l.rec.error(n, "claim の無い作業対象の worker の event を受けた")
	}
	return c, ok
}

// verify は終わった worker の作業対象を読み直し、終わり方を決めて claim を解く。読み直せなければ claim を持ったまま次の tick で
// 確かめ直す (完了とも失敗とも数えない)。作業対象が終端か trigger から外れていれば、worker 自身の失敗に関わらず完了とする。
func (l *loop) verify(issues Issues, n int, c *claim) {
	issue, err := issues.Issue(n)
	if err != nil {
		l.rec.error(n, "終わった worker の作業対象を読み直せない (次の tick で読み直す): "+oneLine(err))
		return
	}
	result := *c.ended
	switch {
	case issue.Closed:
		l.remove(c.workspaces, n)
		l.end(n, c, completed, withFailure(string(stoppedAtTerminal), result), result)
	case !c.trigger.When.Matches(issue):
		l.end(n, c, completed, withFailure(reasonLeftTrigger, result), result)
	case result.Failure != "":
		l.held[n] = c.trigger.Name
		l.end(n, c, failed, result.Failure, result)
	default:
		l.held[n] = c.trigger.Name
		l.end(n, c, failed, reasonStillMatches, result)
	}
}

// withFailure は完了の理由に、worker 自身の失敗を添える。
func withFailure(reason string, result worker.Result) string {
	if result.Failure == "" {
		return reason
	}
	return reason + " (worker は " + result.Failure + ")"
}

// remove は workspace を消す。消せなければ error の行を残し、次の tick の掃除で消し直す。
func (l *loop) remove(ws Workspaces, n int) bool {
	if err := ws.Remove(n); err != nil {
		l.rec.error(n, oneLine(err))
		return false
	}
	return true
}

func (l *loop) end(n int, c *claim, o outcome, reason string, result worker.Result) {
	name := target.Name(n)
	fields := map[string]any{
		"target": name, "trigger": c.trigger.Name, "attempt": c.attempt, "session_id": c.sessionID,
		"outcome": string(o), "reason": reason,
	}
	if result.ExitCode != nil {
		fields["exit_code"] = *result.ExitCode
	}
	delete(l.claims, n)
	l.rec.event("end", fields, "終了 %s (%s): %s — %s", name, c.trigger.Name, o, reason)
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
		names[i] = fmt.Sprintf("%s %s #%d", c.Trigger.Name, c.Trigger.On, c.Issue.Number)
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
