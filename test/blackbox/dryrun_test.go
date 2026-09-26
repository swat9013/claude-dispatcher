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

func TestDryRunReportsInstructionCountsOnOneLineAndWritesNothing(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42), issue{number: 40, labels: []string{wipLabel}})
	before := tree(t, s.stateRoot)

	r := s.tick("--dry-run")

	assertExit(t, r, 0)
	if r.stderr != "" {
		t.Fatalf("成功した試運転が stderr に書いた: %q", r.stderr)
	}
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout が 1 行でない: %q", r.stdout)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &out); err != nil {
		t.Fatalf("stdout が JSON でない: %v\n%s", err, lines[0])
	}
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
	if after := tree(t, s.stateRoot); !reflect.DeepEqual(before, after) {
		t.Fatalf("試運転が state dir に書いた:\nbefore %v\nafter  %v", before, after)
	}
	if calls := s.calls("claude"); len(calls) != 0 {
		t.Fatalf("試運転が claude を起動した: %v", calls)
	}
}

func TestDryRunVerifiesConfigEvenWhenTheMarkerMatches(t *testing.T) {
	s := newSandbox(t)
	assertExit(t, s.tick(), 0) // marker を書かせる
	s.respond("gh", stubRule{ArgsPrefix: []string{"repo", "view"}, Stderr: "GraphQL: Could not resolve to a Repository\n", Exit: 1})

	dry := s.tick("--dry-run")
	real := s.tick()

	assertExit(t, dry, 2)
	assertExit(t, real, 0)
}

func TestFailedDryRunPrintsNothingOnStdoutAndADashTickLineOnStderr(t *testing.T) {
	s := newSandbox(t)
	s.setLabels(defaultReadyLabel)
	before := tree(t, s.stateRoot)

	r := s.tick("--dry-run")

	assertExit(t, r, 2)
	if r.stdout != "" {
		t.Fatalf("失敗した試運転が stdout に書いた: %q", r.stdout)
	}
	assertCronLogLine(t, r.stderr, s.project, "-", "config_error")
	if after := tree(t, s.stateRoot); !reflect.DeepEqual(before, after) {
		t.Fatal("失敗した試運転が state dir に書いた")
	}
}

func TestDryRunOfAQuietProjectReportsEmptyInstructions(t *testing.T) {
	s := newSandbox(t)

	r := s.tick("--dry-run")

	assertExit(t, r, 0)
	var out map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.stdout)), &out); err != nil {
		t.Fatal(err)
	}
	if got := asMap(t, out["instructions"]); len(got) != 0 {
		t.Fatalf("instructions = %v, want {}", got)
	}
}

func TestDryRunFailsWhenClaudeCannotBeResolved(t *testing.T) {
	s := newSandbox(t)
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		if _, err := os.Stat(filepath.Join(dir, "claude")); err == nil {
			t.Skipf("PATH の自己解決が届く %s に claude の実物がある", dir)
		}
	}
	if err := os.Remove(filepath.Join(s.binDir, "claude")); err != nil {
		t.Fatal(err)
	}

	r := s.tick("--dry-run")

	if r.exit == 0 {
		t.Fatalf("claude が解決できないのに試運転が通った: %s", r.stdout)
	}
	s.assertNames(r.stderr, "claude")
}

func TestCronEnvWithoutDryRunIsRejected(t *testing.T) {
	s := newSandbox(t)

	r := s.tick("--cron-env")

	assertExit(t, r, 2)
	if calls := s.calls("gh"); len(calls) != 0 {
		t.Fatalf("引数の誤りなのに観測した: %v", calls)
	}
}

func TestCronEnvDryRunRunsWithOnlyHomeAndAMinimalPath(t *testing.T) {
	s := newSandboxWithHomeDefaults(t)
	// cron の最小 PATH からは見えない。自己解決の置き場 (~/.local/bin) にだけ stub を置く
	s.installStubs(filepath.Join(s.home, ".local", "bin"))
	s.setIssues(readyIssue(42))

	r := s.runWithEnv(map[string]string{"GH_TOKEN": "parent-shell-token", "XDG_STATE_HOME": filepath.Join(s.root, "elsewhere")},
		"tick", s.project, "--dry-run", "--cron-env")

	assertExit(t, r, 0)
	calls := s.calls("gh")
	if len(calls) == 0 {
		t.Fatal("gh が呼ばれていない")
	}
	for _, c := range calls {
		if _, ok := c.Env["GH_TOKEN"]; ok {
			t.Fatalf("親 shell の GH_TOKEN が撃ち直し後に残った: %v", c.Env)
		}
		if _, ok := c.Env["XDG_STATE_HOME"]; ok {
			t.Fatalf("親 shell の XDG_STATE_HOME が撃ち直し後に残った: %v", c.Env)
		}
		if c.Env["HOME"] != s.home {
			t.Fatalf("HOME = %q, want %s", c.Env["HOME"], s.home)
		}
		if strings.Contains(c.Env["PATH"], "stub-bin") {
			t.Fatalf("親 shell の PATH が撃ち直し後に残った: %q", c.Env["PATH"])
		}
		if !strings.HasPrefix(c.Exe, filepath.Join(s.home, ".local", "bin")) {
			t.Fatalf("gh を自己解決の置き場から引いていない: %s", c.Exe)
		}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.stdout)), &out); err != nil {
		t.Fatalf("撃ち直した試運転の stdout が JSON 1 行でない: %v\n%s", err, r.stdout)
	}
	if got := asMap(t, out["instructions"]); number(t, got["start"]) != 1 {
		t.Fatalf("instructions = %v", got)
	}
}
