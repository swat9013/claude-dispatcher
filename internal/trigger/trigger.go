// Package trigger は trigger の述語を正規化した作業対象に当て、候補を並べる (system.md §6、formats.md §2.2 〜 §2.4)。
package trigger

import (
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/target"
)

// Trigger は workflow 定義が宣言する、作業対象に対する述語と action の組。
type Trigger struct {
	Name string
	On   target.Kind
	// Issue は On が issue の trigger の述語
	Issue IssuePredicate
	// CL は On が cl の trigger の述語
	CL CLPredicate
	// Action は worker に渡す prompt の template
	Action string
}

// Matches は作業対象が trigger に当たるかを返す。trigger の種類と違う作業対象には当たらない。
func (t Trigger) Matches(item target.Item) bool {
	switch i := item.(type) {
	case target.Issue:
		return t.On == target.KindIssue && t.Issue.Matches(i)
	case target.CL:
		return t.On == target.KindCL && t.CL.Matches(i)
	}
	return false
}

// Undecided は、作業対象が trigger に当たるかをまだ決められないか。conflict の条件を持つ CL の trigger は、CL host が
// conflict を計算し終えるまで当たるとも外れたとも決めない。終端の CL は決められる (終端として扱う)。
func (t Trigger) Undecided(item target.Item) bool {
	cl, ok := item.(target.CL)
	return ok && !cl.Closed && t.On == target.KindCL && t.CL.Conflict != nil && cl.Mergeable == target.MergeUnknown
}

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

// Matches は issue が述語に当たるかを返す。
func (p IssuePredicate) Matches(issue target.Issue) bool {
	switch {
	case !labelsMatch(p.LabelsAll, p.LabelsAny, p.LabelsNone, issue.Labels):
		return false
	case p.Assignee != "" && !slices.ContainsFunc(issue.Assignees, func(a string) bool { return strings.EqualFold(a, p.Assignee) }):
		return false
	case p.Unassigned != nil && *p.Unassigned != (len(issue.Assignees) == 0):
		return false
	case !p.Author.matches(issue.AuthorIsCollaborator):
		return false
	case p.Milestone != "" && issue.Milestone != p.Milestone:
		return false
	case p.Blocked != nil && *p.Blocked != (issue.OpenBlockers > 0):
		return false
	}
	return true
}

// CLPredicate は CL 側の述語。CL の状態の語彙と絞り込みを、すべて AND で評価する。書かれていない条件は何にでも当たる。
// 語彙の条件は nil でなければ、その状態の CL (true) / その状態でない CL (false) に当たる。
type CLPredicate struct {
	// Conflict は cl.conflict。conflict を計算し終えていない CL には true でも false でも当たらない
	Conflict *bool
	// ReviewUnresolved は cl.review_unresolved
	ReviewUnresolved *bool
	// CIFailed は cl.ci_failed
	CIFailed *bool
	// Approved は cl.approved
	Approved   *bool
	LabelsAll  []string
	LabelsAny  []string
	LabelsNone []string
	// Head が空でなければ、head branch の名前が pattern (path.Match の綴り) に当たる、同じ repo の branch の CL に当たる
	Head string
	// SameRepo が nil でなければ、head が同じ repo の branch (true) / fork の branch (false) の CL に当たる
	SameRepo *bool
	Author   Author
	// Draft が nil でなければ、draft (true) / draft でない (false) CL に当たる
	Draft *bool
}

// Matches は CL が述語に当たるかを返す。
func (p CLPredicate) Matches(cl target.CL) bool {
	switch {
	case p.Conflict != nil && (cl.Mergeable == target.MergeUnknown || *p.Conflict != (cl.Mergeable == target.MergeConflict)):
		return false
	case !state(p.ReviewUnresolved, cl.ReviewUnresolved), !state(p.CIFailed, cl.CIFailed), !state(p.Approved, cl.Approved):
		return false
	case !labelsMatch(p.LabelsAll, p.LabelsAny, p.LabelsNone, cl.Labels):
		return false
	case p.Head != "" && (!cl.SameRepo || !headMatches(p.Head, cl.Head)):
		return false
	case !state(p.SameRepo, cl.SameRepo), !state(p.Draft, cl.Draft):
		return false
	case !p.Author.matches(cl.AuthorIsCollaborator):
		return false
	}
	return true
}

// state は真偽の条件 want に、作業対象の状態 got が当たるか。条件が無ければ当たる。
func state(want *bool, got bool) bool { return want == nil || *want == got }

// headMatches は head branch の名前が pattern に当たるか。pattern の綴りは workflow 定義の検査で確かめてある。
func headMatches(pattern, head string) bool {
	ok, err := path.Match(pattern, head)
	return err == nil && ok
}

func (a Author) matches(isCollaborator bool) bool {
	switch a {
	case Collaborator:
		return isCollaborator
	case NonCollaborator:
		return !isCollaborator
	}
	return true
}

// labelsMatch は label の all / any / none の条件に labels が当たるか。label は大文字と小文字を区別せずに比べる
// (GitHub の label と同じ)。
func labelsMatch(allOf, anyOf, noneOf, labels []string) bool {
	has := func(label string) bool {
		return slices.ContainsFunc(labels, func(l string) bool { return strings.EqualFold(l, label) })
	}
	for _, l := range allOf {
		if !has(l) {
			return false
		}
	}
	return (len(anyOf) == 0 || slices.ContainsFunc(anyOf, has)) && !slices.ContainsFunc(noneOf, has)
}

// Candidate は trigger に当たった作業対象。
type Candidate struct {
	Trigger *Trigger
	Item    target.Item
	// order は Trigger の宣言順
	order int
}

// Evaluate は作業対象ごとに trigger を宣言順に評価して最初に当たった 1 つを採り、候補を trigger の宣言順・作成日時の古い順・
// 番号の小さい順に並べて返す。曖昧な CL には trigger を当てず、候補と一緒に返す。
func Evaluate(triggers []Trigger, items []target.Item) ([]Candidate, []AmbiguousHead) {
	ambiguous := ambiguousHeads(items)
	excluded := AmbiguousRefs(ambiguous)
	candidates := []Candidate{}
	for _, item := range items {
		if excluded[item.Ref()] {
			continue
		}
		for i := range triggers {
			if triggers[i].Matches(item) {
				candidates = append(candidates, Candidate{Trigger: &triggers[i], Item: item, order: i})
				break
			}
		}
	}
	sort.SliceStable(candidates, func(a, b int) bool {
		x, y := candidates[a], candidates[b]
		if x.order != y.order {
			return x.order < y.order
		}
		if !x.Item.Created().Equal(y.Item.Created()) {
			return x.Item.Created().Before(y.Item.Created())
		}
		return x.Item.Ref().Number < y.Item.Ref().Number
	})
	return candidates, ambiguous
}

// AmbiguousHead は曖昧な CL の組: 同じ repo の同じ head branch から開いた、2 本以上の open な CL。
type AmbiguousHead struct {
	Head    string
	Targets []target.Ref
}

// AmbiguousRefs は曖昧な CL の組を、作業対象の集合にする。
func AmbiguousRefs(ambiguous []AmbiguousHead) map[target.Ref]bool {
	refs := map[target.Ref]bool{}
	for _, a := range ambiguous {
		for _, ref := range a.Targets {
			refs[ref] = true
		}
	}
	return refs
}

// ambiguousHeads は items の中の曖昧な CL を、head branch の出てきた順に返す。fork の head branch は fork ごとに別の branch
// として数える。head の repo が消えた fork の CL は、どの fork の branch か分からないので数えない。
func ambiguousHeads(items []target.Item) []AmbiguousHead {
	type key struct{ repo, head string }
	var order []key
	groups := map[key][]target.Ref{}
	for _, item := range items {
		cl, ok := item.(target.CL)
		if !ok || cl.HeadRepo == "" {
			continue
		}
		k := key{cl.HeadRepo, cl.Head}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], cl.Ref())
	}
	var found []AmbiguousHead
	for _, k := range order {
		if refs := groups[k]; len(refs) > 1 {
			found = append(found, AmbiguousHead{Head: k.head, Targets: refs})
		}
	}
	return found
}
