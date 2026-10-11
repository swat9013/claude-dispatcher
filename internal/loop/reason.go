package loop

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

// 2 回目の停止要求で worker を止めた理由
const reasonStopRequested Reason = "stop_requested"

// 再起動を待つ claim を起動せずに解く理由のうち、起動した trigger が workflow 定義から消えたこと (ほかの理由は settle が返す)
const reasonTriggerRemoved Reason = "trigger_removed"

// 当たるかをまだ決められない理由。終わった worker の確かめ (verify_wait) と起動の直前の読み直し (recheck_skip) で使う
const reasonUndecided Reason = "undecided"

// 起動の直前の読み直しだけで使う理由
const reasonEarlierTrigger Reason = "earlier_trigger"

// screens は理由の token から人が読む行に出す文への対応 (formats.md §4 の reason の表)
var screens = map[Reason]string{
	reasonTerminal:       "終端",
	reasonLeftTrigger:    "trigger から外れた",
	reasonStillMatches:   "trigger に当たったまま",
	reasonStopRequested:  "停止要求",
	reasonTriggerRemoved: "trigger が workflow 定義から消えた",
	reasonUndecided:      "当たるかをまだ決められない",
	reasonEarlierTrigger: "宣言順で先の trigger に当たる",
}

// screen は理由を人が読む行に出す文。対応の無い理由は loop の誤りなので panic する。
func (r Reason) screen() string {
	s, ok := screens[r]
	if !ok {
		panic("loop: 画面の文の無い理由 " + string(r))
	}
	return s
}
