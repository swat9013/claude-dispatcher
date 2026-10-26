package blackbox_test

import (
	"os"
	"path/filepath"
	"regexp"
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

// usageSection は usage の出力から、見出し name の節の中身 (次の空行まで) を切り出す。節の綴りは formats.md §10
func usageSection(t *testing.T, out, name string) string {
	t.Helper()
	_, body, found := strings.Cut(out, "\n"+name+":\n")
	if !found {
		t.Fatalf("usage に節 %s が無い:\n%s", name, out)
	}
	body, _, _ = strings.Cut(body, "\n\n")
	return body
}

func TestUsageListsSectionsInTheStandardOrder(t *testing.T) {
	// 節の並びは formats.md §10
	s := newSandbox(t)

	r := s.run("--help")

	assertExit(t, r, 0)
	if !strings.HasPrefix(r.stdout, "Usage: claude-dispatcher ") {
		t.Fatalf("usage が `Usage:` の行で始まらない:\n%s", r.stdout)
	}
	last := 0
	for _, section := range []string{"\nCommands:\n", "\nOptions:\n", "\nArguments:\n"} {
		i := strings.Index(r.stdout, section)
		if i < last {
			t.Fatalf("節 %q が無いか、並びが違う:\n%s", strings.TrimSpace(section), r.stdout)
		}
		last = i
	}
}

func TestUsageWithoutArgumentsGoesToStderrWithExit2(t *testing.T) {
	s := newSandbox(t)

	r := s.run()

	assertExit(t, r, 2)
	if r.stdout != "" || !strings.HasPrefix(r.stderr, "Usage: claude-dispatcher ") {
		t.Fatalf("引数が無いのに usage だけを stderr に出していない: stdout = %q\nstderr:\n%s", r.stdout, r.stderr)
	}
}

func TestHelpFlagAfterAnyCommandPrintsTheSameUsageWithoutReadingTheRest(t *testing.T) {
	// command の後ろの -h・--help は command と他の flag・引数を読まない (formats.md §10)。読めば誤りになる引数を前に置く
	s := newSandbox(t)
	want := s.run("--help")
	commands := regexp.MustCompile(`(?m)^  ([a-z-]+) `).FindAllStringSubmatch(usageSection(t, want.stdout, "Commands"), -1)
	if len(commands) == 0 {
		t.Fatalf("usage から subcommand を読めない:\n%s", want.stdout)
	}
	for _, c := range commands {
		for _, spelling := range []string{"-h", "--help"} {
			t.Run(c[1]+" "+spelling, func(t *testing.T) {
				r := s.run(c[1], "--no-such-flag", "extra", spelling)

				assertExit(t, r, 0)
				if r.stdout != want.stdout || r.stderr != "" {
					t.Fatalf("--help と同じ一覧を stdout だけに出していない: stderr = %q\nstdout:\n%s", r.stderr, r.stdout)
				}
			})
		}
	}
	s.assertNoGh("command の後ろの -h・--help は")
}

func TestHelpWithAnArgumentIsAnArgumentErrorFollowedByTheUsage(t *testing.T) {
	s := newSandbox(t)
	usage := s.run("--help")

	r := s.run("help", "loop")

	assertExit(t, r, 2)
	errLine, rest, _ := strings.Cut(r.stderr, "\n")
	if r.stdout != "" || !strings.Contains(errLine, "loop") || rest != usage.stdout {
		t.Fatalf("誤りの行の後ろに同じ一覧を stderr に出していない: stdout = %q\nstderr:\n%s", r.stdout, r.stderr)
	}
}

func TestUsageMarksHelpFlagAsWritableInPlaceOfOrAfterACommand(t *testing.T) {
	s := newSandbox(t)

	r := s.run("--help")

	assertExit(t, r, 0)
	options := usageSection(t, r.stdout, "Options")
	for spelling, mark := range map[string]string{"-h, --help": "(command の代わりか後ろ) ", "--version": "(command の代わり) "} {
		help := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(spelling) + ` +(.*)$`).FindStringSubmatch(options)
		if help == nil || !strings.HasPrefix(help[1], mark) {
			t.Errorf("Options の %s の説明が %q で始まらない:\n%s", spelling, mark, options)
		}
	}
}

func TestGuideCoversEverySubcommandInTheUsage(t *testing.T) {
	// 利用者の手元には README が無いので、usage に並ぶ subcommand の使い方は案内にしか無い
	s := newSandbox(t)
	usage := s.run("--help")
	guide := s.run("guide")
	commands := usageSection(t, usage.stdout, "Commands")
	subcommand := regexp.MustCompile(`(?m)^  ([a-z]+) `)

	matches := subcommand.FindAllStringSubmatch(commands, -1)

	if len(matches) == 0 {
		t.Fatalf("usage から subcommand を読めない:\n%s", usage.stdout)
	}
	for _, m := range matches {
		if !strings.Contains(guide.stdout, "claude-dispatcher "+m[1]) {
			t.Errorf("案内に `claude-dispatcher %s` が出てこない", m[1])
		}
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
