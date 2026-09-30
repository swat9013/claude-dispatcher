package precheck_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/precheck"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// 事前検査 (formats.md §2.9)。置き場の file の並びは、Claude Code が実際に置く形を写している。

// places は HOME と clone (workflow 定義の dir) の空の置き場。
type places struct{ home, clone string }

func newPlaces(t *testing.T) places {
	t.Helper()
	root := t.TempDir()
	return places{home: filepath.Join(root, "home"), clone: filepath.Join(root, "clone")}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// check は action を持つ trigger 1 つの workflow 定義を事前検査し、落ちた理由を返す (通れば "")。
func (p places) check(action string, args ...string) string {
	def := workflow.Definition{Dir: p.clone, Claude: workflow.Claude{Args: args}, Triggers: []trigger.Trigger{{Name: "t", Action: action}}}
	problems := precheck.Check(def, p.home)
	if len(problems) == 0 {
		return ""
	}
	return problems[0].Trigger + ": " + problems[0].Error
}

func (p places) assertFound(t *testing.T, action string, args ...string) {
	t.Helper()
	if got := p.check(action, args...); got != "" {
		t.Fatalf("%q が見つからない: %s", action, got)
	}
}

func (p places) assertMissing(t *testing.T, action string, args ...string) {
	t.Helper()
	if got := p.check(action, args...); !strings.Contains(got, "見つからない") {
		t.Fatalf("%q の検査 = %q, want 見つからない", action, got)
	}
}

func TestCommandOfTheRepoIsFound(t *testing.T) {
	p := newPlaces(t)
	write(t, filepath.Join(p.clone, ".claude/commands/implement.md"), "実装する")

	p.assertFound(t, "/implement issue #{{ .issue.number }}")
}

func TestCommandInASubdirectoryIsCalledWithAColon(t *testing.T) {
	p := newPlaces(t)
	write(t, filepath.Join(p.home, ".claude/commands/review/fix.md"), "直す")

	p.assertFound(t, "/review:fix")
}

func TestSkillIsCalledByTheNameInItsFrontMatter(t *testing.T) {
	p := newPlaces(t)
	write(t, filepath.Join(p.home, ".claude/skills/deploy-staging/SKILL.md"), "---\nname: deploy\ndescription: x\n---\n本文")

	p.assertFound(t, "/deploy")
}

func TestSkillWithoutANameIsCalledByItsDirectory(t *testing.T) {
	p := newPlaces(t)
	write(t, filepath.Join(p.clone, ".claude/skills/triage/SKILL.md"), "---\ndescription: x\n---\n本文")

	p.assertFound(t, "/triage")
}

func TestLeadingNameThatIsNowhereIsMissing(t *testing.T) {
	p := newPlaces(t)

	got := p.check("/implement")

	want := "t: action の先頭の /implement が見つからない (plugin・repo の .claude・~/.claude の skill と command)"
	if got != want {
		t.Fatalf("検査 = %q, want %q", got, want)
	}
}

// installed は installed_plugins.json に plugin を 1 つ載せ、その install 先を返す。
func (p places) installed(t *testing.T, key, name, scope, projectPath string) string {
	t.Helper()
	dir := filepath.Join(p.home, ".claude/plugins/cache/market", name, "1.0.0")
	write(t, filepath.Join(dir, ".claude-plugin/plugin.json"), `{"name": "`+name+`", "version": "1.0.0"}`)
	write(t, filepath.Join(p.home, ".claude/plugins/installed_plugins.json"), `{"version": 2, "plugins": {"`+key+`": [
  {"scope": "`+scope+`", "installPath": "`+dir+`", "projectPath": "`+projectPath+`", "version": "1.0.0"}]}}`)
	return dir
}

func TestSkillOfAnInstalledPluginIsCalledWithThePluginName(t *testing.T) {
	p := newPlaces(t)
	dir := p.installed(t, "tools@market", "tools", "user", "")
	write(t, filepath.Join(dir, "skills/tdd/SKILL.md"), "---\nname: tdd\n---\n")

	p.assertFound(t, "/tools:tdd")
}

func TestSkillOfAPluginIsNotCalledByItsBareName(t *testing.T) {
	p := newPlaces(t)
	dir := p.installed(t, "tools@market", "tools", "user", "")
	write(t, filepath.Join(dir, "skills/tdd/SKILL.md"), "---\nname: tdd\n---\n")

	p.assertMissing(t, "/tdd")
}

func TestCommandOfAnInstalledPluginIsCalledWithThePluginName(t *testing.T) {
	p := newPlaces(t)
	dir := p.installed(t, "tools@market", "tools", "user", "")
	write(t, filepath.Join(dir, "commands/ship.md"), "出す")

	p.assertFound(t, "/tools:ship")
}

func TestPluginInstalledForAnotherProjectIsNotFound(t *testing.T) {
	p := newPlaces(t)
	dir := p.installed(t, "tools@market", "tools", "project", "/somewhere/else")
	write(t, filepath.Join(dir, "commands/ship.md"), "出す")

	p.assertMissing(t, "/tools:ship")
}

func TestPluginInstalledForThisProjectIsFound(t *testing.T) {
	p := newPlaces(t)
	dir := p.installed(t, "tools@market", "tools", "project", p.clone)
	write(t, filepath.Join(dir, "commands/ship.md"), "出す")

	p.assertFound(t, "/tools:ship")
}

func TestPluginPlacedUnderTheSkillsDirectoryUsesTheSkillPathsOfItsManifest(t *testing.T) {
	// ~/.claude/skills/<plugin> に plugin を置き、plugin.json の skills に skill の dir を並べる形
	p := newPlaces(t)
	dir := filepath.Join(p.home, ".claude/skills/bundle")
	write(t, filepath.Join(dir, ".claude-plugin/plugin.json"), `{"name": "bundle", "skills": ["./skills/procedure/playbook"]}`)
	write(t, filepath.Join(dir, "skills/procedure/playbook/SKILL.md"), "---\nname: playbook\n---\n")

	p.assertFound(t, "/bundle:playbook")
}

func TestPluginDirectoryGivenToClaudeIsSearched(t *testing.T) {
	p := newPlaces(t)
	write(t, filepath.Join(p.clone, "plugins/local/.claude-plugin/plugin.json"), `{"name": "local"}`)
	write(t, filepath.Join(p.clone, "plugins/local/commands/go.md"), "進める")

	p.assertFound(t, "/local:go", "--permission-mode", "auto", "--plugin-dir", "plugins/local")
}

func TestCommandPathsOfTheManifestReplaceTheCommandsDirectory(t *testing.T) {
	p := newPlaces(t)
	dir := filepath.Join(p.home, ".claude/skills/bundle")
	write(t, filepath.Join(dir, ".claude-plugin/plugin.json"), `{"name": "bundle", "commands": ["./cmds/run.md"]}`)
	write(t, filepath.Join(dir, "cmds/run.md"), "走る")
	write(t, filepath.Join(dir, "commands/old.md"), "古い")

	p.assertFound(t, "/bundle:run")
	p.assertMissing(t, "/bundle:old")
}

func TestActionThatStartsWithATemplateVariableFails(t *testing.T) {
	p := newPlaces(t)

	got := p.check("  {{ .trigger.name }} を進める")

	if got != "t: action が template 変数で始まるので、先頭の skill を確かめられない" {
		t.Fatalf("検査 = %q", got)
	}
}

func TestActionWithoutALeadingSlashIsNotChecked(t *testing.T) {
	p := newPlaces(t)

	p.assertFound(t, "issue #{{ .issue.number }} を実装する。/implement は使わない")
}
