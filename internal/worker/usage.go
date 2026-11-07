package worker

import (
	"bytes"
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

// parseUsage は stream-json の 1 行が rate_limit_event なら、その使用率を返す。行の種類を決めた後は形が崩れていても捨てず、読めない
// event として返す。JSON として読めない行 (構文の崩れ・頭だけを持つ長い行) は、行の頭で rate_limit_event かを決める。
func parseUsage(line []byte) (UsageObserved, bool) {
	var l struct {
		Type any             `json:"type"`
		Info json.RawMessage `json:"rate_limit_info"`
	}
	if err := json.Unmarshal(line, &l); err != nil {
		return UsageObserved{}, bytes.HasPrefix(line, []byte(`{"type":"rate_limit_event"`))
	}
	if l.Type != "rate_limit_event" {
		return UsageObserved{}, false
	}
	// 読めなければ window が無いまま。window の外の項目は decode せず、window ごとに読むので、1 か所の崩れが他を巻き込まない
	var info struct {
		Windows map[string]json.RawMessage `json:"unifiedWindows"`
	}
	_ = json.Unmarshal(l.Info, &info)
	return UsageObserved{FiveHour: usageWindow(info.Windows["five_hour"]), SevenDay: usageWindow(info.Windows["seven_day"])}, true
}

// usageWindow は window の項目を読む。utilization か resetsAt が数でないか、範囲を外れれば nil。1 を超える utilization は読む
// (どの閾値でも閉じ、リセットで解けるので、読めない event にして人を待たせない)。
func usageWindow(raw json.RawMessage) *UsageWindow {
	var w struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    *float64 `json:"resetsAt"`
	}
	if err := json.Unmarshal(raw, &w); err != nil || w.Utilization == nil || w.ResetsAt == nil {
		return nil
	}
	if *w.Utilization < 0 || *w.ResetsAt < 0 || *w.ResetsAt > maxResetsAt {
		return nil
	}
	return &UsageWindow{Utilization: *w.Utilization, ResetsAt: time.Unix(int64(*w.ResetsAt), 0).UTC()}
}
