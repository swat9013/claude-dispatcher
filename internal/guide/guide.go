// Package guide は、利用者のセッションへ出す案内 (guide.md)。workflow 定義の書き方の正本で、`claude-dispatcher guide` が
// そのまま出す (formats.md §9、ADR 0012)。
package guide

import _ "embed"

//go:embed guide.md
var text string

// Text は案内の全文。
func Text() string { return text }
