// Package scaffold は setup が置く workflow 定義の雛形 (formats.md §7.4)。
package scaffold

import (
	_ "embed"
	"strings"
)

//go:embed WORKFLOW.md
var workflow string

// Workflow は repo (owner/name) を tracker.repo に埋めた雛形。
func Workflow(repo string) string {
	return strings.Replace(workflow, "{{REPO}}", repo, 1)
}
