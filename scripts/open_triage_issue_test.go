// Package scripts は scripts/ の shell script を Go の test から bash で撃って確かめる。
package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// triageReportLimit は open-triage-issue.sh の limit (報告を切る byte 数)
const triageReportLimit = 60000

var truncatedNote = fmt.Sprintf("\n出力が長いので先頭の %d byte で切った。全文は run の log にある\n", triageReportLimit)

// triageBody は open-triage-issue.sh を source して triage_body に前置きと報告を渡し、組んだ本文を返す。
// source した script は gh を撃たない。guard が壊れても本物の gh を撃たないよう、PATH の先頭に失敗する gh を置く
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
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\necho 'test から gh を撃った' >&2\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("open-triage-issue.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `source "$1" && triage_body "$2" "$3"`, "bash", script, preludeFile, reportFile)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
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

const testPrelude = "前置き\n"

func TestTriageBody(t *testing.T) {
	repeat := strings.Repeat
	tests := []struct {
		name      string
		report    string
		fence     string
		inside    string
		truncated bool
	}{
		{"上限内の報告は 3 個の backtick で囲んでそのまま載せる", "line1\nline2\n", "```", "line1\nline2\n", false},
		{"末尾に改行の無い報告は閉じの囲いの前に改行を足す", "line1\nno newline", "```", "line1\nno newline", false},
		{"上限ちょうどの報告は切らない", repeat("a", triageReportLimit-1) + "\n", "```", repeat("a", triageReportLimit-1) + "\n", false},
		{"上限を超える報告は先頭の上限 byte で切って注記を足す", repeat("a", 70000) + "\n", "```", repeat("a", triageReportLimit), true},
		{
			"報告中の最長の backtick の連なりより 1 つ長い囲いにする",
			"before\n```\ninside ````` run\n```go\nafter\n",
			"``````",
			"before\n```\ninside ````` run\n```go\nafter\n",
			false,
		},
		{"最長の連なりが 2 個以下なら 3 個の囲いにする", "a `b` ``c``\n", "```", "a `b` ``c``\n", false},
		{"最長の連なりがちょうど 3 個なら 4 個の囲いにする", "x\n```\ny\n", "````", "x\n```\ny\n", false},
		{"切れ目が 3 byte 文字の 1 byte 目の後ならその文字を落とす", repeat("a", triageReportLimit-1) + "あtail\n", "```", repeat("a", triageReportLimit-1), true},
		{"切れ目が 3 byte 文字の 2 byte 目の後ならその文字を落とす", repeat("a", triageReportLimit-2) + "あtail\n", "```", repeat("a", triageReportLimit-2), true},
		{"切れ目が 2 byte 文字の途中ならその文字を落とす", repeat("a", triageReportLimit-1) + "étail\n", "```", repeat("a", triageReportLimit-1), true},
		{"切れ目が 4 byte 文字の 1 byte 目の後ならその文字を落とす", repeat("a", triageReportLimit-1) + "😀tail\n", "```", repeat("a", triageReportLimit-1), true},
		{"切れ目が 4 byte 文字の 2 byte 目の後ならその文字を落とす", repeat("a", triageReportLimit-2) + "😀tail\n", "```", repeat("a", triageReportLimit-2), true},
		{"切れ目が 4 byte 文字の 3 byte 目の後ならその文字を落とす", repeat("a", triageReportLimit-3) + "😀tail\n", "```", repeat("a", triageReportLimit-3), true},
		{"切れ目で 3 byte 文字が完結していればその文字を残す", repeat("a", triageReportLimit-3) + "あtail\n", "```", repeat("a", triageReportLimit-3) + "あ", true},
		{"先頭 byte が E2 の 3 byte 文字の途中で切れたらその文字を落とす", repeat("a", triageReportLimit-1) + "—tail\n", "```", repeat("a", triageReportLimit-1), true},
		{"先頭 byte が E8 で継続 byte が AA 以上の 3 byte 文字の途中で切れたらその文字を落とす", repeat("a", triageReportLimit-1) + "語tail\n", "```", repeat("a", triageReportLimit-1), true},
		{"先頭 byte が C2 の 2 byte 文字の途中で切れたらその文字を落とす", repeat("a", triageReportLimit-1) + "©tail\n", "```", repeat("a", triageReportLimit-1), true},
		{"先頭 byte が D0 の 2 byte 文字の途中で切れたらその文字を落とす", repeat("a", triageReportLimit-1) + "дtail\n", "```", repeat("a", triageReportLimit-1), true},
		{"切れ目で 2 byte 文字が完結していればその文字を残す", repeat("a", triageReportLimit-2) + "étail\n", "```", repeat("a", triageReportLimit-2) + "é", true},
		{"切れ目の前が元から継続 byte だけならその byte を残す", repeat("a", triageReportLimit-3) + "\x80\x80\x80\x80tail\n", "```", repeat("a", triageReportLimit-3) + "\x80\x80\x80", true},
		{"切れ目で 4 byte 文字が完結していればその文字を残す", repeat("a", triageReportLimit-4) + "😀tail\n", "```", repeat("a", triageReportLimit-4) + "😀", true},
		{"元から不正な先頭 byte の後が ASCII ならその byte を残す", repeat("a", triageReportLimit-1) + "\xE3btail\n", "```", repeat("a", triageReportLimit-1) + "\xE3", true},
		{"元から不正な先頭 byte の後が別の文字の先頭ならその byte を残す", repeat("a", triageReportLimit-1) + "\xE3あtail\n", "```", repeat("a", triageReportLimit-1) + "\xE3", true},
		{"元から不正な並びが切れ目にまたがるならその byte を残す", repeat("a", triageReportLimit-1) + "\xE3\x80\n", "```", repeat("a", triageReportLimit-1) + "\xE3", true},
		{"元から不正な並びが切れ目の直後で報告ごと終わってもその byte を残す", repeat("a", triageReportLimit-1) + "\xE3\x80", "```", repeat("a", triageReportLimit-1) + "\xE3", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := triageBody(t, testPrelude, tt.report)

			if want := wantBody(testPrelude, tt.fence, tt.inside, tt.truncated); got != want {
				i := firstDiff(got, want)
				t.Errorf("本文が %d byte 目から違う\n got: %q\nwant: %q", i, around(got, i), around(want, i))
			}
		})
	}
}

// firstDiff は a と b が最初に食い違う byte の位置を返す
func firstDiff(a, b string) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}

// around は s の i byte 目の前後 60 byte を返す
func around(s string, i int) string {
	return s[max(0, i-60):min(len(s), i+60)]
}
