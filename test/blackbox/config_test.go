package blackbox_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// config.toml の検査 (formats.md §2)。不正な config では観測を始めず、名指しで config_error にする。

func (s *sandbox) observationCalls() []stubCall {
	return s.callsMatching("gh", func(c stubCall) bool {
		return c.hasPrefix("issue", "list") || c.hasPrefix("api", "graphql")
	})
}

func (s *sandbox) verificationCalls() []stubCall {
	return s.callsMatching("gh", func(c stubCall) bool {
		return c.hasPrefix("repo", "view") || c.hasPrefix("label", "list")
	})
}

func assertConfigErrorWithoutObserving(t *testing.T, s *sandbox, r runResult, fragments ...string) {
	t.Helper()
	assertExit(t, r, 2)
	line := s.onlyTickLine()
	assertResult(t, line, "config_error")
	s.assertErrorNames(line, fragments...)
	if calls := s.observationCalls(); len(calls) != 0 {
		t.Fatalf("config が不正なのに観測した: %v", calls)
	}
	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("config が不正なのに claude を起動した: %v", calls)
	}
}

func TestConfigWithUnknownKeyIsRejectedNamingTheKey(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(fmt.Sprintf("[issue]\nrepo = %q\nready_label = %q\nreadylabel = \"x\"\n\n[limits]\nmax_wip = 2\n", defaultIssueRepo, defaultReadyLabel))

	r := s.tick()

	assertConfigErrorWithoutObserving(t, s, r, "readylabel")
}

func TestConfigWithUnknownTableIsRejectedNamingTheTable(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(s.defaultConfig() + "\n[limit]\nmax_wip = 3\n")

	r := s.tick()

	assertConfigErrorWithoutObserving(t, s, r, "limit")
}

func TestConfigMissingARequiredKeyIsRejectedNamingTheKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  string
		missing string
	}{
		{"repo", fmt.Sprintf("[issue]\nready_label = %q\n\n[limits]\nmax_wip = 2\n", defaultReadyLabel), "repo"},
		{"ready_label", fmt.Sprintf("[issue]\nrepo = %q\n\n[limits]\nmax_wip = 2\n", defaultIssueRepo), "ready_label"},
		{"max_wip", fmt.Sprintf("[issue]\nrepo = %q\nready_label = %q\n", defaultIssueRepo, defaultReadyLabel), "max_wip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeConfig(tc.config)

			r := s.tick()

			assertConfigErrorWithoutObserving(t, s, r, tc.missing)
		})
	}
}

func TestConfigWithWrongTypeOrRangeIsRejected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits string
	}{
		{"max_wip が 0", "max_wip = 0"},
		{"max_wip が文字列", `max_wip = "2"`},
		{"max_wip が真偽値", "max_wip = true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeConfig(fmt.Sprintf("[issue]\nrepo = %q\nready_label = %q\n\n[limits]\n%s\n", defaultIssueRepo, defaultReadyLabel, tc.limits))

			r := s.tick()

			assertConfigErrorWithoutObserving(t, s, r, "max_wip")
		})
	}
}

func TestConfigWithEmptyClTableIsRejected(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(s.defaultConfig() + "\n[cl]\n")

	r := s.tick()

	assertConfigErrorWithoutObserving(t, s, r, "cl")
}

func TestConfigWithEmptyAuthTableIsRejected(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(s.defaultConfig() + "\n[auth]\n")

	r := s.tick()

	assertConfigErrorWithoutObserving(t, s, r, "auth")
}

func TestConfigWithUnsupportedTrackerIsRejectedNamingIt(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(fmt.Sprintf("[issue]\nrepo = %q\nready_label = %q\ntracker = \"gitlab\"\n\n[limits]\nmax_wip = 2\n", defaultIssueRepo, defaultReadyLabel))

	r := s.tick()

	assertConfigErrorWithoutObserving(t, s, r, "gitlab")
}

func TestTriageLabelSpelledLikeTheReadyLabelIsRejected(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(fmt.Sprintf("[issue]\nrepo = %q\nready_label = %q\ntriage_label = %q\n\n[limits]\nmax_wip = 2\n",
		defaultIssueRepo, defaultReadyLabel, defaultReadyLabel))

	r := s.tick()

	assertConfigErrorWithoutObserving(t, s, r, "triage_label")
}

func TestNonexistentRepoIsRejectedNamingTheRepo(t *testing.T) {
	s := newSandbox(t)
	s.respond("gh", stubRule{ArgsPrefix: []string{"repo", "view"}, Stderr: "GraphQL: Could not resolve to a Repository\n", Exit: 1})

	r := s.tick()

	assertConfigErrorWithoutObserving(t, s, r, defaultIssueRepo)
}

func TestClRepoIsVerifiedToo(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(s.defaultConfig() + "\n[cl]\nrepo = \"acme/other\"\n")
	s.respond("gh", stubRule{ArgsPrefix: []string{"repo", "view", "acme/other"}, Stderr: "GraphQL: Could not resolve to a Repository\n", Exit: 1})

	r := s.tick()

	assertConfigErrorWithoutObserving(t, s, r, "acme/other")
}

func TestMissingMechanismLabelIsRejectedNamingTheLabel(t *testing.T) {
	for _, missing := range []string{wipLabel, humanLabel} {
		t.Run(missing, func(t *testing.T) {
			s := newSandbox(t)
			present := wipLabel
			if missing == wipLabel {
				present = humanLabel
			}
			s.setLabels(present, defaultReadyLabel)

			r := s.tick()

			assertConfigErrorWithoutObserving(t, s, r, missing)
		})
	}
}

func TestDeclaredTriageLabelMustExist(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(fmt.Sprintf("[issue]\nrepo = %q\nready_label = %q\ntriage_label = \"triage-me\"\n\n[limits]\nmax_wip = 2\n",
		defaultIssueRepo, defaultReadyLabel))

	r := s.tick()

	assertConfigErrorWithoutObserving(t, s, r, "triage-me")
}

func TestVerifiedConfigLeavesItsSha256AsTheMarker(t *testing.T) {
	s := newSandbox(t)

	r := s.tick()

	assertExit(t, r, 0)
	sum := sha256.Sum256(mustRead(t, s.configFile()))
	if got, want := strings.TrimSpace(string(mustRead(t, s.markerFile()))), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("config-verified = %q, want %q", got, want)
	}
}

func TestUnchangedConfigIsNotVerifiedAgain(t *testing.T) {
	s := newSandbox(t)
	assertExit(t, s.tick(), 0)
	before := len(s.verificationCalls())

	r := s.tick()

	assertExit(t, r, 0)
	if after := len(s.verificationCalls()); after != before {
		t.Fatalf("変わらない config を再検査した: repo view / label list が %d 回増えた", after-before)
	}
	if len(s.observationCalls()) == 0 {
		t.Fatal("観測していない")
	}
}

func TestChangedConfigIsVerifiedAgain(t *testing.T) {
	s := newSandbox(t)
	assertExit(t, s.tick(), 0)
	before := len(s.verificationCalls())
	s.writeConfig(strings.Replace(s.defaultConfig(), "max_wip = 2", "max_wip = 3", 1))

	r := s.tick()

	assertExit(t, r, 0)
	if after := len(s.verificationCalls()); after == before {
		t.Fatal("変わった config を再検査していない")
	}
}

func TestConfigThatFailedVerificationLeavesNoMarker(t *testing.T) {
	s := newSandbox(t)
	s.setLabels(defaultReadyLabel)

	assertExit(t, s.tick(), 2)

	if _, err := os.Stat(s.markerFile()); !os.IsNotExist(err) {
		t.Fatalf("検査に落ちた config の marker が残っている: %v", err)
	}
}

func TestMissingConfigIsAConfigErrorNamingThePath(t *testing.T) {
	s := newSandbox(t)
	if err := os.Remove(s.configFile()); err != nil {
		t.Fatal(err)
	}

	r := s.tick()

	assertConfigErrorWithoutObserving(t, s, r, s.configFile())
}

// --- 認証 ---

func (s *sandbox) writeTokenFile(name, content string, mode os.FileMode) string {
	s.t.Helper()
	file := filepath.Join(s.configDir(), name)
	if err := os.WriteFile(file, []byte(content), mode); err != nil {
		s.t.Fatal(err)
	}
	if err := os.Chmod(file, mode); err != nil {
		s.t.Fatal(err)
	}
	return file
}

func TestTokenFileReachesGhWhenEnvHasNoToken(t *testing.T) {
	s := newSandbox(t)
	file := s.writeTokenFile("gh-token", "ghp_abc\ndef\n", 0o600)
	s.writeConfig(s.defaultConfig() + fmt.Sprintf("\n[auth]\ntoken_file = %q\n", file))

	r := s.tick()

	assertExit(t, r, 0)
	for _, c := range s.calls("gh") {
		if c.Env["GH_TOKEN"] != "ghp_abcdef" {
			t.Fatalf("gh %v に token file の中身 (空白を除いたもの) が GH_TOKEN として届いていない: %q", c.args(), c.Env["GH_TOKEN"])
		}
	}
}

func TestEnvTokenWinsOverTokenFileWithoutCheckingTheFile(t *testing.T) {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		t.Run(name, func(t *testing.T) {
			s := newSandbox(t)
			// file は無い。env が勝つなら検査もされない
			s.writeConfig(s.defaultConfig() + fmt.Sprintf("\n[auth]\ntoken_file = %q\n", filepath.Join(s.configDir(), "absent")))

			r := s.runWithEnv(map[string]string{name: "from-env"}, "tick", s.project)

			assertExit(t, r, 0)
			for _, c := range s.calls("gh") {
				if c.Env[name] != "from-env" || (name != "GH_TOKEN" && c.Env["GH_TOKEN"] != "") {
					t.Fatalf("env の token が勝っていない: %v", c.Env)
				}
			}
		})
	}
}

func TestTokenFileReadableByOthersIsAConfigErrorWithoutLeakingTheToken(t *testing.T) {
	s := newSandbox(t)
	file := s.writeTokenFile("gh-token", "ghp_secret_value", 0o644)
	s.writeConfig(s.defaultConfig() + fmt.Sprintf("\n[auth]\ntoken_file = %q\n", file))

	r := s.tick()

	assertConfigErrorWithoutObserving(t, s, r, file)
	if strings.Contains(r.stderr, "ghp_secret_value") || strings.Contains(string(mustRead(t, s.logFile())), "ghp_secret_value") {
		t.Fatal("token が出力に漏れた")
	}
}

func TestTokenFileProblemsAreConfigErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(s *sandbox) string // token_file に書く値を返す
	}{
		{"file が無い", func(s *sandbox) string { return filepath.Join(s.configDir(), "absent") }},
		{"中身が空白だけ", func(s *sandbox) string { return s.writeTokenFile("gh-token", " \n", 0o600) }},
		{"相対 path", func(s *sandbox) string { return "gh-token" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			value := tc.setup(s)
			s.writeConfig(s.defaultConfig() + fmt.Sprintf("\n[auth]\ntoken_file = %q\n", value))

			r := s.tick()

			assertConfigErrorWithoutObserving(t, s, r, "token_file")
		})
	}
}

func TestClaudeTokenFileReachesClaudeButNotGh(t *testing.T) {
	s := newSandbox(t)
	file := s.writeTokenFile("claude-token", "sk-ant-oat\n01-xyz\n", 0o600)
	s.writeConfig(s.defaultConfig() + fmt.Sprintf("\n[auth]\nclaude_token_file = %q\n", file))
	s.setIssues(readyIssue(42))
	s.orchestratorWrites(decisions{Decisions: []decision{{Issue: 42, Action: "skip", Reason: "テスト"}}}.json(t))

	r := s.tick()

	assertExit(t, r, 0)
	orchestrators := s.callsMatching("claude", isOrchestratorCall)
	if len(orchestrators) != 1 || orchestrators[0].Env["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-oat01-xyz" {
		t.Fatalf("orchestrator に claude の token が届いていない: %v", orchestrators)
	}
	for _, c := range s.calls("gh") {
		if c.Env["GH_TOKEN"] != "" || c.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "" {
			t.Fatalf("gh に claude の token が渡った: %v", c.Env)
		}
	}
}

func TestGhAuthFailureIsAnAuthError(t *testing.T) {
	for _, tc := range []struct {
		name string
		rule stubRule
	}{
		{"exit 4", stubRule{ArgsPrefix: []string{"repo", "view"}, Exit: 4, Stderr: "authentication required\n"}},
		{"HTTP 401", stubRule{ArgsPrefix: []string{"repo", "view"}, Exit: 1, Stderr: "HTTP 401: Bad credentials\n"}},
		{"gh auth login の案内", stubRule{ArgsPrefix: []string{"repo", "view"}, Exit: 1, Stderr: "To get started with GitHub CLI, please run:  gh auth login\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.respond("gh", tc.rule)

			r := s.tick()

			assertExit(t, r, 4)
			assertResult(t, s.onlyTickLine(), "auth_error")
		})
	}
}

func TestGhAuthFailureDuringObservationIsAnAuthErrorToo(t *testing.T) {
	s := newSandbox(t)
	s.respond("gh", stubRule{ArgsPrefix: []string{"api", "graphql"}, Exit: 1, Stderr: "HTTP 401: Bad credentials\n"})

	r := s.tick()

	assertExit(t, r, 4)
	assertResult(t, s.onlyTickLine(), "auth_error")
}
