package places_test

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/places"
	"github.com/swat9013/claude-dispatcher/internal/provider"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// heldCLs は、release が閉じるまで一覧を返さない CL 置き場の部品。返すのは CL 1 件と処理を続けられる失敗。
type heldCLs struct {
	clsOnly
	release <-chan struct{}
}

func (h heldCLs) OpenCLs() ([]target.Item, error) {
	<-h.release
	return []target.Item{target.CL{Number: 2}}, &target.NonFatal{Place: "acme/widgets", Err: errors.New("HTTP 502")}
}

// firedTimer は d を waited に残し、もう明けた channel を返す After。
func firedTimer(waited *[]time.Duration) func(time.Duration) <-chan time.Time {
	return func(d time.Duration) <-chan time.Time {
		*waited = append(*waited, d)
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
}

// neverFiringTimer は明けない channel を返す After。
func neverFiringTimer(time.Duration) <-chan time.Time { return make(chan time.Time) }

// placeOf は kind の provider が host と repo から作る置き場。
func placeOf(kind provider.Kind, host, repo string) provider.Place {
	p, _ := provider.Of(kind)
	return p.NewPlace(host, repo)
}

// slowCLs は、一覧が 15 秒を超える CL 置き場。一覧は知らせが届くまで返らないので、知らせは読み終わる前に届いている。
// 知らせた種類を announced に、測った時間を waited に残す。
func slowCLs(announced *[]target.Kind, waited *[]time.Duration) places.Store {
	release := make(chan struct{})
	return places.Store{Issues: issuesOnly{}, CLs: heldCLs{release: release}}.WithSlow(places.SlowNotice{
		After:  firedTimer(waited),
		Notify: func(kind target.Kind) { *announced = append(*announced, kind); close(release) },
	})
}

func TestAListingThatTakesOverFifteenSecondsIsAnnouncedOnceBeforeItEnds(t *testing.T) {
	var announced []target.Kind
	store := slowCLs(&announced, new([]time.Duration))

	_, _ = store.Open(target.KindCL)

	if !slices.Equal(announced, []target.Kind{target.KindCL}) {
		t.Fatalf("知らせ = %v, want CL の 1 回", announced)
	}
}

func TestTheThresholdOfASlowListingIsFifteenSeconds(t *testing.T) {
	var waited []time.Duration
	store := slowCLs(new([]target.Kind), &waited)

	_, _ = store.Open(target.KindCL)

	if !slices.Equal(waited, []time.Duration{15 * time.Second}) {
		t.Fatalf("測った時間 = %v, want 15 秒", waited)
	}
}

func TestAnAnnouncedListingKeepsItsItemsAndItsFailure(t *testing.T) {
	store := slowCLs(new([]target.Kind), new([]time.Duration))

	open, err := store.Open(target.KindCL)

	var nonFatal *target.NonFatal
	if len(open) != 1 || !errors.As(err, &nonFatal) {
		t.Fatalf("Open = %v, %v, want 知らせの無いときと同じ CL 1 件と処理を続けられる失敗", open, err)
	}
}

func TestAListingThatEndsWithinFifteenSecondsIsNotAnnounced(t *testing.T) {
	var announced []target.Kind
	store := places.Store{Issues: issuesOnly{}, CLs: clsOnly{}}.WithSlow(places.SlowNotice{
		After:  neverFiringTimer,
		Notify: func(kind target.Kind) { announced = append(announced, kind) },
	})

	open, err := store.Open(target.KindIssue)

	if len(announced) != 0 || err != nil || len(open) != 1 {
		t.Fatalf("知らせ = %v, Open = %v, %v, want 知らせなしで issue 1 件", announced, open, err)
	}
}

// heldIssues は、release が閉じるまで一覧を返さない issue 置き場の部品。
type heldIssues struct {
	issuesOnly
	release <-chan struct{}
}

func (h heldIssues) OpenIssues() ([]target.Item, error) {
	<-h.release
	return h.issuesOnly.OpenIssues()
}

func TestSlowListingsReadSideBySideAreAnnouncedOneAtATime(t *testing.T) {
	released := map[target.Kind]chan struct{}{target.KindIssue: make(chan struct{}), target.KindCL: make(chan struct{})}
	// 見張りが 2 つとも測り始めてから知らせる。知らせが同時に呼ばれれば、重なりを overlapped に、lock の無い announced への
	// 書き込みを -race に残す
	var timers, active atomic.Int32
	bothWatching := make(chan struct{})
	var announced []target.Kind
	overlapped := new(atomic.Bool)
	store := places.Store{Issues: heldIssues{release: released[target.KindIssue]}, CLs: heldCLs{release: released[target.KindCL]}}.WithSlow(places.SlowNotice{
		After: func(time.Duration) <-chan time.Time {
			if timers.Add(1) == 2 {
				close(bothWatching)
			}
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		},
		Notify: func(kind target.Kind) {
			if active.Add(1) > 1 {
				overlapped.Store(true)
			}
			<-bothWatching
			announced = append(announced, kind)
			active.Add(-1)
			close(released[kind])
		},
	})

	_, _ = openItemsWithin(t, store, issueFirst)

	slices.Sort(announced)
	if overlapped.Load() || !slices.Equal(announced, []target.Kind{target.KindCL, target.KindIssue}) {
		t.Fatalf("重なり = %v, 知らせ = %v, want issue と CL を 1 回ずつ、重ならずに", overlapped.Load(), announced)
	}
}

func TestTheSlowListingLineNamesThePlaceTheWayDoctorDoes(t *testing.T) {
	onGitHub := workflow.Definition{Tracker: workflow.Tracker{Kind: provider.GitHub, Place: placeOf(provider.GitHub, "", "acme/widgets")}}
	jiraWithGitLab := workflow.Definition{
		Tracker: workflow.Tracker{Kind: provider.Jira, Place: placeOf(provider.Jira, "acme.atlassian.net", "WIDGETS")},
		CL:      &workflow.CL{Kind: provider.GitLab, Place: placeOf(provider.GitLab, "gitlab.com", "acme/widgets")},
	}
	cases := []struct {
		name string
		def  workflow.Definition
		kind target.Kind
		want string
	}{
		{"github の issue", onGitHub, target.KindIssue, "issue 置き場 acme/widgets の一覧の読み出しが 15 秒を超えた"},
		{"github の CL", onGitHub, target.KindCL, "CL 置き場 acme/widgets の一覧の読み出しが 15 秒を超えた"},
		{"jira の issue", jiraWithGitLab, target.KindIssue, "issue 置き場 acme.atlassian.net/WIDGETS の一覧の読み出しが 15 秒を超えた"},
		{"jira の cl: の CL", jiraWithGitLab, target.KindCL, "CL 置き場 gitlab.com/acme/widgets の一覧の読み出しが 15 秒を超えた"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := places.SlowListingLine(c.def, c.kind); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
