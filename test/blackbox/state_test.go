package blackbox_test

import (
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

// status の STATE 列 (formats.md §10): running / stale / cl / human / silent を上から順に判定し、終わった worker も起動から
// 24 時間は載せる。

// setHuman は `ready-for-human` の付いた issue を決める。
func (s *sandbox) setHuman(numbers ...int) {
	listed := []map[string]int{}
	for _, n := range numbers {
		listed = append(listed, map[string]int{"number": n})
	}
	s.respond("gh", stubwire.Rule{ArgsPrefix: []string{"issue", "list"}, ArgContains: humanLabel, Stdout: mustJSON(s.t, listed)})
}

// endedWorkerScenario は issue 42 の worker を 1 時間前に起動し、process が終わった状況を作る (wip・CL・人待ちは呼び出し側が決める)。
func (s *sandbox) endedWorkerScenario() {
	s.statusScenario()
	s.writeSpawnedTickLineAt(time.Now().Add(-time.Hour))
	s.setProcesses()
	s.setWip()
	s.setHuman()
	s.respondLatestCLs(map[int]latestCLPage{42: {}})
}

func stateOf(t *testing.T, stdout string) string {
	t.Helper()
	row := workerRow(stdout, 42)
	if len(row) < 3 {
		t.Fatalf("#42 の行が無い:\n%s", stdout)
	}
	return row[2]
}

func TestStateTellsHowAWorkerEnded(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(s *sandbox)
		want    string
	}{
		{"process が生きている", func(s *sandbox) { s.workerAlive(); s.setWip(42) }, "running"},
		{"wip が残っている (CL があっても)", func(s *sandbox) {
			s.setWip(42)
			s.respondLatestCLs(map[int]latestCLPage{42: {cls: []latestCL{{number: 57, state: "OPEN"}}}})
		}, "stale"},
		{"CL が open", func(s *sandbox) {
			s.respondLatestCLs(map[int]latestCLPage{42: {cls: []latestCL{{number: 57, state: "OPEN"}}}})
		}, "cl"},
		{"CL が merge 済み", func(s *sandbox) {
			s.respondLatestCLs(map[int]latestCLPage{42: {cls: []latestCL{{number: 57, state: "MERGED"}}}})
		}, "cl"},
		{"人待ちが付いている", func(s *sandbox) { s.setHuman(42) }, "human"},
		{"CL が close されただけ", func(s *sandbox) {
			s.respondLatestCLs(map[int]latestCLPage{42: {cls: []latestCL{{number: 57, state: "CLOSED"}}}})
		}, "silent"},
		{"何も残っていない", func(s *sandbox) {}, "silent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.endedWorkerScenario()
			tc.arrange(s)

			r := s.statusPS()

			if got := stateOf(t, r.stdout); got != tc.want {
				t.Fatalf("STATE = %q, want %q\n%s", got, tc.want, r.stdout)
			}
		})
	}
}

func TestStatusLeavesOutAnEndedWorkerStartedMoreThanADayAgo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(s *sandbox)
	}{
		{"cl", func(s *sandbox) {
			s.respondLatestCLs(map[int]latestCLPage{42: {cls: []latestCL{{number: 57, state: "MERGED"}}}})
		}},
		{"human", func(s *sandbox) { s.setHuman(42) }},
		{"silent", func(s *sandbox) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			s.endedWorkerScenario()
			s.writeSpawnedTickLineAt(time.Now().Add(-25 * time.Hour))
			tc.arrange(s)

			r := s.statusPS()

			if row := workerRow(r.stdout, 42); row != nil {
				t.Fatalf("起動から 24 時間を過ぎた終わった worker を載せた: %v", row)
			}
		})
	}
}

func TestStatusKeepsAStaleWorkerStartedMoreThanADayAgo(t *testing.T) {
	s := newSandbox(t)
	s.endedWorkerScenario()
	s.writeSpawnedTickLineAt(time.Now().Add(-25 * time.Hour))
	s.setWip(42)

	r := s.statusPS()

	if got := stateOf(t, r.stdout); got != "stale" {
		t.Fatalf("STATE = %q, want 24 時間を過ぎても stale を載せる\n%s", got, r.stdout)
	}
}

func TestStateIsUnknownWithANoteWhenReadyForHumanCannotBeRead(t *testing.T) {
	s := newSandbox(t)
	s.endedWorkerScenario()
	s.respond("gh", stubwire.Rule{ArgsPrefix: []string{"issue", "list"}, ArgContains: humanLabel, Exit: 1, Stderr: "boom\n"})

	r := s.statusPS()

	if got := stateOf(t, r.stdout); got != "?" || !strings.Contains(r.stdout, "  ! ready-for-human を読めない") {
		t.Fatalf("STATE = %q, want ? と注記\n%s", got, r.stdout)
	}
}

func TestStatusDoesNotReadReadyForHumanWhenNoRowNeedsIt(t *testing.T) {
	s := newSandbox(t)
	s.endedWorkerScenario()
	s.respondLatestCLs(map[int]latestCLPage{42: {cls: []latestCL{{number: 57, state: "OPEN"}}}})
	s.respond("gh", stubwire.Rule{ArgsPrefix: []string{"issue", "list"}, ArgContains: humanLabel, Exit: 1, Stderr: "boom\n"})

	r := s.statusPS()

	if got := stateOf(t, r.stdout); got != "cl" || strings.Contains(r.stdout, "ready-for-human を読めない") {
		t.Fatalf("STATE = %q, want cl のまま (人待ちを読む行が無いので読まない)\n%s", got, r.stdout)
	}
}
