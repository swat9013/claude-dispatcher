package blackbox_test

import (
	"os"
	"path/filepath"
	"testing"
)

// playbook の解決 (system.md §10 / §11)。plugin swat-skills の installPath を installed_plugins.json から引き、
// project scope (projectPath = cwd) → user scope → ~/.claude/skills/swat-skills の順に選ぶ。

func startPlaybooks(t *testing.T, s *sandbox) map[string]string {
	t.Helper()
	starts := instructionsOfKind(s.onlyInstructionFile(), "start")
	if len(starts) != 1 {
		t.Fatalf("start 指示 = %v", starts)
	}
	out := map[string]string{}
	for _, raw := range starts[0]["playbooks"].([]any) {
		p := raw.(map[string]any)
		out[p["path"].(string)] = p["dispatch_when"].(string)
	}
	return out
}

func TestStartCarriesEveryMarkedPlaybookWithItsDispatchWhen(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.orchestratorSkips(42)

	s.tick()

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
	s.setIssues(readyIssue(42))
	s.orchestratorSkips(42)

	s.tick()

	if _, ok := startPlaybooks(t, s)[playbookPath(project, "playbook-implementation")]; !ok {
		t.Fatalf("clone の project scope の install を選んでいない: %v", startPlaybooks(t, s))
	}
}

func TestProjectScopeInstallForAnotherCloneIsIgnored(t *testing.T) {
	s := newSandbox(t)
	s.installPlugin("swat-skills@swat9013", "project", filepath.Join(s.root, "another-clone"))
	s.setIssues(readyIssue(42))
	s.orchestratorSkips(42)

	s.tick()

	if _, ok := startPlaybooks(t, s)[playbookPath(s.defaultInstallPath(), "playbook-implementation")]; !ok {
		t.Fatalf("user scope の install を選んでいない: %v", startPlaybooks(t, s))
	}
}

func TestSkillsDirIsUsedWhenNoPluginIsInstalled(t *testing.T) {
	s := newSandbox(t)
	s.removeInstalledPlugins()
	skillsDir := filepath.Join(s.home, ".claude", "skills", "swat-skills")
	writePluginTree(t, skillsDir)
	s.setIssues(readyIssue(42))
	s.orchestratorSkips(42)

	s.tick()

	if _, ok := startPlaybooks(t, s)[playbookPath(skillsDir, "playbook-implementation")]; !ok {
		t.Fatalf("~/.claude/skills/swat-skills を選んでいない: %v", startPlaybooks(t, s))
	}
}

func TestPluginFromTwoMarketplacesStopsTheTick(t *testing.T) {
	s := newSandbox(t)
	s.installPlugin("swat-skills@a-fork", "user", "")
	s.setIssues(readyIssue(42))

	r := s.tick()

	assertExit(t, r, 1)
	line := s.onlyTickLine()
	assertResult(t, line, "error")
	assertErrorMentions(t, line, "swat-skills")
	if files := s.instructionFiles(); len(files) != 0 {
		t.Fatalf("playbook を解決できないのに指示ファイルを書いた: %v", files)
	}
	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("playbook を解決できないのに claude を起動した: %v", calls)
	}
}

func TestMarkedPlaybookWithoutDispatchWhenStopsTheTickNamingIt(t *testing.T) {
	s := newSandbox(t)
	broken := filepath.Join(s.defaultInstallPath(), "skills", "procedure", "playbook-broken")
	writeSkill(t, broken, "name: playbook-broken\nmetadata:\n  deliverable: cl\n")
	s.setIssues(readyIssue(42))

	r := s.tick()

	assertExit(t, r, 1)
	assertErrorMentions(t, s.onlyTickLine(), filepath.Join(broken, "SKILL.md"))
	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("母集合を決められないのに claude を起動した: %v", calls)
	}
}

func TestUnreadableFrontmatterStopsTheTick(t *testing.T) {
	s := newSandbox(t)
	broken := filepath.Join(s.defaultInstallPath(), "skills", "procedure", "playbook-broken")
	mustMkdir(t, broken)
	mustWrite(t, filepath.Join(broken, "SKILL.md"), "# frontmatter が無い\n")
	s.setIssues(readyIssue(42))

	r := s.tick()

	assertExit(t, r, 1)
	assertErrorMentions(t, s.onlyTickLine(), filepath.Join(broken, "SKILL.md"))
}

func TestOrchestratorPromptCarriesTheResolvedPrincipleIndexPath(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.orchestratorSkips(42)

	s.tick()

	calls := s.callsMatching("claude", isOrchestratorCall)
	if len(calls) != 1 {
		t.Fatalf("orchestrator の起動 = %d 回", len(calls))
	}
	index := principleIndexPath(s.defaultInstallPath())
	if _, err := os.Stat(index); err != nil {
		t.Fatal(err)
	}
	if !calls[0].contains(index) {
		t.Fatalf("orchestrator の prompt に原則索引の絶対 path %s が無い", index)
	}
}
