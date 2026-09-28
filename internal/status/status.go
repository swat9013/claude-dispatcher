// Package status は `claude-dispatcher status` (formats.md §10) の像を組む。
//
// log.jsonl の spawned (起動記録) を起点に、process の一覧・`claude agents --json`・cwd の clone の作業ツリー・
// tracker / CL host を毎回読み直して、今の worker を並べる。読み取り専用で、state dir にも外部 store にも書かない。
// lock も取らない (取ると、その一瞬に重なった tick が `locked` の行を残す)。
package status

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/config"
	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/launch"
	"github.com/swat9013/claude-dispatcher/internal/paths"
	"github.com/swat9013/claude-dispatcher/internal/tick"
	"github.com/swat9013/claude-dispatcher/internal/ticklog"
)

// Probed は外部 process から読んだ値。読めなかったら Known が false で、表では `?` になる (formats.md §10)。
type Probed[T any] struct {
	Value T
	Known bool
}

func known[T any](v T) Probed[T] { return Probed[T]{Value: v, Known: true} }

// Probes は status の現況が読む外部の口。
type Probes struct {
	// Machine は機械の観測 (process の一覧と claude のセッション一覧) を読む口
	Machine Observer
	// Workers は起動部が今生きている worker の一覧を組む口 (起動の形に合わせて選ぶ)
	Workers launch.Census
	// Gh は token を載せた env で gh を撃つ Runner を返す (config の token file を読むので config を受け取る)
	Gh func(config.Config) (github.Runner, error)
	// Git は cwd の clone で git を撃つ
	Git func(args ...string) (string, error)
}

// Observer は機械の観測を読む口。本番は ps と claude を撃つ CommandObserver、テストは fake。
type Observer interface {
	Observe() launch.Machine
}

// CommandObserver は ps と claude を撃って機械を観測する。Output は name を解決して撃ち、stdout を返す口。
type CommandObserver struct {
	Output func(name string, args ...string) (string, error)
}

func (o CommandObserver) Observe() launch.Machine {
	var m launch.Machine
	ps, err := o.Output("ps", "-A", "-o", "pid=,command=")
	m.Processes, m.ProcessesErr = parseProcesses(ps), err
	m.Agents, m.AgentsErr = o.Output("claude", "agents", "--json")
	return m
}

// parseProcesses は `ps -A -o pid=,command=` の出力を pid → command 行にする。
func parseProcesses(out string) map[int]string {
	processes := map[int]string{}
	for _, line := range strings.Split(out, "\n") {
		pid, command, _ := strings.Cut(strings.TrimSpace(line), " ")
		if n, err := strconv.Atoi(pid); err == nil {
			processes[n] = strings.TrimSpace(command)
		}
	}
	return processes
}

// Report は 1 project の像。
type Report struct {
	Project string
	Tick    Tick
	Workers []Worker
	Notes   []string
}

// Tick は tick の状態。Last.Value が nil なら tick 行がまだ無い。
type Tick struct {
	Running Probed[bool]
	Last    Probed[*ticklog.Line]
}

// Worker は表の 1 行。
type Worker struct {
	Spawn   ticklog.Spawned
	TickTS  string
	Elapsed time.Duration
	Alive   Probed[bool]
	WIP     Probed[bool]
	Session Probed[*Session]
	Branch  Probed[*Branch]
	CL      Probed[*github.CLState]
}

// Session は `claude agents --json` の行のうち表に出す分。
type Session struct{ ID, Status, State string }

// Branch は cwd の clone にある worker の作業ツリーの branch。
type Branch struct {
	Name  string
	Ahead Probed[int]
}

// Collect は project ごとの今を組む。機械の観測はマシンで 1 つなので、1 度だけ読んで全 project で共有する。
// 経過の基準の時刻は観測を読んだ後に clock から取る — 観測 (ps / claude) に時間が掛かっても、経過を観測より前の
// 時刻で測って短く見せないため。
func Collect(projects []paths.Project, home string, probes Probes, clock func() time.Time) []Report {
	machine := probes.Machine.Observe()
	alive, aliveErr := probes.Workers(machine)
	reports := make([]Report, 0, len(projects))
	for _, project := range projects {
		c := &collector{
			report:  Report{Project: project.Name, Workers: []Worker{}, Notes: []string{}},
			machine: machine, isAlive: alive, isAliveErr: aliveErr, probes: probes,
		}
		reports = append(reports, c.collect(project, home, clock()))
	}
	return reports
}

// collect は 1 project の今を組む。
func (c *collector) collect(project paths.Project, home string, now time.Time) Report {
	if c.machine.ProcessesErr != nil {
		c.note("process の一覧を読めない — tick の実行中と worker の生死は ? (%v)", c.machine.ProcessesErr)
	} else if c.isAliveErr != nil {
		c.note("worker の生死を読めない — STATE は ? (%v)", c.isAliveErr)
	}
	c.report.Tick.Running = c.tickRunning(project.Name)
	lines, broken, err := ticklog.Read(project.LogFile())
	if err != nil {
		// 途中までの行から最終 tick や起動記録を出すと古い像を今として見せるので、log からは何も出さない
		c.note("log.jsonl を読めない — 最終 tick と worker は出さない (%v)", err)
		return c.report
	}
	if broken > 0 {
		c.note("%s の読めない %d 行を飛ばした", project.LogFile(), broken)
	}
	var last *ticklog.Line
	if l, ok := ticklog.Last(lines); ok {
		last = &l
	}
	c.report.Tick.Last = known(last)

	spawns := spawnRecords(lines)
	if len(spawns) == 0 {
		return c.report
	}
	cfg, cfgErr := config.Load(project.ConfigFile(), home)
	if cfgErr != nil {
		c.note("config を読めない — WIP / BRANCH / CL は ? (%v)", cfgErr)
	}
	var gh github.Runner
	if cfgErr == nil {
		if gh, err = c.probes.Gh(cfg); err != nil {
			c.note("gh を撃てない — WIP / CL は ? (%v)", err)
		}
	}
	c.report.Workers = listed(spawns, c.alive, c.wip(cfg, cfgErr, gh), now)
	if len(c.report.Workers) > 0 {
		c.fillDetails(cfg, cfgErr, gh)
	}
	return c.report
}

type spawnRecord struct {
	ticklog.Spawned
	tick ticklog.Line
}

// spawnRecords は log の起動記録を issue ごと、起動した tick の順に並べる。
func spawnRecords(lines []ticklog.Line) []spawnRecord {
	var spawns []spawnRecord
	for _, line := range lines {
		for _, s := range line.Spawned {
			spawns = append(spawns, spawnRecord{Spawned: s, tick: line})
		}
	}
	slices.SortStableFunc(spawns, func(a, b spawnRecord) int {
		if a.Issue != b.Issue {
			return a.Issue - b.Issue
		}
		return a.tick.At.Compare(b.tick.At)
	})
	return spawns
}

// listed は載せる起動記録を選ぶ (formats.md §10)。issue ごとの最新の起動記録は wip が付いているか process が生きていれば、
// それより古い起動記録は process が生きているときだけ載せる — wip は issue の今の worker にだけ掛け、再入で起こし直した
// issue の前回の worker を「exited + wip」(stale wip の手掛かり) に見せない。確かめられなかった (?) だけでは載せない
// (載せると log に残る過去の起動が全部並ぶ)。spawns は spawnRecords の順に並んでいること。
func listed(spawns []spawnRecord, alive func(ticklog.Spawned) Probed[bool], wip func(issue int) Probed[bool], now time.Time) []Worker {
	workers := []Worker{}
	for i, s := range spawns {
		latest := i == len(spawns)-1 || spawns[i+1].Issue != s.Issue
		a, w := alive(s.Spawned), wip(s.Issue)
		running := a.Known && a.Value
		stale := latest && w.Known && w.Value
		if !running && !stale {
			continue
		}
		workers = append(workers, Worker{Spawn: s.Spawned, TickTS: s.tick.TS, Elapsed: now.Sub(s.tick.At), Alive: a, WIP: w})
	}
	return workers
}

type collector struct {
	report  Report
	machine launch.Machine
	// isAlive は起動部が組んだ今生きている worker の一覧。組めなければ isAliveErr
	isAlive    launch.Alive
	isAliveErr error
	probes     Probes
}

func (c *collector) note(format string, args ...any) {
	c.report.Notes = append(c.report.Notes, fmt.Sprintf(format, args...))
}

// alive は起動記録の worker が今生きているかを起動部の一覧に尋ねる。一覧を組めなければ ?。
func (c *collector) alive(s ticklog.Spawned) Probed[bool] {
	if c.isAliveErr != nil {
		return Probed[bool]{}
	}
	return known(c.isAlive(launch.WorkerLaunch{PID: s.PID, SessionID: s.SessionID}))
}

// wip は issue に wip が付いているかを返す関数。wip を読めなければどの issue も ?。
func (c *collector) wip(cfg config.Config, cfgErr error, gh github.Runner) func(int) Probed[bool] {
	unknown := func(int) Probed[bool] { return Probed[bool]{} }
	if cfgErr != nil || gh == nil {
		return unknown
	}
	numbers, err := github.WIPIssues(gh, cfg.IssueRepo)
	if err != nil {
		c.note("wip を読めない — WIP は ? で、process の生きている worker だけを載せた (%v)", err)
		return unknown
	}
	return func(issue int) Probed[bool] { return known(slices.Contains(numbers, issue)) }
}

func (c *collector) fillDetails(cfg config.Config, cfgErr error, gh github.Runner) {
	branchOf := map[int]string{}
	for _, w := range c.report.Workers {
		branchOf[w.Spawn.Issue] = tick.WorkerBranch(w.Spawn.Issue)
	}
	sessions := c.sessions()
	var branches Probed[map[int]*Branch]
	var cls Probed[map[int]*github.CLState]
	if cfgErr == nil {
		branches = c.branches(cfg, branchOf)
		if gh != nil {
			cls = c.cls(cfg, gh, branchOf)
		}
	}
	for i := range c.report.Workers {
		w := &c.report.Workers[i]
		w.Session = lookupIn(sessions, w.Spawn.SessionID)
		w.Branch = lookupIn(branches, w.Spawn.Issue)
		w.CL = lookupIn(cls, w.Spawn.Issue)
		if _, ok := cls.Value[w.Spawn.Issue]; cls.Known && !ok {
			// LatestCLs が決められなかった issue (fork の CL だけで窓を超えた)。無いと言い切らず ? にする
			w.CL = Probed[*github.CLState]{}
		}
	}
}

// lookupIn は読めた表なら key の値 (無ければ零値) を、読めなかった表なら ? を返す。
func lookupIn[K comparable, V any](table Probed[map[K]V], key K) Probed[V] {
	return Probed[V]{Value: table.Value[key], Known: table.Known}
}

func (c *collector) sessions() Probed[map[string]*Session] {
	err := c.machine.AgentsErr
	var rows []struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		State     string `json:"state"`
		SessionID string `json:"sessionId"`
	}
	if err == nil {
		err = json.Unmarshal([]byte(c.machine.Agents), &rows)
	}
	if err != nil {
		c.note("claude agents を読めない — SESSION は ? (%v)", err)
		return Probed[map[string]*Session]{}
	}
	sessions := map[string]*Session{}
	for _, row := range rows {
		if row.SessionID != "" {
			sessions[row.SessionID] = &Session{ID: row.ID, Status: row.Status, State: row.State}
		}
	}
	return known(sessions)
}

// branches は issue → cwd の clone にある worker の作業ツリーの branch。cwd の clone が CL 置き場でなければ、同じ番号の
// 別 repo の branch を拾うので引かない。
func (c *collector) branches(cfg config.Config, branchOf map[int]string) Probed[map[int]*Branch] {
	url, err := c.probes.Git("remote", "get-url", "origin")
	if err != nil {
		c.note("cwd の clone を読めない — BRANCH は ? (%v)", err)
		return Probed[map[int]*Branch]{}
	}
	if origin, err := config.RepoFromRemote(url); err != nil || !config.SameRepo(origin, cfg.CLRepo) {
		c.note("cwd の clone (%s) は %s でない — BRANCH は ? (その clone を cwd にして撃つ)", strings.TrimSpace(url), cfg.CLRepo)
		return Probed[map[int]*Branch]{}
	}
	trees, err := c.probes.Git("worktree", "list", "--porcelain")
	if err != nil {
		c.note("cwd の clone の作業ツリーを読めない — BRANCH は ? (%v)", err)
		return Probed[map[int]*Branch]{}
	}
	var names []string
	for _, line := range strings.Split(trees, "\n") {
		if name, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			names = append(names, name)
		}
	}
	base, baseErr := c.probes.Git("rev-parse", "--abbrev-ref", "origin/HEAD")
	if baseErr != nil {
		c.note("origin/HEAD を読めない — ahead は ? (%v)", baseErr)
	}
	branches := map[int]*Branch{}
	for issue, name := range branchOf {
		if !slices.Contains(names, name) {
			continue
		}
		b := &Branch{Name: name}
		if baseErr == nil {
			b.Ahead = c.ahead(strings.TrimSpace(base), name)
		}
		branches[issue] = b
	}
	return known(branches)
}

func (c *collector) ahead(base, name string) Probed[int] {
	out, err := c.probes.Git("rev-list", "--count", base+".."+name)
	if err == nil {
		var n int
		if n, err = strconv.Atoi(strings.TrimSpace(out)); err == nil {
			return known(n)
		}
	}
	c.note("%s の ahead を数えられない (%v)", name, err)
	return Probed[int]{}
}

func (c *collector) cls(cfg config.Config, gh github.Runner, branchOf map[int]string) Probed[map[int]*github.CLState] {
	cls, undetermined, err := github.LatestCLs(gh, cfg.CLRepo, branchOf)
	if err != nil {
		c.note("CL を読めない — CL は ? (%v)", err)
		return Probed[map[int]*github.CLState]{}
	}
	for _, issue := range undetermined {
		c.note("#%d の CL を決められない — CL は ? (branch %s の新しい順の %d 本がすべて fork の CL で、まだ続きがある)", issue, branchOf[issue], github.LatestCLsWindow)
	}
	return known(cls)
}

// tickRunning は `claude-dispatcher … tick <project>` の process (試運転を除く) が居るか。
func (c *collector) tickRunning(project string) Probed[bool] {
	if c.machine.ProcessesErr != nil {
		return Probed[bool]{}
	}
	for _, command := range c.machine.Processes {
		fields := strings.Fields(command)
		for i := 0; i+2 < len(fields); i++ {
			if filepath.Base(fields[i]) == "claude-dispatcher" && fields[i+1] == "tick" && fields[i+2] == project &&
				!slices.Contains(fields, "--dry-run") {
				return known(true)
			}
		}
	}
	return known(false)
}
