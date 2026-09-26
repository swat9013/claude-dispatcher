package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelfForCrontabRefusesTheTemporaryBuildOfGoRun(t *testing.T) {
	argv0 := filepath.Join(os.TempDir(), "go-build123", "b001", "exe", "claude-dispatcher")

	_, err := selfForCrontab(argv0, nil)

	if err == nil || !strings.Contains(err.Error(), "go run") {
		t.Fatalf("err = %v, want go run の一時 build を拒む error", err)
	}
}

func TestSelfForCrontabResolvesABareNameThroughPATH(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude-dispatcher")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := selfForCrontab("claude-dispatcher", []string{"PATH=" + dir})

	if err != nil || got != bin {
		t.Fatalf("selfForCrontab = %q, %v, want %q", got, err, bin)
	}
}
