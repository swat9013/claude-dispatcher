// Package loop は tick を周期ごとに回し、trigger に当たった作業対象へ worker を起動して、終わり方を確かめる
// (system.md §7 / §9、formats.md §4 / §6)。claim は loop の memory だけに持ち、tracker には書かない。
package loop

import (
	"encoding/json"
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

// claim は loop が worker を起動中か、終わり方を確かめ待ちの作業対象。
type claim struct {
	issue     target.Issue
	trigger   trigger.Trigger
	attempt   int
	sessionID string
	// run は走っている worker。終わって確かめ待ちなら nil
	run Worker
	// stopReason は loop が worker を止めた理由。止めていなければ ""
	stopReason string
	// ended は終わった worker の終わり方。作業対象を読み直せず、確かめ待ちのときだけ持つ
	ended *worker.Result
}

type loop struct {
	o      Options
	def    workflow.Definition
	claims map[int]*claim
	events chan worker.Event
	// stopping は受けた停止要求の数
	stopping int
	lastStop os.Signal
}

// Run は停止要求で止まるまで tick を回す。起動時に終端の workspace を掃除し、直後に 1 回 tick を撃ち、以後は tick の終了から
// 周期だけ待つ。
func Run(o Options) int {
	l := &loop{o: o, def: o.Definition, claims: map[int]*claim{}, events: make(chan worker.Event)}
	l.cleanUp()
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
		if l.stopping == 0 && next == nil {
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
		l.human("停止待ち: 走っている worker %d 本の終了を待つ (もう一度で止める)", running)
		return
	}
	for _, c := range l.claims {
		if c.run != nil && c.stopReason == "" {
			c.stopReason = "停止要求"
			c.run.Stop()
		}
	}
}

func (l *loop) exit() int {
	for n, c := range l.claims {
		l.logError(n, fmt.Sprintf("終わり方を確かめられないまま止まる (%s)", c.trigger.Name))
	}
	l.human("loop を止めた (停止要求 %s)", SignalName(l.lastStop))
	return 0
}

// running は走っている worker の数。
func (l *loop) running() int {
	n := 0
	for _, c := range l.claims {
		if c.run != nil {
			n++
		}
	}
	return n
}

// cleanUp は、終端になった作業対象の workspace を消す (system.md §7)。
func (l *loop) cleanUp() {
	ws := l.o.Workspaces(l.def)
	numbers, err := ws.Existing()
	if err != nil {
		l.logError(0, err.Error())
		return
	}
	issues := l.o.Open(l.def)
	for _, n := range numbers {
		issue, err := issues.Issue(n)
		if err != nil {
			l.logError(n, fmt.Sprintf("workspace を掃除できない (読み直せない): %s", oneLine(err)))
			continue
		}
		if issue.Closed {
			if err := ws.Remove(n); err != nil {
				l.logError(n, oneLine(err))
				continue
			}
			l.human("掃除 issue#%d: 終端の workspace を消した", n)
		}
	}
}

// tick は 1 周期分の仕事をする (formats.md §6 の tick の手順)。
func (l *loop) tick() {
	issues := l.o.Open(l.def)
	l.reconcile(issues)
	l.recheck(issues)
	def, err := l.o.Load()
	if err != nil {
		l.tickError("workflow 定義の誤り: " + oneLine(err))
		return
	}
	l.def = def
	issues = l.o.Open(def)
	if key := issues.ScopeKey(); key != l.o.ScopeKey {
		l.tickError(fmt.Sprintf("workflow 定義の scope key %s が起動時の %s と違う (loop を起動し直す)", key, l.o.ScopeKey))
		return
	}
	open, err := issues.OpenIssues()
	if err != nil {
		l.tickError(oneLine(err))
		return
	}
	candidates := trigger.Evaluate(def.Triggers, open)
	var launched []string
	for _, c := range candidates {
		if len(l.claims) >= def.MaxConcurrent {
			break
		}
		if _, claimed := l.claims[c.Issue.Number]; claimed {
			continue
		}
		if l.launch(def, c) {
			launched = append(launched, targetName(c.Issue.Number))
		}
	}
	l.write("tick", map[string]any{"result": "ok", "candidates": len(candidates), "launched": nonNil(launched)},
		"tick ok · %s", Summary(candidates))
}

// reconcile は走っている worker の作業対象を読み直し、終端になっていれば worker を止める。trigger から外れただけでは止めない。
func (l *loop) reconcile(issues Issues) {
	for n, c := range l.claims {
		if c.run == nil || c.stopReason != "" {
			continue
		}
		issue, err := issues.Issue(n)
		if err != nil {
			l.logError(n, "走っている worker の作業対象を読み直せない: "+oneLine(err))
			continue
		}
		if issue.Closed {
			c.stopReason = "終端"
			c.run.Stop()
		}
	}
}

// recheck は、終わったが作業対象を読み直せなかった worker を確かめ直す。
func (l *loop) recheck(issues Issues) {
	for n, c := range l.claims {
		if c.run == nil && c.ended != nil {
			l.verify(issues, n, c, *c.ended)
		}
	}
}

func (l *loop) launch(def workflow.Definition, c trigger.Candidate) bool {
	n := c.Issue.Number
	sessionID, err := l.o.NewSessionID()
	if err != nil {
		l.logError(n, err.Error())
		return false
	}
	cl := &claim{issue: c.Issue, trigger: *c.Trigger, attempt: 1, sessionID: sessionID}
	cl.run = l.o.Launch(def, worker.Job{Issue: c.Issue, Trigger: *c.Trigger, Attempt: cl.attempt, SessionID: sessionID, Prompt: def.Prompt}, l.events)
	l.claims[n] = cl
	return true
}

// handle は worker の起動か終わりを受ける。
func (l *loop) handle(ev worker.Event) {
	c, ok := l.claims[ev.Number]
	if !ok {
		return
	}
	if ev.Started != nil {
		l.write("start", map[string]any{
			"target": targetName(ev.Number), "trigger": c.trigger.Name, "attempt": c.attempt,
			"session_id": c.sessionID, "workspace": ev.Started.Workspace, "pid": ev.Started.PID,
		}, "起動 %s (%s, attempt %d, session %s)", targetName(ev.Number), c.trigger.Name, c.attempt, c.sessionID)
		return
	}
	result := *ev.Ended
	c.run = nil
	if result.AfterRunError != "" {
		l.logError(ev.Number, result.AfterRunError)
	}
	if result.Stopped {
		if c.stopReason == "終端" {
			l.removeWorkspace(ev.Number)
		}
		l.end(ev.Number, c, "stopped", c.stopReason, result)
		return
	}
	l.verify(l.o.Open(l.def), ev.Number, c, result)
}

// verify は終わった worker の作業対象を読み直し、終わり方を決めて claim を解く。読み直せなければ claim を持ったまま次の tick で
// 確かめ直す (完了とも失敗とも数えない)。
func (l *loop) verify(issues Issues, n int, c *claim, result worker.Result) {
	issue, err := issues.Issue(n)
	if err != nil {
		c.ended = &result
		l.logError(n, "終わった worker の作業対象を読み直せない (次の tick で読み直す): "+oneLine(err))
		return
	}
	outcome, reason := "completed", "trigger から外れた"
	switch {
	case issue.Closed:
		l.removeWorkspace(n)
		reason = "終端"
		if result.Failure != "" {
			outcome, reason = "failed", result.Failure
		}
	case result.Failure != "":
		outcome, reason = "failed", result.Failure
	case c.trigger.When.Matches(issue):
		outcome, reason = "failed", "trigger に当たったまま"
	}
	l.end(n, c, outcome, reason, result)
}

func (l *loop) removeWorkspace(n int) {
	if err := l.o.Workspaces(l.def).Remove(n); err != nil {
		l.logError(n, oneLine(err))
	}
}

func (l *loop) end(n int, c *claim, outcome, reason string, result worker.Result) {
	fields := map[string]any{
		"target": targetName(n), "trigger": c.trigger.Name, "attempt": c.attempt, "session_id": c.sessionID,
		"outcome": outcome, "reason": reason,
	}
	if result.ExitCode != nil {
		fields["exit_code"] = *result.ExitCode
	}
	delete(l.claims, n)
	l.write("end", fields, "終了 %s (%s): %s — %s", targetName(n), c.trigger.Name, outcome, reason)
}

func (l *loop) tickError(message string) {
	l.write("tick", map[string]any{"result": "error", "error": message}, "tick error · %s", message)
}

// logError は処理を続けるが運用者が知るべき失敗を残す。n が 0 なら作業対象を持たない。
func (l *loop) logError(n int, message string) {
	fields := map[string]any{"error": message}
	if n == 0 {
		l.write("error", fields, "error: %s", message)
		return
	}
	fields["target"] = targetName(n)
	l.write("error", fields, "error %s: %s", targetName(n), message)
}

// write は log.jsonl に 1 行書き、同じ事象を人が読む行で stdout に出す。書き込みの失敗では止まらない。
func (l *loop) write(event string, fields map[string]any, format string, a ...any) {
	fields["ts"] = l.o.Now().UTC().Format(time.RFC3339)
	fields["scope"] = l.o.ScopeKey
	fields["event"] = event
	if raw, err := json.Marshal(fields); err == nil {
		_, _ = fmt.Fprintf(l.o.Log, "%s\n", raw)
	}
	l.human(format, a...)
}

// human は時刻を前置した 1 行を stdout に出す。読み手の消えた pipe への書き込みの失敗では止まらない。
func (l *loop) human(format string, a ...any) {
	_, _ = fmt.Fprintf(l.o.Stdout, "%s %s\n", l.o.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
}

func targetName(n int) string { return fmt.Sprintf("issue#%d", n) }

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
