package status

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/config"
	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/launch"
	"github.com/swat9013/claude-dispatcher/internal/paths"
)

// fakeMachine は ps / claude の代わりに決めた観測を返し、観測された回数を数える。
type fakeMachine struct {
	machine  launch.Machine
	observed int
}

func (f *fakeMachine) Observe() launch.Machine {
	f.observed++
	return f.machine
}

func quietMachine() *fakeMachine {
	return &fakeMachine{machine: launch.Machine{Processes: map[int]string{}, Agents: "[]"}}
}

// answers は起動部の代わりに、pids の worker だけが生きていると答える。
func answers(pids ...int) launch.Census {
	return func(launch.Machine) (launch.Alive, error) {
		return func(w launch.WorkerLaunch) bool { return slices.Contains(pids, w.PID) }, nil
	}
}

// cannotAnswer は起動部の代わりに、一覧を組めなかったと答える。
func cannotAnswer(err error) launch.Census {
	return func(launch.Machine) (launch.Alive, error) { return nil, err }
}

// projectWithSpawn は issue 42 の worker (pid 4242) を起動した tick 行だけを持つ project を置く (config は置かない)。
func projectWithSpawn(t *testing.T, name string) paths.Project {
	t.Helper()
	dir := t.TempDir()
	p := paths.Project{Name: name, ConfigDir: filepath.Join(dir, "config"), StateDir: filepath.Join(dir, "state")}
	if err := os.MkdirAll(p.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"ts":"2026-09-26T02:48:00.000000Z","result":"ok","spawned":[{"issue":42,"kind":"start","pid":4242,"session_id":"s-42"}]}` + "\n"
	if err := os.WriteFile(p.LogFile(), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func probesWith(machine Observer, workers launch.Census) Probes {
	return Probes{
		Machine: machine,
		Workers: workers,
		Gh:      func(config.Config) (github.Runner, error) { return nil, errors.New("not used") },
		Git:     func(...string) (string, error) { return "", errors.New("not used") },
	}
}

func collectOne(t *testing.T, probes Probes) Report {
	t.Helper()
	return Collect([]paths.Project{projectWithSpawn(t, "acme")}, t.TempDir(), probes, time.Now)[0]
}

func TestCollectListsAWorkerTheLauncherTellsAlive(t *testing.T) {
	r := collectOne(t, probesWith(quietMachine(), answers(4242)))

	if w := r.Workers; len(w) != 1 || !w[0].Alive.Known || !w[0].Alive.Value {
		t.Fatalf("workers = %+v, want 起動部が生きていると答えた issue 42 の worker が running で載る", w)
	}
}

func TestCollectDoesNotListAWorkerTheLauncherTellsGone(t *testing.T) {
	r := collectOne(t, probesWith(quietMachine(), answers()))

	if len(r.Workers) != 0 {
		t.Fatalf("workers = %+v, want 生きていない worker は (wip を確かめられなければ) 載らない", r.Workers)
	}
}

func TestRunningWorkersCountsTheListedWorkersWhoseProcessLives(t *testing.T) {
	r := collectOne(t, probesWith(quietMachine(), answers(4242)))

	if got := r.RunningWorkers; !got.Known || got.Value != 1 {
		t.Fatalf("running workers = %+v, want 1", got)
	}
}

func TestRunningWorkersIsUnknownWhenTheProcessListCannotBeRead(t *testing.T) {
	machine := &fakeMachine{machine: launch.Machine{ProcessesErr: errors.New("ps failed"), Agents: "[]"}}

	r := collectOne(t, probesWith(machine, launch.ClaudePrintCensus))

	if got := r.RunningWorkers; got.Known {
		t.Fatalf("running workers = %+v, want ?", got)
	}
}

func TestCollectObservesTheMachineOnceForEveryProject(t *testing.T) {
	machine := quietMachine()
	projects := []paths.Project{projectWithSpawn(t, "acme"), projectWithSpawn(t, "beta")}

	Collect(projects, t.TempDir(), probesWith(machine, answers(4242)), time.Now)

	if machine.observed != 1 {
		t.Fatalf("機械を %d 回観測した, want 1 回の描画で 1 度だけ", machine.observed)
	}
}

func TestCollectMeasuresElapsedFromATimeTakenAfterObservingTheMachine(t *testing.T) {
	machine := quietMachine()
	observedAt := time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		if machine.observed == 0 {
			return observedAt.Add(-time.Hour) // 観測より前に取った時刻
		}
		return observedAt
	}

	r := Collect([]paths.Project{projectWithSpawn(t, "acme")}, t.TempDir(), probesWith(machine, answers(4242)), clock)[0]

	if got, want := r.Workers[0].Elapsed, observedAt.Sub(time.Date(2026, 9, 26, 2, 48, 0, 0, time.UTC)); got != want {
		t.Fatalf("elapsed = %v, want 観測の後の時刻から測った %v", got, want)
	}
}

func TestCollectNotesFirstThatTheProcessListCannotBeRead(t *testing.T) {
	machine := &fakeMachine{machine: launch.Machine{ProcessesErr: errors.New("ps failed"), Agents: "[]"}}

	r := collectOne(t, probesWith(machine, launch.ClaudePrintCensus))

	if len(r.Notes) == 0 || !strings.HasPrefix(r.Notes[0], "process の一覧を読めない — loop と worker の生死は ?") {
		t.Fatalf("notes = %q, want 先頭に process の一覧を読めない注記", r.Notes)
	}
}

func TestCollectLeavesTheLoopUnknownWhenTheProcessListCannotBeRead(t *testing.T) {
	machine := &fakeMachine{machine: launch.Machine{ProcessesErr: errors.New("ps failed"), Agents: "[]"}}

	r := collectOne(t, probesWith(machine, launch.ClaudePrintCensus))

	if r.Loop.Known {
		t.Fatalf("loop = %+v, want ?", r.Loop)
	}
}

func TestCollectNotesThatLivenessIsUnknownWhenTheLauncherCannotAnswer(t *testing.T) {
	r := collectOne(t, probesWith(quietMachine(), cannotAnswer(errors.New("claude agents failed"))))

	if len(r.Notes) == 0 || !strings.HasPrefix(r.Notes[0], "worker の生死を読めない — STATE は ?") {
		t.Fatalf("notes = %q, want 先頭に worker の生死を読めない注記", r.Notes)
	}
}

func TestCollectDoesNotListAWorkerWhoseLivenessIsUnknown(t *testing.T) {
	r := collectOne(t, probesWith(quietMachine(), cannotAnswer(errors.New("claude agents failed"))))

	if len(r.Workers) != 0 {
		t.Fatalf("workers = %+v, want 生死を確かめられない worker は (wip も ? なら) 載らない", r.Workers)
	}
}

func TestReportHeadingShowsTheProjectTheLoopAndTheLastTick(t *testing.T) {
	r := collectOne(t, probesWith(quietMachine(), answers(4242)))

	if got, want := r.Heading(), "acme  loop なし  最終 tick 2026-09-26T02:48:00Z ok"; got != want {
		t.Fatalf("heading = %q, want %q", got, want)
	}
}

func TestReportNoteLinesMarkEachNote(t *testing.T) {
	r := Report{Notes: []string{"config を読めない"}}

	if got := r.NoteLines(); len(got) != 1 || got[0] != "  ! config を読めない" {
		t.Fatalf("note lines = %q, want 注記ごとに `  ! ` を前置した行", got)
	}
}

func TestReportTableLinesAreEmptyWithoutWorkers(t *testing.T) {
	if got := (Report{}).TableLines(); len(got) != 0 {
		t.Fatalf("table lines = %q, want 載せる worker が居なければ表を出さない", got)
	}
}

func TestRenderTablePutsHeadingJudgmentNotesTableInOrderAndABlankLineBetweenProjects(t *testing.T) {
	noted := reportWithSkip("仕様に受け入れ条件が無い")
	noted.Project, noted.Notes = "acme", []string{"wip を読めない"}
	quiet := Report{Project: "beta"}

	got := RenderTable([]Report{noted, quiet}, FitPlain())

	want := "acme  loop ?  最終 tick ?\n判断 2026-09-26T03:00:00Z\n  #51 skip: 仕様に受け入れ条件が無い\n  ! wip を読めない\n\nbeta  loop ?  最終 tick ?"
	if got != want {
		t.Fatalf("RenderTable = %q, want %q", got, want)
	}
}

func TestReportTableLinesStartWithTheColumnHeaderThenOneRowPerWorker(t *testing.T) {
	r := collectOne(t, probesWith(quietMachine(), answers(4242)))

	table := r.TableLines()

	if len(table) != 2 || !strings.HasPrefix(table[0], "ISSUE") || !strings.HasPrefix(table[1], "#42") {
		t.Fatalf("table = %q, want 列の見出し行と issue 42 の行", table)
	}
}
