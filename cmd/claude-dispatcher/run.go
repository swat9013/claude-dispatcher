package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/coord"
	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/printable"
	"github.com/swat9013/claude-dispatcher/internal/state"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/worker"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
	"github.com/swat9013/claude-dispatcher/internal/workspace"
)

// runOnce は単発起動 `run <trigger> <作業対象> [<workflow の path>]` を撃つ (formats.md §11)。loop を立てず、claim も
// 打ち切りも持たない。
func runOnce(args []string, stdout, stderr io.Writer) int {
	a, err := commandSpecOf("run").parse(args)
	if err != nil {
		return usageError(stderr, "%v", err)
	}
	triggerName, spelled := a.operands[0], a.operands[1]
	ref, ok := target.ParseRef(spelled)
	if !ok {
		return usageError(stderr, "作業対象は issue#<番号> か cl#<番号> と書く: %s", spelled)
	}
	abs, err := filepath.Abs(a.workflowPath)
	if err != nil {
		fmt.Fprintf(stderr, "workflow 定義の path を解決できない (%s): %v\n", a.workflowPath, err)
		return exitFailed
	}
	def, err := workflow.Load(abs, unresolvedEnvironment().getenv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, guidePointer)
		return exitUsage
	}
	e := definitionEnvironment(def)
	// 起動前の検査は loop の起動時と同じ (system.md §7 の単発起動)
	if problems := e.precheck(def); len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(stderr, p)
		}
		return exitUsage
	}
	if code := e.confirmLaunchable(def, stderr); code != 0 {
		return code
	}
	t, ok := namedTrigger(def, triggerName)
	if !ok {
		fmt.Fprintf(stderr, "trigger %s が workflow 定義に無い\n", triggerName)
		return exitUsage
	}
	if t.On != ref.Kind {
		fmt.Fprintf(stderr, "trigger %s は on: %s なので、%s を起動できない\n", t.Name, t.On, ref)
		return exitUsage
	}
	scopeKey, dir := e.stateDir(def)
	lock, code := lockState(state.LockRun, dir, scopeKey, stderr)
	if code != 0 {
		return code
	}
	defer lock.Close()
	// 停止要求は lock を取った直後から受ける。起動の前に受けたら起動しない
	signals := stopRequests()

	orphaned, err := worker.Orphaned(dir, ref)
	if err != nil {
		fmt.Fprintf(stderr, "%s の orphan を確かめられない: %v\n", ref, err)
		return exitFailed
	}
	if orphaned {
		fmt.Fprintf(stderr, "%s は前の loop の claude (orphan) がまだ動いているので起動しない\n", ref)
		return exitNotLaunched
	}
	store := e.places(def).Store()
	item, err := store.Read(ref)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return failureExit(err)
	}
	if observed := t.Observe(item); observed != trigger.ObservedStillMatches {
		fmt.Fprintf(stderr, "%s は %s に当たっていないので起動しない (%s)\n", ref, t.Name, observationText(observed))
		return exitNotLaunched
	}
	party, code := e.join(def, stderr)
	if code != 0 {
		return code
	}
	if party != nil {
		var claimSHA string
		// 起動しなかったときも、置いた分の着手宣言と生存報告を消す
		defer func() { leaveOnce(party, ref, claimSHA, stderr) }()
		if claimSHA, code = claimOnce(party, ref, stderr); code != 0 {
			return code
		}
		stop := keepReporting(party, coord.HeartbeatInterval(def.Interval), stderr)
		defer stop()
	}
	sessionID, err := worker.NewSessionID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailed
	}
	select {
	case sig := <-signals:
		fmt.Fprintf(stderr, "停止要求 %s: 起動しない\n", loop.SignalName(sig))
		return exitNotLaunched
	default:
	}
	surviveClosedStdout()
	events := make(chan worker.Event, 4)
	ws := e.workspaces(def, &sync.Mutex{})
	run := worker.Runner{Workspaces: ws, Definition: def, StateDir: dir}.
		Start(worker.Job{Item: item, Trigger: t, Attempt: 1, SessionID: sessionID, Prompt: def.Prompt}, events)
	o := once{store: store, workspaces: ws, stateDir: dir, trigger: t, ref: ref, stdout: stdout, stderr: stderr}
	return o.await(sessionID, run, events, signals)
}

// once は単発起動の 1 回分の、終わりを待って確かめるのに使うもの。
type once struct {
	store      loop.Store
	workspaces workspace.Manager
	stateDir   string
	trigger    trigger.Trigger
	ref        target.Ref
	stdout     io.Writer
	stderr     io.Writer
}

// await は単発起動の worker の終わりを待ち、exit code を返す (formats.md §11)。停止要求は 1 回目で worker の終わりを待ち、
// 2 回目で worker を止める。
func (o once) await(sessionID string, run *worker.Run, events <-chan worker.Event, signals <-chan os.Signal) int {
	stops, started := 0, false
	// exited は止められない claude の終わりで閉じる。nil なら止められない claude はいない
	var exited <-chan struct{}
	// unstoppedAlive は、止められない claude がまだ終わっていないか (loop の claudeAlive と同じ。system.md §7)
	unstoppedAlive := func() bool {
		if exited == nil {
			return false
		}
		select {
		case <-exited:
			return false
		default:
			return true
		}
	}
	unstoppedEnd := func() int {
		fmt.Fprintf(o.stdout, "終了: %s 未完了 (claude を止められない)\n", o.ref)
		return exitNotCompleted
	}
	for {
		select {
		case sig := <-signals:
			stops++
			switch {
			case unstoppedAlive():
				// 止められない claude の終わりは待たない
				fmt.Fprintf(o.stderr, "停止要求 %s: 止められない claude を残して終える\n", loop.SignalName(sig))
				return unstoppedEnd()
			case stops == 1 && started:
				fmt.Fprintf(o.stderr, "停止要求 %s: worker の終了を待つ\n", loop.SignalName(sig))
				continue
			}
			// 2 回目か、claude を起動する前 (hooks の間) の停止要求は worker を止める
			fmt.Fprintf(o.stderr, "停止要求 %s: worker を止める\n", loop.SignalName(sig))
			run.Stop()
		case ev := <-events:
			switch ev := ev.(type) {
			case worker.Started:
				started = true
				fmt.Fprintf(o.stdout, "起動: %s (%s, session %s, workspace %s, pid %d)\n", o.ref, o.trigger.Name, sessionID, ev.Workspace, ev.PID)
			case worker.Unstopped:
				fmt.Fprintln(o.stderr, printable.Line("claude を止められない: "+ev.StopError))
				exited = ev.Exited
				if stops >= 1 && unstoppedAlive() {
					return unstoppedEnd()
				}
			case worker.Ended:
				// 同じ周回に届いた停止要求を数えてから決める
				for drained := false; !drained; {
					select {
					case <-signals:
						stops++
					default:
						drained = true
					}
				}
				return o.ended(ev.Result, started, stops >= 2)
			}
		}
	}
}

// ended は終わった worker の作業対象を読み直し、完了したかを exit code で返す。claude を起動できなかった (Started が
// 届かなかった) なら起動しなかったとし、2 回目の停止要求の後なら読み直さずに完了しなかったとする。
func (o once) ended(result worker.Result, started, stopped bool) int {
	for _, message := range []string{result.Failure, result.AfterRunError, result.StopError, result.ResultSummaryError} {
		if message != "" {
			fmt.Fprintln(o.stderr, printable.Line(message))
		}
	}
	switch {
	case !started:
		fmt.Fprintf(o.stdout, "終了: %s 起動できなかった\n", o.ref)
		return exitNotLaunched
	case stopped || result.Stopped:
		fmt.Fprintf(o.stdout, "終了: %s 未完了 (停止要求)\n", o.ref)
		return exitNotCompleted
	}
	item, err := o.store.Read(o.ref)
	if err != nil {
		fmt.Fprintf(o.stderr, "終わった worker の作業対象 %s を読み直せない: %v\n", o.ref, err)
		return exitFailed
	}
	observed := o.trigger.Observe(item)
	switch observed {
	case trigger.ObservedLeftTrigger, trigger.ObservedTerminal:
		// workspace は loop と同じく、終端なら before_remove を撃って消す
		if observed == trigger.ObservedTerminal {
			// fd を受け継いだ claude の子孫が残っていれば、loop の掃除と同じく消さない (system.md §7 の orphan)
			if orphaned, err := worker.Orphaned(o.stateDir, o.ref); err != nil || orphaned {
				fmt.Fprintf(o.stderr, "%s の workspace は claude の子孫が使っているかもしれないので消さない\n", o.ref)
			} else if err := o.workspaces.Remove(o.ref); err != nil {
				fmt.Fprintln(o.stderr, printable.Line(err.Error()))
			}
		}
		fmt.Fprintf(o.stdout, "終了: %s 完了 (%s)\n", o.ref, observationText(observed))
		return 0
	}
	fmt.Fprintf(o.stdout, "終了: %s 未完了 (%s)\n", o.ref, observationText(observed))
	return exitNotCompleted
}

// claimOnce は、単発起動の作業対象の着手宣言を置き、置いた sha を返す (formats.md §11)。ref を 1 回しか読まないので死んだ loop
// を判定せず、他の loop の着手宣言があれば起動しない。起動しないなら stderr に理由を出して exit code を返す。
func claimOnce(party *coord.Party, ref target.Ref, stderr io.Writer) (string, int) {
	view, err := party.Observe()
	if err != nil {
		fmt.Fprintln(stderr, printable.Line(err.Error()))
		return "", exitFailed
	}
	if view.Unread != nil {
		fmt.Fprintln(stderr, printable.Line(view.Unread.Error()))
	}
	for _, m := range view.Messages {
		if m.Newer() {
			fmt.Fprintln(stderr, "調停 remote に新しい版の ref があるので起動しない (dispatcher を上げる)")
			return "", exitNotLaunched
		}
	}
	if sha, held := view.Claims[ref]; held {
		fmt.Fprintf(stderr, "%s は loop %s が着手しているので起動しない\n", ref, view.Messages[sha].Loop)
		return "", exitNotLaunched
	}
	sha, err := party.Claim(ref, coord.Absent())
	switch {
	case errors.Is(err, coord.ErrLost):
		fmt.Fprintf(stderr, "%s は他の loop が先に着手したので起動しない\n", ref)
		return "", exitNotLaunched
	case err != nil:
		fmt.Fprintf(stderr, "%s の着手宣言を置けない: %s\n", ref, printable.Line(err.Error()))
		return "", exitFailed
	}
	return sha, 0
}

// keepReporting は、単発起動が終わるまで生存報告を間隔ごとに置き直す。返した関数で止める (止まるまで待つ)。
func keepReporting(party *coord.Party, interval time.Duration, stderr io.Writer) func() {
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := party.Heartbeat(interval); err != nil {
					fmt.Fprintf(stderr, "生存報告を置けない: %s\n", printable.Line(err.Error()))
				}
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

// leaveOnce は、単発起動が置いた着手宣言 (置けていれば) と生存報告を消す。消せなければ stderr に出す。
func leaveOnce(party *coord.Party, ref target.Ref, claimSHA string, stderr io.Writer) {
	if claimSHA != "" {
		if err := party.Delete(party.ClaimRef(ref), claimSHA); err != nil {
			fmt.Fprintf(stderr, "%s の着手宣言を消せない: %s\n", ref, printable.Line(err.Error()))
		}
	}
	if err := party.Leave(); err != nil {
		fmt.Fprintf(stderr, "生存報告を消せない: %s\n", printable.Line(err.Error()))
	}
}

// namedTrigger は workflow 定義の trigger を名前で引く。
func namedTrigger(def workflow.Definition, name string) (trigger.Trigger, bool) {
	for _, t := range def.Triggers {
		if t.Name == name {
			return t, true
		}
	}
	return trigger.Trigger{}, false
}

// observationText は観測の画面の文 (formats.md §4 の reason の画面の文と同じ綴り)。
func observationText(o trigger.Observation) string {
	switch o {
	case trigger.ObservedStillMatches:
		return "trigger に当たったまま"
	case trigger.ObservedTerminal:
		return "終端"
	case trigger.ObservedLeftTrigger:
		return "trigger から外れた"
	case trigger.ObservedUndecided:
		return "当たるかをまだ決められない"
	}
	panic(fmt.Sprintf("trigger.Observation の値 %d を写せない", o))
}
