package trigger_test

import (
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
)

// 読み直した作業対象を trigger に照らした 4 つの観測 (statechart.puml の出来事)。

func TestObserveClassifiesARereadItemIntoOneOfFourObservations(t *testing.T) {
	readyIssue := trigger.Trigger{On: target.KindIssue, Issue: trigger.IssuePredicate{LabelsAll: []string{"ready"}}}
	conflicted := trigger.Trigger{On: target.KindCL, CL: trigger.CLPredicate{Conflict: yes()}}
	anyCL := trigger.Trigger{On: target.KindCL}
	cases := []struct {
		name    string
		trigger trigger.Trigger
		item    target.Item
		want    trigger.Observation
	}{
		{"当たる open な issue は当たったまま", readyIssue, target.Issue{Labels: []string{"ready"}}, trigger.ObservedStillMatches},
		{"当たらない open な issue は trigger 外れ", readyIssue, target.Issue{}, trigger.ObservedLeftTrigger},
		{"閉じた issue は当たっていても終端", readyIssue, target.Issue{Closed: true, Labels: []string{"ready"}}, trigger.ObservedTerminal},
		{"issue の trigger に open な CL は trigger 外れ", readyIssue, target.CL{}, trigger.ObservedLeftTrigger},
		{"当たる open な CL は当たったまま", conflicted, target.CL{Mergeable: target.MergeConflict}, trigger.ObservedStillMatches},
		{"当たらない open な CL は trigger 外れ", conflicted, target.CL{Mergeable: target.MergeClean}, trigger.ObservedLeftTrigger},
		{"conflict 未計算の open な CL は conflict の条件の trigger に未決", conflicted, target.CL{Mergeable: target.MergeUnknown}, trigger.ObservedUndecided},
		{"conflict 未計算の CL も conflict の条件の無い trigger には決められる", anyCL, target.CL{Mergeable: target.MergeUnknown}, trigger.ObservedStillMatches},
		{"閉じた CL は終端", conflicted, target.CL{Closed: true, Mergeable: target.MergeConflict}, trigger.ObservedTerminal},
		{"merge した CL は conflict 未計算でも未決でなく終端", conflicted, target.CL{Closed: true, Mergeable: target.MergeUnknown}, trigger.ObservedTerminal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.trigger.Observe(c.item); got != c.want {
				t.Errorf("Observe = %d, want %d", got, c.want)
			}
		})
	}
}

func TestObservationScreenIsTheSentenceOfTheReasonScreen(t *testing.T) {
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
		if got := c.observed.Screen(); got != c.want {
			t.Errorf("Observation(%d).Screen() = %q, want %q", c.observed, got, c.want)
		}
	}
}
