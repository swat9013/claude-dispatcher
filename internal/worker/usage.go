package worker

import (
	"encoding/json"
	"time"
)

// UsageWindow は usage limit の window 1 つの観測。
type UsageWindow struct {
	// Utilization は使用率。上限で 1 になる小数
	Utilization float64
	ResetsAt    time.Time
}

// UsageObserved は stream の rate_limit_event の 1 行から読んだ使用率 (formats.md §6 の admission control)。読めなかった
// window は nil。
type UsageObserved struct {
	FiveHour, SevenDay *UsageWindow
}

func (UsageObserved) isEvent() {}

// Unreadable は、どの window も読めなかった行か。
func (u UsageObserved) Unreadable() bool { return u.FiveHour == nil && u.SevenDay == nil }

// parseUsage は stream-json の 1 行が rate_limit_event なら、その使用率を返す。window の形が崩れていても行全体を捨てないよう、
// 型を決めずに読む。
func parseUsage(line []byte) (UsageObserved, bool) {
	var l struct {
		Type any `json:"type"`
		Info any `json:"rate_limit_info"`
	}
	if err := json.Unmarshal(line, &l); err != nil || l.Type != "rate_limit_event" {
		return UsageObserved{}, false
	}
	info, _ := l.Info.(map[string]any)
	windows, _ := info["unifiedWindows"].(map[string]any)
	return UsageObserved{FiveHour: usageWindow(windows["five_hour"]), SevenDay: usageWindow(windows["seven_day"])}, true
}

// usageWindow は window の項目を読む。utilization か resetsAt が数でなければ nil。
func usageWindow(v any) *UsageWindow {
	window, _ := v.(map[string]any)
	utilization, ok := window["utilization"].(float64)
	if !ok {
		return nil
	}
	resetsAt, ok := window["resetsAt"].(float64)
	if !ok {
		return nil
	}
	return &UsageWindow{Utilization: utilization, ResetsAt: time.Unix(int64(resetsAt), 0)}
}
