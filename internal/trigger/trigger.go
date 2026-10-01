// Package trigger は trigger の述語を正規化した作業対象に当て、候補を並べる (system.md §6、formats.md §2.2 / §2.3)。
package trigger

import (
	"slices"
	"sort"
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/target"
)

// Trigger は workflow 定義が宣言する、作業対象に対する述語と action の組。
type Trigger struct {
	Name string
	On   Kind
	When IssuePredicate
}

// Kind は作業対象の種類。
type Kind string

// Issue は issue の作業対象。CL は #80 で足す
const Issue Kind = "issue"

// Author は作者の立場の条件。空なら立場を問わない。
type Author string

const (
	Collaborator    Author = "collaborator"
	NonCollaborator Author = "non_collaborator"
)

// IssuePredicate は issue 側の述語。書かれた条件はすべて AND で評価する。書かれていない条件は何にでも当たる。
type IssuePredicate struct {
	LabelsAll  []string
	LabelsAny  []string
	LabelsNone []string
	// Assignee が空でなければ、その login の人が assignee に居る issue に当たる
	Assignee string
	// Unassigned が nil でなければ、assignee が居ない (true) / 居る (false) issue に当たる
	Unassigned *bool
	Author     Author
	// Milestone が空でなければ、その題名の milestone の issue に当たる
	Milestone string
	// Blocked が nil でなければ、未解決の依存先がある (true) / 無い (false) issue に当たる
	Blocked *bool
}

// Matches は issue が述語に当たるかを返す。label は大文字と小文字を区別せずに比べる (GitHub の label と同じ)。
func (p IssuePredicate) Matches(issue target.Issue) bool {
	has := func(label string) bool {
		return slices.ContainsFunc(issue.Labels, func(l string) bool { return strings.EqualFold(l, label) })
	}
	switch {
	case !all(p.LabelsAll, has):
		return false
	case len(p.LabelsAny) > 0 && !slices.ContainsFunc(p.LabelsAny, has):
		return false
	case slices.ContainsFunc(p.LabelsNone, has):
		return false
	case p.Assignee != "" && !slices.ContainsFunc(issue.Assignees, func(a string) bool { return strings.EqualFold(a, p.Assignee) }):
		return false
	case p.Unassigned != nil && *p.Unassigned != (len(issue.Assignees) == 0):
		return false
	case p.Author == Collaborator && !issue.AuthorIsCollaborator, p.Author == NonCollaborator && issue.AuthorIsCollaborator:
		return false
	case p.Milestone != "" && issue.Milestone != p.Milestone:
		return false
	case p.Blocked != nil && *p.Blocked != (issue.OpenBlockers > 0):
		return false
	}
	return true
}

func all(labels []string, has func(string) bool) bool {
	for _, l := range labels {
		if !has(l) {
			return false
		}
	}
	return true
}

// Candidate は trigger に当たった作業対象。
type Candidate struct {
	Trigger *Trigger
	Issue   target.Issue
	// order は Trigger の宣言順
	order int
}

// Evaluate は issue ごとに trigger を宣言順に評価して最初に当たった 1 つを採り、候補を trigger の宣言順・作成日時の古い順・
// 番号の小さい順に並べて返す。
func Evaluate(triggers []Trigger, issues []target.Issue) []Candidate {
	candidates := []Candidate{}
	for _, issue := range issues {
		for i := range triggers {
			if triggers[i].On == Issue && triggers[i].When.Matches(issue) {
				candidates = append(candidates, Candidate{Trigger: &triggers[i], Issue: issue, order: i})
				break
			}
		}
	}
	sort.SliceStable(candidates, func(a, b int) bool {
		x, y := candidates[a], candidates[b]
		if x.order != y.order {
			return x.order < y.order
		}
		if !x.Issue.CreatedAt.Equal(y.Issue.CreatedAt) {
			return x.Issue.CreatedAt.Before(y.Issue.CreatedAt)
		}
		return x.Issue.Number < y.Issue.Number
	})
	return candidates
}
