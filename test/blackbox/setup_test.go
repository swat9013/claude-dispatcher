package blackbox_test

import (
	"os"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// `setup <project>` (formats.md §11)。書くもの (config の雛形・label) は導入者の承認の後だけ。

// newInstallSandbox は setup / doctor が雛形の origin を clone から読める sandbox を作る。
func newInstallSandbox(t *testing.T) *sandbox {
	s := newSandbox(t)
	s.respond("git", stubwire.Rule{ArgsPrefix: []string{"remote", "get-url", "origin"}, Stdout: "git@github.com:" + defaultIssueRepo + ".git\n"})
	return s
}

func (s *sandbox) setup(input string) runResult {
	s.t.Helper()
	return s.runWithInput(input, "setup", s.project)
}

// loopCommandLine は setup が最後に示す loop の起動コマンド (formats.md §11)。
func (s *sandbox) loopCommandLine() string {
	return "cd " + s.clone + " && claude-dispatcher loop " + s.project + " 5m"
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
	if len(s.labelCreates()) != 0 {
		t.Fatal("雛形を書いただけの段で label を作った")
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

func TestSetupRunsTheDryRunInProcessWithoutStartingClaude(t *testing.T) {
	s := newInstallSandbox(t)
	s.setIssues(readyIssue(42))

	r := s.setup("")

	assertExit(t, r, 0)
	if strings.Count(r.stdout, `"dry_run":true`) != 1 {
		t.Fatalf("試運転 1 本の出力が無い:\n%s", r.stdout)
	}
	s.assertNoClaude("setup の試運転で")
	s.assertNoLog()
}

func TestSetupStopsWithTheDryRunExitCodeWhenTheDryRunFails(t *testing.T) {
	s := newInstallSandbox(t)
	s.ghFails([]string{"issue", "list"}, 1, "HTTP 502: Bad Gateway\n")

	r := s.setup("y\n")

	assertExit(t, r, 1)
	if strings.Contains(r.stdout, s.loopCommandLine()) {
		t.Fatalf("試運転が落ちたのに loop の起動コマンドを示した:\n%s", r.stdout)
	}
	if !strings.Contains(r.stderr, "502") {
		t.Fatalf("試運転の出力をそのまま示していない:\n%s", r.stderr)
	}
}

func TestSetupEndsByShowingTheLoopCommandWithoutStartingIt(t *testing.T) {
	s := newInstallSandbox(t)

	r := s.setup("")

	assertExit(t, r, 0)
	if !strings.Contains(r.stdout, s.loopCommandLine()) {
		t.Fatalf("loop の起動コマンド %q を示していない:\n%s", s.loopCommandLine(), r.stdout)
	}
	s.assertNoLog()
}

func TestSetupRunTwiceConvergesWithoutWritingAgain(t *testing.T) {
	s := newInstallSandbox(t)
	assertExit(t, s.setup("y\n"), 0)
	config := string(mustRead(t, s.configFile()))

	r := s.setup("y\n")

	assertExit(t, r, 0)
	if len(s.labelCreates()) != 0 || string(mustRead(t, s.configFile())) != config {
		t.Fatal("揃った導入に 2 回目の setup が書いた")
	}
}

// assertNoLog は log.jsonl が書かれていないことを確かめる (試運転は state dir に何も書かない)。
func (s *sandbox) assertNoLog() {
	s.t.Helper()
	if _, err := os.Stat(s.logFile()); !os.IsNotExist(err) {
		s.t.Fatal("log.jsonl を書いた")
	}
}
