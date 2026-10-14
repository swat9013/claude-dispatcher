package worker

import (
	"encoding/json"
)

// progressLineLimit は、改行を含めてこれを超える行を type を見ずに progress と数える大きさ。1 行は頭のこの大きさまでだけを
// 持って判定する (formats.md §6 の progress)。活動の行の上限 (activityLineLimit) とは別に決める
const progressLineLimit = 64 << 10

// isProgress は stream-json の 1 行 (progressLineLimit 以下) が progress か (formats.md §6 の progress の表)。止まっていても
// 書かれる行 (heartbeat・rate limit の通知) と、中身を判定できない行だけを数えない。
func isProgress(line []byte) bool {
	// type は文字列に限らない。文字列でない type も「知らない type」として数える
	var l struct {
		Type any `json:"type"`
	}
	if err := json.Unmarshal(line, &l); err != nil {
		return false
	}
	switch l.Type {
	case nil, "", "tool_progress", "rate_limit_event":
		return false
	}
	return true
}

// countsAsProgress は完結した 1 行が progress か。head は行の頭 (改行を含めて progressLineLimit 以上を持つ)、length は改行を
// 含めた行の長さ。
func countsAsProgress(head []byte, length int64) bool {
	return length > progressLineLimit || isProgress(head)
}
