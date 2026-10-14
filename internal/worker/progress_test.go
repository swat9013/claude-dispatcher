package worker

import (
	"strings"
	"testing"
	"time"
)

// progress の表 (formats.md §6 の progress)。行ごとに stall の時計を戻すかを決める。

func TestLinesThatCountAsProgress(t *testing.T) {
	for _, line := range []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"調べる"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"result","subtype":"success"}`,
		`{"type":"system","subtype":"init"}`,
		`{"type":"system","subtype":"task_started"}`,
		`{"type":"system","subtype":"task_progress"}`,
		`{"type":"system","subtype":"task_notification"}`,
		`{"type":"system","subtype":"api_retry"}`,
		`{"type":"system","subtype":"compact_boundary"}`,
		`{"type":"system","subtype":"thinking_tokens"}`,
		`{"type":"system","subtype":"permission_check_status"}`,
		`{"type":"stream_event"}`,
		`{"type":42}`,
	} {
		if !isProgress([]byte(line)) {
			t.Errorf("isProgress(%s) = false, want 数える", line)
		}
	}
}

func TestLinesThatDoNotCountAsProgress(t *testing.T) {
	for _, line := range []string{
		`{"type":"tool_progress","tool_name":"Bash","elapsed_time_seconds":30,"heartbeat":true}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning"}}`,
		`{"type":"assist`,
		`plain text`,
		``,
		`[1]`,
		`{"message":{}}`,
		`{"type":""}`,
		`{"type":null}`,
	} {
		if isProgress([]byte(line)) {
			t.Errorf("isProgress(%q) = true, want 数えない", line)
		}
	}
}

// heartbeatPadded は改行を含めて size byte の heartbeat の行。
func heartbeatPadded(t *testing.T, size int) string {
	t.Helper()
	line := `{"type":"tool_progress","pad":"` + `"}` + "\n"
	return line[:len(line)-3] + strings.Repeat("x", size-len(line)) + line[len(line)-3:]
}

// progressed は content を書いた stream を offset から見始め、1 回確かめて stall の時計が戻ったか。
func progressed(t *testing.T, content string, offset int64) bool {
	t.Helper()
	started := time.Now()
	w := newStreamWatch(streamFile(t, content).Name(), offset, started, "/work")
	w.check(started.Add(10 * time.Second))
	return w.sinceProgress(started.Add(10*time.Second)) == 0
}

func TestALineLongerThan64KiBCountsAsProgressWhateverItsType(t *testing.T) {
	if !progressed(t, heartbeatPadded(t, progressLineLimit+1), 0) {
		t.Fatal("時計が戻らない; want 64 KiB を超える行は type を見ずに数える")
	}
}

func TestALineOfExactly64KiBIsJudgedByItsType(t *testing.T) {
	if progressed(t, heartbeatPadded(t, progressLineLimit), 0) {
		t.Fatal("時計が戻った; want 改行を含めてちょうど 64 KiB の heartbeat は数えない")
	}
}

func TestProgressSkipsTheUnfinishedLine(t *testing.T) {
	if progressed(t, heartbeat+`{"type":"assistant"`, 0) {
		t.Fatal("時計が戻った; want 完結していない行は見ない")
	}
}

func TestProgressDoesNotLookBeforeTheOffset(t *testing.T) {
	if progressed(t, text("前の attempt")+heartbeat, int64(len(text("前の attempt")))) {
		t.Fatal("時計が戻った; want offset より前の行は見ない")
	}
}
