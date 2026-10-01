// Package loop は tick を周期ごとに回す (system.md §9、formats.md §6)。tick ごとに workflow 定義を読み直し、
// snapshot を作って trigger を評価する。worker の起動はまだ持たない (#78)。
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
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// Issues は issue 置き場の部品 (system.md §13)。本番は gh の adapter、テストは in-memory。
type Issues interface {
	ScopeKey() string
	// OpenIssues の失敗は *target.Failure
	OpenIssues() ([]target.Issue, error)
}

// Options は loop の入力。
type Options struct {
	// Load は workflow 定義を読み直す
	Load func() (workflow.Definition, error)
	// Open は workflow 定義から issue 置き場の部品を組み立てる
	Open func(workflow.Definition) Issues
	// Interval は起動時に検査に通った workflow 定義の周期。読み直しが検査に落ちている間はこれか、最後に通った版の周期を使う
	Interval time.Duration
	// ScopeKey は起動時の scope key (lock を取った鍵)
	ScopeKey string
	Stdout   io.Writer
	Signals  <-chan os.Signal
	Now      func() time.Time
	// After は d 後に届く channel を返す (time.After。テストは差し替える)
	After func(d time.Duration) <-chan time.Time
}

// Run は停止要求が届くまで tick を回す。起動直後に 1 回撃ち、以後は tick の終了から周期だけ待つ。
func Run(o Options) int {
	interval := o.Interval
	for {
		if next, ok := o.tick(); ok {
			interval = next
		}
		// tick の間に届いた停止要求は、tick を終えてから受ける
		select {
		case sig := <-o.Signals:
			o.stop(sig)
			return 0
		default:
		}
		select {
		case sig := <-o.Signals:
			o.stop(sig)
			return 0
		case <-o.After(interval):
		}
	}
}

// tick は 1 周期分の仕事をして行を出す。workflow 定義が検査に通ったら、その版の周期と true を返す。
func (o Options) tick() (time.Duration, bool) {
	def, err := o.Load()
	if err != nil {
		o.line("tick error · workflow 定義の誤り: %s", oneLine(err))
		return 0, false
	}
	issues := o.Open(def)
	if key := issues.ScopeKey(); key != o.ScopeKey {
		o.line("tick error · workflow 定義の scope key %s が起動時の %s と違う (loop を起動し直す)", key, o.ScopeKey)
		return def.Interval, true
	}
	open, err := issues.OpenIssues()
	if err != nil {
		o.line("tick error · %s", oneLine(err))
		return def.Interval, true
	}
	o.line("tick ok · %s", Summary(trigger.Evaluate(def.Triggers, open)))
	return def.Interval, true
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

func (o Options) stop(sig os.Signal) {
	o.line("loop を止めた (停止要求 %s)", SignalName(sig))
}

// line は時刻を前置した 1 行を出す。読み手の消えた pipe への書き込みの失敗では止まらない。
func (o Options) line(format string, a ...any) {
	_, _ = fmt.Fprintf(o.Stdout, "%s %s\n", o.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
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
