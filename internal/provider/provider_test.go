package provider_test

import (
	"slices"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/provider"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

func TestEveryKindHasAProviderThatDeclaresEveryField(t *testing.T) {
	for _, kind := range provider.Kinds() {
		t.Run(string(kind), func(t *testing.T) {
			p, ok := provider.Of(kind)

			if !ok {
				t.Fatalf("種類 %s の provider が無い", kind)
			}
			if p.Kind != kind || p.Command == "" || p.ParseRepo == nil || p.NewPlace == nil || p.Build == nil {
				t.Fatalf("provider %s の field が欠けている: %+v", kind, p)
			}
			if len(p.Tracker.Settings) == 0 || (p.CLHost != nil && len(p.CLHost.Settings) == 0) {
				t.Fatalf("provider %s が役割の settings の entry を宣言していない", kind)
			}
			if p.CLHost != nil && p.CLHost.Prefix == "" {
				t.Fatalf("provider %s が CL の呼び方を宣言していない", kind)
			}
			if p.Tracker.KeyedIssues != (p.Tracker.IssueKey != nil) || (!p.Tracker.KeyedIssues && p.Tracker.Prefix == "") {
				t.Fatalf("provider %s の issue の呼び方が key の有無と合わない", kind)
			}
			if p.Host.Parse != nil && p.Host.Default == "" && p.Host.RequiredHint == "" {
				t.Fatalf("provider %s の tracker.host は必須なのに、欠落の誤りに添える案内が無い", kind)
			}
			if p.Setup.OwnsOrigin != nil && (p.Setup.CurrentPlace == nil || p.Setup.Template == nil) {
				t.Fatalf("provider %s は origin を認めるのに、clone から雛形を書けない", kind)
			}
			if p.Setup.Flags != "" && (p.Setup.Template == nil || p.Host.Parse == nil) {
				t.Fatalf("provider %s は flag で受けるのに、--host を読めないか雛形を書けない", kind)
			}
		})
	}
}

func TestEveryProviderEitherSupportsOrDeclinesEachDeclarationWithAReason(t *testing.T) {
	for _, kind := range provider.Kinds() {
		p, _ := provider.Of(kind)
		for _, f := range provider.TrackerFeatures {
			t.Run(string(kind)+"/"+string(f), func(t *testing.T) {
				supported := p.Supported(f)
				reason, declined := p.Tracker.Declines[f]

				if supported == declined {
					t.Fatalf("支える (%v) と支えない (%v) のどちらか一方だけを宣言する", supported, declined)
				}
				if declined && reason == "" {
					t.Fatalf("支えない理由の文言が空")
				}
			})
		}
	}
}

func TestTheTokenIsSetOverTheVariableAlreadyInTheEnvironment(t *testing.T) {
	p, _ := provider.Of(provider.GitLab)

	env := p.WithToken([]string{"GITLAB_TOKEN=loop-own", "HOME=/home/u"}, "secret-1")

	if deps.Getenv(env, "GITLAB_TOKEN") != "secret-1" || deps.Getenv(env, "HOME") != "/home/u" || len(env) != 2 {
		t.Fatalf("env = %v, want GITLAB_TOKEN を secret-1 に置き換え、HOME は残す", env)
	}
}

func TestNoTokenLeavesTheEnvironmentAsItIs(t *testing.T) {
	p, _ := provider.Of(provider.GitHub)

	env := p.WithToken([]string{"GH_TOKEN=loop-own"}, "")

	if !slices.Equal(env, []string{"GH_TOKEN=loop-own"}) {
		t.Fatalf("env = %v", env)
	}
}

func TestAProviderWithoutTheCLHostRoleSaysWhyCLTriggersCannotBeWritten(t *testing.T) {
	for _, kind := range provider.Kinds() {
		p, _ := provider.Of(kind)

		if p.CLHost == nil && (p.Supported(provider.CL) || p.NoCLHost == "") {
			t.Fatalf("%s: CL host の役割を持たないのに、on: cl を支えるか、支えない理由が無い", kind)
		}
	}
}

func TestAnUndeclaredDeclarationIsNotSupported(t *testing.T) {
	p := provider.Provider{Kind: "x"}

	supported, reason := p.Supported(provider.Token), p.DeclineReason(provider.Token)

	if supported || reason == "" {
		t.Fatalf("宣言の無いものは支えないものとして名指しする: supported = %v, reason = %q", supported, reason)
	}
}

func TestAWorkItemIsReferredToTheWayItsRoleDeclares(t *testing.T) {
	cases := []struct {
		kind provider.Kind
		ref  target.Ref
		want string
	}{
		{provider.GitHub, target.Ref{Kind: target.KindIssue, Number: 42}, "#42"},
		{provider.GitHub, target.Ref{Kind: target.KindCL, Number: 7}, "#7"},
		{provider.GitLab, target.Ref{Kind: target.KindIssue, Number: 42}, "#42"},
		{provider.GitLab, target.Ref{Kind: target.KindCL, Number: 7}, "!7"},
		{provider.Jira, target.Ref{Kind: target.KindIssue, Number: 123}, "WIDGETS-123"},
	}
	for _, c := range cases {
		t.Run(string(c.kind)+" "+c.ref.String(), func(t *testing.T) {
			p, _ := provider.Of(c.kind)
			place := placeOf(t, p)

			got := p.Reference(place, c.ref)

			if got != c.want {
				t.Fatalf("参照 = %q, want %q", got, c.want)
			}
		})
	}
}

func TestOriginOnGitHubIsGitHubAndAnyOtherHostIsGitLab(t *testing.T) {
	for host, want := range map[string]provider.Kind{"github.com": provider.GitHub, "gitlab.com": provider.GitLab, "git.example.com": provider.GitLab} {
		p := provider.ForOrigin(host)

		if p.Kind != want || p.Setup.CurrentPlace == nil || p.Setup.Template == nil {
			t.Fatalf("origin の host %s の種類 = %s, want clone から雛形を書ける %s", host, p.Kind, want)
		}
	}
}

func TestNoKindHasTwoProviders(t *testing.T) {
	kinds := provider.Kinds()

	if compacted := slices.Compact(slices.Sorted(slices.Values(kinds))); len(compacted) != len(kinds) {
		t.Fatalf("種類 %v に重なりがある", kinds)
	}
}

func TestOnlyJiraIsWrittenFromSetupFlags(t *testing.T) {
	got := provider.SetupFlagKinds()

	if !slices.Equal(got, []provider.Kind{provider.Jira}) {
		t.Fatalf("flag で受ける種類 = %v, want [jira]", got)
	}
}

// placeOf は p の見本の置き場。
func placeOf(t *testing.T, p provider.Provider) provider.Place {
	t.Helper()
	repo := map[provider.Kind]string{provider.GitHub: "acme/widgets", provider.GitLab: "acme/widgets", provider.Jira: "WIDGETS"}[p.Kind]
	host := map[provider.Kind]string{provider.GitLab: "gitlab.com", provider.Jira: "acme.atlassian.net"}[p.Kind]
	parsed, err := p.ParseRepo(repo)
	if err != nil {
		t.Fatalf("ParseRepo(%q): %v", repo, err)
	}
	return p.NewPlace(host, parsed)
}
