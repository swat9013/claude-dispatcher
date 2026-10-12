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

// refs は issue と CL の作業対象 1 件ずつ。
var refs = []target.Ref{{Kind: target.KindIssue, Number: 5}, {Kind: target.KindCL, Number: 6}}

func TestStoreListsEachKindFromThePlaceThatHoldsIt(t *testing.T) {
	store := loop.Store{Issues: issuesOnly{}, CLs: clsOnly{}}
	for _, ref := range refs {
		t.Run(string(ref.Kind), func(t *testing.T) {
			open, err := store.Open(ref.Kind)

			if err != nil || len(open) != 1 || open[0].Ref().Kind != ref.Kind {
				t.Fatalf("Open(%s) = %v, %v, want %s の置き場の一覧", ref.Kind, open, err, ref.Kind)
			}
		})
	}
}

func TestStoreRereadsEachKindFromThePlaceThatHoldsIt(t *testing.T) {
	store := loop.Store{Issues: issuesOnly{}, CLs: clsOnly{}}
	for _, ref := range refs {
		t.Run(ref.String(), func(t *testing.T) {
			item, err := store.Read(ref)

			if err != nil || item.Ref() != ref {
				t.Fatalf("Read(%s) = %v, %v, want %s の置き場から読む", ref, item, err, ref.Kind)
			}
		})
	}
}

func TestStoreWithoutACLPlaceRefusesToListCLs(t *testing.T) {
	store := loop.Store{Issues: issuesOnly{}}

	_, err := store.Open(target.KindCL)

	if err == nil {
		t.Fatal("CL 置き場の無い置き場で CL の一覧を読めた")
	}
}

func TestStoreWithoutACLPlaceRefusesToRereadACL(t *testing.T) {
	store := loop.Store{Issues: issuesOnly{}}

	_, err := store.Read(target.Ref{Kind: target.KindCL, Number: 1})

	if err == nil {
		t.Fatal("CL 置き場の無い置き場で CL を読み直せた")
	}
}
