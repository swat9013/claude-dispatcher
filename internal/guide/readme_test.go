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

// readmeRef は README が案内の節を指す 1 箇所
type readmeRef struct {
	line    int
	section string
}

// guideRefs は README の `claude-dispatcher guide` を含む行の「」の語を、すべて案内の節名の参照とみなして返す
func guideRefs(readme string) []readmeRef {
	var refs []readmeRef
	for i, l := range strings.Split(readme, "\n") {
		if !strings.Contains(l, "claude-dispatcher guide") {
			continue
		}
		for _, m := range quoted.FindAllStringSubmatch(l, -1) {
			refs = append(refs, readmeRef{line: i + 1, section: m[1]})
		}
	}
	return refs
}

func TestSectionsTheReadmePointsToExistInTheGuide(t *testing.T) {
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("README を読めない: %v", err)
	}
	text := Text()

	refs := guideRefs(string(data))

	if len(refs) == 0 {
		t.Fatalf("README.md に `claude-dispatcher guide` の節名の参照が 1 つも無い (読み方が README とずれている)")
	}
	for _, r := range refs {
		if _, err := FindSection(text, r.section); err != nil {
			t.Errorf("README.md:%d が指す案内の節 %q が guide.md に無い", r.line, r.section)
		}
	}
}
