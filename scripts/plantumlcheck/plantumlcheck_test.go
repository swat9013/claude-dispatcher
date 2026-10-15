// Package plantumlcheck は scripts/plantuml-check.sh を、PATH の plantuml を stub に差し替えて bash で撃って検査する
package plantumlcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stub の plantuml は -version に渡された版を名乗り、--check-syntax は FAIL_SYNTAX が立っていれば落ちる。撃たれた引数を calls に残す
const stub = `#!/usr/bin/env bash
echo "$*" >> "$STUB_CALLS"
if [ "$1" = "-version" ]; then
  [ "$STUB_VERSION" = broken ] && { echo "no java" >&2; exit 3; }
  echo "Picked up JAVA_TOOL_OPTIONS: x" >&2
  echo "PlantUML version $STUB_VERSION / abc"; exit 0
fi
[ -z "${FAIL_SYNTAX:-}" ]
`

func run(t *testing.T, stubVersion string, failSyntax bool) (int, string, string) {
	t.Helper()
	script, err := filepath.Abs("../plantuml-check.sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bin := t.TempDir()
	for path, content := range map[string]string{
		filepath.Join(root, "scripts/plantuml.version"): "1.2026.8\n",
		filepath.Join(root, "docs/design/a.puml"):       "@startuml\n@enduml\n",
		filepath.Join(bin, "plantuml"):                  stub,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	calls := filepath.Join(root, "calls")
	cmd := exec.Command("bash", script)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "STUB_CALLS="+calls, "STUB_VERSION="+stubVersion)
	if failSyntax {
		cmd.Env = append(cmd.Env, "FAIL_SYNTAX=1")
	}
	out, err := cmd.CombinedOutput()
	if cmd.ProcessState == nil {
		t.Fatalf("bash で script を撃てない: %v", err)
	}
	called, _ := os.ReadFile(calls)
	return cmd.ProcessState.ExitCode(), string(out), string(called)
}

func TestPlantumlCheck_固定した版なら構文を確かめて通す(t *testing.T) {
	code, out, called := run(t, "1.2026.8", false)
	if code != 0 || !strings.Contains(called, "--check-syntax docs/design/a.puml") {
		t.Fatalf("exit %d, out %q, calls %q", code, out, called)
	}
}

func TestPlantumlCheck_構文の誤りがあれば落とす(t *testing.T) {
	code, out, _ := run(t, "1.2026.8", true)
	if code != 1 || !strings.Contains(out, "docs/design/a.puml") {
		t.Fatalf("exit %d, out %q", code, out)
	}
}

func TestPlantumlCheck_版を取れなければ理由と入れ方を出して落とす(t *testing.T) {
	code, out, called := run(t, "broken", false)
	if code != 1 || strings.Contains(called, "--check-syntax") || !strings.Contains(out, "no java") || !strings.Contains(out, "入れ直す") {
		t.Fatalf("exit %d, out %q, calls %q", code, out, called)
	}
}

func TestPlantumlCheck_手元の版が固定した版と違えば構文を確かめずに落とす(t *testing.T) {
	code, out, called := run(t, "1.2026.9", false)
	if code != 1 || strings.Contains(called, "--check-syntax") || !strings.Contains(out, "1.2026.8") {
		t.Fatalf("exit %d, out %q, calls %q", code, out, called)
	}
}
