package coord

import (
	"regexp"
	"testing"
	"time"
)

// 調停 remote の commit message と push の結果の読み方 (formats.md §12)。

func TestPushIsLostWhenAPorcelainLineIsRejected(t *testing.T) {
	cases := map[string]string{
		"server が old-oid の照合で拒んだ": "To origin\n!\tabc:refs/dispatcher/x/claims/issue-1\t[remote rejected] (cannot lock ref 'refs/dispatcher/x/claims/issue-1': reference already exists)\nDone\n",
		"広告が期待値と違い git が送らなかった":    "To origin\n!\tabc:refs/dispatcher/x/claims/issue-1\t[rejected] (stale info)\nDone\n",
		"削除が拒まれた":                  "To origin\n!\t(delete):refs/dispatcher/x/claims/issue-1\t[rejected] (stale info)\nDone\n",
	}
	for name, stdout := range cases {
		t.Run(name, func(t *testing.T) {
			if !lost(stdout) {
				t.Fatalf("負けと読まなかった:\n%s", stdout)
			}
		})
	}
}

func TestPushIsNotLostWithoutARejectedLine(t *testing.T) {
	cases := map[string]string{
		"porcelain の行が無い (network の失敗)": "",
		"! の行が rejected を含まない":          "To origin\n!\tabc:refs/x\t[remote failure] (remote unexpectedly hung up)\nDone\n",
		"rejected が ! の行に無い":            "To origin\n*\tabc:refs/x\t[new reference]\nrejected\nDone\n",
	}
	for name, stdout := range cases {
		t.Run(name, func(t *testing.T) {
			if lost(stdout) {
				t.Fatalf("負けと読んだ:\n%s", stdout)
			}
		})
	}
}

func TestClaimMessageIsReadBack(t *testing.T) {
	m := Message{Kind: KindClaim, Format: Format, Loop: "host-1a2b3c4d", Target: "issue#42"}

	got := ParseCommit("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nauthor a <a@b> 0 +0000\ncommitter a <a@b> 0 +0000\n\n" + m.String())

	if got != m {
		t.Fatalf("読み戻した = %+v, 書いた = %+v", got, m)
	}
}

func TestHeartbeatMessageIsReadBack(t *testing.T) {
	m := Message{Kind: KindHeartbeat, Format: Format, Loop: "host-1a2b3c4d", Interval: 90 * time.Second, Seq: 17}

	got := ParseCommit("tree x\n\n" + m.String())

	if got != m {
		t.Fatalf("読み戻した = %+v, 書いた = %+v", got, m)
	}
}

func TestMessageWithoutAReadableFormatIsNewer(t *testing.T) {
	cases := map[string]string{
		"format が無い":       "claude-dispatcher claim\n\nloop: a\n",
		"format が数でない":     "claude-dispatcher claim\n\nformat: two\nloop: a\n",
		"format が知る版より新しい": "claude-dispatcher claim\n\nformat: 2\nloop: a\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if !ParseCommit("tree x\n\n" + body).Newer() {
				t.Fatalf("新しい版と読まなかった: %q", body)
			}
		})
	}
}

func TestMessageOfTheKnownFormatIsNotNewer(t *testing.T) {
	if ParseCommit("tree x\n\nclaude-dispatcher claim\n\nformat: 1\nloop: a\nunknown: x\n").Newer() {
		t.Fatal("知る版を新しい版と読んだ")
	}
}

func TestLoopNameIsTheSanitizedHostnameAndEightHex(t *testing.T) {
	name, err := newLoopName("Build.Host:01", func(b []byte) (int, error) {
		copy(b, []byte{0x1a, 0x2b, 0x3c, 0x4d})
		return len(b), nil
	})

	if err != nil {
		t.Fatal(err)
	}
	if name != "build.host_01-1a2b3c4d" {
		t.Fatalf("loop 名 = %q", name)
	}
}

func TestLoopHostReturnsTheHostnamePartOfABuiltLoopName(t *testing.T) {
	for host, want := range map[string]string{"build": "build", "build-host-01": "build-host-01", "Build.Host:01": "build.host_01"} {
		name, err := newLoopName(host, func(b []byte) (int, error) { return len(b), nil })
		if err != nil {
			t.Fatal(err)
		}

		if got := LoopHost(name); got != want {
			t.Fatalf("LoopHost(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestLoopHostIsTheWholeNameWithoutAHyphen(t *testing.T) {
	if got := LoopHost("nohyphen"); got != "nohyphen" {
		t.Fatalf("LoopHost = %q", got)
	}
}

func TestLoopNameChangesOnEveryStart(t *testing.T) {
	a, errA := NewLoopName()
	b, errB := NewLoopName()

	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	if a == b || !regexp.MustCompile(`-[0-9a-f]{8}$`).MatchString(a) {
		t.Fatalf("loop 名 = %q, %q", a, b)
	}
}
