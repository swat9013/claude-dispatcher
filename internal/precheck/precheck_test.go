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

func TestSkillOfAPluginWithoutANameIsNotCalledByItsBareName(t *testing.T) {
	p := newPlaces(t)
	dir := p.installed(t, "tools@market", "tools", "user", "")
	write(t, filepath.Join(dir, "skills/tdd/SKILL.md"), "---\ndescription: x\n---\n")

	p.assertMissing(t, "/tdd")
}

func TestSkillOfAPluginWithANameIsAlsoCalledByTheBareName(t *testing.T) {
	p := newPlaces(t)
	dir := p.installed(t, "tools@market", "tools", "user", "")
	write(t, filepath.Join(dir, "skills/review/SKILL.md"), "---\nname: fancy\n---\n")

	p.assertFound(t, "/fancy")
}

func TestSkillAtTheRootOfAPluginIsCalledWithThePluginName(t *testing.T) {
	p := newPlaces(t)
	dir := p.installed(t, "tools@market", "tools", "user", "")
	write(t, filepath.Join(dir, "SKILL.md"), "---\nname: review\n---\n")

	p.assertFound(t, "/tools:review")
}

func TestSkillAtTheRootOfAPluginWithASkillsDirectoryIsNotFound(t *testing.T) {
	// root の SKILL.md を単一の skill として読むのは、skills/ も manifest の skills も無い plugin だけ
	p := newPlaces(t)
	dir := p.installed(t, "tools@market", "tools", "user", "")
	write(t, filepath.Join(dir, "SKILL.md"), "---\nname: review\n---\n")
	write(t, filepath.Join(dir, "skills/tdd/SKILL.md"), "---\nname: tdd\n---\n")

	p.assertMissing(t, "/tools:review")
}

func TestMissingNameShowsThePlacesThatCouldNotBeRead(t *testing.T) {
	p := newPlaces(t)
	write(t, filepath.Join(p.home, ".claude/plugins/installed_plugins.json"), "{壊れた")

	got := p.check("/tools:ship")

	if !strings.Contains(got, "見つからない") || !strings.Contains(got, "読めなかった置き場: "+filepath.Join(p.home, ".claude/plugins/installed_plugins.json")) {
		t.Fatalf("検査 = %q", got)
	}
}

func TestCommandOfAnInstalledPluginIsCalledWithThePluginName(t *testing.T) {
	p := newPlaces(t)
	dir := p.installed(t, "tools@market", "tools", "user", "")
	write(t, filepath.Join(dir, "commands/ship.md"), "出す")

	p.assertFound(t, "/tools:ship")
}

func TestPluginInstalledForTheProjectOfTheCloneIsFound(t *testing.T) {
	// project の plugin は commit された .claude/settings.json で有効になり、clone の worktree でも読まれる
	p := newPlaces(t)
	dir := p.installed(t, "tools@market", "tools", "project", p.clone)
	write(t, filepath.Join(dir, "commands/ship.md"), "出す")

	p.assertFound(t, "/tools:ship")
}

func TestPluginInstalledForAnotherProjectIsNotFound(t *testing.T) {
	p := newPlaces(t)
	dir := p.installed(t, "tools@market", "tools", "project", filepath.Join(p.home, "other"))
	write(t, filepath.Join(dir, "commands/ship.md"), "出す")

	p.assertMissing(t, "/tools:ship")
}

func TestPluginInstalledLocallyIsNotFound(t *testing.T) {
	// local の plugin は commit しない .claude/settings.local.json で有効になり、worktree では読まれない
	p := newPlaces(t)
	dir := p.installed(t, "tools@market", "tools", "local", p.clone)
	write(t, filepath.Join(dir, "commands/ship.md"), "出す")

	p.assertMissing(t, "/tools:ship")
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

// bundle は ~/.claude/skills/bundle に manifest を置いた plugin を作り、その dir を返す。
func (p places) bundle(t *testing.T, manifest string) string {
	t.Helper()
	dir := filepath.Join(p.home, ".claude/skills/bundle")
	write(t, filepath.Join(dir, ".claude-plugin/plugin.json"), manifest)
	return dir
}

func TestCommandPathsOfTheManifestAreSearched(t *testing.T) {
	p := newPlaces(t)
	dir := p.bundle(t, `{"name": "bundle", "commands": ["./cmds/run.md"]}`)
	write(t, filepath.Join(dir, "cmds/run.md"), "走る")

	p.assertFound(t, "/bundle:run")
}

func TestCommandPathsOfTheManifestReplaceTheCommandsDirectory(t *testing.T) {
	p := newPlaces(t)
	dir := p.bundle(t, `{"name": "bundle", "commands": ["./cmds/run.md"]}`)
	write(t, filepath.Join(dir, "commands/stop.md"), "止める")

	p.assertMissing(t, "/bundle:stop")
}

func TestCommandsOfTheManifestGivenAsAMapAreCalledByTheirKeys(t *testing.T) {
	p := newPlaces(t)
	p.bundle(t, `{"name": "bundle", "commands": {"about": {"source": "./README.md"}}}`)

	p.assertFound(t, "/bundle:about")
}

func TestEmptyCommandPathOfTheManifestIsNotThePluginRoot(t *testing.T) {
	p := newPlaces(t)
	dir := p.bundle(t, `{"name": "bundle", "commands": ""}`)
	write(t, filepath.Join(dir, "README.md"), "説明")

	p.assertMissing(t, "/bundle:README")
}

func TestCommandsDirectoryThatIsASymlinkIsSearched(t *testing.T) {
	p := newPlaces(t)
	dotfiles := filepath.Join(p.home, "dotfiles/commands")
	write(t, filepath.Join(dotfiles, "implement.md"), "実装する")
	write(t, filepath.Join(p.home, ".claude/settings.json"), "{}")
	if err := os.Symlink(dotfiles, filepath.Join(p.home, ".claude/commands")); err != nil {
		t.Fatal(err)
	}

	p.assertFound(t, "/implement")
}

func TestSkillNameIsReadAsYAML(t *testing.T) {
	p := newPlaces(t)
	write(t, filepath.Join(p.home, ".claude/skills/deploy-staging/SKILL.md"), "---\nname: deploy  # staging だけ\n---\n本文")

	p.assertFound(t, "/deploy")
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

// assertNoUnreadablePlace は、action の先頭の名前が見つからず、見つからない理由の後ろに注記 (` · ` で始まる読めなかった
// 置き場) が添えられていないことを確かめる。
func (p places) assertNoUnreadablePlace(t *testing.T, action string) {
	t.Helper()
	if got := p.check(action); !strings.Contains(got, "見つからない") || strings.Contains(got, " · ") {
		t.Fatalf("%q の検査 = %q, want 読めなかった置き場の無い 見つからない", action, got)
	}
}

func TestFileDirectlyUnderTheSkillsDirectoryIsNotAPlaceThatCouldNotBeRead(t *testing.T) {
	// Finder が置く .DS_Store・README 等は skill の dir にも plugin の dir にもなりえない
	p := newPlaces(t)
	write(t, filepath.Join(p.home, ".claude/skills/.DS_Store"), "\x00")
	write(t, filepath.Join(p.clone, ".claude/skills/notes.json"), "{}")

	p.assertNoUnreadablePlace(t, "/x")
}

func TestFileDirectlyUnderTheSkillsDirectoryOfAPluginIsNotAPlaceThatCouldNotBeRead(t *testing.T) {
	p := newPlaces(t)
	dir := p.installed(t, "tools@market", "tools", "user", "")
	write(t, filepath.Join(dir, "skills/catalog.json"), "{}")
	write(t, filepath.Join(dir, "skills/tdd/SKILL.md"), "---\nname: tdd\n---\n")

	p.assertNoUnreadablePlace(t, "/x")
}

func TestSkillDirectoryThatIsASymlinkIsFound(t *testing.T) {
	p := newPlaces(t)
	dotfiles := filepath.Join(p.home, "dotfiles/skills/deploy")
	write(t, filepath.Join(dotfiles, "SKILL.md"), "---\nname: deploy\n---\n")
	write(t, filepath.Join(p.home, ".claude/skills/.keep"), "")
	if err := os.Symlink(dotfiles, filepath.Join(p.home, ".claude/skills/deploy")); err != nil {
		t.Fatal(err)
	}

	p.assertFound(t, "/deploy")
}

// lockedSkill は ~/.claude/skills/locked に skill を置いて権限を外し、その dir を返す。root は権限に関わらず読めるので skip する。
func (p places) lockedSkill(t *testing.T) string {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("root は権限に関わらず読める")
	}
	locked := filepath.Join(p.home, ".claude/skills/locked")
	write(t, filepath.Join(locked, "SKILL.md"), "---\nname: locked\n---\n")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// TempDir が片付けられるよう、権限を戻す
		if err := os.Chmod(locked, 0o755); err != nil {
			t.Error(err)
		}
	})
	return locked
}

func TestSkillDirectoryWithoutPermissionIsAPlaceThatCouldNotBeRead(t *testing.T) {
	p := newPlaces(t)
	locked := p.lockedSkill(t)

	got := p.check("/x")

	if !strings.Contains(got, "読めなかった置き場: ") || !strings.Contains(got, filepath.Join(locked, "SKILL.md")+" (") {
		t.Fatalf("検査 = %q", got)
	}
}

func TestSkillDirectoryWithoutPermissionIsShownOnce(t *testing.T) {
	// skills の置き場の dir は skill の dir でも plugin の dir でもありうるが、同じ原因の失敗を別の path で並べない
	p := newPlaces(t)
	locked := p.lockedSkill(t)

	got := p.check("/x")

	_, note, _ := strings.Cut(got, "読めなかった置き場: ")
	n := 0
	for _, place := range strings.Split(note, ", ") {
		if strings.HasPrefix(place, locked+string(filepath.Separator)) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%s の下の失敗が %d 件載る: %q", locked, n, got)
	}
}

func TestPluginWhoseSkillFileCannotBeFollowedIsFound(t *testing.T) {
	// SKILL.md だけが辿れない (輪になった symlink) dir は、plugin.json を確かめられるので plugin の dir として数える
	p := newPlaces(t)
	dir := p.bundle(t, `{"name": "bundle"}`)
	write(t, filepath.Join(dir, "skills/foo/SKILL.md"), "---\nname: foo\n---\n")
	loop := filepath.Join(dir, "SKILL.md")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}

	p.assertFound(t, "/bundle:foo")
}

func TestDirectoryWithBothASkillAndAManifestIsASkillAndAPlugin(t *testing.T) {
	p := newPlaces(t)
	dir := p.bundle(t, `{"name": "bundle", "commands": ["./cmds/run.md"]}`)
	// front matter に name の無い SKILL.md は、plugin の skill としては `bundle:bundle` でだけ呼べる。素の `bundle` は skill の
	// dir として数えたときだけ呼べる
	write(t, filepath.Join(dir, "SKILL.md"), "手順")
	write(t, filepath.Join(dir, "cmds/run.md"), "走る")

	p.assertFound(t, "/bundle")
	p.assertFound(t, "/bundle:run")
}

func TestBrokenManifestOfAPluginIsAPlaceThatCouldNotBeRead(t *testing.T) {
	p := newPlaces(t)
	dir := p.bundle(t, "{壊れた")

	got := p.check("/x")

	if !strings.Contains(got, "読めなかった置き場: ") || !strings.Contains(got, filepath.Join(dir, ".claude-plugin/plugin.json")+" (") {
		t.Fatalf("検査 = %q", got)
	}
}

func TestSymlinkUnderTheSkillsDirectoryThatCannotBeFollowedIsShownOnce(t *testing.T) {
	// 辿れない symlink の失敗は、skill の置き場としても plugin の置き場としても 1 度だけ並べる
	p := newPlaces(t)
	loop := filepath.Join(p.home, ".claude/skills/loop")
	write(t, filepath.Join(p.home, ".claude/skills/.keep"), "")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}

	got := p.check("/x")

	if n := strings.Count(got, loop+" ("); n != 1 {
		t.Fatalf("%s が %d 回載る: %q", loop, n, got)
	}
}
