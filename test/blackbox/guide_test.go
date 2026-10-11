package blackbox_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `guide` と、案内を指す行 (formats.md §9・§2.8)。

// guidePointer は workflow 定義の誤りの後ろに足す、案内を指す行
const guidePointer = "書き方は `claude-dispatcher guide` を参照"

// invalidWorkflow は検査に落ちる workflow 定義 (未知の値の on)
var invalidWorkflow = workflowWithTriggers("\n  - {name: implement, on: pr, action: /implement}")

// lastLine は出力の最後の行。
func lastLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return lines[len(lines)-1]
}

func TestGuidePrintsTheEmbeddedGuideWithoutAWorkflowDefinition(t *testing.T) {
	s := newSandbox(t)
	if err := os.Remove(s.workflowFile()); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "internal", "guide", "guide.md"))
	if err != nil {
		t.Fatal(err)
	}

	r := s.run("guide")

	assertExit(t, r, 0)
	if r.stdout != string(want) || r.stderr != "" {
		t.Fatalf("guide が案内をそのまま出していない: stderr = %q\nstdout:\n%s", r.stderr, r.stdout)
	}
	s.assertNoGh("guide は")
}

func TestGuideRejectsArguments(t *testing.T) {
	s := newSandbox(t)

	r := s.run("guide", "github")

	assertExit(t, r, 2)
	if r.stdout != "" {
		t.Fatalf("引数の誤りなのに stdout に出た: %q", r.stdout)
	}
}

func TestUsageEndsWithAPointerToTheGuide(t *testing.T) {
	s := newSandbox(t)

	r := s.run("--help")

	assertExit(t, r, 0)
	if !strings.Contains(lastLine(r.stdout), "claude-dispatcher guide") {
		t.Fatalf("usage の最後の行が案内を指していない:\n%s", r.stdout)
	}
}

func TestWorkflowDefinitionErrorsAreFollowedByAPointerToTheGuide(t *testing.T) {
	cases := []struct {
		name string
		args []string
		exit int
		// out は誤りの行と案内の行を出す先
		out func(runResult) string
	}{
		{"loop の起動", []string{"loop"}, 2, func(r runResult) string { return r.stderr }},
		{"試運転", []string{"loop", "--dry-run"}, 2, func(r runResult) string { return r.stderr }},
		{"doctor", []string{"doctor"}, 1, func(r runResult) string { return r.stdout }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeWorkflowWithCommands(invalidWorkflow)

			r := s.run(c.args...)

			assertExit(t, r, c.exit)
			out := c.out(r)
			if !strings.Contains(out, "triggers[0].on") || lastLine(out) != guidePointer {
				t.Fatalf("誤りの行の後ろに案内の行が無い:\n%s", out)
			}
		})
	}
}

func TestMissingWorkflowDefinitionIsFollowedByAPointerToTheGuide(t *testing.T) {
	s := newSandbox(t)
	if err := os.Remove(s.workflowFile()); err != nil {
		t.Fatal(err)
	}

	r := s.dryRun()

	assertExit(t, r, 2)
	if lastLine(r.stderr) != guidePointer {
		t.Fatalf("workflow 定義が無いのに案内の行が無い:\n%s", r.stderr)
	}
}

func TestPrecheckFailuresAreNotFollowedByAPointerToTheGuide(t *testing.T) {
	// 事前検査の失敗の多くは skill の未導入で、書き方では解けない
	cases := []struct {
		name string
		args []string
	}{
		{"loop の起動", []string{"loop"}},
		{"試運転", []string{"loop", "--dry-run"}},
		{"doctor", []string{"doctor"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeWorkflowWithoutCommands(workflowWithTriggers("\n  - {name: implement, on: issue, action: /missing}"))

			r := s.run(c.args...)

			if !strings.Contains(r.stdout+r.stderr, missingSkill) {
				t.Fatalf("事前検査に落ちていない: stdout:\n%s\nstderr:\n%s", r.stdout, r.stderr)
			}
			if strings.Contains(r.stdout+r.stderr, "claude-dispatcher guide") {
				t.Fatalf("事前検査の失敗に案内の行が付いた: stdout:\n%s\nstderr:\n%s", r.stdout, r.stderr)
			}
		})
	}
}

func TestStatusAndPathsDoNotPointToTheGuideOnWorkflowDefinitionErrors(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"paths", "--json"}} {
		t.Run(args[0], func(t *testing.T) {
			s := newSandbox(t)
			s.writeWorkflowWithCommands(invalidWorkflow)

			r := s.run(args...)

			assertExit(t, r, 2)
			if !strings.Contains(r.stderr, "triggers[0].on") {
				t.Fatalf("workflow 定義の誤りを名指ししていない:\n%s", r.stderr)
			}
			if strings.Contains(r.stderr, "claude-dispatcher guide") {
				t.Fatalf("%s の失敗に案内の行が付いた:\n%s", args[0], r.stderr)
			}
		})
	}
}
