package places_test

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/places"
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
	store := places.Store{Issues: issuesOnly{}, CLs: clsOnly{}}
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
	store := places.Store{Issues: issuesOnly{}, CLs: clsOnly{}}
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

// bothKinds は issue と CL の両方の trigger を持つ workflow 定義。種類の順 (作業対象の並びと返す失敗の順) は cl が先
var bothKinds = workflow.Definition{Triggers: []trigger.Trigger{{On: target.KindCL}, {On: target.KindIssue}}}

func TestOpenItemsKeepsReadingAfterANonFatalFailureAndReturnsItWithTheItems(t *testing.T) {
	store := places.Store{Issues: issuesOnly{}, CLs: clsWithNonFatal{}}

	items, err := places.OpenItems(store, bothKinds)

	var nonFatal *target.NonFatal
	if !errors.As(err, &nonFatal) || len(items) != 2 {
		t.Fatalf("items = %v, err = %v, want CL と issue の両方と処理を続けられる失敗", items, err)
	}
}

func TestOpenItemsFailsWhenALaterKindFailsAfterANonFatalFailure(t *testing.T) {
	store := places.Store{Issues: failingIssues{}, CLs: clsWithNonFatal{}}

	items, err := places.OpenItems(store, bothKinds)

	var failure *target.Failure
	if !errors.As(err, &failure) || failure.Kind != target.Auth || items != nil {
		t.Fatalf("items = %v, err = %v, want 認証の失敗だけ", items, err)
	}
}

// issueFirst は issue と CL の両方の trigger を持ち、種類の順 (作業対象の並びと返す失敗の順) が issue から始まる workflow 定義。
var issueFirst = workflow.Definition{Triggers: []trigger.Trigger{{On: target.KindIssue}, {On: target.KindCL}}}

// waitingIssues は、CL の一覧の読み出しが始まるまで一覧を返さない issue 置き場の部品。
type waitingIssues struct {
	issuesOnly
	clStarted <-chan struct{}
}

func (w waitingIssues) OpenIssues() ([]target.Item, error) {
	<-w.clStarted
	return w.issuesOnly.OpenIssues()
}

// startingCLs は、一覧の読み出しが始まったら started を閉じる CL 置き場の部品。
type startingCLs struct {
	clsOnly
	started chan<- struct{}
}

func (s startingCLs) OpenCLs() ([]target.Item, error) {
	close(s.started)
	return s.clsOnly.OpenCLs()
}

// openItemsWithin は OpenItems を呼び、5 秒で返らなければ落とす (1 つずつ読むと、互いを待つ部品で返らなくなる)。
func openItemsWithin(t *testing.T, store places.Store, def workflow.Definition) ([]target.Item, error) {
	t.Helper()
	type result struct {
		items []target.Item
		err   error
	}
	done := make(chan result, 1)
	go func() {
		items, err := places.OpenItems(store, def)
		done <- result{items, err}
	}()
	select {
	case r := <-done:
		return r.items, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("OpenItems が 5 秒で返らない: 一覧を 1 つずつ読んでいる")
		return nil, nil
	}
}

func TestOpenItemsReadsTheListingsOfEveryKindSideBySide(t *testing.T) {
	started := make(chan struct{})
	store := places.Store{Issues: waitingIssues{clStarted: started}, CLs: startingCLs{started: started}}

	items, err := openItemsWithin(t, store, issueFirst)

	if err != nil || len(items) != 2 {
		t.Fatalf("items = %v, err = %v, want issue と CL の 1 件ずつ", items, err)
	}
}

func TestOpenItemsReturnsTheItemsInTheOrderOfTheKindsWhicheverListingEndsFirst(t *testing.T) {
	started := make(chan struct{})
	// issue の一覧は CL の読み出しが始まるまで返らない。どちらの結果が先に届いても、並びは種類の順
	store := places.Store{Issues: waitingIssues{clStarted: started}, CLs: startingCLs{started: started}}

	items, _ := openItemsWithin(t, store, issueFirst)

	if len(items) != 2 || items[0].Ref().Kind != target.KindIssue || items[1].Ref().Kind != target.KindCL {
		t.Fatalf("items = %v, want issue → CL の順", items)
	}
}

// startingFailingIssues は、一覧の読み出しが始まったら started を閉じ、致命的な失敗を返す issue 置き場の部品。
type startingFailingIssues struct {
	issuesOnly
	started chan<- struct{}
}

func (s startingFailingIssues) OpenIssues() ([]target.Item, error) {
	close(s.started)
	return nil, &target.Failure{Kind: target.Auth, Place: "acme/widgets", Err: errors.New("401 issue")}
}

// endingCLs は、issue の一覧の読み出しが始まるまで待ってから、読み終えたことを ended に残して致命的な失敗を返す CL 置き場の部品。
type endingCLs struct {
	clsOnly
	issueStarted <-chan struct{}
	ended        *atomic.Bool
}

func (e endingCLs) OpenCLs() ([]target.Item, error) {
	<-e.issueStarted
	e.ended.Store(true)
	return nil, &target.Failure{Kind: target.Auth, Place: "acme/widgets", Err: errors.New("401 cl")}
}

func TestOpenItemsReturnsOnlyAfterEveryListingHasEnded(t *testing.T) {
	started, ended := make(chan struct{}), new(atomic.Bool)
	store := places.Store{Issues: startingFailingIssues{started: started}, CLs: endingCLs{issueStarted: started, ended: ended}}

	_, _ = openItemsWithin(t, store, issueFirst)

	if !ended.Load() {
		t.Fatal("OpenItems が CL の一覧を読み終わる前に返った")
	}
}

func TestOpenItemsReturnsTheFailureOfTheFirstKindWhenEveryListingFails(t *testing.T) {
	started := make(chan struct{})
	// CL の失敗は issue の読み出しが始まってから返るが、返すのは種類の順で先の issue の失敗だけ
	store := places.Store{Issues: startingFailingIssues{started: started}, CLs: endingCLs{issueStarted: started, ended: new(atomic.Bool)}}

	items, err := openItemsWithin(t, store, issueFirst)

	if items != nil || err == nil || !strings.Contains(err.Error(), "401 issue") || strings.Contains(err.Error(), "401 cl") {
		t.Fatalf("items = %v, err = %v, want issue の認証の失敗だけ", items, err)
	}
}

// failedIssues は、致命的な失敗を返すときに failed を閉じる issue 置き場の部品。
type failedIssues struct {
	issuesOnly
	failed chan<- struct{}
}

func (f failedIssues) OpenIssues() ([]target.Item, error) {
	defer close(f.failed)
	return nil, &target.Failure{Kind: target.Auth, Place: "acme/widgets", Err: errors.New("401 issue")}
}

// clsEndingAfterIssueFailed は、issue の一覧が失敗を返すまで待ってから、読み終えたことを ended に残して CL を返す
// CL 置き場の部品 (例: issue 置き場の認証が切れた tick の、遅いが読める CL の一覧)。
type clsEndingAfterIssueFailed struct {
	clsOnly
	issueFailed <-chan struct{}
	ended       *atomic.Bool
}

func (c clsEndingAfterIssueFailed) OpenCLs() ([]target.Item, error) {
	<-c.issueFailed
	c.ended.Store(true)
	return c.clsOnly.OpenCLs()
}

func TestOpenItemsWaitsForTheOtherListingAfterTheFirstKindFailsAndReturnsThatFailure(t *testing.T) {
	failed, ended := make(chan struct{}), new(atomic.Bool)
	store := places.Store{Issues: failedIssues{failed: failed}, CLs: clsEndingAfterIssueFailed{issueFailed: failed, ended: ended}}

	items, err := openItemsWithin(t, store, issueFirst)

	var failure *target.Failure
	if !ended.Load() || items != nil || !errors.As(err, &failure) || !strings.Contains(err.Error(), "401 issue") {
		t.Fatalf("ended = %v, items = %v, err = %v, want CL の一覧を読み終えてから、作業対象なしで issue の認証の失敗", ended.Load(), items, err)
	}
}

// waitingIssuesWithNonFatal は、CL の一覧の読み出しが始まるまで待ってから、issue 1 件と処理を続けられる失敗を返す
// issue 置き場の部品。
type waitingIssuesWithNonFatal struct {
	issuesOnly
	clStarted <-chan struct{}
}

func (w waitingIssuesWithNonFatal) OpenIssues() ([]target.Item, error) {
	<-w.clStarted
	return []target.Item{target.Issue{Number: 1}}, &target.NonFatal{Place: "acme/widgets", Err: errors.New("issue の補助の要求")}
}

// startingCLsWithNonFatal は、一覧の読み出しが始まったら started を閉じ、CL 1 件と処理を続けられる失敗を返す CL 置き場の部品。
type startingCLsWithNonFatal struct {
	clsOnly
	started chan<- struct{}
}

func (s startingCLsWithNonFatal) OpenCLs() ([]target.Item, error) {
	close(s.started)
	return []target.Item{target.CL{Number: 2}}, &target.NonFatal{Place: "acme/widgets", Err: errors.New("CL の補助の要求")}
}

func TestOpenItemsJoinsTheNonFatalFailuresOfEveryKindInTheOrderOfTheKinds(t *testing.T) {
	started := make(chan struct{})
	// issue の失敗は CL の読み出しが始まってから返る。どちらの失敗が先に届いても、まとめる順は種類の順
	store := places.Store{Issues: waitingIssuesWithNonFatal{clStarted: started}, CLs: startingCLsWithNonFatal{started: started}}

	items, err := openItemsWithin(t, store, issueFirst)

	issue, cl := strings.Index(fmt.Sprint(err), "issue の補助の要求"), strings.Index(fmt.Sprint(err), "CL の補助の要求")
	if len(items) != 2 || issue < 0 || cl < 0 || issue > cl {
		t.Fatalf("items = %v, err = %v, want issue と CL の 1 件ずつと、issue → CL の順にまとめた 2 つの失敗", items, err)
	}
}
