package worker

import (
	"strings"
	"testing"
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
		`{"type":42}`,
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

func TestALineLongerThan64KiBCountsAsProgressWhateverItsType(t *testing.T) {
	stream := streamFile(t, heartbeatPadded(t, progressLineLimit+1))

	found, _, err := scanProgress(stream.Name(), 0, progressLineLimit+1)

	if err != nil || !found {
		t.Fatalf("scanProgress = %v, %v; want 64 KiB を超える行は type を見ずに数える", found, err)
	}
}

func TestALineOfExactly64KiBIsJudgedByItsType(t *testing.T) {
	stream := streamFile(t, heartbeatPadded(t, progressLineLimit))

	found, _, err := scanProgress(stream.Name(), 0, progressLineLimit)

	if err != nil || found {
		t.Fatalf("scanProgress = %v, %v; want 改行を含めてちょうど 64 KiB の heartbeat は数えない", found, err)
	}
}

func TestScanProgressStopsBeforeTheUnfinishedLine(t *testing.T) {
	content := heartbeat + `{"type":"assistant"`
	stream := streamFile(t, content)

	found, end, err := scanProgress(stream.Name(), 0, int64(len(content)))

	if err != nil || found || end != int64(len(heartbeat)) {
		t.Fatalf("scanProgress = %v, %d, %v; want 完結していない行は見ずに heartbeat の終わり %d まで", found, end, err, len(heartbeat))
	}
}

func TestScanProgressDoesNotLookBeforeTheOffset(t *testing.T) {
	content := text("前の attempt") + heartbeat
	stream := streamFile(t, content)

	found, end, err := scanProgress(stream.Name(), int64(len(text("前の attempt"))), int64(len(content)))

	if err != nil || found || end != int64(len(content)) {
		t.Fatalf("scanProgress = %v, %d, %v; want offset より前の行は見ない", found, end, err)
	}
}
