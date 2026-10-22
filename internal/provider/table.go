package provider

import (
	"fmt"

	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/gitlab"
	"github.com/swat9013/claude-dispatcher/internal/jira"
	"github.com/swat9013/claude-dispatcher/internal/scaffold"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
)

// statusAndType は github と gitlab が status と type を支えない理由
const statusAndType = "status と issue type は Jira の issue にだけある"

// sameCLHost は github と gitlab が cl: を支えない理由
const sameCLHost = "issue 置き場と同じ場所が CL 置き場"

// 役割を担う CLI の名前。Provider.Command と、部品が撃つ Runner の名前 (Runtime.runner) に同じ綴りを使う
const (
	ghCommand   = "gh"
	glabCommand = "glab"
	acliCommand = "acli"
)

// providers は種類ごとの provider の表。並びは誤りで候補を挙げる順。支えない理由の文言は formats.md §2.8・案内の
// 「置き場のサービスごとの違い」と合わせる。
var providers = []Provider{
	{
		Kind:    GitHub,
		Command: ghCommand,
		ParseRepo: func(s string) (string, error) {
			repo, err := github.ParseRepo(s)
			return repo.String(), err
		},
		NewPlace: func(_, repo string) Place {
			r := mustParse(github.ParseRepo, repo)
			return Place{name: r.String(), value: r}
		},
		scopeKey: func(p Place) string { return github.ScopeKey(p.value.(github.Repo)) },
		Tracker: TrackerRole{
			declared: Declarations{
				Supports: []Feature{Author, Milestone, Blocked},
				Declines: map[Feature]string{CLPlace: sameCLHost, Status: statusAndType, Type: statusAndType},
			},
			TokenVariable: "GH_TOKEN",
			Prefix:        "#",
			Settings:      []string{"Bash(gh issue:*)"},
		},
		CLHost: &CLHostRole{
			Prefix:       "#",
			Declarations: Declarations{Supports: []Feature{Author}},
			Settings:     []string{"Bash(gh pr:*)"},
			Scaffold: func(p Place) scaffold.CLHost {
				return scaffold.GitHubCL(p.value.(github.Repo).String())
			},
		},
		Build: func(rt Runtime, s Setting) Parts {
			store := github.NewStore(rt.runner(ghCommand, github.Exec{Env: rt.Env, Timeout: rt.Timeout}), s.Place.value.(github.Repo))
			return Parts{Issues: store, CLs: store}
		},
		Setup: Setup{
			ownsOrigin: func(host string) bool { return host == "github.com" },
			currentPlace: func(rt Runtime) (Place, error) {
				repo, err := github.CurrentRepo(rt.runner(ghCommand, github.Exec{Env: rt.Env, Timeout: rt.Timeout}))
				return Place{name: repo.String(), value: repo}, err
			},
			template: func(p Place, _ scaffold.CLHost) string { return scaffold.GitHub(p.value.(github.Repo).String()) },
		},
	},
	{
		Kind:      GitLab,
		Command:   glabCommand,
		Host:      Host{parse: gitlab.ParseHost, defaultHost: gitlab.DefaultHost, hasDefault: true},
		ParseRepo: gitlab.ParsePath,
		NewPlace: func(host, repo string) Place {
			p := gitlab.Project{Host: host, Path: repo}
			return Place{name: p.String(), value: p}
		},
		scopeKey: func(p Place) string { return gitlab.ScopeKey(p.value.(gitlab.Project)) },
		Tracker: TrackerRole{
			declared: Declarations{
				Supports: []Feature{Author, Milestone},
				Declines: map[Feature]string{
					CLPlace: sameCLHost,
					Blocked: "GitLab の CE は issue の依存を API で返さないので、blocked: false が全件に当たる",
					Status:  statusAndType,
					Type:    statusAndType,
				},
			},
			TokenVariable: "GITLAB_TOKEN",
			Prefix:        "#",
			Settings:      []string{"Bash(glab issue:*)"},
		},
		CLHost: &CLHostRole{
			// GitLab は issue と merge request に別々に番号を振るので、同じ番号の issue と取り違えないよう綴りを分ける
			Prefix:       "!",
			Declarations: Declarations{Supports: []Feature{Author}},
			Settings:     []string{"Bash(glab mr:*)"},
			Scaffold: func(p Place) scaffold.CLHost {
				project := p.value.(gitlab.Project)
				return scaffold.GitLabCL(project.Host, project.Path)
			},
		},
		Build: func(rt Runtime, s Setting) Parts {
			store := gitlab.NewStore(rt.runner(glabCommand, gitlab.Exec{Env: rt.Env, Timeout: rt.Timeout}), s.Place.value.(gitlab.Project), trigger.NeedsReviewUnresolved(s.Triggers))
			return Parts{Issues: store, CLs: store}
		},
		Setup: Setup{
			currentPlace: func(rt Runtime) (Place, error) {
				p, err := gitlab.CurrentProject(rt.runner(glabCommand, gitlab.Exec{Env: rt.Env, Timeout: rt.Timeout}))
				return Place{name: p.String(), value: p}, err
			},
			template: func(p Place, _ scaffold.CLHost) string {
				project := p.value.(gitlab.Project)
				return scaffold.GitLab(project.Host, project.Path)
			},
		},
	},
	{
		Kind:      Jira,
		Command:   acliCommand,
		Host:      Host{parse: jira.ParseSite, requiredHint: "Jira Cloud の site を書く"},
		ParseRepo: jira.ParseKey,
		NewPlace: func(host, repo string) Place {
			p := jira.Project{Site: host, Key: repo}
			return Place{name: p.String(), value: p}
		},
		scopeKey: func(p Place) string { return jira.ScopeKey(p.value.(jira.Project)) },
		Tracker: TrackerRole{
			declared: Declarations{
				Supports: []Feature{Blocked, Status, Type},
				Declines: map[Feature]string{
					Token:     "acli は acli jira auth login の認証を使う",
					Author:    "worker を誰の書き込みで起動してよいかの線を、Jira の権限に引けない",
					Milestone: "sprint は無いことがあり、fixVersions は acli の一覧で読めない",
				},
			},
			KeyedIssues: true,
			IssueKey:    func(p Place, number int) string { return p.value.(jira.Project).IssueKey(number) },
			Settings:    []string{"Bash(acli jira workitem:*)"},
		},
		Build: func(rt Runtime, s Setting) Parts {
			store := jira.NewStore(rt.runner(acliCommand, jira.Exec{Env: rt.Env, Timeout: rt.Timeout}), s.Place.value.(jira.Project), trigger.NeedsBlocked(s.Triggers))
			confirm := func() (IssueStore, error) {
				confirmed, err := store.Confirm(jira.NamesOf(s.Triggers))
				if err != nil {
					return nil, err
				}
				return confirmed, nil
			}
			return Parts{Issues: store, Confirm: confirm}
		},
		Setup: Setup{
			flags: "--host <site> と --repo <key>",
			template: func(p Place, cl scaffold.CLHost) string {
				project := p.value.(jira.Project)
				return scaffold.Jira(project.Site, project.Key, cl)
			},
		},
	},
}

// originFallback は、どの provider も認めない origin の host (github.com 以外) を読む種類。self-managed の GitLab と読む
const originFallback = GitLab

// mustParse は、検査を通った値を parse し直す。通った値で失敗するのは呼び出し側の誤りなので panic する。
func mustParse[T any](parse func(string) (T, error), s string) T {
	v, err := parse(s)
	if err != nil {
		panic(fmt.Sprintf("検査を通っていない値 %q: %v", s, err))
	}
	return v
}
