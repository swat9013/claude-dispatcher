package target_test

import (
	"slices"
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

func TestSortedRefsOrdersByKindThenNumber(t *testing.T) {
	refs := map[target.Ref]bool{
		{Kind: target.KindIssue, Number: 10}: true, {Kind: target.KindCL, Number: 2}: true, {Kind: target.KindIssue, Number: 9}: true,
	}

	got := target.SortedRefs(refs)

	want := []target.Ref{{Kind: target.KindCL, Number: 2}, {Kind: target.KindIssue, Number: 9}, {Kind: target.KindIssue, Number: 10}}
	if !slices.Equal(got, want) {
		t.Fatalf("SortedRefs = %v, want %v", got, want)
	}
}
