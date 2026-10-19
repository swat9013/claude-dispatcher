// Package checkstatechart は、vendor した scripts/check-statechart.py を python3 で撃って検査する
// (gate で撃つ script の test を `go test ./...` に載せる。CONTRIBUTING.md の「gate」)
package checkstatechart

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const header = "@startuml\n[*] --> Idle\nIdle --> Busy : start\nBusy --> Idle : finish\n"

func run(t *testing.T, puml string) (int, string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("python3 が PATH に無い (gate の前提 tool。CONTRIBUTING.md の「セットアップ」): %v", err)
	}
	path := t.TempDir() + "/chart.puml"
	if err := os.WriteFile(path, []byte(puml), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(python, "-I", "../check-statechart.py", path).CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &exit):
		return exit.ExitCode(), string(out)
	default:
		t.Fatal(err)
		return 0, ""
	}
}

func TestCheckStatechart_遷移の無い組み合わせに理由を書けば通す(t *testing.T) {
	code, out := run(t, header+"' uncovered: Idle x finish — 走っていない\n' uncovered: Busy x start — 走っている\n@enduml\n")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestCheckStatechart_遷移も理由も無い組み合わせがあれば落とす(t *testing.T) {
	code, out := run(t, header+"' uncovered: Idle x finish — 走っていない\n@enduml\n")
	if code != 1 || !strings.Contains(out, "(Busy x start)") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestCheckStatechart_理由の無いuncovered宣言は落とす(t *testing.T) {
	code, out := run(t, header+"' uncovered: Idle x finish\n' uncovered: Busy x start — 走っている\n@enduml\n")
	if code != 1 || !strings.Contains(out, "uncovered malformed") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestCheckStatechart_遷移のある組み合わせへの宣言の残骸は落とす(t *testing.T) {
	code, out := run(t, header+"' uncovered: Idle x finish — 走っていない\n' uncovered: Busy x start — 走っている\n' uncovered: Idle x start — 残骸\n@enduml\n")
	if code != 1 || !strings.Contains(out, "decl stale") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestCheckStatechart_初期状態から届かない状態は落とす(t *testing.T) {
	code, out := run(t, "@startuml\n[*] --> Idle\nLost --> Idle : back\n' uncovered: Idle x back — 戻る先\n@enduml\n")
	if code != 1 || !strings.Contains(out, "unreachable") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestCheckStatechart_色や線種を付けた矢印も遷移として数える(t *testing.T) {
	code, out := run(t, header+"Idle -[#red,dashed]-> Busy : retry\n' uncovered: Idle x finish — 走っていない\n' uncovered: Busy x start — 走っている\n@enduml\n")
	if code != 1 || !strings.Contains(out, "(Busy x retry)") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestCheckStatechart_図に描かれない行の遷移は数えない(t *testing.T) {
	cases := map[string]string{
		"block comment": "/'\nBusy --> Idle : start\n'/\n@enduml\n",
		"複数行 note":      "note left of Busy\nBusy --> Idle : start\nend note\n@enduml\n",
		"@enduml の後":    "@enduml\nBusy --> Idle : start\n",
	}
	for name, tail := range cases {
		t.Run(name, func(t *testing.T) {
			code, out := run(t, header+"' uncovered: Idle x finish — 走っていない\n"+tail)
			if code != 1 || !strings.Contains(out, "(Busy x start)") {
				t.Fatalf("exit %d: %s", code, out)
			}
		})
	}
}

func TestCheckStatechart_拒否する図の形を落とす(t *testing.T) {
	cases := map[string]struct{ puml, want string }{
		"自分以外へ出ていく遷移の無い状態":    {"@startuml\n[*] --> Idle\nIdle --> Busy\n@enduml\n", "dead end"},
		"final state へ届かない状態": {"@startuml\n[*] --> Idle\nIdle --> Done : stop\nDone --> [*]\nIdle --> Busy : go\nBusy --> Spin\nSpin --> Busy\n@enduml\n", "no termination"},
		"guard で分かれない遷移先":     {"@startuml\n[*] --> Idle\nIdle --> Busy\nIdle --> Done\nBusy --> Idle\nDone --> Idle\n@enduml\n", "nondeterministic"},
		"対応記法で読めない矢印":         {header + "Busy <-- Idle : back\n@enduml\n", "unsupported arrow"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			code, out := run(t, c.puml)
			if code != 1 || !strings.Contains(out, c.want) {
				t.Fatalf("exit %d: %s", code, out)
			}
		})
	}
}
