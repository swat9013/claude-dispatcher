// Package printable は外から来た文字列を、端末とタブ区切りの列に 1 行で出せる形にする。
package printable

import "strings"

// Line は制御文字 (タブ・改行・ESC など) を空白に置き換える。端末と、タブ区切りの列を守る。
func Line(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
}
