package blackbox_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// `--version` と doctor の版の行 (formats.md §12 / §14)。

// versionLine は `--version` の 1 行 (版と commit)。どちらも空白を含まない
var versionLine = regexp.MustCompile(`^claude-dispatcher (\S+) \((\S+)\)\n$`)

func TestVersionPrintsTheVersionAndCommitWithoutAConfig(t *testing.T) {
	s := newBareSandbox(t)

	r := s.run("--version")

	assertExit(t, r, 0)
	if !versionLine.MatchString(r.stdout) || r.stderr != "" {
		t.Fatalf("--version: stdout = %q, stderr = %q", r.stdout, r.stderr)
	}
}

func TestDoctorShowsTheSameVersionAsTheVersionFlagAsInformation(t *testing.T) {
	s := newInstallSandbox(t)
	s.satisfied()
	want := versionLine.FindStringSubmatch(s.run("--version").stdout)
	if want == nil {
		t.Fatal("--version の出力が読めない")
	}

	r := s.doctor()

	line := assertDoctorMark(t, r.stdout, "版", "--")
	if !strings.HasSuffix(line, want[1]+" ("+want[2]+")") {
		t.Fatalf("doctor の版の行 = %q, want 末尾 %s (%s)", line, want[1], want[2])
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
	flags := strings.Join(doc.Builds[0].Ldflags, " ")
	return regexp.MustCompile(`\{\{\s*\.(\w+)\s*\}\}`).ReplaceAllStringFunc(flags, func(tmpl string) string {
		name := regexp.MustCompile(`\w+`).FindString(tmpl)
		value, ok := values[name]
		if !ok {
			t.Fatalf(".goreleaser.yaml の ldflags に、テストが値を持たない template がある: %s", tmpl)
		}
		return value
	})
}

func TestReleaseBuildCarriesTheVersionAndCommitGoreleaserInjects(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude-dispatcher")
	ldflags := goreleaserLdflags(t, map[string]string{"Version": "9.8.7", "Commit": "0123456789abcdef"})
	if out, err := exec.Command("go", "build", "-ldflags", ldflags, "-o", bin, dispatcherPackage).CombinedOutput(); err != nil {
		t.Fatalf(".goreleaser.yaml の ldflags で build できない (%s): %v\n%s", ldflags, err, out)
	}

	out, err := exec.Command(bin, "--version").Output()

	if err != nil || string(out) != "claude-dispatcher 9.8.7 (0123456789abcdef)\n" {
		t.Fatalf("--version = %q (%v), want GoReleaser が埋めた版と commit", out, err)
	}
}
