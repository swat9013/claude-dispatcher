package launch

import "strings"

// Machine はマシン全体で 1 つの観測。今生きている worker の一覧を組む材料で、読むのは呼び出し側 (1 回の描画で 1 度)。
// 起動の形ごとに使う材料が違うので、どちらも渡す。
type Machine struct {
	// Processes は生きている process の pid → command 行
	Processes    map[int]string
	ProcessesErr error
	// Agents は `claude agents --json` の出力
	Agents    string
	AgentsErr error
}

// LiveWorkers は今生きている worker の一覧。起動記録の worker がその一覧に居るかを答える。
type LiveWorkers func(WorkerLaunch) bool

// Census は起動の形ごとの、今生きている worker の一覧の組み方。生死の見分け方は起動の形ごとに違うので、
// 起動部の外に出さない (system.md §13)。材料を読めず一覧を組めなければ error を返す。
type Census func(Machine) (LiveWorkers, error)

// ClaudePrintWorkers は ClaudePrint で起動した worker の一覧を process の一覧から組む Census。pid は再利用されるので、
// 起動のときに渡した session id が pid の command 行に在るかで見る (formats.md §10)。
func ClaudePrintWorkers(m Machine) (LiveWorkers, error) {
	if m.ProcessesErr != nil {
		return nil, m.ProcessesErr
	}
	return func(w WorkerLaunch) bool {
		command := m.Processes[w.PID]
		return command != "" && w.SessionID != "" && strings.Contains(command, w.SessionID)
	}, nil
}
