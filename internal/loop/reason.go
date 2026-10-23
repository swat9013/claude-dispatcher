package loop

import "github.com/swat9013/claude-dispatcher/internal/trigger"

// Reason は log.jsonl の reason の値 (formats.md §4)。言語に依らない token で書き、人が読む行には screen の文を出す
// (ADR 0013)。
type Reason string

// 作業対象を読み直したときの理由。終わった worker の確かめ・再起動・起動の直前の読み直しで使う。terminal は loop が worker を
// 止めた理由にも使う
const (
	reasonTerminal     Reason = "terminal"
	reasonLeftTrigger  Reason = "left_trigger"
	reasonStillMatches Reason = "still_matches"
)

// 2 回目の停止要求で worker を止めた理由 (stoppedByRequest)
const reasonStopRequested Reason = "stop_requested"

// 再起動を待つ claim を起動せずに解く理由のうち、起動した trigger が workflow 定義から消えたこと (ほかの理由は reasonOf が Trigger.Observe の観測から写す)
const reasonTriggerRemoved Reason = "trigger_removed"

// 当たるかをまだ決められない理由。終わった worker の確かめ (verify_wait) と起動の直前の読み直し (recheck_skip) で使う
const reasonUndecided Reason = "undecided"

// 起動の直前の読み直しだけで使う理由
const reasonEarlierTrigger Reason = "earlier_trigger"

// 着手宣言が乗っ取られて worker を止めた・claim を解いた理由 (formats.md §12)
const reasonTakenOver Reason = "taken_over"

// screens は理由の token から人が読む行に出す文への対応 (formats.md §4 の reason の表)。観測に当たる理由の文は trigger.Observation が持つ
var screens = map[Reason]string{
	reasonTerminal:       trigger.ObservedTerminal.Screen(),
	reasonLeftTrigger:    trigger.ObservedLeftTrigger.Screen(),
	reasonStillMatches:   trigger.ObservedStillMatches.Screen(),
	reasonStopRequested:  "停止要求",
	reasonTriggerRemoved: "trigger が workflow 定義から消えた",
	reasonUndecided:      trigger.ObservedUndecided.Screen(),
	reasonEarlierTrigger: "宣言順で先の trigger に当たる",
	reasonTakenOver:      "着手宣言が乗っ取られた",
}

// screen は理由を人が読む行に出す文。対応の無い理由は loop の誤りなので panic する。
func (r Reason) screen() string {
	s, ok := screens[r]
	if !ok {
		panic("loop: 画面の文の無い理由 " + string(r))
	}
	return s
}
