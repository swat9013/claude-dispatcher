package loop

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/coord"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

// coord.Board が返した結果から、log.jsonl の行 (formats.md §4) と人が読む行を書く写し方。Board の判定は coord の表の test が、
// Board を繋いだ loop の通しは coordination_test.go が確かめる。ここは結果の値 1 つずつの、行の綴りを確かめる。

func TestOutcomesAreWrittenAsLogLines(t *testing.T) {
	issue42 := target.Ref{Kind: target.KindIssue, Number: 42}
	errPush := errors.New("push が落ちた")
	cases := []struct {
		name    string
		outcome coord.Outcome
		// log は log.jsonl に書く行の field (ts と scope を除く)。human は人が読む行
		log   map[string]any
		human string
	}{
		{"commit message を読めなかった", coord.Unread{Err: errPush},
			map[string]any{"event": "error", "error": "push が落ちた"}, "error: push が落ちた"},
		{"持ち主を読めない着手宣言", coord.OwnerUnreadable{Ref: issue42, Name: "claims/issue-42"},
			map[string]any{"event": "error", "target": "issue#42", "error": "持ち主の loop を読めない着手宣言がある (この作業対象は起動しない): claims/issue-42"},
			"error issue#42: 持ち主の loop を読めない着手宣言がある (この作業対象は起動しない): claims/issue-42"},
		{"間隔を読めない生存報告", coord.IntervalUnreadable{Loop: "odd-00000004", Name: "loops/odd-00000004"},
			map[string]any{"event": "error", "error": "間隔を読めない生存報告がある (loop odd-00000004 の死を判定しない): loops/odd-00000004"},
			"error: 間隔を読めない生存報告がある (loop odd-00000004 の死を判定しない): loops/odd-00000004"},
		{"引き継いだ着手宣言が置き換わった", coord.InheritReplaced{Ref: issue42},
			map[string]any{"event": "error", "target": "issue#42", "error": "orphan のために引き継いだ着手宣言が置き換わった (orphan の claude と二重に動きうる)"},
			"error issue#42: orphan のために引き継いだ着手宣言が置き換わった (orphan の claude と二重に動きうる)"},
		{"消し損ねた着手宣言を消せない", coord.StaleDeleteFailed{Ref: issue42, Err: errPush},
			map[string]any{"event": "error", "target": "issue#42", "error": "消し損ねた自分の着手宣言を消せない: push が落ちた"},
			"error issue#42: 消し損ねた自分の着手宣言を消せない: push が落ちた"},
		{"着手宣言を引き継いだ", coord.Inherited{Ref: issue42, From: "dead-00000002"},
			map[string]any{"event": "inherit", "target": "issue#42", "from": "dead-00000002"},
			"引き継ぎ issue#42: orphan の claude が動いているので、死んだ loop dead-00000002 の着手宣言を引き継いだ"},
		{"着手宣言を引き継げない", coord.InheritFailed{Ref: issue42, Err: errPush},
			map[string]any{"event": "error", "target": "issue#42", "error": "orphan の作業対象の着手宣言を引き継げない (次の tick で試み直す): push が落ちた"},
			"error issue#42: orphan の作業対象の着手宣言を引き継げない (次の tick で試み直す): push が落ちた"},
		{"死んだ loop の着手宣言を消せない", coord.DeadRefDeleteFailed{Ref: issue42, Loop: "dead-00000002", Err: errPush},
			map[string]any{"event": "error", "target": "issue#42", "error": "死んだ loop dead-00000002 の ref を消せない: push が落ちた"},
			"error issue#42: 死んだ loop dead-00000002 の ref を消せない: push が落ちた"},
		{"死んだ loop の生存報告を消せない", coord.DeadRefDeleteFailed{Loop: "dead-00000002", Err: errPush},
			map[string]any{"event": "error", "error": "死んだ loop dead-00000002 の ref を消せない: push が落ちた"},
			"error: 死んだ loop dead-00000002 の ref を消せない: push が落ちた"},
		{"引き継いだ着手宣言を消せない", coord.InheritedReleaseFailed{Ref: issue42, Err: errPush},
			map[string]any{"event": "error", "target": "issue#42", "error": "引き継いだ着手宣言を消せない (次の tick で消し直す): push が落ちた"},
			"error issue#42: 引き継いだ着手宣言を消せない (次の tick で消し直す): push が落ちた"},
		{"解いた claim の着手宣言を消せない", coord.ClaimReleaseFailed{Ref: issue42, Err: errPush},
			map[string]any{"event": "error", "target": "issue#42", "error": "着手宣言を消せない (次の tick で消し直す): push が落ちた"},
			"error issue#42: 着手宣言を消せない (次の tick で消し直す): push が落ちた"},
		{"orphan が生きている引き継いだ着手宣言を残して止まる", coord.InheritKept{Ref: issue42, OrphanAlive: true},
			map[string]any{"event": "error", "target": "issue#42", "error": "orphan のために引き継いだ着手宣言を残して止まる (orphan の claude が作業を続けうる)"},
			"error issue#42: orphan のために引き継いだ着手宣言を残して止まる (orphan の claude が作業を続けうる)"},
		{"orphan は終わったが消せなかった引き継いだ着手宣言を残して止まる", coord.InheritKept{Ref: issue42},
			map[string]any{"event": "error", "target": "issue#42", "error": "orphan のために引き継いだ着手宣言を残して止まる (orphan は終わったが消せなかった)"},
			"error issue#42: orphan のために引き継いだ着手宣言を残して止まる (orphan は終わったが消せなかった)"},
		{"引き継ぎの結果が分からないまま止まる", coord.UnsureInheritKept{Ref: issue42},
			map[string]any{"event": "error", "target": "issue#42", "error": "orphan のために引き継いだかもしれない着手宣言を残して止まる (引き継ぎの結果が分からず、orphan の claude が作業を続けうる)"},
			"error issue#42: orphan のために引き継いだかもしれない着手宣言を残して止まる (引き継ぎの結果が分からず、orphan の claude が作業を続けうる)"},
		{"止まるときに生存報告を消せない", coord.LeaveFailed{Err: errPush},
			map[string]any{"event": "error", "error": "生存報告を消せない: push が落ちた"}, "error: 生存報告を消せない: push が落ちた"},
		{"生存報告を置けない", coord.BeatFailed{Err: errPush},
			map[string]any{"event": "error", "error": "生存報告を置けない: push が落ちた"}, "error: 生存報告を置けない: push が落ちた"},
		{"生存報告の置き直しの結果を捨てた", coord.BeatsDropped{N: 3},
			map[string]any{"event": "error", "error": "生存報告の置き直しの結果 3 件を読まずに捨てた"}, "error: 生存報告の置き直しの結果 3 件を読まずに捨てた"},
		{"死んだ loop の生存報告と着手宣言を消した", coord.DeadLoopCleaned{Loop: "dead-00000002", Refs: []string{"claims/issue-7", "loops/dead-00000002"}, Heartbeat: true, Claims: 1},
			map[string]any{"event": "dead_loop", "loop": "dead-00000002", "refs": []any{"claims/issue-7", "loops/dead-00000002"}},
			"死んだ loop dead-00000002: 生存報告と着手宣言 1 本を消した"},
		{"死んだ loop の生存報告だけを消した", coord.DeadLoopCleaned{Loop: "dead-00000002", Refs: []string{"loops/dead-00000002"}, Heartbeat: true},
			map[string]any{"event": "dead_loop", "loop": "dead-00000002", "refs": []any{"loops/dead-00000002"}},
			"死んだ loop dead-00000002: 生存報告を消した"},
		{"死んだ loop の着手宣言だけを消した", coord.DeadLoopCleaned{Loop: "dead-00000002", Refs: []string{"claims/issue-7"}, Claims: 1},
			map[string]any{"event": "dead_loop", "loop": "dead-00000002", "refs": []any{"claims/issue-7"}},
			"死んだ loop dead-00000002: 着手宣言 1 本を消した"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var log bytes.Buffer
			var out lines
			now := func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
			l := &loop{rec: recorder{log: &log, output: &out, now: now, scope: "scope-a"}}

			l.report([]coord.Outcome{c.outcome})

			var got map[string]any
			if err := json.Unmarshal(log.Bytes(), &got); err != nil {
				t.Fatalf("log.jsonl の行 %q: %v", log.String(), err)
			}
			delete(got, "ts")
			delete(got, "scope")
			if !reflect.DeepEqual(got, c.log) {
				t.Errorf("log.jsonl の行 = %v, want %v", got, c.log)
			}
			if len(out) != 1 || out[0].Label+out[0].Rest != c.human {
				t.Errorf("人が読む行 = %+v, want %q", out, c.human)
			}
		})
	}
}
