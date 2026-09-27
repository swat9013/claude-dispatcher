package launch

import (
	"errors"
	"testing"
)

func TestClaudePrintWorkersTellsAWorkerAliveWhenItsPidRunsItsSession(t *testing.T) {
	machine := Machine{Processes: map[int]string{100: "/usr/local/bin/claude -p prompt --permission-mode auto --session-id s-100"}}

	live, err := ClaudePrintWorkers(machine)

	if err != nil || !live(WorkerLaunch{PID: 100, SessionID: "s-100"}) {
		t.Fatalf("live = %v, err = %v, want pid 100 の worker は生きている", live, err)
	}
}

func TestClaudePrintWorkersDoesNotMistakeAReusedPidForTheWorker(t *testing.T) {
	machine := Machine{Processes: map[int]string{100: "/usr/bin/vim notes.txt"}}

	live, err := ClaudePrintWorkers(machine)

	if err != nil || live(WorkerLaunch{PID: 100, SessionID: "s-100"}) {
		t.Fatalf("err = %v, want 別の process に再利用された pid の worker は生きていない", err)
	}
}

func TestClaudePrintWorkersDoesNotTellAWorkerWithoutASessionAlive(t *testing.T) {
	machine := Machine{Processes: map[int]string{100: "/usr/local/bin/claude -p prompt"}}

	live, err := ClaudePrintWorkers(machine)

	if err != nil || live(WorkerLaunch{PID: 100}) {
		t.Fatalf("err = %v, want session id の無い起動記録は生きていると言わない", err)
	}
}

func TestClaudePrintWorkersCannotAnswerWithoutTheProcessList(t *testing.T) {
	psFailed := errors.New("ps failed")

	_, err := ClaudePrintWorkers(Machine{ProcessesErr: psFailed})

	if !errors.Is(err, psFailed) {
		t.Fatalf("err = %v, want process の一覧を読めなかった error", err)
	}
}
