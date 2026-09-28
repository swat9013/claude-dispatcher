package blackbox_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// playbook の解決 (system.md §10 / §11)。plugin swat-skills の installPath を installed_plugins.json から引き、
// project scope (projectPath = cwd) → user scope → ~/.claude/skills/swat-skills の順に選ぶ。

func startPlaybooks(t *testing.T, s *sandbox) map[string]string {
	t.Helper()
	starts := instructionsOfKind(t, s.onlyInstructionFile(), "start")
	if len(starts) != 1 {
		t.Fatalf("start 指示 = %v", starts)
	}
	out := map[string]string{}
	for _, raw := range asList(t, starts[0]["playbooks"]) {
		p := asMap(t, raw)
		out[asString(t, p["path"])] = asString(t, p["dispatch_when"])
	}
	return out
}

// assertPlaybooksOnlyFrom は start の選定母集合が installPath の playbook だけから成ることを確かめる。
func assertPlaybooksOnlyFrom(t *testing.T, s *sandbox, installPath string) {
	t.Helper()
	got := startPlaybooks(t, s)
	if _, ok := got[playbookPath(installPath, "playbook-implementation")]; !ok {
		t.Fatalf("%s の install を選んでいない: %v", installPath, got)
	}
	for path := range got {
		if !strings.HasPrefix(path, installPath+"/") {
			t.Fatalf("%s 以外の install の playbook が混ざった: %v", installPath, got)
		}
	}
}

func (s *sandbox) tickWithOneCandidate() {
	s.t.Helper()
	s.setIssues(readyIssue(42))
	s.orchestratorSkips(42)
	assertExit(s.t, s.tick(), 0)
}

func TestStartCarriesEveryMarkedPlaybookWithItsDispatchWhen(t *testing.T) {
	s := newSandbox(t)

	s.tickWithOneCandidate()

	install := s.defaultInstallPath()
	got := startPlaybooks(t, s)
	want := map[string]string{
		playbookPath(install, "playbook-implementation"): defaultDispatchWhen,
		playbookPath(install, "playbook-docs"):           docsDispatchWhen,
	}
	if len(got) != len(want) {
		t.Fatalf("選定母集合 = %v, want %v (印の無い playbook は入らない)", got, want)
	}
	for path, when := range want {
		if got[path] != when {
			t.Fatalf("選定母集合 = %v, want %v", got, want)
		}
	}
}

func TestProjectScopeInstallForTheCloneWinsOverUserScope(t *testing.T) {
	s := newSandbox(t)
	project := s.installPlugin("swat-skills@swat9013", "project", s.clone)

	s.tickWithOneCandidate()

	assertPlaybooksOnlyFrom(t, s, project)
}

func TestProjectScopeInstallForAnotherCloneIsIgnored(t *testing.T) {
	s := newSandbox(t)
	s.installPlugin("swat-skills@swat9013", "project", filepath.Join(s.root, "another-clone"))

	s.tickWithOneCandidate()

	assertPlaybooksOnlyFrom(t, s, s.defaultInstallPath())
}

func TestSkillsDirIsUsedWhenNoInstallMatchesTheClone(t *testing.T) {
	skillsDir := func(s *sandbox) string { return filepath.Join(s.home, ".claude", "skills", "swat-skills") }
	for _, tc := range []struct {
		name  string
		setup func(s *sandbox)
	}{
		{"installed_plugins.json が無い", func(s *sandbox) { s.removeInstalledPlugins() }},
		{"entry が別 clone の project scope だけ", func(s *sandbox) {
			s.clearPluginEntries()
			s.installPlugin("swat-skills@swat9013", "project", filepath.Join(s.root, "another-clone"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			tc.setup(s)
			writePluginTree(t, skillsDir(s))

			s.tickWithOneCandidate()

			assertPlaybooksOnlyFrom(t, s, skillsDir(s))
		})
	}
}

func TestPluginFromTwoMarketplacesStopsTheTick(t *testing.T) {
	s := newSandbox(t)
	s.installPlugin("swat-skills@a-fork", "user", "")
	s.setIssues(readyIssue(42))

	r := s.tick()

	s.assertErrorNames(s.assertOutcome(r, outcomeError), "swat-skills")
	s.assertNoInstructionFile("playbook を解決できないのに")
	s.assertNoClaude("playbook を解決できないのに")
}

func TestMarkedPlaybookWithoutDispatchWhenStopsTheTickNamingIt(t *testing.T) {
	s := newSandbox(t)
	broken := filepath.Join(s.defaultInstallPath(), "skills", "procedure", "playbook-broken")
	writeSkill(t, broken, "name: playbook-broken\nmetadata:\n  deliverable: cl\n")
	s.setIssues(readyIssue(42))

	r := s.tick()

	s.assertErrorNames(s.assertOutcome(r, outcomeError), filepath.Join(broken, "SKILL.md"))
	s.assertNoClaude("母集合を決められないのに")
}

func TestUnreadableFrontmatterStopsTheTickNamingThePlaybook(t *testing.T) {
	s := newSandbox(t)
	broken := filepath.Join(s.defaultInstallPath(), "skills", "procedure", "playbook-broken")
	mustMkdir(t, broken)
	mustWrite(t, filepath.Join(broken, "SKILL.md"), "# frontmatter が無い\n")
	s.setIssues(readyIssue(42))

	r := s.tick()

	s.assertErrorNames(s.assertOutcome(r, outcomeError), filepath.Join(broken, "SKILL.md"))
}

func TestUnusablePrincipleIndexStopsTheTickWithoutLaunchingTheOrchestrator(t *testing.T) {
	for _, tc := range []struct {
		name       string
		breakIndex func(t *testing.T, index string)
	}{
		{"file が無い", func(t *testing.T, index string) {
			if err := os.Remove(index); err != nil {
				t.Fatal(err)
			}
		}},
		{"file でなくディレクトリ", func(t *testing.T, index string) {
			if err := os.Remove(index); err != nil {
				t.Fatal(err)
			}
			mustMkdir(t, index)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			index := principleIndexPath(s.defaultInstallPath())
			tc.breakIndex(t, index)
			s.setIssues(readyIssue(42))

			r := s.tick()

			s.assertErrorNames(s.assertOutcome(r, outcomeError), index)
			s.assertNoClaude("原則索引が " + tc.name + "のに")
		})
	}
}

// unmarkStartPlaybooks は選定母集合の印 (metadata.deliverable: cl) を持つ playbook を、印の無い SKILL.md に書き換えて母集合を空にする。
func (s *sandbox) unmarkStartPlaybooks() {
	s.t.Helper()
	files, err := filepath.Glob(filepath.Join(s.defaultInstallPath(), "skills", "procedure", "playbook-*", "SKILL.md"))
	if err != nil {
		s.t.Fatal(err)
	}
	unmarked := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			s.t.Fatal(err)
		}
		if strings.Contains(string(raw), "deliverable: cl") {
			dir := filepath.Dir(file)
			writeSkill(s.t, dir, "name: "+filepath.Base(dir)+"\n")
			unmarked++
		}
	}
	if unmarked == 0 {
		s.t.Fatal("印の付いた playbook が無い")
	}
}

func TestEmptyStartSetYieldsNoStartButStillYieldsReenter(t *testing.T) {
	s := newSandbox(t)
	s.unmarkStartPlaybooks()
	reenterScenario(s)
	s.setIssues(issue{number: 39}, readyIssue(42))
	s.orchestratorSkips(39)

	assertExit(t, s.tick(), 0)

	file := s.onlyInstructionFile()
	if starts := instructionsOfKind(t, file, "start"); len(starts) != 0 {
		t.Fatalf("選定母集合が空なのに start を出した: %v", starts)
	}
	if reenters := instructionsOfKind(t, file, "reenter"); len(reenters) != 1 {
		t.Fatalf("reenter = %v, want 1 件", reenters)
	}
}

func TestEmptyStartSetWithOnlyCandidatesLaunchesNoOrchestrator(t *testing.T) {
	s := newSandbox(t)
	s.unmarkStartPlaybooks()
	s.setIssues(readyIssue(42))

	r := s.tick()

	s.assertOutcome(r, outcomeOK)
	s.assertNoInstructionFile("選定母集合が空で start しか無いのに")
	s.assertNoClaude("選定母集合が空で start しか無いのに")
}

func TestTickThatLaunchesNoOrchestratorDoesNotRequireThePrincipleIndex(t *testing.T) {
	s := newSandbox(t)
	if err := os.Remove(principleIndexPath(s.defaultInstallPath())); err != nil {
		t.Fatal(err)
	}
	// 母集合が空なので start が落ち、指示が残らない
	s.unmarkStartPlaybooks()
	s.setIssues(readyIssue(42))

	r := s.tick()

	s.assertOutcome(r, outcomeOK)
	s.assertNoClaude("orchestrator を起動しない tick なのに")
}

func TestOrchestratorPromptCarriesTheResolvedPrincipleIndexPath(t *testing.T) {
	s := newSandbox(t)

	s.tickWithOneCandidate()

	index := principleIndexPath(s.defaultInstallPath())
	if _, err := os.Stat(index); err != nil {
		t.Fatal(err)
	}
	if !onlyOrchestratorCall(s).contains(index) {
		t.Fatalf("orchestrator の prompt に原則索引の絶対 path %s が無い", index)
	}
}
