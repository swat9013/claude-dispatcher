package blackbox_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// `paths --json` (formats.md §9)。外部の読み手 (debrief) が log.jsonl を引く公開契約。

func (s *sandbox) pathsJSON(args ...string) map[string]any {
	s.t.Helper()
	r := s.run(append([]string{"paths", "--json"}, args...)...)
	assertExit(s.t, r, 0)
	var doc map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		s.t.Fatalf("stdout が JSON でない: %v\n%s", err, r.stdout)
	}
	return doc
}

func TestPathsOfAProjectPointAtItsConfigStateAndLog(t *testing.T) {
	s := newSandbox(t)

	doc := s.pathsJSON(s.project)

	want := map[string]any{"project": s.project, "config_file": s.configFile(), "state_dir": s.stateDir(), "log_file": s.logFile()}
	for key, value := range want {
		if doc[key] != value {
			t.Fatalf("%s = %v, want %v (%v)", key, doc[key], value, doc)
		}
	}
}

func TestPathsWithoutAProjectListTheRootsAndTheProjectsThatHaveAConfig(t *testing.T) {
	s := newSandbox(t)
	mustMkdir(t, filepath.Join(s.configRoot, "aaa"))
	mustWrite(t, filepath.Join(s.configRoot, "aaa", "config.toml"), s.defaultConfig())
	mustMkdir(t, filepath.Join(s.configRoot, "no-config"))

	doc := s.pathsJSON()

	if doc["config_root"] != s.configRoot || doc["state_root"] != s.stateRoot {
		t.Fatalf("root = %v", doc)
	}
	var projects []string
	for _, p := range asList(t, doc["projects"]) {
		projects = append(projects, asString(t, p))
	}
	if !slices.Equal(projects, []string{"aaa", s.project}) {
		t.Fatalf("projects = %v", projects)
	}
}

func TestPathsCountAProjectDirPlacedAsASymlink(t *testing.T) {
	s := newSandbox(t)
	dotfiles := filepath.Join(s.root, "dotfiles", "linked")
	mustMkdir(t, dotfiles)
	mustWrite(t, filepath.Join(dotfiles, "config.toml"), s.defaultConfig())
	if err := os.Symlink(dotfiles, filepath.Join(s.configRoot, "linked")); err != nil {
		t.Fatal(err)
	}

	doc := s.pathsJSON()

	if !slices.Contains(asList(t, doc["projects"]), any("linked")) {
		t.Fatalf("symlink の project dir を数えていない: %v", doc["projects"])
	}
}

func TestPathsFollowTheHomeDefaultsWhenXDGIsUnset(t *testing.T) {
	s := newSandboxWithHomeDefaults(t)

	doc := s.pathsJSON(s.project)

	if doc["state_dir"] != filepath.Join(s.home, ".local", "state", "claude-dispatcher", s.project) {
		t.Fatalf("state_dir = %v", doc["state_dir"])
	}
}

func TestPathsWithAnInvalidProjectNameIsAUsageError(t *testing.T) {
	s := newSandbox(t)

	r := s.run("paths", "--json", "../etc")

	assertExit(t, r, 2)
}

func TestPathsWriteNothing(t *testing.T) {
	s := newSandbox(t)
	if err := os.RemoveAll(s.stateDir()); err != nil {
		t.Fatal(err)
	}
	before := fileFingerprints(t, s.root)

	s.pathsJSON(s.project)

	if after := fileFingerprints(t, s.root); !maps.Equal(before, after) {
		t.Fatal("paths が何かを書いた")
	}
}
