package loop_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/provider"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
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

// placeOf は kind の provider が host と repo から作る置き場。
func placeOf(kind provider.Kind, host, repo string) provider.Place {
	p, _ := provider.Of(kind)
	return p.NewPlace(host, repo)
}

// neverFiringTimer は明けない channel を返す After。
func neverFiringTimer(time.Duration) <-chan time.Time { return make(chan time.Time) }

func TestAListingThatTakesOverFifteenSecondsIsAnnouncedOnceBeforeItEndsAndKeepsItsResult(t *testing.T) {
	release := make(chan struct{})
	var waited []time.Duration
	var announced []target.Kind
	store := loop.Store{Issues: issuesOnly{}, CLs: heldCLs{release: release}, Slow: loop.SlowNotice{
		After: firedTimer(&waited),
		// 一覧はこの知らせが閉じるまで返らないので、知らせは読み終わる前に届いている
		Notify: func(kind target.Kind) { announced = append(announced, kind); close(release) },
	}}

	open, err := store.Open(target.KindCL)

	var nonFatal *target.NonFatal
	if !slices.Equal(announced, []target.Kind{target.KindCL}) || !slices.Equal(waited, []time.Duration{15 * time.Second}) {
		t.Errorf("知らせ = %v, 測った時間 = %v, want CL の 1 回と 15 秒", announced, waited)
	}
	if len(open) != 1 || !errors.As(err, &nonFatal) {
		t.Errorf("Open = %v, %v, want 知らせの無いときと同じ CL 1 件と処理を続けられる失敗", open, err)
	}
}

func TestAListingThatEndsWithinFifteenSecondsIsNotAnnounced(t *testing.T) {
	var announced []target.Kind
	store := loop.Store{Issues: issuesOnly{}, CLs: clsOnly{}, Slow: loop.SlowNotice{
		After:  neverFiringTimer,
		Notify: func(kind target.Kind) { announced = append(announced, kind) },
	}}

	open, err := store.Open(target.KindIssue)

	if len(announced) != 0 || err != nil || len(open) != 1 {
		t.Fatalf("知らせ = %v, Open = %v, %v, want 知らせなしで issue 1 件", announced, open, err)
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
			if got := loop.SlowListingLine(c.def, c.kind); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
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

func TestATickWhoseListingTakesOverFifteenSecondsShowsALineBeforeTheTickLine(t *testing.T) {
	r := newRig()
	r.timer = periodsThenStop(0)
	seen := make(chan struct{})
	var waited []time.Duration
	held := memoryStore{
		// 一覧は遅い一覧の行が出るまで返らない
		observe: func() ([]target.Item, error) { <-seen; return []target.Item{readyIssue(1)}, nil },
		reread:  func(target.Ref) (target.Item, error) { return readyIssue(1), nil },
	}
	def := workflow.Definition{Tracker: workflow.Tracker{Kind: provider.GitHub, Place: placeOf(provider.GitHub, "", "acme/widgets")}, Interval: time.Minute, MaxConcurrent: 1, MaxAttempts: 1, Triggers: []trigger.Trigger{readyTrigger}}

	r.run(t, loop.Options{
		Definition: def,
		Store:      func(workflow.Definition) loop.Store { return held.store() },
		Output:     announcingOutput{next: loop.Appender{W: &r.stdout}, seen: seen},
		SlowAfter:  firedTimer(&waited),
	})

	lines := strings.Split(strings.TrimSpace(r.stdout.String()), "\n")
	slow := slices.IndexFunc(lines, func(l string) bool {
		return strings.HasSuffix(l, " issue 置き場 acme/widgets の一覧の読み出しが 15 秒を超えた")
	})
	tick := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "tick ok") })
	if slow < 0 || tick < slow || !slices.Equal(waited, []time.Duration{15 * time.Second}) {
		t.Errorf("測った時間 %v, 出力:\n%s", waited, r.stdout.String())
	}
	if ticks := r.logEvents(t, "tick"); len(ticks) != 1 || ticks[0]["result"] != "ok" || strings.Contains(r.log.String(), "15 秒") {
		t.Errorf("log.jsonl:\n%s\nwant 遅い一覧を書かず、tick は ok", r.log.String())
	}
}
