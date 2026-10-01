package workspace_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/workspace"
)

// workspace で checkout されている branch の読み方 (formats.md §6 の tick の手順 7)。本物の git を撃つ。

var ref = target.Ref{Kind: target.KindIssue, Number: 7}

func manager(t *testing.T, root string) workspace.Manager {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("git が無い (git は CL 側の trigger で loop が撃つ依存)")
	}
	return workspace.Manager{Root: root, Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// repoWorkspace は ref の workspace を、commit を 1 つ持つ git の repo として作る。
func repoWorkspace(t *testing.T, m workspace.Manager) string {
	t.Helper()
	path := m.Path(ref)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, path, "init", "-q", "-b", "worktree-issue-7")
	git(t, path, "commit", "-q", "--allow-empty", "-m", "c")
	return path
}

func TestBranchIsTheBranchCheckedOutInTheWorkspace(t *testing.T) {
	m := manager(t, t.TempDir())
	repoWorkspace(t, m)

	branch, err := m.Branch(ref)

	if err != nil || branch != "worktree-issue-7" {
		t.Fatalf("branch = %q (%v), want worktree-issue-7", branch, err)
	}
}

func TestWorkspaceWithADetachedHeadHasNoBranch(t *testing.T) {
	m := manager(t, t.TempDir())
	path := repoWorkspace(t, m)
	git(t, path, "checkout", "-q", "--detach")

	branch, err := m.Branch(ref)

	if err != nil || branch != "" {
		t.Fatalf("branch = %q (%v), want 無し", branch, err)
	}
}

func TestWorkspaceInsideAnotherRepoHasNoBranchOfItsOwn(t *testing.T) {
	clone := t.TempDir()
	m := manager(t, filepath.Join(clone, ".claude-dispatcher", "workspaces"))
	git(t, clone, "init", "-q", "-b", "main")
	if err := os.MkdirAll(m.Path(ref), 0o755); err != nil {
		t.Fatal(err)
	}

	branch, err := m.Branch(ref)

	if err != nil || branch != "" {
		t.Fatalf("branch = %q (%v), want 親の repo の branch を返さない", branch, err)
	}
}

func TestMissingWorkspaceHasNoBranch(t *testing.T) {
	m := manager(t, t.TempDir())

	branch, err := m.Branch(ref)

	if err != nil || branch != "" {
		t.Fatalf("branch = %q (%v), want 無し", branch, err)
	}
}
