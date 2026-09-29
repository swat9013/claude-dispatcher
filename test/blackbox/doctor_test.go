package blackbox_test

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `doctor <project>` (formats.md §12)。導入の充足を 1 項目 1 行で示し、何も書かない。

func (s *sandbox) doctor() runResult {
	s.t.Helper()
	return s.run("doctor", s.project)
}

// doctorLine は項目名の行を返す。無ければ t.Fatal。
func doctorLine(t *testing.T, stdout, item string) string {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && (fields[0] == "ok" || fields[0] == "NG" || fields[0] == "--") && strings.HasPrefix(strings.TrimSpace(line[len(fields[0]):]), item) {
			return line
		}
	}
	t.Fatalf("doctor の出力に %q の行が無い:\n%s", item, stdout)
	return ""
}

func assertDoctorMark(t *testing.T, stdout, item, mark string) string {
	t.Helper()
	line := doctorLine(t, stdout, item)
	if !strings.HasPrefix(line, mark+" ") {
		t.Fatalf("%s の行 = %q, want %s", item, line, mark)
	}
	return line
}

func TestDoctorOnASatisfiedProjectReportsNoNG(t *testing.T) {
	s := newInstallSandbox(t)

	r := s.doctor()

	assertExit(t, r, 0)
	for _, item := range []string{"config", "state dir", "依存 CLI", "置き場", "label", "plugin", "playbook", "原則索引", "試運転"} {
		assertDoctorMark(t, r.stdout, item, "ok")
	}
}

func TestDoctorWritesNothing(t *testing.T) {
	s := newInstallSandbox(t)
	mustMkdir(t, filepath.Join(s.clone, ".claude"))
	mustWrite(t, filepath.Join(s.clone, ".claude", "settings.json"), `{"sandbox": {}}`)
	before := map[string]map[string]string{}
	for _, dir := range []string{s.stateDir(), s.configRoot, s.home, s.clone} {
		before[dir] = fileFingerprints(t, dir)
	}

	s.doctor()

	for dir, fingerprints := range before {
		if !maps.Equal(fingerprints, fileFingerprints(t, dir)) {
			t.Fatalf("doctor が %s の下に書いた", dir)
		}
	}
	if len(s.labelCreates()) != 0 {
		t.Fatal("doctor が label を作った")
	}
}

func TestDoctorShowsTheSettingsEntriesItNeedsWithoutWritingThem(t *testing.T) {
	s := newInstallSandbox(t)

	r := s.doctor()

	for _, want := range []string{"allowWrite", s.stateDir(), "excludedCommands", `"gh"`, `"claude-dispatcher"`} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("settings に要る entry に %q が無い:\n%s", want, r.stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(s.clone, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatal("doctor が settings を作った")
	}
}

func TestDoctorReportsPluginFromTwoMarketplacesAsNG(t *testing.T) {
	s := newInstallSandbox(t)
	s.installPlugin("swat-skills@other", "user", "")

	r := s.doctor()

	assertExit(t, r, 1)
	line := assertDoctorMark(t, r.stdout, "plugin", "NG")
	s.assertNames(line, "swat-skills@other", "swat-skills@swat9013")
}

func TestDoctorFollowsThePluginScopeOrder(t *testing.T) {
	s := newInstallSandbox(t)
	projectInstall := s.installPlugin("swat-skills@swat9013", "project", s.clone)

	r := s.doctor()

	if line := assertDoctorMark(t, r.stdout, "plugin", "ok"); !strings.Contains(line, projectInstall) {
		t.Fatalf("clone の project scope を選んでいない: %q", line)
	}
}

func TestDoctorReportsAMissingPlaybookOrPrincipleIndexAsNG(t *testing.T) {
	for _, tc := range []struct {
		item    string
		missing func(install string) string
	}{
		{"playbook", func(install string) string { return playbookPath(install, "playbook-ci-fix") }},
		{"原則索引", principleIndexPath},
	} {
		t.Run(tc.item, func(t *testing.T) {
			s := newInstallSandbox(t)
			missing := tc.missing(s.defaultInstallPath())
			if err := os.Remove(missing); err != nil {
				t.Fatal(err)
			}

			r := s.doctor()

			assertExit(t, r, 1)
			if line := assertDoctorMark(t, r.stdout, tc.item, "NG"); !strings.Contains(line, missing) {
				t.Fatalf("無い file を名指ししていない: %q", line)
			}
		})
	}
}

func TestDoctorReportsAMissingLabelAsNG(t *testing.T) {
	s := newInstallSandbox(t)
	s.setLabels(humanLabel, defaultReadyLabel)

	r := s.doctor()

	assertExit(t, r, 1)
	s.assertNames(assertDoctorMark(t, r.stdout, "label", "NG"), wipLabel)
}

func TestDoctorShowsTheLastTickAsInformation(t *testing.T) {
	s := newInstallSandbox(t)
	s.writeSpawnedTickLine()

	r := s.doctor()

	if line := assertDoctorMark(t, r.stdout, "最終 tick", "--"); !strings.Contains(line, "2026-09-26T02:48:00Z ok") {
		t.Fatalf("最終 tick の行 = %q", line)
	}
}

func TestDoctorWithABrokenConfigStillChecksTheRest(t *testing.T) {
	s := newInstallSandbox(t)
	s.writeConfig(s.configWith(`readylabel = "x"`, "max_wip = 2"))

	r := s.doctor()

	assertExit(t, r, 1)
	s.assertNames(assertDoctorMark(t, r.stdout, "config", "NG"), "readylabel")
	assertDoctorMark(t, r.stdout, "plugin", "ok")
}

func TestDoctorReportsARepoGhCannotSeeAsNG(t *testing.T) {
	s := newInstallSandbox(t)
	s.repoMissing()

	r := s.doctor()

	assertExit(t, r, 1)
	s.assertNames(assertDoctorMark(t, r.stdout, "置き場", "NG"), defaultIssueRepo)
}

func TestDoctorReportsADependencyItCannotResolveAsNG(t *testing.T) {
	s := newInstallSandbox(t)
	skipIfSelfResolutionReachesARealOne(t, "claude")
	if err := os.Remove(filepath.Join(s.binDir, "claude")); err != nil {
		t.Fatal(err)
	}

	r := s.doctor()

	assertExit(t, r, 1)
	s.assertNames(assertDoctorMark(t, r.stdout, "依存 CLI", "NG"), "claude")
}

func TestDoctorReportsAFailingDryRunAsNGWithItsOutput(t *testing.T) {
	s := newInstallSandbox(t)
	s.ghFails([]string{"issue", "list"}, 1, "HTTP 502: Bad Gateway\n")

	r := s.doctor()

	assertExit(t, r, 1)
	assertDoctorMark(t, r.stdout, "試運転", "NG")
	if !strings.Contains(r.stdout, "--dry-run") || !strings.Contains(r.stdout, "502") {
		t.Fatalf("撃った試運転と落ちた理由を示していない:\n%s", r.stdout)
	}
}

func TestDoctorRunsTheDryRunWithoutWritingTheStateDir(t *testing.T) {
	s := newInstallSandbox(t)

	r := s.doctor()

	if line := assertDoctorMark(t, r.stdout, "試運転", "ok"); !strings.Contains(line, `"dry_run":true`) {
		t.Fatalf("試運転の出力を示していない: %q", line)
	}
	s.assertNoClaude("doctor の試運転で")
}

func TestDoctorCountsTheLogLinesItCouldNotRead(t *testing.T) {
	s := newInstallSandbox(t)
	s.writeSpawnedTickLine()
	appendFile(t, s.logFile(), "{\"ts\": \"2026-09-26T03:00\n")
	// 後片付け (起動した worker を待つ) は log を JSON として読むので、読めない行を消してから渡す
	t.Cleanup(s.writeSpawnedTickLine)

	r := s.doctor()

	if line := assertDoctorMark(t, r.stdout, "最終 tick", "--"); !strings.Contains(line, "読めない 1 行") {
		t.Fatalf("読めない行の件数を示していない: %q", line)
	}
}
