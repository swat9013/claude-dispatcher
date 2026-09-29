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

func (s *sandbox) assertConfigErrorWithoutObserving(r runResult, names ...string) {
	s.t.Helper()
	line := s.assertOutcome(r, outcomeConfigError)
	s.assertErrorNames(line, names...)
	if calls := s.observationCalls(); len(calls) != 0 {
		s.t.Fatalf("config が不正なのに観測した: %v", calls)
	}
	s.assertNoClaude("config が不正なのに")
}

func TestConfigWithUnknownKeyIsRejectedNamingTheKey(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(s.configWith(`readylabel = "x"`, "max_wip = 2"))

	r := s.tick()

	s.assertConfigErrorWithoutObserving(r, "readylabel")
}

func TestConfigWithUnknownTableIsRejectedNamingTheTable(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(s.defaultConfig() + "\n[limit]\nmax_wip = 3\n")

	r := s.tick()

	s.assertConfigErrorWithoutObserving(r, "limit")
}

func TestConfigThatIsNotTOMLIsRejected(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(s.defaultConfig() + "\n[issue\n")

	r := s.tick()

	s.assertConfigErrorWithoutObserving(r, s.configFile())
}

func TestUnreadableConfigIsRejected(t *testing.T) {
	s := newSandbox(t)
	if err := os.Chmod(s.configFile(), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(s.configFile(), 0o644); err != nil {
			t.Error(err)
		}
	})

	r := s.tick()

	s.assertConfigErrorWithoutObserving(r, s.configFile())
}

func TestConfigMissingARequiredKeyIsRejectedNamingTheKey(t *testing.T) {
	for _, tc := range []struct {
		missing string
		config  string
	}{
		{"repo", fmt.Sprintf("[issue]\nready_label = %q\n\n[limits]\nmax_wip = 2\n", defaultReadyLabel)},
		{"ready_label", fmt.Sprintf("[issue]\nrepo = %q\n\n[limits]\nmax_wip = 2\n", defaultIssueRepo)},
		{"max_wip", fmt.Sprintf("[issue]\nrepo = %q\nready_label = %q\n", defaultIssueRepo, defaultReadyLabel)},
	} {
		t.Run(tc.missing, func(t *testing.T) {
			s := newSandbox(t)
			s.writeConfig(tc.config)

			r := s.tick()

			s.assertConfigErrorWithoutObserving(r, tc.missing)
		})
	}
}

func TestMaxWIPOtherThanAPositiveIntegerIsRejected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits string
	}{
		{"0", "max_wip = 0"},
		{"文字列", `max_wip = "2"`},
		{"真偽値", "max_wip = true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeConfig(s.configWith("", tc.limits))

			r := s.tick()

			s.assertConfigErrorWithoutObserving(r, "max_wip")
		})
	}
}

func TestEmptyTableThatRequiresKeysIsRejected(t *testing.T) {
	for _, table := range []string{"cl", "auth"} {
		t.Run(table, func(t *testing.T) {
			s := newSandbox(t)
			s.writeConfig(s.defaultConfig() + "\n[" + table + "]\n")

			r := s.tick()

			s.assertConfigErrorWithoutObserving(r, table)
		})
	}
}

func TestUnsupportedTrackerIsRejectedNamingIt(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(s.configWith(`tracker = "gitlab"`, "max_wip = 2"))

	r := s.tick()

	s.assertConfigErrorWithoutObserving(r, "gitlab")
}

func TestTriageLabelSpelledLikeTheReadyLabelIsRejected(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(s.configWith(fmt.Sprintf("triage_label = %q", defaultReadyLabel), "max_wip = 2"))

	r := s.tick()

	s.assertConfigErrorWithoutObserving(r, "triage_label")
}

func TestNonexistentIssueRepoIsRejectedNamingTheRepo(t *testing.T) {
	s := newSandbox(t)
	s.repoMissing()

	r := s.tick()

	s.assertConfigErrorWithoutObserving(r, defaultIssueRepo)
}

func TestNonexistentClRepoIsRejectedNamingTheRepo(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(s.defaultConfig() + "\n[cl]\nrepo = \"acme/other\"\n")
	s.repoMissing("acme/other")

	r := s.tick()

	s.assertConfigErrorWithoutObserving(r, "acme/other")
}

func TestMissingMechanismLabelIsRejectedNamingTheLabel(t *testing.T) {
	for _, tc := range []struct{ missing, present string }{
		{wipLabel, humanLabel},
		{humanLabel, wipLabel},
	} {
		t.Run(tc.missing, func(t *testing.T) {
			s := newSandbox(t)
			s.setLabels(tc.present, defaultReadyLabel)

			r := s.tick()

			s.assertConfigErrorWithoutObserving(r, tc.missing)
		})
	}
}

func TestDeclaredTriageLabelMustExist(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig(s.configWith(`triage_label = "triage-me"`, "max_wip = 2"))

	r := s.tick()

	s.assertConfigErrorWithoutObserving(r, "triage-me")
}

func TestMissingConfigIsAConfigErrorNamingThePath(t *testing.T) {
	s := newSandbox(t)
	if err := os.Remove(s.configFile()); err != nil {
		t.Fatal(err)
	}

	r := s.tick()

	s.assertConfigErrorWithoutObserving(r, s.configFile())
}

// --- 検査済み marker ---

func TestVerifiedConfigLeavesItsSha256AsTheMarker(t *testing.T) {
	s := newSandbox(t)

	assertExit(t, s.tick(), 0)

	sum := sha256.Sum256(mustRead(t, s.configFile()))
	if got, want := strings.TrimSpace(string(mustRead(t, s.markerFile()))), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("config-verified = %q, want %q", got, want)
	}
}

func TestUnchangedConfigIsNotVerifiedAgain(t *testing.T) {
	s := newSandbox(t)
	assertExit(t, s.tick(), 0)
	before := len(s.verificationCalls())

	assertExit(t, s.tick(), 0)

	if after := len(s.verificationCalls()); after != before {
		t.Fatalf("変わらない config を再検査した: repo view / label list が %d 回増えた", after-before)
	}
	if len(s.observationCalls()) < 2 {
		t.Fatal("再検査を省いた tick が観測していない")
	}
}

func TestChangedConfigIsVerifiedAgain(t *testing.T) {
	s := newSandbox(t)
	assertExit(t, s.tick(), 0)
	before := len(s.verificationCalls())
	s.setMaxWIP(3)

	assertExit(t, s.tick(), 0)

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

// --- 認証 (system.md §8) ---

func writeTokenFile(t *testing.T, file, content string, mode os.FileMode) string {
	t.Helper()
	if err := os.WriteFile(file, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile は umask を通すので、mode を確実に付け直す
	if err := os.Chmod(file, mode); err != nil {
		t.Fatal(err)
	}
	return file
}

func (s *sandbox) writeAuth(key, path string) {
	s.writeConfig(s.defaultConfig() + fmt.Sprintf("\n[auth]\n%s = %q\n", key, path))
}

func TestTokenFileReachesGhWhenEnvHasNoToken(t *testing.T) {
	s := newSandbox(t)
	s.writeAuth("token_file", writeTokenFile(t, filepath.Join(s.configDir(), "gh-token"), "ghp_abc\ndef\n", 0o600))

	assertExit(t, s.tick(), 0)

	for _, c := range s.calls("gh") {
		if c.Env["GH_TOKEN"] != "ghp_abcdef" {
			t.Fatalf("gh %v に token file の中身 (空白を除いたもの) が GH_TOKEN として届いていない: %q", c.args(), c.Env["GH_TOKEN"])
		}
	}
}

func TestTokenFileUnderTildeIsReadFromHome(t *testing.T) {
	s := newSandbox(t)
	writeTokenFile(t, filepath.Join(s.home, "gh-token"), "ghp_home", 0o600)
	s.writeAuth("token_file", "~/gh-token")

	assertExit(t, s.tick(), 0)

	for _, c := range s.calls("gh") {
		if c.Env["GH_TOKEN"] != "ghp_home" {
			t.Fatalf("~ 始まりの token file が HOME の下から読まれていない: %q", c.Env["GH_TOKEN"])
		}
	}
}

func TestEnvTokenWinsOverTokenFileWithoutReadingTheFile(t *testing.T) {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		t.Run(name, func(t *testing.T) {
			s := newSandbox(t)
			// file は無い。env が勝つなら読まれもしない (system.md §8)
			s.writeAuth("token_file", filepath.Join(s.configDir(), "absent"))

			r := s.runWithEnv(map[string]string{name: "from-env"}, "tick", s.project)

			assertExit(t, r, 0)
			for _, c := range s.calls("gh") {
				if c.Env[name] != "from-env" {
					t.Fatalf("env の token が gh に届いていない: %v", c.Env)
				}
			}
		})
	}
}

func TestTokenFileReadableByOthersIsRejectedWithoutLeakingTheToken(t *testing.T) {
	for _, key := range []string{"token_file", "claude_token_file"} {
		t.Run(key, func(t *testing.T) {
			s := newSandbox(t)
			file := writeTokenFile(t, filepath.Join(s.configDir(), "token"), "secret_value_123", 0o644)
			s.writeAuth(key, file)

			r := s.tick()

			s.assertConfigErrorWithoutObserving(r, file)
			if strings.Contains(r.stderr, "secret_value_123") || strings.Contains(string(mustRead(t, s.logFile())), "secret_value_123") {
				t.Fatal("token が出力に漏れた")
			}
		})
	}
}

func TestTokenFileProblemsAreConfigErrors(t *testing.T) {
	for _, key := range []string{"token_file", "claude_token_file"} {
		for _, tc := range []struct {
			name string
			path func(s *sandbox) string
		}{
			{"file が無い", func(s *sandbox) string { return filepath.Join(s.configDir(), "absent") }},
			{"中身が空白だけ", func(s *sandbox) string {
				return writeTokenFile(s.t, filepath.Join(s.configDir(), "token"), " \n", 0o600)
			}},
			{"相対 path", func(s *sandbox) string { return "token" }},
		} {
			t.Run(key+"/"+tc.name, func(t *testing.T) {
				s := newSandbox(t)
				s.writeAuth(key, tc.path(s))

				r := s.tick()

				s.assertConfigErrorWithoutObserving(r, key)
			})
		}
	}
}

func TestClaudeTokenFileReachesClaudeButNotGh(t *testing.T) {
	s := newSandbox(t)
	s.writeAuth("claude_token_file", writeTokenFile(t, filepath.Join(s.configDir(), "claude-token"), "sk-ant-oat\n01-xyz\n", 0o600))
	s.setIssues(readyIssue(42))
	s.orchestratorSkips(42)

	assertExit(t, s.tick(), 0)

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
		name   string
		exit   int
		stderr string
	}{
		{"exit 4", 4, "authentication required\n"},
		{"HTTP 401", 1, "HTTP 401: Bad credentials\n"},
		{"gh auth login の案内", 1, "To get started with GitHub CLI, please run:  gh auth login\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.ghFails([]string{"repo", "view"}, tc.exit, tc.stderr)

			r := s.tick()

			s.assertOutcome(r, outcomeAuthError)
		})
	}
}

func TestGhAuthFailureDuringObservationIsAnAuthError(t *testing.T) {
	s := newSandbox(t)
	s.ghFails([]string{"api", "graphql"}, 1, "HTTP 401: Bad credentials\n")

	r := s.tick()

	s.assertOutcome(r, outcomeAuthError)
}

// 綴りを直しても直らない gh の失敗を config_error にすると、読み手が config を探し回る (system.md §8)
func TestGhFailureOtherThanAnUnseenRepoDuringVerificationIsAnError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix []string
		stderr string
	}{
		{"repo view が 5xx", []string{"repo", "view"}, "HTTP 502: Bad Gateway\n"},
		{"label list が network 断", []string{"label", "list"}, "error connecting to api.github.com\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.ghFails(tc.prefix, 1, tc.stderr)

			r := s.tick()

			s.assertOutcome(r, outcomeError)
		})
	}
}

// label を名前で 1 つずつ確かめるので、repo の label の総数では止まらない
func TestRepoWithManyLabelsPassesTheVerification(t *testing.T) {
	s := newSandbox(t)
	names := []string{wipLabel, humanLabel, defaultReadyLabel}
	for i := len(names); i < 1000; i++ {
		names = append(names, fmt.Sprintf("label-%d", i))
	}
	s.setLabels(names...)

	r := s.tick()

	s.assertOutcome(r, outcomeOK)
}
