package plugin_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/plugin"
)

func writePlaybook(t *testing.T, root, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, "skills", "procedure", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestFrontmatterWrittenWithCRLFIsRead(t *testing.T) {
	root := t.TempDir()
	file := writePlaybook(t, root, "playbook-implementation", "---\r\nmetadata:\r\n  deliverable: cl\r\n  dispatch-when: 既定\r\n---\r\n本文\r\n")

	got, err := plugin.Install{Path: root}.StartPlaybooks()

	if err != nil || len(got) != 1 || got[0].Path != file || got[0].DispatchWhen != "既定" {
		t.Fatalf("StartPlaybooks = %v, %v", got, err)
	}
}

func TestClosingDelimiterWithTrailingSpacesEndsTheFrontmatter(t *testing.T) {
	root := t.TempDir()
	writePlaybook(t, root, "playbook-implementation", "---\nmetadata:\n  deliverable: cl\n  dispatch-when: 既定\n---  \n本文\n")

	got, err := plugin.Install{Path: root}.StartPlaybooks()

	if err != nil || len(got) != 1 {
		t.Fatalf("StartPlaybooks = %v, %v", got, err)
	}
}

func TestEmptyFrontmatterLeavesThePlaybookOutOfTheStartPopulation(t *testing.T) {
	root := t.TempDir()
	writePlaybook(t, root, "playbook-empty", "---\n---\n本文\n")

	got, err := plugin.Install{Path: root}.StartPlaybooks()

	if err != nil || len(got) != 0 {
		t.Fatalf("StartPlaybooks = %v, %v", got, err)
	}
}

func TestPlaybookWithoutAClosingDelimiterIsAnError(t *testing.T) {
	root := t.TempDir()
	writePlaybook(t, root, "playbook-open", "---\nmetadata:\n  deliverable: cl\n本文\n")

	if _, err := (plugin.Install{Path: root}).StartPlaybooks(); err == nil {
		t.Fatal("閉じの無い frontmatter を読んだ")
	}
}
