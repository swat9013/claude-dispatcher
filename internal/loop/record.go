package loop

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
)

// Line は人が読むログの 1 行 (formats.md §6)。端末の画面は種類で色を付け、活動の行をログから外す。
type Line struct {
	At   time.Time
	Kind LineKind
	// Label は行の頭の語 (画面は種類の色をここに付ける)、Rest はそれに続く部分 (区切りを含む)
	Label string
	Rest  string
}

// LineKind はログの行の種類。
type LineKind int

const (
	// LineNote は色を付けない行
	LineNote LineKind = iota
	LineTickOK
	LineStart
	LineCompleted
	LineFailed
	LineError
	// LineTickError は失敗した tick の行
	LineTickError
	// LineRetry は再起動の予定
	LineRetry
	// LineStopping は停止待ち
	LineStopping
	LineActivity
)

// Output は人が読む行の出し先。
type Output interface {
	Show(Line)
}

// Appender は stdout が端末でないときの出し先。RFC 3339 の UTC の時刻を前置して 1 行ずつ追記する。読み手の消えた pipe への
// 書き込みの失敗では止まらない。
type Appender struct {
	W io.Writer
}

func (a Appender) Show(l Line) {
	_, _ = fmt.Fprintf(a.W, "%s %s%s\n", l.At.UTC().Format(time.RFC3339), l.Label, l.Rest)
}

// recorder は loop のログを log.jsonl の 1 行 (formats.md §4) と、人が読む 1 行に書く。
type recorder struct {
	log    io.Writer
	output Output
	now    func() time.Time
	scope  string
}

// event は log.jsonl に 1 行書き、同じことを人が読む行で出す。log.jsonl に書けなければ、そのことを人が読む行に出して
// 続ける (loop は log を書けないことでは止まらない)。
func (r recorder) event(event string, fields map[string]any, kind LineKind, label, format string, a ...any) {
	r.write(event, fields)
	r.human(kind, label, format, a...)
}

// write は log.jsonl に 1 行だけ書き、人が読む行を出さない。書けなければ、そのことを人が読む行に出して続ける。
func (r recorder) write(event string, fields map[string]any) {
	fields["ts"] = r.now().UTC().Format(time.RFC3339)
	fields["scope"] = r.scope
	fields["event"] = event
	raw, err := json.Marshal(fields)
	if err == nil {
		_, err = fmt.Fprintf(r.log, "%s\n", raw)
	}
	if err != nil {
		r.human(LineError, "log.jsonl", " に %s の行を書けない: %v", event, err)
	}
}

func (r recorder) tickError(message string) {
	r.event("tick", map[string]any{"result": "error", "error": message}, LineTickError, "tick error", " · %s", message)
}

// error は処理を続けるが運用者が知るべき失敗を残す。ref がゼロ値なら作業対象を持たない。
func (r recorder) error(ref target.Ref, message string) {
	fields := map[string]any{"error": message}
	if ref == (target.Ref{}) {
		r.event("error", fields, LineError, "error", ": %s", message)
		return
	}
	fields["target"] = ref.String()
	r.event("error", fields, LineError, "error", " %s: %s", ref, message)
}

// human は人が読む 1 行を出す。行の頭の語 label に、format で組んだ残りが続く。
func (r recorder) human(kind LineKind, label, format string, a ...any) {
	r.output.Show(Line{At: r.now(), Kind: kind, Label: label, Rest: fmt.Sprintf(format, a...)})
}
