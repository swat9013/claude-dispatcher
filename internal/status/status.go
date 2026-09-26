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
	"github.com/swat9013/claude-dispatcher/internal/paths"
	"github.com/swat9013/claude-dispatcher/internal/ticklog"
)

// Unknown は外部 process が失敗して確かめられなかった値 (表でも JSON でも同じ綴り)。
const Unknown = "?"

// Probes は project ごとに撃つ外部 process の読み口。失敗したら error を返し、status はその列を Unknown にして注記を残す。
type Probes struct {
	// Gh は token を載せた env で gh を撃つ Runner を返す (config の token file を読むので config を受け取る)
	Gh func(config.Config) (github.Runner, error)
	// Git は cwd の clone で git を撃つ
	Git func(args ...string) (string, error)
}

// Machine はマシン全体で 1 つの観測 (process の一覧と claude のセッション一覧)。1 回の描画で 1 度だけ読み、全 project で共有する。
type Machine struct {
	// Processes は生きている process の pid → command 行
	Processes    map[int]string
	ProcessesErr error
	// Agents は `claude agents --json` の出力
	Agents    string
	AgentsErr error
}

// Report は 1 project の像。表と `--json` の共通の形。
type Report struct {
	Project string   `json:"project"`
	Tick    Tick     `json:"tick"`
	Workers []Worker `json:"workers"`
	Notes   []string `json:"notes"`
}

type Tick struct {
	// Running は bool か Unknown
	Running    any     `json:"running"`
	LastTS     *string `json:"last_ts"`
	LastResult *string `json:"last_result"`
}

// Worker は表の 1 行。any の field は値か Unknown。
type Worker struct {
	Issue      int    `json:"issue"`
	Kind       string `json:"kind"`
	PID        int    `json:"pid"`
	State      string `json:"state"`
	ElapsedSec int    `json:"elapsed_sec"`
	WIP        any    `json:"wip"`
	TickTS     string `json:"tick_ts"`
	Session    any    `json:"session"`
	Branch     any    `json:"branch"`
	CL         any    `json:"cl"`

	sessionID string
}

// Session は `claude agents --json` の行のうち表に出す分。
type Session struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	State  string `json:"state"`
}

// Branch は cwd の clone にある worker の作業ツリーの branch。Ahead は数か Unknown。
type Branch struct {
	Name  string `json:"name"`
	Ahead any    `json:"ahead"`
}

// Collect は 1 project の今を組む。
func Collect(project paths.Project, home string, machine Machine, probes Probes, now time.Time) Report {
	r := &collector{report: Report{Project: project.Name, Workers: []Worker{}, Notes: []string{}}, probes: probes, machine: machine}
	processes, procErr := machine.Processes, machine.ProcessesErr
	if procErr != nil {
		r.note("process の一覧を読めない — tick の実行中と worker の生死は ? (%v)", procErr)
	}
	lines, broken, err := ticklog.Read(project.LogFile())
	if err != nil {
		// 途中までの行から最終 tick や起動記録を出すと古い像を今として見せるので、log からは何も出さない
		r.note("log.jsonl を読めない — 最終 tick と worker は出さない (%v)", err)
		unknown := Unknown
		r.report.Tick = Tick{Running: tickRunning(project.Name, processes, procErr), LastTS: &unknown, LastResult: &unknown}
		return r.report
	}
	if broken > 0 {
		r.note("%s の読めない %d 行を飛ばした", project.LogFile(), broken)
	}
	r.report.Tick = Tick{Running: tickRunning(project.Name, processes, procErr)}
	if last, ok := ticklog.Last(lines); ok {
		r.report.Tick.LastTS, r.report.Tick.LastResult = &last.TS, &last.Result
	}

	var spawns []spawnRecord
	for _, line := range lines {
		for _, s := range line.Spawned {
			spawns = append(spawns, spawnRecord{Spawned: s, tick: line})
		}
	}
	if len(spawns) == 0 {
		return r.report
	}

	cfg, cfgErr := config.Load(project.ConfigFile(), home)
	if cfgErr != nil {
		r.note("config を読めない — WIP / BRANCH / CL は ? (%v)", cfgErr)
	}
	var gh github.Runner
	if cfgErr == nil {
		if gh, err = probes.Gh(cfg); err != nil {
			r.note("gh を撃てない — WIP / CL は ? (%v)", err)
		}
	}
	wip := r.wip(cfg, cfgErr, gh)

	slices.SortStableFunc(spawns, func(a, b spawnRecord) int {
		if a.Issue != b.Issue {
			return a.Issue - b.Issue
		}
		return a.tick.At.Compare(b.tick.At)
	})
	for i, s := range spawns {
		alive := any(Unknown)
		if procErr == nil {
			alive = isWorkerProcess(processes[s.PID], s.SessionID)
		}
		issueWIP := any(Unknown)
		if wip != nil {
			issueWIP = slices.Contains(wip, s.Issue)
		}
		// wip はその issue の今の worker (最新の起動記録) にだけ掛ける。古い起動記録に掛けると、再入で起こし直した
		// issue の前回の worker が「exited + wip」(stale wip の手掛かり) に見える。古い起動は process が生きているときだけ載せる
		latest := i == len(spawns)-1 || spawns[i+1].Issue != s.Issue
		// 確かめられなかった (?) だけでは載せない — 載せると log に残る過去の起動が全部並ぶ
		if alive != true && (!latest || issueWIP != true) {
			continue
		}
		state := Unknown
		if alive != Unknown {
			state = map[bool]string{true: "running", false: "exited"}[alive.(bool)]
		}
		r.report.Workers = append(r.report.Workers, Worker{
			Issue: s.Issue, Kind: s.Kind, PID: s.PID, State: state, ElapsedSec: int(now.Sub(s.tick.At).Seconds()),
			WIP: issueWIP, TickTS: s.tick.TS, sessionID: s.SessionID,
		})
	}
	if len(r.report.Workers) > 0 {
		r.fillDetails(cfg, cfgErr, gh)
	}
	return r.report
}

type spawnRecord struct {
	ticklog.Spawned
	tick ticklog.Line
}

type collector struct {
	report  Report
	probes  Probes
	machine Machine
}

func (r *collector) note(format string, args ...any) {
	r.report.Notes = append(r.report.Notes, fmt.Sprintf(format, args...))
}

// wip は wip の付いた issue の番号。読めなければ nil。
func (r *collector) wip(cfg config.Config, cfgErr error, gh github.Runner) []int {
	if cfgErr != nil || gh == nil {
		return nil
	}
	numbers, err := github.WIPIssues(gh, cfg.IssueRepo)
	if err != nil {
		r.note("wip を読めない — process の生きている worker だけを載せた (%v)", err)
		return nil
	}
	return numbers
}

func (r *collector) fillDetails(cfg config.Config, cfgErr error, gh github.Runner) {
	var issues []int
	for _, w := range r.report.Workers {
		if !slices.Contains(issues, w.Issue) {
			issues = append(issues, w.Issue)
		}
	}
	sessions := r.sessions()
	branches := any(Unknown)
	cls := any(Unknown)
	if cfgErr == nil {
		branches = r.branches(cfg, issues)
		if gh != nil {
			cls = r.cls(cfg, gh, issues)
		}
	}
	for i := range r.report.Workers {
		w := &r.report.Workers[i]
		w.Session = lookup(sessions, w.sessionID)
		w.Branch = lookup(branches, w.Issue)
		w.CL = lookup(cls, w.Issue)
	}
}

// lookup は table が Unknown なら Unknown を、map なら key の値 (無ければ nil) を返す。
func lookup[K comparable](table any, key K) any {
	m, ok := table.(map[K]any)
	if !ok {
		return Unknown
	}
	return m[key]
}

func (r *collector) sessions() any {
	out, err := r.machine.Agents, r.machine.AgentsErr
	var rows []struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		State     string `json:"state"`
		SessionID string `json:"sessionId"`
	}
	if err == nil {
		err = json.Unmarshal([]byte(out), &rows)
	}
	if err != nil {
		r.note("claude agents を読めない — SESSION は ? (%v)", err)
		return Unknown
	}
	sessions := map[string]any{}
	for _, row := range rows {
		if row.SessionID != "" {
			sessions[row.SessionID] = Session{ID: row.ID, Status: row.Status, State: row.State}
		}
	}
	return sessions
}

// branches は issue → cwd の clone にある worker の作業ツリーの branch。cwd の clone が CL 置き場でなければ、同じ番号の
// 別 repo の branch を拾うので引かない。
func (r *collector) branches(cfg config.Config, issues []int) any {
	origin, err := r.probes.Git("remote", "get-url", "origin")
	if err != nil {
		r.note("cwd の clone を読めない — BRANCH は ? (%v)", err)
		return Unknown
	}
	// GitHub の owner / name は大文字小文字を区別しない
	origin = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(origin), "/"), ".git")
	want := strings.ToLower(cfg.CLRepo.String())
	if lower := strings.ToLower(origin); !strings.HasSuffix(lower, "/"+want) && !strings.HasSuffix(lower, ":"+want) {
		r.note("cwd の clone (%s) は %s でない — BRANCH は ? (その clone を cwd にして撃つ)", origin, cfg.CLRepo)
		return Unknown
	}
	listed, err := r.probes.Git("worktree", "list", "--porcelain")
	if err != nil {
		r.note("cwd の clone の作業ツリーを読めない — BRANCH は ? (%v)", err)
		return Unknown
	}
	var names []string
	for _, line := range strings.Split(listed, "\n") {
		if name, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			names = append(names, name)
		}
	}
	base, baseErr := r.probes.Git("rev-parse", "--abbrev-ref", "origin/HEAD")
	if baseErr != nil {
		r.note("origin/HEAD を読めない — ahead は ? (%v)", baseErr)
	}
	branches := map[int]any{}
	for _, issue := range issues {
		name := fmt.Sprintf("worktree-issue-%d", issue)
		if !slices.Contains(names, name) {
			continue
		}
		ahead := any(Unknown)
		if baseErr == nil {
			ahead = r.ahead(strings.TrimSpace(base), name)
		}
		branches[issue] = Branch{Name: name, Ahead: ahead}
	}
	return branches
}

func (r *collector) ahead(base, name string) any {
	out, err := r.probes.Git("rev-list", "--count", base+".."+name)
	if err == nil {
		var n int
		if n, err = strconv.Atoi(strings.TrimSpace(out)); err == nil {
			return n
		}
	}
	r.note("%s の ahead を数えられない (%v)", name, err)
	return Unknown
}

func (r *collector) cls(cfg config.Config, gh github.Runner, issues []int) any {
	found, err := github.LatestCLs(gh, cfg.CLRepo, issues)
	if err != nil {
		r.note("CL を読めない — CL は ? (%v)", err)
		return Unknown
	}
	cls := map[int]any{}
	for issue, cl := range found {
		if cl != nil {
			cls[issue] = *cl
		}
	}
	return cls
}

// isWorkerProcess は pid の process がその worker か。pid は再利用されるので、tick が渡した session id が
// command 行に在るかで見る。
func isWorkerProcess(command, sessionID string) bool {
	return command != "" && sessionID != "" && strings.Contains(command, sessionID)
}

// tickRunning は `claude-dispatcher … tick <project>` の process (試運転を除く) が居るか。一覧を読めなければ Unknown。
func tickRunning(project string, processes map[int]string, procErr error) any {
	if procErr != nil {
		return Unknown
	}
	for _, command := range processes {
		fields := strings.Fields(command)
		for i := 0; i+2 < len(fields); i++ {
			if filepath.Base(fields[i]) == "claude-dispatcher" && fields[i+1] == "tick" && fields[i+2] == project &&
				!slices.Contains(fields, "--dry-run") {
				return true
			}
		}
	}
	return false
}

// ReadMachine はマシン全体の観測を 1 度読む。
func ReadMachine(processes func() (map[int]string, error), agents func() (string, error)) Machine {
	var m Machine
	m.Processes, m.ProcessesErr = processes()
	m.Agents, m.AgentsErr = agents()
	return m
}
