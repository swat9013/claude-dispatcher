package workspace_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/workspace"
)

// workspace root の専有 (system.md §7、formats.md §1・§6)。

const (
	widgets = "acme.atlassian.net/WIDGETS"
	gadgets = "acme.atlassian.net/GADGETS"
)

func scopeKeyFile(root string) string { return filepath.Join(root, ".scope-key") }

func writeScopeKeyFile(t *testing.T, root, content string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scopeKeyFile(root), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertScopeKeyFile(t *testing.T, root, want string) {
	t.Helper()
	got, err := os.ReadFile(scopeKeyFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf(".scope-key = %q, want %q", got, want)
	}
}

func assertInUseBy(t *testing.T, err error, root, owner string) {
	t.Helper()
	var inUse *workspace.RootInUse
	if !errors.As(err, &inUse) {
		t.Fatalf("err = %v, want *workspace.RootInUse", err)
	}
	if want := "workspace.root " + root + " は scope key " + owner + " が使っている"; err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}
}

func assertFailsWithoutOwner(t *testing.T, err error) {
	t.Helper()
	var inUse *workspace.RootInUse
	if err == nil || errors.As(err, &inUse) {
		t.Fatalf("err = %v, want 別の scope key ではない失敗", err)
	}
}

func TestClaimingARootThatDoesNotExistCreatesItAndWritesTheScopeKey(t *testing.T) {
	root := filepath.Join(t.TempDir(), "a", "workspaces")

	err := workspace.ClaimRoot(root, widgets)

	if err != nil {
		t.Fatal(err)
	}
	assertScopeKeyFile(t, root, widgets+"\n")
}

func TestClaimingARootWithTheSameScopeKeyPassesAgain(t *testing.T) {
	root := t.TempDir()
	writeScopeKeyFile(t, root, widgets+"\n")

	err := workspace.ClaimRoot(root, widgets)

	if err != nil {
		t.Fatal(err)
	}
	assertScopeKeyFile(t, root, widgets+"\n")
}

func TestClaimingARootThatAnotherScopeKeyUsesFailsAndKeepsTheFile(t *testing.T) {
	root := t.TempDir()
	writeScopeKeyFile(t, root, gadgets+"\n")

	err := workspace.ClaimRoot(root, widgets)

	assertInUseBy(t, err, root, gadgets)
	assertScopeKeyFile(t, root, gadgets+"\n")
}

func TestOnlyOneOfTheConcurrentClaimsOnAnEmptyRootSucceeds(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	const claims = 32
	errs := make([]error, claims)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range claims {
		wg.Go(func() {
			<-start
			errs[i] = workspace.ClaimRoot(root, fmt.Sprintf("scope-%d", i))
		})
	}

	close(start)
	wg.Wait()

	winner := -1
	for i, err := range errs {
		if err == nil {
			if winner != -1 {
				t.Fatalf("scope-%d と scope-%d の両方が通った", winner, i)
			}
			winner = i
		}
	}
	if winner == -1 {
		t.Fatalf("どれも通らなかった: %v", errs)
	}
	owner := fmt.Sprintf("scope-%d", winner)
	assertScopeKeyFile(t, root, owner+"\n")
	for i, err := range errs {
		if i != winner {
			assertInUseBy(t, err, root, owner)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("root の中身 = %v, want .scope-key だけ", entries)
	}
}

func TestClaimFailsWithoutNamingAnOwnerWhenTheScopeKeyFileCannotBeRead(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(scopeKeyFile(root), 0o755); err != nil {
		t.Fatal(err)
	}

	err := workspace.ClaimRoot(root, widgets)

	assertFailsWithoutOwner(t, err)
}

func TestAnEmptyScopeKeyFileIsTreatedAsUnreadable(t *testing.T) {
	root := t.TempDir()
	writeScopeKeyFile(t, root, "\n")

	err := workspace.ClaimRoot(root, widgets)

	assertFailsWithoutOwner(t, err)
	assertScopeKeyFile(t, root, "\n")
}

func TestClaimFailsWhenTheScopeKeyFileIsADanglingSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("missing", scopeKeyFile(root)); err != nil {
		t.Fatal(err)
	}

	err := workspace.ClaimRoot(root, widgets)

	assertFailsWithoutOwner(t, err)
}

func TestCheckFailsWhenTheScopeKeyFileIsADanglingSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("missing", scopeKeyFile(root)); err != nil {
		t.Fatal(err)
	}

	err := workspace.CheckRoot(root, widgets)

	assertFailsWithoutOwner(t, err)
}

func TestClaimFailsWhenTheRootCannotBeWritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root は dir の権限を無視する")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	err := workspace.ClaimRoot(root, widgets)

	assertFailsWithoutOwner(t, err)
}

func TestCheckingARootWithoutAScopeKeyFilePassesWithoutWriting(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")

	err := workspace.CheckRoot(root, widgets)

	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("検査が root を作った: %v", err)
	}
}

func TestCheckingARootPassesForTheSameScopeKey(t *testing.T) {
	root := t.TempDir()
	writeScopeKeyFile(t, root, widgets+"\n")

	err := workspace.CheckRoot(root, widgets)

	if err != nil {
		t.Fatal(err)
	}
}

func TestCheckingARootThatAnotherScopeKeyUsesFails(t *testing.T) {
	root := t.TempDir()
	writeScopeKeyFile(t, root, gadgets+"\n")

	err := workspace.CheckRoot(root, widgets)

	assertInUseBy(t, err, root, gadgets)
	assertScopeKeyFile(t, root, gadgets+"\n")
}
