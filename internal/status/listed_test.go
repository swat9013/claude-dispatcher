package status

import (
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/ticklog"
)

func spawnAt(issue, pid int, at time.Time) spawnRecord {
	return spawnRecord{Spawned: ticklog.Spawned{Issue: issue, Kind: "start", PID: pid}, tick: ticklog.Line{At: at}}
}

func TestListedPutsWIPOnlyOnTheLatestSpawnOfAnIssue(t *testing.T) {
	now := time.Now()
	older, latest := spawnAt(42, 100, now.Add(-time.Hour)), spawnAt(42, 200, now.Add(-time.Minute))
	dead := func(ticklog.Spawned) Probed[bool] { return known(false) }
	wip := func(int) Probed[bool] { return known(true) }

	got := listed([]spawnRecord{older, latest}, dead, wip, now)

	if len(got) != 1 || got[0].Spawn.PID != 200 {
		t.Fatalf("listed = %+v, want issue 42 の最新の起動 (pid 200) だけ", got)
	}
}

func TestListedKeepsAnOlderSpawnWhoseProcessIsAlive(t *testing.T) {
	now := time.Now()
	older, latest := spawnAt(42, 100, now.Add(-time.Hour)), spawnAt(42, 200, now.Add(-time.Minute))
	alive := func(s ticklog.Spawned) Probed[bool] { return known(s.PID == 100) }
	noWIP := func(int) Probed[bool] { return known(false) }

	got := listed([]spawnRecord{older, latest}, alive, noWIP, now)

	if len(got) != 1 || got[0].Spawn.PID != 100 {
		t.Fatalf("listed = %+v, want 生きている古い起動 (pid 100) だけ", got)
	}
}

func TestListedDoesNotListSpawnsItCouldNotCheck(t *testing.T) {
	now := time.Now()
	unknown := func(ticklog.Spawned) Probed[bool] { return Probed[bool]{} }
	unknownWIP := func(int) Probed[bool] { return Probed[bool]{} }

	got := listed([]spawnRecord{spawnAt(42, 100, now), spawnAt(43, 200, now)}, unknown, unknownWIP, now)

	if len(got) != 0 {
		t.Fatalf("listed = %+v, want 確かめられなかった起動は載せない", got)
	}
}
