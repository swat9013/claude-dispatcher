// Package termtext は端末に表を出すときの桁揃えを持つ。status の表と doctor の行が共有する。
package termtext

import "strings"

// Width は端末での表示幅。CJK 以降の文字 (U+2E80 以降) を 2 桁と数える近似 — 表に出るのは ASCII と日本語の項目名
// だけで、結合文字や絵文字の幅の厳密さは要らないので、幅の表の library には依存しない。
func Width(s string) int {
	width := 0
	for _, r := range s {
		if r >= 0x2E80 {
			width += 2
		} else {
			width++
		}
	}
	return width
}

// Cut は s を表示幅 width に収まるところまでで切る。width が負なら切らない。
func Cut(s string, width int) string {
	if width < 0 {
		return s
	}
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
