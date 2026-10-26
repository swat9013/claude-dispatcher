package loop_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/places"
	"github.com/swat9013/claude-dispatcher/internal/provider"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// firedTimer は d を waited に残し、もう明けた channel を返す After。
func firedTimer(waited *[]time.Duration) func(time.Duration) <-chan time.Time {
	return func(d time.Duration) <-chan time.Time {
		*waited = append(*waited, d)
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
}

// placeOf は kind の provider が host と repo から作る置き場。
func placeOf(kind provider.Kind, host, repo string) provider.Place {
	p, _ := provider.Of(kind)
	return p.NewPlace(host, repo)
}

// announcingOutput は人が読む行を next へ渡し、遅い一覧の行を見たら seen を閉じる。
type announcingOutput struct {
	next loop.Output
	seen chan struct{}
}

func (a announcingOutput) Show(l loop.Line) {
	a.next.Show(l)
	if strings.Contains(l.Label+l.Rest, "15 秒を超えた") {
		close(a.seen)
	}
}

// slowTick は、issue の一覧が遅い一覧の行が出るまで返らない tick を 1 回回した rig。
func slowTick(t *testing.T) *rig {
	t.Helper()
	r := newRig()
	r.timer = periodsThenStop(0)
	seen := make(chan struct{})
	held := memoryStore{
		observe: func() ([]target.Item, error) { <-seen; return []target.Item{readyIssue(1)}, nil },
		reread:  func(target.Ref) (target.Item, error) { return readyIssue(1), nil },
	}
	def := workflow.Definition{Tracker: workflow.Tracker{Kind: provider.GitHub, Place: placeOf(provider.GitHub, "", "acme/widgets")}, Interval: time.Minute, MaxConcurrent: 1, MaxAttempts: 1, Triggers: []trigger.Trigger{readyTrigger}}
	r.run(t, loop.Options{
		Definition: def,
		Store:      func(workflow.Definition) places.Store { return held.store() },
		Output:     announcingOutput{next: loop.Appender{W: &r.stdout}, seen: seen},
		SlowAfter:  firedTimer(new([]time.Duration)),
	})
	return r
}

func TestATickWhoseListingTakesOverFifteenSecondsShowsALineBeforeTheTickLine(t *testing.T) {
	r := slowTick(t)

	lines := strings.Split(strings.TrimSpace(r.stdout.String()), "\n")
	slow := slices.IndexFunc(lines, func(l string) bool {
		return strings.HasSuffix(l, " issue 置き場 acme/widgets の一覧の読み出しが 15 秒を超えた")
	})
	tick := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "tick ok") })
	if slow < 0 || tick < slow {
		t.Fatalf("出力:\n%s", r.stdout.String())
	}
}

func TestATickWhoseListingTakesOverFifteenSecondsWritesASlowListingEventToTheLog(t *testing.T) {
	r := slowTick(t)

	slow := r.logEvents(t, "slow_listing")
	if len(slow) != 1 || slow[0]["kind"] != "issue" || slow[0]["place"] != "acme/widgets" {
		t.Fatalf("log.jsonl:\n%s\nwant slow_listing (kind issue, place acme/widgets) の行 1 本", r.log.String())
	}
}

func TestASlowListingDoesNotChangeTheResultOfTheTick(t *testing.T) {
	r := slowTick(t)

	if ticks := r.logEvents(t, "tick"); len(ticks) != 1 || ticks[0]["result"] != "ok" {
		t.Fatalf("log.jsonl:\n%s\nwant tick ok の行 1 本", r.log.String())
	}
}

// placeAnnouncingOutput は人が読む行を next へ渡し、遅い一覧の行を見たら、その行が名指す置き場の channel を閉じる。
type placeAnnouncingOutput struct {
	next              loop.Output
	issueSeen, clSeen chan struct{}
}

func (a placeAnnouncingOutput) Show(l loop.Line) {
	a.next.Show(l)
	switch line := l.Label + l.Rest; {
	case strings.HasPrefix(line, "issue 置き場 ") && strings.HasSuffix(line, "15 秒を超えた"):
		close(a.issueSeen)
	case strings.HasPrefix(line, "CL 置き場 ") && strings.HasSuffix(line, "15 秒を超えた"):
		close(a.clSeen)
	}
}

// slowTickOfBothKinds は、issue と CL の一覧がどちらも、自分の遅い一覧の行が出るまで返らない tick を 1 回回した rig。
// 2 つの知らせは並べて読む一覧から届き、画面の行と log.jsonl の行は lock の無い buffer に書く。
func slowTickOfBothKinds(t *testing.T) *rig {
	t.Helper()
	r := newRig()
	r.timer = periodsThenStop(0)
	issueSeen, clSeen := make(chan struct{}), make(chan struct{})
	// 一覧はどちらも空なので、読み直しは起きない
	issues := memoryStore{observe: func() ([]target.Item, error) { <-issueSeen; return nil, nil }}
	cls := memoryStore{observe: func() ([]target.Item, error) { <-clSeen; return nil, nil }}
	fixCI := trigger.Trigger{Name: "fix-ci", On: target.KindCL}
	def := workflow.Definition{Tracker: workflow.Tracker{Kind: provider.GitHub, Place: placeOf(provider.GitHub, "", "acme/widgets")}, Interval: time.Minute, MaxConcurrent: 1, MaxAttempts: 1, Triggers: []trigger.Trigger{readyTrigger, fixCI}}
	r.run(t, loop.Options{
		Definition: def,
		Store:      func(workflow.Definition) places.Store { return places.Store{Issues: issues, CLs: cls} },
		Output:     placeAnnouncingOutput{next: loop.Appender{W: &r.stdout}, issueSeen: issueSeen, clSeen: clSeen},
		SlowAfter: func(time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		},
	})
	return r
}

func TestSlowListingsOfBothKindsInOneTickWriteOneWholeLogLineEach(t *testing.T) {
	r := slowTickOfBothKinds(t)

	// logEvents は log.jsonl の行が JSON として読めなければ落ちる
	var kinds []string
	for _, l := range r.logEvents(t, "slow_listing") {
		kinds = append(kinds, l["kind"].(string))
	}
	slices.Sort(kinds)
	if !slices.Equal(kinds, []string{"cl", "issue"}) {
		t.Fatalf("log.jsonl:\n%s\nwant slow_listing の行が issue と cl に 1 本ずつ", r.log.String())
	}
}

func TestSlowListingsOfBothKindsInOneTickShowOneWholeLineEach(t *testing.T) {
	r := slowTickOfBothKinds(t)

	for _, want := range []string{"issue 置き場 acme/widgets の一覧の読み出しが 15 秒を超えた", "CL 置き場 acme/widgets の一覧の読み出しが 15 秒を超えた"} {
		n := 0
		for line := range strings.Lines(r.stdout.String()) {
			if strings.HasSuffix(line, " "+want+"\n") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("出力の %q = %d 行, want 1:\n%s", want, n, r.stdout.String())
		}
	}
}

// slowStartup は、起動時の確認で issue の一覧が遅かったと渡して loop を回した rig。
func slowStartup(t *testing.T) *rig {
	t.Helper()
	r := newRig()
	r.timer = periodsThenStop(0)
	store := memoryStore{observe: func() ([]target.Item, error) { return nil, nil }}
	def := workflow.Definition{Tracker: workflow.Tracker{Kind: provider.GitHub, Place: placeOf(provider.GitHub, "", "acme/widgets")}, Interval: time.Minute, MaxConcurrent: 1, MaxAttempts: 1, Triggers: []trigger.Trigger{readyTrigger}}
	r.run(t, loop.Options{
		Definition:       def,
		Store:            func(workflow.Definition) places.Store { return store.store() },
		StartupSlowKinds: []target.Kind{target.KindIssue},
	})
	return r
}

func TestASlowListingOfTheStartupConfirmationIsWrittenToTheLog(t *testing.T) {
	r := slowStartup(t)

	slow := r.logEvents(t, "slow_listing")
	if len(slow) != 1 || slow[0]["kind"] != "issue" || slow[0]["place"] != "acme/widgets" {
		t.Fatalf("log.jsonl:\n%s\nwant slow_listing (kind issue, place acme/widgets) の行 1 本", r.log.String())
	}
}

func TestASlowListingOfTheStartupConfirmationIsNotShownAgain(t *testing.T) {
	r := slowStartup(t)

	// 画面の行は確認のときに呼び出し側が出している
	if strings.Contains(r.stdout.String(), "15 秒を超えた") {
		t.Fatalf("出力:\n%s", r.stdout.String())
	}
}
