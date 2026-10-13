package provider_test

import (
	"slices"
	"testing"

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

func TestEveryRoleEitherSupportsOrDeclinesEachOfItsDeclarationsWithAReason(t *testing.T) {
	for _, kind := range provider.Kinds() {
		p, _ := provider.Of(kind)
		roles := []struct {
			name     string
			declared provider.Declarations
			features []provider.Feature
		}{{"tracker", p.Tracker.Declarations, provider.TrackerFeatures}}
		if p.CLHost != nil {
			roles = append(roles, struct {
				name     string
				declared provider.Declarations
				features []provider.Feature
			}{"CL host", p.CLHost.Declarations, provider.CLHostFeatures})
		}
		for _, role := range roles {
			for _, f := range role.features {
				t.Run(string(kind)+"/"+role.name+"/"+string(f), func(t *testing.T) {
					supported := slices.Contains(role.declared.Supports, f)
					reason, declined := role.declared.Declines[f]

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
}

func TestOnlyATrackerWithoutTheCLHostRoleAcceptsASeparateCLPlace(t *testing.T) {
	for _, kind := range provider.Kinds() {
		p, _ := provider.Of(kind)

		if p.Tracker.Supported(provider.CLPlace) != (p.CLHost == nil) {
			t.Fatalf("%s: cl: を書けるのは CL host の役割を持たない種類だけ (CL host の役割: %v)", kind, p.CLHost != nil)
		}
	}
}

func TestCLHostKindsAreTheKindsWithTheCLHostRole(t *testing.T) {
	got := provider.CLHostKinds()

	if !slices.Equal(got, []provider.Kind{provider.GitHub, provider.GitLab}) {
		t.Fatalf("CL 置き場になる種類 = %v, want [github gitlab]", got)
	}
}

func TestAnUndeclaredDeclarationIsNotSupported(t *testing.T) {
	d := provider.Declarations{}

	supported, reason := d.Supported(provider.Token), d.DeclineReason(provider.Token)

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
