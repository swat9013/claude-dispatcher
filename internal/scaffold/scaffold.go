// Package scaffold は setup が置く workflow 定義の雛形 (formats.md §7.4)。雛形は tracker.kind ごとに持つ。
package scaffold

import (
	_ "embed"
	"strings"
)

var (
	//go:embed WORKFLOW.md
	github string
	//go:embed WORKFLOW.gitlab.md
	gitlab string
)

// GitHub は repo (owner/name) を tracker.repo に埋めた github の雛形。
func GitHub(repo string) string {
	return strings.Replace(github, "{{REPO}}", repo, 1)
}

// GitLab は host を tracker.host に、path を tracker.repo に埋めた gitlab の雛形。
func GitLab(host, path string) string {
	return strings.NewReplacer("{{HOST}}", host, "{{REPO}}", path).Replace(gitlab)
}
