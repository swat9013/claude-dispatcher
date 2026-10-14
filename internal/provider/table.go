package provider

import (
	"fmt"

	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/gitlab"
	"github.com/swat9013/claude-dispatcher/internal/jira"
	"github.com/swat9013/claude-dispatcher/internal/scaffold"
)

// statusAndType は github と gitlab が status と type を支えない理由
const statusAndType = "status と issue type は Jira の issue にだけある"

// sameCLHost は github と gitlab が cl: を支えない理由
const sameCLHost = "issue 置き場と同じ場所が CL 置き場"

// providers は種類ごとの provider の表。並びは誤りで候補を挙げる順。支えない理由の文言は formats.md §2.8・案内の
// 「置き場のサービスごとの違い」と合わせる。
var providers = []Provider{
	{
		Kind:    GitHub,
		Command: "gh",
		ParseRepo: func(s string) (string, error) {
			repo, err := github.ParseRepo(s)
			return repo.String(), err
		},
		NewPlace: func(_, repo string) Place {
			r := mustParse(github.ParseRepo, repo)
			return Place{name: r.String(), value: r}
		},
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
			store := github.NewStore(github.Exec{Env: rt.Env, Timeout: rt.Timeout}, s.Place.value.(github.Repo))
			return Parts{Issues: store, CLs: store}
		},
		Setup: Setup{
			OwnsOrigin: func(host string) bool { return host == "github.com" },
			CurrentPlace: func(rt Runtime) (Place, error) {
				repo, err := github.CurrentRepo(github.Exec{Env: rt.Env, Timeout: rt.Timeout})
				return Place{name: repo.String(), value: repo}, err
			},
			Template: func(p Place, _ scaffold.CLHost) string { return scaffold.GitHub(p.value.(github.Repo).String()) },
		},
	},
	{
		Kind:      GitLab,
		Command:   "glab",
		Host:      Host{Parse: gitlab.ParseHost, Default: gitlab.DefaultHost},
		ParseRepo: gitlab.ParsePath,
		NewPlace: func(host, repo string) Place {
			p := gitlab.Project{Host: host, Path: repo}
			return Place{name: p.String(), value: p}
		},
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
			store := gitlab.NewStore(gitlab.Exec{Env: rt.Env, Timeout: rt.Timeout}, s.Place.value.(gitlab.Project))
			return Parts{Issues: store, CLs: store}
		},
		Setup: Setup{
			CurrentPlace: func(rt Runtime) (Place, error) {
				p, err := gitlab.CurrentProject(gitlab.Exec{Env: rt.Env, Timeout: rt.Timeout})
				return Place{name: p.String(), value: p}, err
			},
			Template: func(p Place, _ scaffold.CLHost) string {
				project := p.value.(gitlab.Project)
				return scaffold.GitLab(project.Host, project.Path)
			},
		},
	},
	{
		Kind:      Jira,
		Command:   "acli",
		Host:      Host{Parse: jira.ParseSite, RequiredHint: "Jira Cloud の site を書く"},
		ParseRepo: jira.ParseKey,
		NewPlace: func(host, repo string) Place {
			p := jira.Project{Site: host, Key: repo}
			return Place{name: p.String(), value: p}
		},
		Tracker: TrackerRole{
			declared: Declarations{
				Supports: []Feature{CLPlace, Blocked, Status, Type},
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
			store := jira.NewStore(jira.Exec{Env: rt.Env, Timeout: rt.Timeout}, s.Place.value.(jira.Project), s.Triggers)
			return Parts{Issues: store, Confirm: func() error { return store.Confirm(jira.NamesOf(s.Triggers)) }}
		},
		Setup: Setup{
			Flags: "--host <site> と --repo <project key>",
			Template: func(p Place, cl scaffold.CLHost) string {
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
