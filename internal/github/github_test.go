package github_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/config"
	"github.com/swat9013/claude-dispatcher/internal/github"
)

// TestTimedOutGhIsStoppedWithTheChildrenItStarted は、stdout を握ったまま眠る孫を残す gh が timeout で
// process group ごと止まり、Run が上限の近くで戻ることを見る。
func TestTimedOutGhIsStoppedWithTheChildrenItStarted(t *testing.T) {
	gh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nsleep 30 &\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := time.Now()

	_, err := github.Exec{Path: gh, Timeout: 200 * time.Millisecond}.Run("repo", "view")

	if err == nil || !strings.Contains(err.Error(), "を超えても終わらない") {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout 後も孫が pipe を握って戻らなかった (%s)", elapsed)
	}
}

func TestRepoThatGhCannotResolveIsNotFound(t *testing.T) {
	gh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\necho \"GraphQL: Could not resolve to a Repository with the name 'acme/x'. (repository)\" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := github.RepoExists(github.Exec{Path: gh, Timeout: 5 * time.Second}, config.Repo{Owner: "acme", Name: "x"})

	if !github.IsNotFound(err) || github.IsAuth(err) {
		t.Fatalf("err = %v", err)
	}
}
