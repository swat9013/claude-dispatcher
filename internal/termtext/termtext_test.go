package termtext

import "testing"

func TestCutDoesNotSplitAWideCharacterAcrossTheWidth(t *testing.T) {
	if got := Cut("ab日本", 5); got != "ab日" {
		t.Fatalf("Cut = %q, want ab日 (本 は幅 5 を越えるので入れない)", got)
	}
}

func TestCutLeavesTheStringWhenTheWidthIsNegative(t *testing.T) {
	if got := Cut("abc", -1); got != "abc" {
		t.Fatalf("Cut = %q, want 切らない", got)
	}
}
