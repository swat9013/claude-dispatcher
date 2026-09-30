package blackbox_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"
)

// `--version` (formats.md §8)。

// versionLine は `--version` の 1 行 (版と commit)。どちらも空白を含まない
var versionLine = regexp.MustCompile(`^claude-dispatcher (\S+) \((\S+)\)\n$`)

func TestVersionPrintsTheVersionAndCommitWithoutAWorkflowDefinition(t *testing.T) {
	s := newSandbox(t)
	if err := os.Remove(s.workflowFile()); err != nil {
		t.Fatal(err)
	}

	r := s.run("--version")

	assertExit(t, r, 0)
	if !versionLine.MatchString(r.stdout) || r.stderr != "" {
		t.Fatalf("--version: stdout = %q, stderr = %q", r.stdout, r.stderr)
	}
}

// goreleaserLdflags は .goreleaser.yaml の build の ldflags を、template に values を入れて 1 本の文字列にする。
func goreleaserLdflags(t *testing.T, values map[string]string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Builds []struct {
			Ldflags []string `yaml:"ldflags"`
		} `yaml:"builds"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil || len(doc.Builds) != 1 {
		t.Fatalf(".goreleaser.yaml の builds を読めない (%d 件): %v", len(doc.Builds), err)
	}
	flags, err := template.New("ldflags").Option("missingkey=error").Parse(strings.Join(doc.Builds[0].Ldflags, " "))
	if err != nil {
		t.Fatalf(".goreleaser.yaml の ldflags を template として読めない: %v", err)
	}
	var out strings.Builder
	if err := flags.Execute(&out, values); err != nil {
		t.Fatalf(".goreleaser.yaml の ldflags に、テストが値を持たない template がある: %v", err)
	}
	return out.String()
}

func TestReleaseBuildCarriesTheVersionAndCommitGoreleaserInjects(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude-dispatcher")
	ldflags := goreleaserLdflags(t, map[string]string{"Tag": "v9.8.7", "Commit": "0123456789abcdef"})
	if out, err := exec.Command("go", "build", "-ldflags", ldflags, "-o", bin, dispatcherPackage).CombinedOutput(); err != nil {
		t.Fatalf(".goreleaser.yaml の ldflags で build できない (%s): %v\n%s", ldflags, err, out)
	}

	out, err := exec.Command(bin, "--version").Output()

	if err != nil || string(out) != "claude-dispatcher v9.8.7 (0123456789abcdef)\n" {
		t.Fatalf("--version = %q (%v), want GoReleaser が埋めた版と commit", out, err)
	}
}
