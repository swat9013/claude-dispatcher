package deps

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// black-box テストは internal を import しないので、自己解決が探す HOME の外の置き場を selfResolutionDirs に写して持つ。
// 写しがずれると「解決できない」を確かめるテストが実物に届くので、ここで一致を確かめる。
func TestBlackboxHarnessListsTheSameCandidatesOutsideHome(t *testing.T) {
	raw, err := os.ReadFile("../../test/blackbox/harness_test.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`var selfResolutionDirs = \[\]string\{([^}]*)\}`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("test/blackbox/harness_test.go に selfResolutionDirs が無い")
	}
	var listed []string
	for _, quoted := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[1], -1) {
		listed = append(listed, quoted[1])
	}
	var outsideHome []string
	for _, c := range candidates {
		if !strings.HasPrefix(c, "~/") {
			outsideHome = append(outsideHome, c)
		}
	}

	if !slices.Equal(listed, outsideHome) {
		t.Fatalf("selfResolutionDirs = %q, want candidates の HOME の外の置き場 %q", listed, outsideHome)
	}
}
