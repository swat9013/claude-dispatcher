// Package scripts は scripts/ の shell script を Go の test から bash で撃って確かめる。
package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const truncatedNote = "\n出力が長いので先頭の 60000 byte で切った。全文は run の log にある\n"

// triageBody は open-triage-issue.sh を source して triage_body に前置きと報告を渡し、組んだ本文を返す。
// source した script は gh を撃たない
func triageBody(t *testing.T, prelude, report string) string {
	t.Helper()
	dir := t.TempDir()
	preludeFile := filepath.Join(dir, "prelude")
	reportFile := filepath.Join(dir, "report")
	if err := os.WriteFile(preludeFile, []byte(prelude), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportFile, []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("open-triage-issue.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `source "$1" && triage_body "$2" "$3"`, "bash", script, preludeFile, reportFile)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("triage_body が失敗した: %v\n%s", err, stderr)
	}
	return string(out)
}

// wantBody は前置き・囲い・code block の中身から、期待する本文を組む
func wantBody(prelude, fence, inside string, truncated bool) string {
	var b strings.Builder
	b.WriteString(prelude)
	b.WriteString("\n" + fence + "\n" + inside)
	if !strings.HasSuffix(inside, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(fence + "\n")
	if truncated {
		b.WriteString(truncatedNote)
	}
	return b.String()
}

const prelude = "前置き\n"

func TestTriageBody(t *testing.T) {
	a := strings.Repeat
	tests := []struct {
		name      string
		report    string
		fence     string
		inside    string
		truncated bool
	}{
		{"上限内の報告は 3 個の backtick で囲んでそのまま載せる", "line1\nline2\n", "```", "line1\nline2\n", false},
		{"末尾に改行の無い報告は閉じの囲いの前に改行を足す", "line1\nno newline", "```", "line1\nno newline", false},
		{"上限を超える報告は先頭 60000 byte で切って注記を足す", a("a", 70000) + "\n", "```", a("a", 60000), true},
		{
			"報告中の最長の backtick の連なりより 1 つ長い囲いにする",
			"before\n```\ninside ````` run\n```go\nafter\n",
			"``````",
			"before\n```\ninside ````` run\n```go\nafter\n",
			false,
		},
		{"切れ目が 3 byte 文字の 1 byte 目の後ならその文字を落とす", a("a", 59999) + "あtail\n", "```", a("a", 59999), true},
		{"切れ目が 3 byte 文字の 2 byte 目の後ならその文字を落とす", a("a", 59998) + "あtail\n", "```", a("a", 59998), true},
		{"切れ目が 2 byte 文字の途中ならその文字を落とす", a("a", 59999) + "étail\n", "```", a("a", 59999), true},
		{"切れ目が 4 byte 文字の 3 byte 目の後ならその文字を落とす", a("a", 59997) + "😀tail\n", "```", a("a", 59997), true},
		{"切れ目で文字が完結していればその文字を残す", a("a", 59997) + "あtail\n", "```", a("a", 59997) + "あ", true},
		{"元から不正な先頭 byte の後が ASCII ならその byte を残す", a("a", 59999) + "\xE3btail\n", "```", a("a", 59999) + "\xE3", true},
		{"元から不正な並びが切れ目にまたがるならその byte を残す", a("a", 59999) + "\xE3\x80\n", "```", a("a", 59999) + "\xE3", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := triageBody(t, prelude, tt.report)

			if want := wantBody(prelude, tt.fence, tt.inside, tt.truncated); got != want {
				t.Errorf("本文が違う\n got 末尾: %q\nwant 末尾: %q", tail(got), tail(want))
			}
		})
	}
}

func TestTriageBodyKeepsValidUTF8WhenCuttingInsideCharacter(t *testing.T) {
	report := strings.Repeat("a", 59999) + "あtail\n"

	got := triageBody(t, prelude, report)

	if !utf8.ValidString(got) {
		t.Errorf("本文が UTF-8 として不正: 末尾 %q", tail(got))
	}
}

func tail(s string) string {
	if len(s) > 120 {
		return s[len(s)-120:]
	}
	return s
}
