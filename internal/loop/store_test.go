package loop_test

import (
	"errors"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// issuesOnly は issue だけを持つ issue 置き場の部品。
type issuesOnly struct{}

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

// clsWithNonFatal は、一覧とともに処理を続けられる失敗を返す CL 置き場の部品。
type clsWithNonFatal struct{}

func (clsWithNonFatal) OpenCLs() ([]target.Item, error) {
	return []target.Item{target.CL{Number: 2}}, &target.NonFatal{Place: "acme/widgets", Err: errors.New("HTTP 502")}
}
func (clsWithNonFatal) ReadCL(n int) (target.Item, error) { return target.CL{Number: n}, nil }

// failingIssues は一覧を読めない issue 置き場の部品。
type failingIssues struct{ issuesOnly }

func (failingIssues) OpenIssues() ([]target.Item, error) {
	return nil, &target.Failure{Kind: target.Auth, Place: "acme/widgets", Err: errors.New("401")}
}

// bothKinds は issue と CL の両方の trigger を持つ workflow 定義。cl を先に読む
var bothKinds = workflow.Definition{Triggers: []trigger.Trigger{{On: target.KindCL}, {On: target.KindIssue}}}

func TestOpenItemsKeepsReadingAfterANonFatalFailureAndReturnsItWithTheItems(t *testing.T) {
	store := loop.Store{Issues: issuesOnly{}, CLs: clsWithNonFatal{}}

	items, err := loop.OpenItems(store, bothKinds)

	var nonFatal *target.NonFatal
	if !errors.As(err, &nonFatal) || len(items) != 2 {
		t.Fatalf("items = %v, err = %v, want CL と issue の両方と処理を続けられる失敗", items, err)
	}
}

func TestOpenItemsFailsWhenALaterKindFailsAfterANonFatalFailure(t *testing.T) {
	store := loop.Store{Issues: failingIssues{}, CLs: clsWithNonFatal{}}

	items, err := loop.OpenItems(store, bothKinds)

	var failure *target.Failure
	if !errors.As(err, &failure) || failure.Kind != target.Auth || items != nil {
		t.Fatalf("items = %v, err = %v, want 認証の失敗だけ", items, err)
	}
}
