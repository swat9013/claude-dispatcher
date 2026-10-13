// Package scaffold は setup が置く workflow 定義の雛形 (formats.md §7.4)。雛形は tracker.kind ごとに持ち、CL 側の trigger は
// CL 置き場の種類ごとに 1 つ持って、その種類を CL 置き場にする雛形 (同じ種類の雛形と、cl: を書く jira の雛形) が共有する。
package scaffold

import (
	_ "embed"
	"fmt"
	"strings"
)

var (
	//go:embed WORKFLOW.md
	github string
	//go:embed WORKFLOW.gitlab.md
	gitlab string
	//go:embed WORKFLOW.jira.md
	jira string
	//go:embed cl.github.md
	githubCLTriggers string
	//go:embed cl.gitlab.md
	gitlabCLTriggers string
)

// CLHost は雛形が CL 置き場を書くための綴り。CL 置き場の種類ごとに GitHubCL か GitLabCL で作る。
type CLHost struct {
	// block は cl: の block の行、triggers は CL 側の trigger の行
	block, triggers string
	// noun は CL の呼び名、cli は操作する CLI、create は CL を出すコマンド
	noun, cli, create string
}

// GitHubCL は repo (owner/name) の pull request を CL 置き場にする綴り。
func GitHubCL(repo string) CLHost {
	return CLHost{
		block:    fmt.Sprintf("cl:\n  kind: github\n  repo: %s\n", repo),
		triggers: githubCLTriggers, noun: "pull request", cli: "gh", create: "gh pr create",
	}
}

// GitLabCL は host の path の merge request を CL 置き場にする綴り。
func GitLabCL(host, path string) CLHost {
	return CLHost{
		block:    fmt.Sprintf("cl:\n  kind: gitlab\n  host: %s\n  repo: %s\n", host, path),
		triggers: gitlabCLTriggers, noun: "merge request", cli: "glab", create: "glab mr create",
	}
}

// Jira は site を tracker.host に、project key を tracker.repo に、cl を cl: に埋めた jira の雛形。CL 側の trigger と CL を
// 操作する CLI は cl のものを書く。
func Jira(site, key string, cl CLHost) string {
	return strings.NewReplacer(
		"{{HOST}}", site, "{{REPO}}", key, "{{CL}}", cl.block, "{{CL_TRIGGERS}}\n", cl.triggers,
		"{{CL_NOUN}}", cl.noun, "{{CL_CLI}}", cl.cli, "{{CL_CREATE}}", cl.create,
	).Replace(jira)
}

// GitHub は repo (owner/name) を tracker.repo に埋めた github の雛形。
func GitHub(repo string) string {
	return strings.NewReplacer("{{REPO}}", repo, "{{CL_TRIGGERS}}\n", githubCLTriggers).Replace(github)
}

// GitLab は host を tracker.host に、path を tracker.repo に埋めた gitlab の雛形。
func GitLab(host, path string) string {
	return strings.NewReplacer("{{HOST}}", host, "{{REPO}}", path, "{{CL_TRIGGERS}}\n", gitlabCLTriggers).Replace(gitlab)
}
