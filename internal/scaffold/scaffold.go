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

// CLHost は雛形が CL 置き場を書くための値。Kind は github か gitlab。Host は gitlab でだけ書く。
type CLHost struct {
	Kind, Host, Repo string
}

// clHostWords は CL 置き場の種類ごとの雛形の綴り。
type clHostWords struct {
	// triggers は CL 側の trigger の行
	triggers string
	// noun は CL の呼び名、cli は操作する CLI、create は CL を出すコマンド
	noun, cli, create string
}

func (c CLHost) words() clHostWords {
	switch c.Kind {
	case "github":
		return clHostWords{triggers: githubCLTriggers, noun: "pull request", cli: "gh", create: "gh pr create"}
	case "gitlab":
		return clHostWords{triggers: gitlabCLTriggers, noun: "merge request", cli: "glab", create: "glab mr create"}
	}
	// CL 置き場の種類は provider が渡す。ほかの値は呼び出し側の誤り
	panic(fmt.Sprintf("CL 置き場の雛形を持たない種類 %q", c.Kind))
}

// block は cl: の block の行。
func (c CLHost) block() string {
	if c.Host == "" {
		return fmt.Sprintf("cl:\n  kind: %s\n  repo: %s\n", c.Kind, c.Repo)
	}
	return fmt.Sprintf("cl:\n  kind: %s\n  host: %s\n  repo: %s\n", c.Kind, c.Host, c.Repo)
}

// Jira は site を tracker.host に、project key を tracker.repo に、cl を cl: に埋めた jira の雛形。CL 側の trigger と CL を
// 操作する CLI は cl の種類のものを書く。
func Jira(site, key string, cl CLHost) string {
	w := cl.words()
	return strings.NewReplacer(
		"{{HOST}}", site, "{{REPO}}", key, "{{CL}}", cl.block(), "{{CL_TRIGGERS}}\n", w.triggers,
		"{{CL_NOUN}}", w.noun, "{{CL_CLI}}", w.cli, "{{CL_CREATE}}", w.create,
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
