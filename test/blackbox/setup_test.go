package blackbox_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// `setup <project>` (formats.md §11)。書くもの (config の雛形・label・crontab) は導入者の承認の後だけ。

// newInstallSandbox は setup / doctor が cron 相当の試運転 (`tick --dry-run --cron-env`) まで通せる sandbox を作る。
// 撃ち直しは XDG_* と親の PATH を剥がすので、置き場は $HOME の既定、依存 CLI は自己解決の置き場 (~/.local/bin) にも置く。
func newInstallSandbox(t *testing.T) *sandbox {
	s := newSandboxWithHomeDefaults(t)
	s.installStubs(filepath.Join(s.home, ".local", "bin"))
	s.respond("git", stubwire.Rule{ArgsPrefix: []string{"remote", "get-url", "origin"}, Stdout: "git@github.com:" + defaultIssueRepo + ".git\n"})
	return s
}

func (s *sandbox) setup(input string) runResult {
	s.t.Helper()
	return s.runWithInput(input, "setup", s.project)
}

// tickCronLine は setup が組む crontab の行 (formats.md §11)。
func (s *sandbox) tickCronLine() string {
	return fmt.Sprintf("*/5 * * * * cd %s && %s tick %s >> %s 2>&1", s.clone, dispatcherBin, s.project, filepath.Join(s.stateDir(), "cron.log"))
}

func (s *sandbox) setCrontab(content string) {
	mustWrite(s.t, stubwire.CrontabFile(s.stubRoot), content)
}

func (s *sandbox) crontab() string {
	raw, err := os.ReadFile(stubwire.CrontabFile(s.stubRoot))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		s.t.Fatal(err)
	}
	return string(raw)
}

func (s *sandbox) crontabWrites() []stubCall {
	return s.callsMatching("crontab", func(c stubCall) bool { return c.hasArg("-") })
}

func (s *sandbox) labelCreates() []stubCall {
	return s.callsMatching("gh", func(c stubCall) bool { return c.hasPrefix("label", "create") })
}

func TestSetupWritesAConfigTemplateAndTheStateDirThenStopsForTheHumanToFillIt(t *testing.T) {
	s := newInstallSandbox(t)
	for _, dir := range []string{s.configDir(), s.stateDir()} {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}

	r := s.setup("")

	assertExit(t, r, 1)
	if config := string(mustRead(t, s.configFile())); !strings.Contains(config, `repo = "`+defaultIssueRepo+`"`) {
		t.Fatalf("雛形に clone の origin が入っていない:\n%s", config)
	}
	if info, err := os.Stat(s.stateDir()); err != nil || !info.IsDir() {
		t.Fatal("state dir を作っていない")
	}
	s.assertNames(r.stdout+r.stderr, s.configFile())
	if len(s.crontabWrites()) != 0 || len(s.labelCreates()) != 0 {
		t.Fatal("雛形を書いただけの段で label か crontab に書いた")
	}
}

func TestSetupNeverOverwritesAnExistingConfig(t *testing.T) {
	s := newInstallSandbox(t)
	before := string(mustRead(t, s.configFile()))

	s.setup("y\n")

	if after := string(mustRead(t, s.configFile())); after != before {
		t.Fatalf("既存の config を書き換えた:\n%s", after)
	}
}

func TestSetupWithAMisspelledConfigStopsNamingIt(t *testing.T) {
	s := newInstallSandbox(t)
	s.writeConfig(s.configWith(`readylabel = "x"`, "max_wip = 2"))

	r := s.setup("y\n")

	assertExit(t, r, 2)
	s.assertNames(r.stderr, "readylabel")
}

func TestSetupWithAMissingTokenFileIsAConfigError(t *testing.T) {
	s := newInstallSandbox(t)
	s.writeConfig(s.defaultConfig() + "\n[auth]\ntoken_file = \"~/no-such-token\"\n")

	r := s.setup("y\n")

	assertExit(t, r, 2)
	s.assertNames(r.stderr, "token_file")
}

func TestSetupCreatesMissingLabelsWhenApproved(t *testing.T) {
	s := newInstallSandbox(t)
	s.setLabels(humanLabel, defaultReadyLabel)
	s.respond("gh", stubwire.Rule{ArgsPrefix: []string{"label", "create"}})

	r := s.setup("y\n")

	if creates := s.labelCreates(); len(creates) != 1 || !creates[0].hasArg(wipLabel) {
		t.Fatalf("label create = %v\n%s%s", creates, r.stdout, r.stderr)
	}
}

func TestSetupDoesNotCreateLabelsWithoutApprovalAndShowsTheCommands(t *testing.T) {
	for _, tc := range []struct{ name, input string }{{"no-answer", ""}, {"n", "n\n"}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newInstallSandbox(t)
			s.setLabels(humanLabel, defaultReadyLabel)

			r := s.setup(tc.input)

			assertExit(t, r, 1)
			if creates := s.labelCreates(); len(creates) != 0 {
				t.Fatalf("承認なしに label を作った: %v", creates)
			}
			if !strings.Contains(r.stdout, "gh label create \""+wipLabel+"\"") {
				t.Fatalf("自分で作るコマンドを示していない:\n%s%s", r.stdout, r.stderr)
			}
		})
	}
}

func TestSetupRunsBothDryRunsWithoutStartingClaude(t *testing.T) {
	s := newInstallSandbox(t)
	s.setIssues(readyIssue(42))

	r := s.setup("")

	if strings.Count(r.stdout, `"dry_run":true`) != 2 {
		t.Fatalf("試運転 2 本の出力が無い:\n%s", r.stdout)
	}
	s.assertNoClaude("setup の試運転で")
}

func TestSetupStopsWhenADryRunFails(t *testing.T) {
	s := newInstallSandbox(t)
	s.ghFails([]string{"issue", "list"}, 1, "HTTP 502: Bad Gateway\n")

	r := s.setup("y\n")

	assertExit(t, r, 1)
	if calls := s.calls("crontab"); len(calls) != 0 {
		t.Fatalf("試運転が落ちたのに crontab を触った: %v", calls)
	}
}

func TestSetupRegistersTheCrontabLineWhenApproved(t *testing.T) {
	s := newInstallSandbox(t)
	s.setCrontab("0 3 * * * /usr/bin/backup\n")

	r := s.setup("yes\n")

	assertExit(t, r, 0)
	if got := s.crontab(); got != "0 3 * * * /usr/bin/backup\n"+s.tickCronLine()+"\n" {
		t.Fatalf("crontab = %q", got)
	}
}

func TestSetupDoesNotRegisterTheCrontabLineWithoutApprovalAndShowsIt(t *testing.T) {
	for _, tc := range []struct{ name, input string }{{"no-answer", ""}, {"n", "n\n"}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newInstallSandbox(t)
			s.setCrontab("0 3 * * * /usr/bin/backup\n")

			r := s.setup(tc.input)

			assertExit(t, r, 1)
			if len(s.crontabWrites()) != 0 {
				t.Fatal("承認なしに crontab を書いた")
			}
			if !strings.Contains(r.stdout, s.tickCronLine()) {
				t.Fatalf("足す行を示していない:\n%s", r.stdout)
			}
		})
	}
}

func TestSetupStopsWhenTheCrontabRefusesTheWrite(t *testing.T) {
	s := newInstallSandbox(t)
	mustWrite(t, stubwire.CrontabRefuseFile(s.stubRoot), "")

	r := s.setup("y\n")

	assertExit(t, r, 1)
	if !strings.Contains(r.stderr, "crontab に書けない") || !strings.Contains(r.stdout, s.tickCronLine()) {
		t.Fatalf("書けなかったことと自分で足す行を示していない:\n%s%s", r.stdout, r.stderr)
	}
}

func TestSetupWithTheLineAlreadyRegisteredEndsWithoutAsking(t *testing.T) {
	s := newInstallSandbox(t)
	s.setCrontab(s.tickCronLine() + "\n")

	r := s.setup("")

	assertExit(t, r, 0)
	if len(s.crontabWrites()) != 0 {
		t.Fatal("登録済みの crontab を書き直した")
	}
}

func TestSetupShowsButDoesNotReplaceADifferentTickLineOfTheProject(t *testing.T) {
	s := newInstallSandbox(t)
	existing := "*/10 * * * * cd /elsewhere && /old/claude-dispatcher tick " + s.project + " >> /tmp/cron.log 2>&1\n"
	s.setCrontab(existing)

	r := s.setup("y\n")

	assertExit(t, r, 1)
	if len(s.crontabWrites()) != 0 || s.crontab() != existing {
		t.Fatal("既存の tick 行を置き換えた")
	}
	if !strings.Contains(r.stdout, strings.TrimSpace(existing)) || !strings.Contains(r.stdout, s.tickCronLine()) {
		t.Fatalf("現行の行と組んだ行を並べていない:\n%s", r.stdout)
	}
}

func TestSetupRunTwiceConvergesWithoutWritingAgain(t *testing.T) {
	s := newInstallSandbox(t)
	assertExit(t, s.setup("y\n"), 0)
	writes := len(s.crontabWrites())

	r := s.setup("")

	assertExit(t, r, 0)
	if len(s.crontabWrites()) != writes || strings.Count(s.crontab(), " tick "+s.project) != 1 {
		t.Fatalf("2 回目の setup が crontab を書いた: %q", s.crontab())
	}
}
