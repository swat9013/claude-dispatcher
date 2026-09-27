package status

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/config"
	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/launch"
	"github.com/swat9013/claude-dispatcher/internal/paths"
)

// fakeMachine は ps / claude の代わりに決めた観測を返し、読まれた回数を数える。
type fakeMachine struct {
	processes    map[int]string
	processesErr error
	agents       string
	reads        int
}

func (f *fakeMachine) Processes() (map[int]string, error) {
	f.reads++
	return f.processes, f.processesErr
}

func (f *fakeMachine) Agents() (string, error) { return f.agents, nil }

// liveWorkers は起動部の代わりに、pids の worker だけが生きていると答える。
func liveWorkers(pids ...int) launch.Census {
	return func(m launch.Machine) (launch.LiveWorkers, error) {
		if m.ProcessesErr != nil {
			return nil, m.ProcessesErr
		}
		return func(w launch.WorkerLaunch) bool {
			for _, pid := range pids {
				if w.PID == pid {
					return true
				}
			}
			return false
		}, nil
	}
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

func TestCollectListsAWorkerTheLauncherTellsAlive(t *testing.T) {
	project := projectWithSpawn(t, "acme")
	probes := probesWith(&fakeMachine{processes: map[int]string{}, agents: "[]"}, liveWorkers(4242))

	reports := Collect([]paths.Project{project}, t.TempDir(), probes, time.Now())

	if w := reports[0].Workers; len(w) != 1 || !w[0].Alive.Known || !w[0].Alive.Value {
		t.Fatalf("workers = %+v, want 起動部が生きていると答えた issue 42 の worker が running で載る", w)
	}
}

func TestCollectDoesNotListAWorkerTheLauncherTellsGone(t *testing.T) {
	project := projectWithSpawn(t, "acme")
	probes := probesWith(&fakeMachine{processes: map[int]string{}, agents: "[]"}, liveWorkers())

	reports := Collect([]paths.Project{project}, t.TempDir(), probes, time.Now())

	if w := reports[0].Workers; len(w) != 0 {
		t.Fatalf("workers = %+v, want 生きていない worker は (wip を確かめられなければ) 載らない", w)
	}
}

func TestCollectReadsTheMachineOnceForEveryProject(t *testing.T) {
	machine := &fakeMachine{processes: map[int]string{}, agents: "[]"}
	projects := []paths.Project{projectWithSpawn(t, "acme"), projectWithSpawn(t, "beta")}

	Collect(projects, t.TempDir(), probesWith(machine, liveWorkers(4242)), time.Now())

	if machine.reads != 1 {
		t.Fatalf("process の一覧を %d 回読んだ, want 1 回の描画で 1 度だけ", machine.reads)
	}
}

func TestCollectMarksLivenessUnknownWhenTheProcessListCannotBeRead(t *testing.T) {
	project := projectWithSpawn(t, "acme")
	machine := &fakeMachine{processesErr: errors.New("ps failed"), agents: "[]"}

	reports := Collect([]paths.Project{project}, t.TempDir(), probesWith(machine, liveWorkers(4242)), time.Now())

	r := reports[0]
	if r.Tick.Running.Known || len(r.Workers) != 0 {
		t.Fatalf("report = %+v, want tick の実行中が ? で、生死を確かめられない worker は載らない", r)
	}
	if len(r.Notes) == 0 || !strings.HasPrefix(r.Notes[0], "process の一覧を読めない — tick の実行中と worker の生死は ?") {
		t.Fatalf("notes = %q, want 先頭に process の一覧を読めない注記", r.Notes)
	}
}

func TestReportSplitsIntoHeadingNotesAndTable(t *testing.T) {
	project := projectWithSpawn(t, "acme")
	probes := probesWith(&fakeMachine{processes: map[int]string{}, agents: "[]"}, liveWorkers(4242))
	r := Collect([]paths.Project{project}, t.TempDir(), probes, time.Now())[0]

	heading, notes, table := r.Heading(), r.NoteLines(), r.TableLines()

	if !strings.HasPrefix(heading, "acme  待機  最終 tick ") {
		t.Fatalf("heading = %q, want project 名・tick の状態・最終 tick", heading)
	}
	if len(notes) == 0 || !strings.HasPrefix(notes[0], "  ! config を読めない") {
		t.Fatalf("notes = %q, want config を読めない注記", notes)
	}
	if len(table) != 2 || !strings.HasPrefix(table[0], "ISSUE") || !strings.HasPrefix(table[1], "#42") {
		t.Fatalf("table = %q, want 見出し行と issue 42 の行", table)
	}
	if got, want := RenderTable([]Report{r}), strings.Join(append(append([]string{heading}, notes...), table...), "\n"); got != want {
		t.Fatalf("RenderTable = %q, want 見出し・注記・表をこの順に並べたもの %q", got, want)
	}
}
