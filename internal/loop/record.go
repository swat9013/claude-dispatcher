package loop

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
)

// recorder は loop の事象を log.jsonl の 1 行 (formats.md §4) と、人が読む stdout の 1 行に書く。
type recorder struct {
	log    io.Writer
	stdout io.Writer
	now    func() time.Time
	scope  string
}

// event は log.jsonl に 1 行書き、同じ事象を人が読む行で stdout に出す。log.jsonl に書けなければ、そのことを stdout に出して
// 続ける (loop は log を書けないことでは止まらない)。
func (r recorder) event(event string, fields map[string]any, format string, a ...any) {
	fields["ts"] = r.now().UTC().Format(time.RFC3339)
	fields["scope"] = r.scope
	fields["event"] = event
	raw, err := json.Marshal(fields)
	if err == nil {
		_, err = fmt.Fprintf(r.log, "%s\n", raw)
	}
	if err != nil {
		r.human("log.jsonl に %s の行を書けない: %v", event, err)
	}
	r.human(format, a...)
}

func (r recorder) tickError(message string) {
	r.event("tick", map[string]any{"result": "error", "error": message}, "tick error · %s", message)
}

// error は処理を続けるが運用者が知るべき失敗を残す。n が 0 なら作業対象を持たない。
func (r recorder) error(n int, message string) {
	fields := map[string]any{"error": message}
	if n == 0 {
		r.event("error", fields, "error: %s", message)
		return
	}
	fields["target"] = target.Name(n)
	r.event("error", fields, "error %s: %s", target.Name(n), message)
}

// human は時刻を前置した 1 行を stdout に出す。読み手の消えた pipe への書き込みの失敗では止まらない。
func (r recorder) human(format string, a ...any) {
	_, _ = fmt.Fprintf(r.stdout, "%s %s\n", r.now().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
}
