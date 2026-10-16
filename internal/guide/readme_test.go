package guide

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// readmePath は repo の root の README.md。README は細部を `claude-dispatcher guide` の節名で指す
const readmePath = "../../README.md"

// quoted は「」で囲んだ語
var quoted = regexp.MustCompile(`「([^」]+)」`)

func TestSectionsTheReadmePointsToExistInTheGuide(t *testing.T) {
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("README を読めない: %v", err)
	}
	text := Text()

	refs := 0
	for i, l := range strings.Split(string(data), "\n") {
		if !strings.Contains(l, "claude-dispatcher guide") {
			continue
		}
		for _, m := range quoted.FindAllStringSubmatch(l, -1) {
			refs++
			if _, err := FindSection(text, m[1]); err != nil {
				t.Errorf("README.md:%d が指す案内の節「%s」が guide.md に無い: %v", i+1, m[1], err)
			}
		}
	}

	if refs == 0 {
		t.Fatalf("README.md に `claude-dispatcher guide` の節名の参照が 1 つも無い (読み方が README とずれている)")
	}
}
