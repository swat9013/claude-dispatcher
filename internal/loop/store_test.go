package loop_test

import (
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

// issuesOnly は issue だけを持つ issue 置き場の部品。
type issuesOnly struct{}

func (issuesOnly) ScopeKey() string { return "issues" }
func (issuesOnly) OpenIssues() ([]target.Item, error) {
	return []target.Item{target.Issue{Number: 1}}, nil
}
func (issuesOnly) ReadIssue(n int) (target.Item, error) { return target.Issue{Number: n}, nil }

// clsOnly は CL だけを持つ CL 置き場の部品。
type clsOnly struct{}

func (clsOnly) OpenCLs() ([]target.Item, error)   { return []target.Item{target.CL{Number: 2}}, nil }
func (clsOnly) ReadCL(n int) (target.Item, error) { return target.CL{Number: n}, nil }

func TestStoreReadsEachKindFromThePlaceThatHoldsIt(t *testing.T) {
	store := loop.Store{Issues: issuesOnly{}, CLs: clsOnly{}}
	for _, ref := range []target.Ref{{Kind: target.KindIssue, Number: 5}, {Kind: target.KindCL, Number: 6}} {
		t.Run(ref.String(), func(t *testing.T) {
			open, openErr := store.Open(ref.Kind)
			item, readErr := store.Read(ref)

			if openErr != nil || readErr != nil || len(open) != 1 || open[0].Ref().Kind != ref.Kind || item.Ref() != ref {
				t.Fatalf("Open = %v, %v / Read = %v, %v, want %s の置き場から読む", open, openErr, item, readErr, ref.Kind)
			}
		})
	}
}

func TestStoreWithoutACLPlaceRefusesToReadCLs(t *testing.T) {
	store := loop.Store{Issues: issuesOnly{}}

	_, openErr := store.Open(target.KindCL)
	_, readErr := store.Read(target.Ref{Kind: target.KindCL, Number: 1})

	if openErr == nil || readErr == nil {
		t.Fatalf("CL 置き場の無い置き場で CL を読めた: Open の誤り = %v, Read の誤り = %v", openErr, readErr)
	}
}
