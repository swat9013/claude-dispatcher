package places

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/provider"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// stub は CLI を撃たず、撃った CLI と環境を記録する Runner を作る。
type stub struct {
	// built は Runner を作った順の CLI と環境
	built []built
	// calls は撃った CLI と引数 (`<command> <args...>`)
	calls []string
	// reply は CLI と引数への応答
	reply func(command string, args []string) ([]byte, error)
}

type built struct {
	command string
	env     []string
}

type stubRunner struct {
	s       *stub
	command string
}

func (r stubRunner) Run(args ...string) ([]byte, error) {
	r.s.calls = append(r.s.calls, r.command+" "+strings.Join(args, " "))
	return r.s.reply(r.command, args)
}

func (s *stub) runtime(env []string) provider.Runtime {
	return provider.Runtime{Env: env, Runner: func(command string, env []string) provider.Runner {
		s.built = append(s.built, built{command: command, env: env})
		return stubRunner{s: s, command: command}
	}}
}

func load(t *testing.T, text string) workflow.Definition {
	t.Helper()
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Load(path, func(key string) string {
		if key == "T" {
			return "secret"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return def
}

const githubWithToken = `---
tracker:
  kind: github
  repo: acme/widgets
  token: $T
triggers:
  - {name: implement, on: issue, action: /implement}
---
`

const githubWithCLTrigger = `---
tracker:
  kind: github
  repo: acme/widgets
triggers:
  - {name: fix, on: cl, action: /fix}
---
`

const jiraWithCL = `---
tracker:
  kind: jira
  host: acme.atlassian.net
  repo: widgets
cl:
  kind: github
  repo: acme/widgets
triggers:
  - {name: implement, on: issue, action: /implement}
---
`

func TestTrackerTokenReachesTheIssuePlaceButNotTheSeparateCLPlace(t *testing.T) {
	def := load(t, githubWithToken)
	// 検査は github に cl: を書かせないが、渡し先の規則は cl: の有無と種類に依らず Places が持つ
	clProvider, _ := provider.Of(provider.GitHub)
	def.CL = &workflow.CL{Kind: provider.GitHub, Place: clProvider.NewPlace("", "acme/cls")}
	s := &stub{reply: func(string, []string) ([]byte, error) { return nil, errors.New("stub") }}

	New(def, s.runtime([]string{"HOME=/h"})).Store()

	// issue 置き場と CL 置き場は同じ gh を撃つので、組んだ順ではなく token の有無で数える
	var withToken, withoutToken int
	for _, b := range s.built {
		switch {
		case slices.Contains(b.env, "GH_TOKEN=secret"):
			withToken++
		case !slices.ContainsFunc(b.env, func(kv string) bool { return strings.HasPrefix(kv, "GH_TOKEN=") }):
			withoutToken++
		}
	}
	if withToken != 1 || withoutToken != 1 || len(s.built) != 2 {
		t.Fatalf("組んだ Runner = %+v, want token を置いた issue 置き場と置かない CL 置き場の 1 つずつ", s.built)
	}
}

func TestTokenEnvPutsTrackerTokenForHooksAndWorkers(t *testing.T) {
	def := load(t, githubWithToken)

	env := New(def, provider.Runtime{Env: []string{"HOME=/h"}}).TokenEnv()

	if !slices.Contains(env, "GH_TOKEN=secret") || !slices.Contains(env, "HOME=/h") {
		t.Fatalf("環境 = %v", env)
	}
}

func TestConfirmOfJiraWithCLConfirmsBothTheIssuePlaceAndTheCLPlace(t *testing.T) {
	def := load(t, jiraWithCL)
	s := &stub{reply: func(command string, args []string) ([]byte, error) {
		switch {
		case command == "acli" && slices.Contains(args, "auth"):
			return []byte("Site: acme.atlassian.net\n"), nil
		case command == "acli":
			return []byte("[]"), nil
		}
		return nil, errors.New("gh を撃った")
	}}

	err := New(def, s.runtime(nil)).Confirm()

	if err == nil || !strings.Contains(err.Error(), "gh を撃った") {
		t.Fatalf("確認 = %v, want CL 置き場の失敗", err)
	}
	if !slices.ContainsFunc(s.calls, func(c string) bool { return strings.HasPrefix(c, "acli jira workitem search") }) ||
		!slices.ContainsFunc(s.calls, func(c string) bool { return strings.HasPrefix(c, "gh ") }) {
		t.Fatalf("撃った CLI = %v, want acli の検索と gh の両方", s.calls)
	}
}

func TestScopeKeyIsReadWithoutRunningAnyCLI(t *testing.T) {
	def := load(t, jiraWithCL)

	got := ScopeKey(def)

	if got != "acme.atlassian.net/widgets" {
		t.Fatalf("scope key = %q", got)
	}
}

func TestRequiredCommandsLeaveOutGitWithoutCLTriggers(t *testing.T) {
	def := load(t, jiraWithCL)

	got := RequiredCommands(def)

	if !slices.Equal(got, []string{"acli", "gh", "claude"}) {
		t.Fatalf("loop が撃つ CLI = %v", got)
	}
}

func TestRequiredCommandsAddGitForCLTriggers(t *testing.T) {
	def := load(t, githubWithCLTrigger)

	got := RequiredCommands(def)

	if !slices.Equal(got, []string{"gh", "claude", "git"}) {
		t.Fatalf("loop が撃つ CLI = %v", got)
	}
}

func TestCommandsAlwaysResolveGitForWorkersAndHooks(t *testing.T) {
	def := load(t, jiraWithCL)

	got := Commands(def)

	if !slices.Equal(got, []string{"acli", "gh", "claude", "git"}) {
		t.Fatalf("PATH を解決する CLI = %v", got)
	}
}
