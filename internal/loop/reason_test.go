package loop_test

import (
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
)

// 観測の画面の文は、その観測の reason の画面の文 (formats.md §4 の reason の表)。
func TestObservationScreenIsTheSentenceOfItsReason(t *testing.T) {
	cases := []struct {
		observed trigger.Observation
		want     string
	}{
		{trigger.ObservedStillMatches, "trigger に当たったまま"},
		{trigger.ObservedTerminal, "終端"},
		{trigger.ObservedLeftTrigger, "trigger から外れた"},
		{trigger.ObservedUndecided, "当たるかをまだ決められない"},
	}
	for _, c := range cases {
		if got := loop.ObservationScreen(c.observed); got != c.want {
			t.Errorf("ObservationScreen(%d) = %q, want %q", c.observed, got, c.want)
		}
	}
}
