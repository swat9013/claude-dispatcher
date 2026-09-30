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
	"github.com/swat9013/claude-dispatcher/internal/ticknow"
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
	// ClaudeConfigDir は Claude Code の設定 dir。transcript をこの下の projects で探す (ClaudeConfigDir 関数が決める)
	ClaudeConfigDir string
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
	// Loop は project の loop の process が居るか
	Loop Probed[bool]
	// LastTick は log.jsonl の最後の tick 行。Value が nil なら tick 行がまだ無い
	LastTick Probed[*ticklog.Line]
	// Tick は走っている tick (tick.now)。走っていないか、確かめられなければ nil
	Tick    *RunningTick
	Workers []Worker
	// RunningWorkers は起動記録の worker のうち process が生きている数 (loop の終了行 — formats.md §13.2)。
	// 起動部が生死を答えられないか、起動記録 (log.jsonl) を読めなければ ?
	RunningWorkers Probed[int]
	Notes          []string
}

// RunningTick は走っている tick。
type RunningTick struct {
	// TS は tick の開始時刻 (tick 行の ts と同じ綴り)
	TS      string
	Elapsed time.Duration
	// Orchestrator は orchestrator の実行中だけ埋まる
	Orchestrator *RunningOrchestrator
}

// RunningOrchestrator は走っている orchestrator。表の orchestrator の行になる。
type RunningOrchestrator struct {
	Elapsed time.Duration
	Session Probed[*Session]
	// Activity は transcript から読んだ最新の活動
	Activity Probed[*Activity]
	// sessionID は Session と Activity を引く鍵 (tick.now の session id)
	sessionID string
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
	// Human は issue に人待ち (ready-for-human) が付いているか。STATE の判定で要る行だけ読む
	Human Probed[bool]
	// Activity は process が生きている worker だけ埋まる。それ以外は Value が nil (表では `-`)
	Activity Probed[*Activity]
}

// WorkerState は STATE 列の値 (formats.md §10)。
type WorkerState string

const (
	// StateRunning は process が生きている
	StateRunning WorkerState = "running"
	// StateStale は process が死んでいて issue に wip が残っている (stale wip)
	StateStale WorkerState = "stale"
	// StateCL は worker の branch の最新の CL が open か merge 済み
	StateCL WorkerState = "cl"
	// StateHuman は issue に人待ちが付いている
	StateHuman WorkerState = "human"
	// StateSilent は上のどれでもない (無言の終了 — CONTEXT.md)
	StateSilent WorkerState = "silent"
)

// recentlySpawned は、終わった worker も載せる起動からの期間 (formats.md §10)。起動記録に終了時刻は無いので起動から数える
const recentlySpawned = 24 * time.Hour

// State は STATE を上から順に判定する。判定に要る値を読めていなければ ? (それより後の値は読まない)。
func (w Worker) State() Probed[WorkerState] {
	if !w.Alive.Known {
		return Probed[WorkerState]{}
	}
	if w.Alive.Value {
		return known(StateRunning)
	}
	if !w.WIP.Known {
		return Probed[WorkerState]{}
	}
	if w.WIP.Value {
		return known(StateStale)
	}
	if !w.CL.Known {
		return Probed[WorkerState]{}
	}
	if w.CL.Value != nil && w.CL.Value.Settled() {
		return known(StateCL)
	}
	if !w.Human.Known {
		return Probed[WorkerState]{}
	}
	if w.Human.Value {
		return known(StateHuman)
	}
	return known(StateSilent)
}

// needsHuman は STATE の判定に人待ちを読む必要があるか。判定の段を State 1 か所に置くため、Human だけを「読めていない」と
// 「読めた」に変えて State が変わる行を、人待ちを読む必要のある行とする (Human 以外の列が原因で ? になる行は、どちらでも
// ? なので除かれる)。
func (w Worker) needsHuman() bool {
	unread, read := w, w
	unread.Human, read.Human = Probed[bool]{}, known(false)
	return unread.State() != read.State()
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
	observedAt := clock()
	machine := probes.Machine.Observe()
	alive, aliveErr := probes.Workers(machine)
	reports := make([]Report, 0, len(projects))
	for _, project := range projects {
		c := &collector{
			report:  Report{Project: project.Name, Workers: []Worker{}, Notes: []string{}},
			machine: machine, observedAt: observedAt, isAlive: alive, isAliveErr: aliveErr, probes: probes,
		}
		report := c.collect(project, home, clock())
		report.RunningWorkers = runningWorkers(report, aliveErr)
		reports = append(reports, report)
	}
	return reports
}

// collect は 1 project の今を組む。
func (c *collector) collect(project paths.Project, home string, now time.Time) Report {
	if c.machine.ProcessesErr != nil {
		c.note("process の一覧を読めない — loop と worker の生死は ?、走っている tick は出さない (%v)", c.machine.ProcessesErr)
	} else if c.isAliveErr != nil {
		c.note("worker の生死を読めない — STATE は ? (%v)", c.isAliveErr)
	}
	c.report.Loop = c.loopRunning(project.Name)
	c.report.Tick = c.runningTick(project, now)
	c.collectWorkers(project, home, now)
	c.fillSessions()
	c.fillActivities(now)
	return c.report
}

// fillActivities は orchestrator の行と running の worker の行の ACTIVITY を transcript から埋める。読めない行は ? にして
// 注記を残し、ほかの列には影響させない (transcript の形は Claude Code の内部の仕様なので)。
func (c *collector) fillActivities(now time.Time) {
	if c.report.Tick != nil && c.report.Tick.Orchestrator != nil {
		o := c.report.Tick.Orchestrator
		o.Activity = c.activity(o.sessionID, "orchestrator", now)
	}
	for i := range c.report.Workers {
		w := &c.report.Workers[i]
		if !w.Alive.Known || !w.Alive.Value {
			w.Activity = known[*Activity](nil)
			continue
		}
		w.Activity = c.activity(w.Spawn.SessionID, fmt.Sprintf("#%d", w.Spawn.Issue), now)
	}
}

// activity は sessionID の transcript から最新の活動を読む。row は注記で行を名指す綴り。
func (c *collector) activity(sessionID, row string, now time.Time) Probed[*Activity] {
	a, err := readActivity(c.probes.ClaudeConfigDir, sessionID, now)
	if err != nil {
		c.note("%s の transcript を読めない — ACTIVITY は ? (%v)", row, err)
		return Probed[*Activity]{}
	}
	return known(a)
}

// fillSessions は orchestrator の行と worker の行の SESSION を埋める。どちらの行も無ければ claude agents を読まない。
func (c *collector) fillSessions() {
	var orchestrator *RunningOrchestrator
	if c.report.Tick != nil {
		orchestrator = c.report.Tick.Orchestrator
	}
	if orchestrator == nil && len(c.report.Workers) == 0 {
		return
	}
	sessions := c.sessions()
	if orchestrator != nil {
		orchestrator.Session = lookupIn(sessions, orchestrator.sessionID)
	}
	for i := range c.report.Workers {
		w := &c.report.Workers[i]
		w.Session = lookupIn(sessions, w.Spawn.SessionID)
	}
}

// collectWorkers は log.jsonl の起動記録から載せる worker を組む (SESSION 以外の列を埋める)。
func (c *collector) collectWorkers(project paths.Project, home string, now time.Time) {
	lines, broken, err := ticklog.Read(project.LogFile())
	if err != nil {
		// 途中までの行から最終 tick や起動記録を出すと古い像を今として見せるので、log からは何も出さない
		c.note("log.jsonl を読めない — 最終 tick と worker は出さない (%v)", err)
		return
	}
	if broken > 0 {
		c.note("%s の読めない %d 行を飛ばした", project.LogFile(), broken)
	}
	var last *ticklog.Line
	if l, ok := ticklog.Last(lines); ok {
		last = &l
	}
	c.report.LastTick = known(last)

	spawns := spawnRecords(lines)
	if len(spawns) == 0 {
		return
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

// listed は載せる起動記録を選ぶ (formats.md §10)。issue ごとの最新の起動記録は、wip が付いているか、process が生きているか、
// 起動から recentlySpawned 以内なら載せる。それより古い起動記録は process が生きているときだけ載せる — wip は issue の今の
// worker にだけ掛け、再入で起こし直した issue の前回の worker を stale に見せない。確かめられなかった (?) だけでは載せない
// (載せると log に残る過去の起動が全部並ぶ)。spawns は spawnRecords の順に並んでいること。
func listed(spawns []spawnRecord, alive func(ticklog.Spawned) Probed[bool], wip func(issue int) Probed[bool], now time.Time) []Worker {
	workers := []Worker{}
	for i, s := range spawns {
		latest := i == len(spawns)-1 || spawns[i+1].Issue != s.Issue
		a, w := alive(s.Spawned), wip(s.Issue)
		running := a.Known && a.Value
		stale := latest && w.Known && w.Value
		recent := latest && now.Sub(s.tick.At) <= recentlySpawned
		if !running && !stale && !recent {
			continue
		}
		workers = append(workers, Worker{Spawn: s.Spawned, TickTS: s.tick.TS, Elapsed: now.Sub(s.tick.At), Alive: a, WIP: w})
	}
	return workers
}

// runningWorkers は載せた worker のうち生きている数を数える (生きている worker はすべて載るので、載せた分で足りる)。
// 起動部が生死を答えられないか、起動記録 (log.jsonl) を読めないか、生死の分からない worker が居れば ?。
func runningWorkers(r Report, aliveErr error) Probed[int] {
	if aliveErr != nil || !r.LastTick.Known {
		return Probed[int]{}
	}
	n := 0
	for _, w := range r.Workers {
		if !w.Alive.Known {
			return Probed[int]{}
		}
		if w.Alive.Value {
			n++
		}
	}
	return known(n)
}

type collector struct {
	report  Report
	machine launch.Machine
	// observedAt は machine を読み始めた時刻。それより後に書かれた tick.now の pid は machine に写っていない
	observedAt time.Time
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
		c.note("wip を読めない — WIP は ? で、process の生きている worker と起動から %d 時間以内の worker だけを載せた (%v)", int(recentlySpawned.Hours()), err)
		return unknown
	}
	return func(issue int) Probed[bool] { return known(slices.Contains(numbers, issue)) }
}

func (c *collector) fillDetails(cfg config.Config, cfgErr error, gh github.Runner) {
	branchOf := map[int]string{}
	for _, w := range c.report.Workers {
		branchOf[w.Spawn.Issue] = tick.WorkerBranch(w.Spawn.Issue)
	}
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
		w.Branch = lookupIn(branches, w.Spawn.Issue)
		w.CL = lookupIn(cls, w.Spawn.Issue)
		if _, ok := cls.Value[w.Spawn.Issue]; cls.Known && !ok {
			// LatestCLs が決められなかった issue (fork の CL だけで窓を超えた)。無いと言い切らず ? にする
			w.CL = Probed[*github.CLState]{}
		}
	}
	if slices.ContainsFunc(c.report.Workers, Worker.needsHuman) {
		human := c.human(cfg, cfgErr, gh)
		for i := range c.report.Workers {
			w := &c.report.Workers[i]
			w.Human = lookupIn(human, w.Spawn.Issue)
		}
	}
}

// human は issue → 人待ちが付いているか。読めなければ ? (STATE の判定に使う行が ? になる)。
func (c *collector) human(cfg config.Config, cfgErr error, gh github.Runner) Probed[map[int]bool] {
	if cfgErr != nil || gh == nil {
		return Probed[map[int]bool]{}
	}
	numbers, err := github.HumanIssues(gh, cfg.IssueRepo)
	if err != nil {
		c.note("%s を読めない — STATE の判定に使う行は ? (%v)", config.HumanLabel, err)
		return Probed[map[int]bool]{}
	}
	human := map[int]bool{}
	for _, n := range numbers {
		human[n] = true
	}
	return known(human)
}

// lookupIn は読めた表なら key の値 (無ければ零値) を、読めなかった表なら ? を返す。
func lookupIn[K comparable, V any](table Probed[map[K]V], key K) Probed[V] {
	return Probed[V]{Value: table.Value[key], Known: table.Known}
}

// sessions は `claude agents --json` の表 (orchestrator の行と worker の行の SESSION)。
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

// loopRunning は project の loop の process (`claude-dispatcher loop <project> …`) が居るか。
func (c *collector) loopRunning(project string) Probed[bool] {
	if c.machine.ProcessesErr != nil {
		return Probed[bool]{}
	}
	for _, command := range c.machine.Processes {
		if subcommand, of := dispatcherRun(command); subcommand == "loop" && of == project {
			return known(true)
		}
	}
	return known(false)
}

// dispatcherRun は command 行が `claude-dispatcher <subcommand> <project> …` の process なら、その subcommand と project を
// 返す。そうでなければ空。argv の先頭で照合する — worker の command 行には spawn prompt の本文が載るので、途中の綴りで
// 照合すると prompt の中の文字列に当たる。
func dispatcherRun(command string) (subcommand, project string) {
	fields := strings.Fields(command)
	if len(fields) < 3 || filepath.Base(fields[0]) != "claude-dispatcher" {
		return "", ""
	}
	return fields[1], fields[2]
}

// runningTick は tick.now から走っている tick と、orchestrator の実行中ならその session id を組む (formats.md §10)。
// file の pid がこの project の tick か loop の process でなければ、異常終了で残った file (か、pid の再利用) として無視し、
// 注記に残す。ただし process の一覧を読んだ後に書かれた file は、一覧に pid が写っていないだけなので黙って出さない
// (次の描き直しで出る)。process の一覧を読めなければ確かめられないので出さない (注記は process の一覧の方で残す)。
func (c *collector) runningTick(project paths.Project, now time.Time) *RunningTick {
	file := project.TickNowFile()
	state, err := ticknow.Read(file)
	if err != nil {
		c.note("tick.now を読めない — 走っている tick は出さない (%v)", err)
		return nil
	}
	if state == nil || c.machine.ProcessesErr != nil {
		return nil
	}
	if !isTickProcess(c.machine.Processes[state.PID], project.Name) {
		if state.Written.Before(c.observedAt) {
			c.note("%s の pid %d は %s の tick の process でない — 前の tick が異常終了で残した file として無視した", file, state.PID, project.Name)
		}
		return nil
	}
	started, err := ticklog.ParseTS(state.TS)
	if err != nil {
		c.note("tick.now の開始時刻を読めない — 走っている tick は出さない (%v)", err)
		return nil
	}
	running := &RunningTick{TS: state.TS, Elapsed: now.Sub(started)}
	if state.Stage != ticknow.Orchestrator || state.Orchestrator == nil {
		return running
	}
	orchestratorStarted, err := ticklog.ParseTS(state.Orchestrator.Started)
	if err != nil {
		c.note("tick.now の orchestrator の起動時刻を読めない — orchestrator の行は出さない (%v)", err)
		return running
	}
	running.Orchestrator = &RunningOrchestrator{Elapsed: now.Sub(orchestratorStarted), sessionID: state.Orchestrator.SessionID}
	return running
}

// isTickProcess は command 行が project の tick を走らせる process (`claude-dispatcher tick <project>` か
// `claude-dispatcher loop <project> …`) か。pid が別の process に再利用されていれば false。
func isTickProcess(command, project string) bool {
	subcommand, of := dispatcherRun(command)
	return (subcommand == "tick" || subcommand == "loop") && of == project
}
