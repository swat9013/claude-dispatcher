// Package termtext は端末に行を出すときの桁揃えと切り詰めを持つ。status の表・判断の行と doctor の行が共有する。
package termtext

import "strings"

// Width は端末での表示幅。CJK 以降の文字 (U+2E80 以降。U+1F300 以降の絵文字を含む) と、絵文字として描かれやすい記号
// (U+2600-U+27BF) を 2 桁と数える近似。狭い側へ外れない (結合文字を 1 桁と数え、1 桁の記号を 2 桁と数えうる) ので、
// Cut で切った行は端末の幅を超えにくい。East Asian Ambiguous の文字 (→ ※ など) は端末の設定で幅が変わるので 1 桁と
// 数える。表に出る項目名と orchestrator の判断の reason にはこの近似で足り、幅の表の library には依存しない。
func Width(s string) int {
	width := 0
	for _, r := range s {
		if r >= 0x2E80 || (r >= 0x2600 && r <= 0x27BF) {
			width += 2
		} else {
			width++
		}
	}
	return width
}

// Cut は s を表示幅 width に収まるところまでで切る。
func Cut(s string, width int) string {
	used := 0
	for i, r := range s {
		w := Width(string(r))
		if used+w > width {
			return s[:i]
		}
		used += w
	}
	return s
}

// Pad は s の後ろを空白で埋めて表示幅を width にする (既に超えていればそのまま)。
func Pad(s string, width int) string {
	return s + strings.Repeat(" ", max(0, width-Width(s)))
}
