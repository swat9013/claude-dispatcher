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

// maxResetsAt は読める resetsAt の上限 (9999-12-31T23:59:59Z)。これより先の時刻は状態 file に書けない。単位がミリ秒に変わった
// 値もここで弾く
const maxResetsAt = 253402300799

// parseUsage は stream-json の 1 行が rate_limit_event なら、その使用率を返す。行の種類を決めるまでは rate_limit_info を読まず
// (ほかの行を decode しない)、決めた後は形が崩れていても読めない event として返す。
func parseUsage(line []byte) (UsageObserved, bool) {
	var l struct {
		Type any             `json:"type"`
		Info json.RawMessage `json:"rate_limit_info"`
	}
	if err := json.Unmarshal(line, &l); err != nil || l.Type != "rate_limit_event" {
		return UsageObserved{}, false
	}
	var info map[string]any
	if err := json.Unmarshal(l.Info, &info); err != nil {
		return UsageObserved{}, true
	}
	windows, _ := info["unifiedWindows"].(map[string]any)
	return UsageObserved{FiveHour: usageWindow(windows["five_hour"]), SevenDay: usageWindow(windows["seven_day"])}, true
}

// usageWindow は window の項目を読む。utilization か resetsAt が数でないか、範囲を外れれば nil。1 を超える utilization は読む
// (どの閾値でも閉じ、リセットで解けるので、読めない event にして人を待たせない)。
func usageWindow(v any) *UsageWindow {
	window, _ := v.(map[string]any)
	utilization, ok := window["utilization"].(float64)
	if !ok || utilization < 0 {
		return nil
	}
	resetsAt, ok := window["resetsAt"].(float64)
	if !ok || resetsAt < 0 || resetsAt > maxResetsAt {
		return nil
	}
	return &UsageWindow{Utilization: utilization, ResetsAt: time.Unix(int64(resetsAt), 0).UTC()}
}
