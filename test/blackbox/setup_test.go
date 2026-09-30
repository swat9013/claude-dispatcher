package blackbox_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// setup (formats.md §7.4): workflow 定義の雛形を置く。

// repoView は `gh repo view` が cwd の repo として acme/widgets を返す応答 rule。
var repoView = stubwire.Rule{ArgsPrefix: []string{"repo", "view"}, Stdout: "acme/widgets\n"}

func TestSetupTemplateAloneLetsTheDryRunPassWithoutPlugins(t *testing.T) {
	s := newSandbox(t)
	if err := os.RemoveAll(filepath.Join(s.home, ".claude")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.workflowFile()); err != nil {
		t.Fatal(err)
	}
	s.respondAll("gh", append([]stubwire.Rule{repoView}, ghRules([]issue{readyIssue(42)}, nil)...))
	assertExit(t, s.run("setup"), 0)

	r := s.dryRun()

	assertExit(t, r, 0)
	if !strings.HasPrefix(r.stdout, "implement\tissue\t#42\t") {
		t.Fatalf("試運転の stdout:\n%s\nstderr:\n%s", r.stdout, r.stderr)
	}
}

func TestSetupWritesTheRepoOfTheCloneAndLeavesApprovedCLsUnset(t *testing.T) {
	s := newSandbox(t)
	if err := os.Remove(s.workflowFile()); err != nil {
		t.Fatal(err)
	}
	s.respond("gh", repoView)

	r := s.run("setup")

	assertExit(t, r, 0)
	written, err := os.ReadFile(s.workflowFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "  repo: acme/widgets\n") || strings.Contains(string(written), "\n      approved: true") {
		t.Fatalf("雛形:\n%s", written)
	}
}

func TestSetupDoesNotOverwriteAnExistingWorkflow(t *testing.T) {
	s := newSandbox(t)
	s.respond("gh", repoView)

	r := s.run("setup")

	assertExit(t, r, 0)
	if written, _ := os.ReadFile(s.workflowFile()); string(written) != defaultWorkflow {
		t.Fatalf("既存の workflow 定義を書き換えた:\n%s", written)
	}
	if !strings.Contains(r.stdout, "既にある") {
		t.Fatalf("stdout:\n%s", r.stdout)
	}
}

func TestSetupFailsWhenTheRepoCannotBeDetermined(t *testing.T) {
	s := newSandbox(t)
	if err := os.Remove(s.workflowFile()); err != nil {
		t.Fatal(err)
	}
	s.respond("gh", stubwire.Rule{ArgsPrefix: []string{"repo", "view"}, Stderr: "no git remotes found", Exit: 1})

	r := s.run("setup")

	assertExit(t, r, 1)
	if _, err := os.Stat(s.workflowFile()); !os.IsNotExist(err) {
		t.Fatalf("repo を決められないのに書いた: %v", err)
	}
}
