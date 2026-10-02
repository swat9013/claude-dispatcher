package status

import (
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/printable"
)

// Tone は文字の色と太さ。値は ANSI の SGR の引数
type Tone string

const (
	Plain      Tone = ""
	Bold       Tone = "1"
	Faint      Tone = "2"
	Gray       Tone = "90"
	Red        Tone = "31"
	Green      Tone = "32"
	Yellow     Tone = "33"
	Blue       Tone = "34"
	Cyan       Tone = "36"
	BoldGreen  Tone = "1;32"
	BoldYellow Tone = "1;33"
)

// Palette は色の付け方。
type Palette int

const (
	// Monochrome は色を付けない (stdout が端末でないか、NO_COLOR があるとき)
	Monochrome Palette = iota
	// ANSI は ANSI の escape で色を付ける
	ANSI
)

func (p Palette) paint(t Tone, s string) string {
	if p == Monochrome || t == Plain || s == "" {
		return s
	}
	return "\033[" + string(t) + "m" + s + "\033[0m"
}

// Display は描き先の端末の性質 (formats.md §7.2 の色と幅)。
type Display struct {
	Palette Palette
	// Width は端末の幅 (桁)。0 なら幅を取れないか端末でないので、行を切らない
	Width int
}

// ruleWidth は幅を取れないときにセクションの 1 行目を引き延ばす桁
const ruleWidth = 60

// Span は 1 つの色で描く文字列。
type Span struct {
	Tone Tone
	Text string
}

// Line は spans を 1 行に描く。制御文字 (改行・タブ・ESC など) は空白にし、端末の幅を超えれば切って `…` を付ける。改行は
// 付けない。
func (d Display) Line(spans ...Span) string {
	printables := make([]Span, len(spans))
	for i, s := range spans {
		printables[i] = Span{s.Tone, printable.Line(s.Text)}
	}
	spans = fit(printables, d.Width)
	var b strings.Builder
	// 同じ色が続く span は 1 つの escape にまとめる
	for i := 0; i < len(spans); {
		tone, text := spans[i].Tone, spans[i].Text
		for i++; i < len(spans) && spans[i].Tone == tone; i++ {
			text += spans[i].Text
		}
		b.WriteString(d.Palette.paint(tone, text))
	}
	return b.String()
}

// Section はセクションの 1 行目: 名前の後に、端末の幅まで (幅を取れなければ 60 桁まで) 罫線を引く。
func (d Display) Section(title Span) string {
	width := d.Width
	if width <= 0 {
		width = ruleWidth
	}
	return d.Line(title, Span{Faint, " " + strings.Repeat("─", max(width-cells(title.Text)-1, 0))})
}

// fit は spans を width 桁に収める。収まらなければ width−1 桁で切って `…` を足す。width が 0 以下なら切らない。
func fit(spans []Span, width int) []Span {
	total := 0
	for _, s := range spans {
		total += cells(s.Text)
	}
	if width <= 0 || total <= width {
		return spans
	}
	var out []Span
	room := width - 1
	for _, s := range spans {
		var b strings.Builder
		for _, r := range s.Text {
			w := cellWidth(r)
			if w > room {
				out = append(out, Span{s.Tone, b.String() + "…"})
				return out
			}
			room -= w
			b.WriteRune(r)
		}
		out = append(out, Span{s.Tone, b.String()})
	}
	return out
}

// cells は s を端末に描いたときの桁数。
func cells(s string) int {
	n := 0
	for _, r := range s {
		n += cellWidth(r)
	}
	return n
}

// wideRanges は端末で 2 桁を占める文字 (Unicode の East Asian Width が Wide か Fullwidth) の主な範囲。絵文字として描く
// 記号 (✅ ❌ ✨ など) を含む。曖昧幅の文字 (`●` `─` `…` `·` など) は 1 桁と数える。依存に足せるのは golang.org/x/term
// だけ (幅の表を持つ library を入れない) なので、表を自前で持つ。漏れは多く数える側に倒す (少なく数えると行が折り返す)
var wideRanges = [][2]rune{
	{0x1100, 0x115F}, {0x231A, 0x231B}, {0x2329, 0x232A}, {0x23E9, 0x23EC}, {0x23F0, 0x23F0}, {0x23F3, 0x23F3},
	{0x25FD, 0x25FE}, {0x2614, 0x2615}, {0x2648, 0x2653}, {0x267F, 0x267F}, {0x2693, 0x2693}, {0x26A1, 0x26A1},
	{0x26AA, 0x26AB}, {0x26BD, 0x26BE}, {0x26C4, 0x26C5}, {0x26CE, 0x26CE}, {0x26D4, 0x26D4}, {0x26EA, 0x26EA},
	{0x26F2, 0x26F3}, {0x26F5, 0x26F5}, {0x26FA, 0x26FA}, {0x26FD, 0x26FD}, {0x2705, 0x2705}, {0x270A, 0x270B},
	{0x2728, 0x2728}, {0x274C, 0x274C}, {0x274E, 0x274E}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797},
	{0x27B0, 0x27B0}, {0x27BF, 0x27BF}, {0x2B1B, 0x2B1C}, {0x2B50, 0x2B50}, {0x2B55, 0x2B55},
	{0x2E80, 0x303E}, {0x3041, 0x33FF}, {0x3400, 0x4DBF}, {0x4E00, 0x9FFF}, {0xA000, 0xA4CF},
	{0xAC00, 0xD7A3}, {0xF900, 0xFAFF}, {0xFE30, 0xFE4F}, {0xFF00, 0xFF60}, {0xFFE0, 0xFFE6},
	{0x1F000, 0x1FAFF}, {0x20000, 0x3FFFD},
}

// zeroRanges は桁を占めない文字 (結合文字・幅 0 の空白と接合子・異体字セレクタ) の主な範囲。U+FE0F (絵文字で描かせる
// 異体字セレクタ) は、前の文字を 2 桁で描かせる端末が多いので 1 桁と数える
var zeroRanges = [][2]rune{
	{0x0300, 0x036F}, {0x200B, 0x200F}, {0x20D0, 0x20FF}, {0x3099, 0x309A}, {0xFE00, 0xFE0E}, {0xE0100, 0xE01EF},
}

func cellWidth(r rune) int {
	switch {
	case inRanges(r, zeroRanges):
		return 0
	case inRanges(r, wideRanges):
		return 2
	}
	return 1
}

func inRanges(r rune, ranges [][2]rune) bool {
	for _, w := range ranges {
		if r >= w[0] && r <= w[1] {
			return true
		}
	}
	return false
}
