package trigger_test

import (
	"slices"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
)

// issue 側の述語の意味と、候補の並べ方 (formats.md §2.2 / §2.3)。

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestEachIssuePredicateNarrowsTheIssuesItMatches(t *testing.T) {
	cases := []struct {
		name      string
		predicate trigger.IssuePredicate
		matching  []target.Issue
		missing   []target.Issue
	}{
		{"書いていない条件は何にでも当たる", trigger.IssuePredicate{},
			[]target.Issue{{}, {Labels: []string{"x"}, Assignees: []string{"a"}, OpenBlockers: 1}}, nil},
		{"labels.all は列の label をすべて持つ issue に当たる", trigger.IssuePredicate{LabelsAll: []string{"a", "b"}},
			[]target.Issue{{Labels: []string{"b", "a", "c"}}}, []target.Issue{{Labels: []string{"a"}}}},
		{"labels.any は列のどれかを持つ issue に当たる", trigger.IssuePredicate{LabelsAny: []string{"bug", "docs"}},
			[]target.Issue{{Labels: []string{"docs"}}}, []target.Issue{{Labels: []string{"feature"}}}},
		{"labels.none は列の label を持たない issue に当たる", trigger.IssuePredicate{LabelsNone: []string{"needs-info"}},
			[]target.Issue{{}}, []target.Issue{{Labels: []string{"needs-info"}}}},
		{"label は大文字と小文字を区別せずに比べる", trigger.IssuePredicate{LabelsAll: []string{"Ready-For-Agent"}},
			[]target.Issue{{Labels: []string{"ready-for-agent"}}}, nil},
		{"assignee はその人が assignee に居る issue に当たる", trigger.IssuePredicate{Assignee: "alice"},
			[]target.Issue{{Assignees: []string{"bob", "Alice"}}}, []target.Issue{{Assignees: []string{"bob"}}, {}}},
		{"unassigned: true は assignee の居ない issue に当たる", trigger.IssuePredicate{Unassigned: yes()},
			[]target.Issue{{}}, []target.Issue{{Assignees: []string{"bob"}}}},
		{"unassigned: false は assignee の居る issue に当たる", trigger.IssuePredicate{Unassigned: no()},
			[]target.Issue{{Assignees: []string{"bob"}}}, []target.Issue{{}}},
		{"author: collaborator は作者が collaborator の issue に当たる", trigger.IssuePredicate{Author: trigger.Collaborator},
			[]target.Issue{{AuthorIsCollaborator: true}}, []target.Issue{{}}},
		{"author: non_collaborator は作者が collaborator でない issue に当たる", trigger.IssuePredicate{Author: trigger.NonCollaborator},
			[]target.Issue{{}}, []target.Issue{{AuthorIsCollaborator: true}}},
		{"milestone はその題名の milestone の issue に当たる", trigger.IssuePredicate{Milestone: "v1"},
			[]target.Issue{{Milestone: "v1"}}, []target.Issue{{Milestone: "v2"}, {}}},
		{"blocked: false は未解決の依存先が無い issue に当たる", trigger.IssuePredicate{Blocked: no()},
			[]target.Issue{{}}, []target.Issue{{OpenBlockers: 1}}},
		{"blocked: true は未解決の依存先がある issue に当たる", trigger.IssuePredicate{Blocked: yes()},
			[]target.Issue{{OpenBlockers: 2}}, []target.Issue{{}}},
		{"書いた条件はすべて AND で評価する", trigger.IssuePredicate{LabelsAll: []string{"ready"}, Unassigned: yes()},
			[]target.Issue{{Labels: []string{"ready"}}}, []target.Issue{{Labels: []string{"ready"}, Assignees: []string{"bob"}}, {}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, issue := range c.matching {
				if !c.predicate.Matches(issue) {
					t.Errorf("%+v に当たらない", issue)
				}
			}
			for _, issue := range c.missing {
				if c.predicate.Matches(issue) {
					t.Errorf("%+v に当たった", issue)
				}
			}
		})
	}
}

func numbers(candidates []trigger.Candidate) []int {
	var got []int
	for _, c := range candidates {
		got = append(got, c.Issue.Number)
	}
	return got
}

func TestCandidatesOfOneTriggerAreOrderedByCreationTimeNotByNumber(t *testing.T) {
	triggers := []trigger.Trigger{{Name: "t", On: trigger.Issue}}
	issues := []target.Issue{
		{Number: 10, CreatedAt: base.Add(2 * time.Hour)},
		{Number: 50, CreatedAt: base},
		{Number: 30, CreatedAt: base.Add(time.Hour)},
	}

	got := numbers(trigger.Evaluate(triggers, issues))

	if want := []int{50, 30, 10}; !slices.Equal(got, want) {
		t.Fatalf("候補 = %v, want %v", got, want)
	}
}

func TestCandidatesCreatedAtTheSameTimeAreOrderedByNumber(t *testing.T) {
	triggers := []trigger.Trigger{{Name: "t", On: trigger.Issue}}
	issues := []target.Issue{{Number: 9, CreatedAt: base}, {Number: 4, CreatedAt: base}}

	got := numbers(trigger.Evaluate(triggers, issues))

	if want := []int{4, 9}; !slices.Equal(got, want) {
		t.Fatalf("候補 = %v, want %v", got, want)
	}
}
