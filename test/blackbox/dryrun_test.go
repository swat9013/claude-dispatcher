package blackbox_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// `tick --dry-run` (formats.md §7, system.md §9)。state dir に何も書かず、指示の種別と件数を stdout に 1 行で出す。

func dryRunLine(t *testing.T, r runResult) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout が 1 行でない: %q", r.stdout)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &out); err != nil {
		t.Fatalf("stdout が JSON でない: %v\n%s", err, lines[0])
	}
	return out
}

func TestDryRunReportsInstructionCountsOnOneLine(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42), issue{number: 40, labels: []string{wipLabel}})

	r := s.tick("--dry-run")

	assertExit(t, r, 0)
	if r.stderr != "" {
		t.Fatalf("成功した試運転が stderr に書いた: %q", r.stderr)
	}
	out := dryRunLine(t, r)
	want := []string{"candidates", "dry_run", "instructions", "observed", "project", "result", "ts", "wip"}
	if got := keys(out); !slices.Equal(got, want) {
		t.Fatalf("試運転の行の key = %v, want %v", got, want)
	}
	if out["dry_run"] != true || out["result"] != "ok" || out["project"] != s.project {
		t.Fatalf("試運転の行 = %v", out)
	}
	if got := asMap(t, out["instructions"]); len(got) != 1 || number(t, got["start"]) != 1 {
		t.Fatalf("instructions = %v, want {start: 1}", got)
	}
	if number(t, out["candidates"]) != 1 || number(t, out["wip"]) != 1 {
		t.Fatalf("candidates / wip = %v / %v", out["candidates"], out["wip"])
	}
}

func TestDryRunWritesNothingToTheStateDir(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	before := fileFingerprints(t, s.stateRoot)

	assertExit(t, s.tick("--dry-run"), 0)

	if after := fileFingerprints(t, s.stateRoot); !reflect.DeepEqual(before, after) {
		t.Fatalf("試運転が state dir に書いた:\nbefore %v\nafter  %v", before, after)
	}
	s.assertNoClaude("試運転が")
}

func TestDryRunOfAQuietProjectReportsEmptyInstructions(t *testing.T) {
	s := newSandbox(t)

	r := s.tick("--dry-run")

	assertExit(t, r, 0)
	if got := asMap(t, dryRunLine(t, r)["instructions"]); len(got) != 0 {
		t.Fatalf("instructions = %v, want {}", got)
	}
}

func TestDryRunVerifiesConfigEvenWhenTheMarkerMatches(t *testing.T) {
	s := newSandbox(t)
	assertExit(t, s.tick(), 0) // marker を書かせる
	s.repoMissing()

	r := s.tick("--dry-run")

	assertExit(t, r, 2)
}

func TestFailedDryRunReportsOnlyADashTickLineOnStderr(t *testing.T) {
	s := newSandbox(t)
	s.setLabels(defaultReadyLabel)
	before := fileFingerprints(t, s.stateRoot)

	r := s.tick("--dry-run")

	assertExit(t, r, 2)
	if r.stdout != "" {
		t.Fatalf("失敗した試運転が stdout に書いた: %q", r.stdout)
	}
	assertFailureLine(t, r.stderr, s.project, "-", "config_error")
	if after := fileFingerprints(t, s.stateRoot); !reflect.DeepEqual(before, after) {
		t.Fatal("失敗した試運転が state dir に書いた")
	}
}

func TestDryRunFailsNamingADependencyItCannotResolve(t *testing.T) {
	for _, name := range []string{"claude", "gh"} {
		t.Run(name, func(t *testing.T) {
			s := newSandbox(t)
			skipIfSelfResolutionReachesARealOne(t, name)
			if err := os.Remove(filepath.Join(s.binDir, name)); err != nil {
				t.Fatal(err)
			}

			r := s.tick("--dry-run")

			if r.exit == 0 {
				t.Fatalf("%s を解決できないのに試運転が通った: %s", name, r.stdout)
			}
			s.assertNames(r.stderr, name)
		})
	}
}

func TestTickRejectsTheRemovedCronEnvFlag(t *testing.T) {
	s := newSandbox(t)

	r := s.tick("--dry-run", "--cron-env")

	assertExit(t, r, 2)
	s.assertNames(r.stderr, "--cron-env")
}
