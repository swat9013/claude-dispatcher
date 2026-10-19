package target_test

import (
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/target"
)

func TestParseRefReadsOnlyTheSpellingOfString(t *testing.T) {
	for _, s := range []string{"issue#42", "cl#7"} {
		ref, ok := target.ParseRef(s)
		if !ok || ref.String() != s {
			t.Errorf("ParseRef(%q) = %v, %v", s, ref, ok)
		}
	}
	for _, s := range []string{"issue-42", "#42", "issue#0", "issue#-1", "issue#+5", "issue#007", "issue#4#2", "pr#1", "issue#"} {
		if ref, ok := target.ParseRef(s); ok {
			t.Errorf("ParseRef(%q) = %v, want false", s, ref)
		}
	}
}
