package deps_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/deps"
)

// pathWith は names の実行 file だけを置いた dir と、候補の置き場 (~/.local/bin) を持つ HOME を作る。
func pathWith(t *testing.T, names ...string) (onPath, home, local string) {
	t.Helper()
	onPath, home = t.TempDir(), t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(onPath, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	local = filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	return onPath, home, local
}

func TestPATHIsExtendedWhenOnlyGitIsMissing(t *testing.T) {
	onPath, home, local := pathWith(t, "gh", "claude")

	got := deps.ResolvePATH(onPath, home)

	if !strings.HasPrefix(got, local+string(os.PathListSeparator)) || !strings.HasSuffix(got, onPath) {
		t.Fatalf("PATH = %s", got)
	}
}

func TestPATHIsExtendedWhenNeitherTrackerCLIIsOnIt(t *testing.T) {
	onPath, home, local := pathWith(t, "claude", "git")

	got := deps.ResolvePATH(onPath, home)

	if !strings.HasPrefix(got, local+string(os.PathListSeparator)) {
		t.Fatalf("PATH = %s", got)
	}
}

func TestPATHIsKeptWhenOneOfTheTrackerCLIsIsOnIt(t *testing.T) {
	for _, tracker := range []string{"gh", "glab"} {
		t.Run(tracker, func(t *testing.T) {
			onPath, home, _ := pathWith(t, tracker, "claude", "git")

			got := deps.ResolvePATH(onPath, home)

			if got != onPath {
				t.Fatalf("PATH = %s, want %s (使わない方の tracker の CLI が無いだけで書き換えた)", got, onPath)
			}
		})
	}
}
