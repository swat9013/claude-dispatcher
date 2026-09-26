package deps_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/deps"
)

func TestPATHIsExtendedWhenOnlyGitIsMissing(t *testing.T) {
	onPath, home := t.TempDir(), t.TempDir()
	for _, name := range []string{"gh", "claude"} {
		if err := os.WriteFile(filepath.Join(onPath, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	local := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}

	got := deps.ResolvePATH(onPath, home)

	if !strings.HasPrefix(got, local+string(os.PathListSeparator)) || !strings.HasSuffix(got, onPath) {
		t.Fatalf("PATH = %s", got)
	}
}
