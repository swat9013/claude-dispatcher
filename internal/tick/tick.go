// Package tick は tick の 1 回分を回す。単発の `claude-dispatcher tick` と loop が呼ぶ (system.md §13)。
//
// 観測の正規化と、機械的に確定する指示の導出までを CLI が持ち、選ぶ・見送る・人へ返すは orchestrator (LLM) に残す。
// 外部 store (tracker / CL host) には書かない。書くのは state dir だけ (system.md §1 / §5)。
package tick

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/config"
	"github.com/swat9013/claude-dispatcher/internal/contract"
	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/launch"
	"github.com/swat9013/claude-dispatcher/internal/paths"
	"github.com/swat9013/claude-dispatcher/internal/plugin"
)

// OrchestratorTimeout は orchestrator の上限。超えたら kill して log に残す (system.md §9)
const OrchestratorTimeout = 15 * time.Minute

// Options は 1 tick の入力。Gh と Launcher は外部 CLI の起動口で、テストが差し替える seam。
type Options struct {
	Project string
	Roots   paths.Roots
	Home    string
	Cwd     string
	// Env は子プロセスへ渡す env (依存 CLI の PATH 解決後)
	Env    []string
	Now    time.Time
	Stdout io.Writer
	Stderr io.Writer

	Gh       func(env []string) (github.Runner, error)
	Launcher func(env []string) (launch.Launcher, error)

	// StopOrchestrator は「orchestrator を止めよ」の要求で、閉じると届く。nil なら届かない (単発の tick)。
	// 段階の解釈は tick が持つ: 起動前なら起動せず、起動中なら止めて決定ファイルを読まず、正常終了の後なら無視する (system.md §13)
	StopOrchestrator <-chan struct{}
	// OrchestratorStarted は orchestrator を起動する直前に呼ぶ (loop の画面の経過の起点)。nil なら呼ばない
	OrchestratorStarted func(at time.Time)
}

// Halt は停止要求が orchestrator に何をしたか。
type Halt int

const (
	// NotHalted は停止要求が届かなかったか、orchestrator の正常終了の後に届いたので無視した
	NotHalted Halt = iota
	// HaltedBeforeLaunch は起動前に届いたので orchestrator を起動しなかった
	HaltedBeforeLaunch
	// HaltedDuringRun は起動中に届いたので orchestrator の process group を止めた
	HaltedDuringRun
)

// Outcome は確定した tick 行。中身は log.jsonl に書いたものと同じで、書けなかったときは Error にその理由を足し、Logged が false。
// exit code と失敗行を出すかは呼び出し側が決める (system.md §13)。
type Outcome struct {
	TS     string
	Result Result
	// Error は tick 行の error (1 行に畳んだもの)
	Error  string
	Logged bool
	// Instructions は指示の種別 → 件数。観測に至らなかった tick は nil
	Instructions map[string]int
	// Spawned は起動した worker の issue
	Spawned []int
	Halt    Halt
	// OrchestratorLog は orchestrator を起動した tick の orchestrator log の path
	OrchestratorLog string
}

// stop は tick を止める失敗。result と error 文を持つ。
type stop struct {
	result Result
	msg    string
}

func (s *stop) Error() string { return s.msg }

func stopf(result Result, format string, args ...any) error {
	return &stop{result, fmt.Sprintf(format, args...)}
}

// resultOf は tick を止めた失敗の result と error 文を返す (nil なら ok)。stop 以外の失敗は error。
func resultOf(err error) (Result, string) {
	if err == nil {
		return ResultOK, ""
	}
	var s *stop
	if errors.As(err, &s) {
		return s.result, err.Error()
	}
	return ResultError, err.Error()
}

// onPanic は panic を想定外の失敗として end へ渡す。stack trace を先に出す — 失敗行は呼び出し側が最後に置き、
// 末尾の行だけで読めるようにする (formats.md §6)。defer で直に呼ぶ。
func onPanic(stderr io.Writer, end func(error)) {
	if v := recover(); v != nil {
		fmt.Fprintf(stderr, "panic: %v\n%s", v, debug.Stack())
		end(stopf(ResultError, "想定外の失敗で止まった: %v", v))
	}
}

type run struct {
	o       Options
	project paths.Project
	stem    string
	line    tickLine
	halt    Halt
	// orchestratorLog は orchestrator を起動したときだけ埋まる
	orchestratorLog string
}

func newRun(o Options) *run {
	now := o.Now.UTC()
	return &run{
		o:       o,
		project: o.Roots.Project(o.Project),
		stem:    now.Format(stemLayout),
		line:    tickLine{TS: now.Format(logTimeLayout), Project: o.Project, Cwd: o.Cwd},
	}
}

func (r *run) getenv(key string) string { return deps.Getenv(r.o.Env, key) }

// Run は単発の tick を 1 回回して exit code を返す。失敗したら失敗行 (formats.md §6) を stderr の末尾に出す。
func Run(o Options) int {
	out := Once(o)
	if out.Result != ResultOK || !out.Logged {
		loggedTS := ""
		if out.Logged {
			loggedTS = out.TS
		}
		fmt.Fprintln(o.Stderr, failureLine(time.Now(), o.Project, loggedTS, out.Result, out.Error))
	}
	return out.Result.Exit
}

// Once は 1 tick を回して確定した tick 行を返す。どこで止まっても log.jsonl に 1 行を残す (書ける限り)。
//
// lock は tick 行を書き終えてから外す (system.md §9)。先に外すと、次の tick の行が前の tick の行より先に書かれうる。
func Once(o Options) (out Outcome) {
	r := newRun(o)
	var lock *os.File
	// 後に積んだ defer から走るので、onPanic が行を書いた後に lock を外す
	defer func() {
		if lock != nil {
			lock.Close() // 閉じると flock も外れる
		}
	}()
	defer onPanic(o.Stderr, func(err error) { out = r.finish(err) })

	if err := r.requireStateDir(); err != nil {
		return r.finish(err)
	}
	lock, err := r.acquireLock()
	if err != nil {
		return r.finish(err)
	}
	return r.finish(r.tick())
}

// requireStateDir は config と state dir の実在を確かめる。state dir が無いと log.jsonl を置く先も無い。
func (r *run) requireStateDir() error {
	if err := config.RequireFile(r.project.ConfigFile()); err != nil {
		return stopf(ResultConfigError, "%s", err.Error())
	}
	if info, err := os.Stat(r.project.StateDir); err != nil || !info.IsDir() {
		return stopf(ResultError, "state dir が無い: %s (`claude-dispatcher setup %s` が作る)", r.project.StateDir, r.o.Project)
	}
	return nil
}

// acquireLock は単一実行の lock を取る。取れなければ locked (loop の tick と単発の tick を直列化する。重なった側も
// log に行を残す)。
func (r *run) acquireLock() (*os.File, error) {
	lock, err := os.OpenFile(r.project.LockFile(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock file を開けない (%s): %w", r.project.LockFile(), err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, stopf(ResultLocked, "前の tick がまだ走っている (%s)", r.project.LockFile())
	}
	return lock, nil
}

// finish は tick 行を書き、確定した行を返す。行を書けなかったときは、書けなかった理由を Error に足す
// (呼び出し側がそれを人に見せる)。
func (r *run) finish(err error) Outcome {
	result, msg := resultOf(err)
	r.line.Result = result.Name
	r.line.Error = foldLines(msg)
	out := Outcome{TS: r.line.TS, Result: result, Logged: true, Halt: r.halt, OrchestratorLog: r.orchestratorLog}
	if werr := appendLine(r.project.LogFile(), r.line); werr != nil {
		out.Logged = false
		msg = strings.TrimSpace(msg + "\nlog.jsonl に書けない: " + werr.Error())
	}
	out.Error = foldLines(msg)
	if r.line.observedKeys != nil {
		out.Instructions = r.line.Instructions
	}
	if r.line.launchedKeys != nil {
		for _, s := range r.line.Spawned {
			out.Spawned = append(out.Spawned, s.Issue)
		}
	}
	return out
}

// stopRequested は「orchestrator を止めよ」の要求が届いているか。
func (r *run) stopRequested() bool {
	select {
	case <-r.o.StopOrchestrator:
		return true
	default:
		return false
	}
}

func (r *run) tick() error {
	obs, err := r.observe(r.verifyOnce)
	if err != nil {
		return err
	}
	r.line.observedKeys = newObservedKeys(obs)
	if len(obs.instructions) == 0 {
		return nil
	}

	instructionFile := r.project.InstructionFile(r.stem)
	if err := writeJSON(instructionFile, map[string]any{"snapshot": obs.snapshot, "instructions": obs.instructions}); err != nil {
		return fmt.Errorf("指示ファイルを書けない: %w", err)
	}
	r.line.InstructionFile = &instructionFile
	return r.driveOrchestrator(obs, instructionFile)
}

type observation struct {
	config       config.Config
	claudeEnv    []string // orchestrator / worker へ渡す env (token file の token を載せた後)
	snapshot     Snapshot
	instructions []Instruction
	install      plugin.Install // instructions があるときだけ解決済み
}

// observe は config の検査 → token の解決 → 置き場の検査 (verify) → 観測 → 指示の導出。本番 tick と試運転が共有する。
func (r *run) observe(verify func(config.Config, github.Runner) error) (observation, error) {
	cfg, err := config.Load(r.project.ConfigFile(), r.o.Home)
	if err != nil {
		return observation{}, stopf(ResultConfigError, "%s", err.Error())
	}
	tokens, err := cfg.Tokens(r.getenv)
	if err != nil {
		return observation{}, stopf(ResultConfigError, "%s", err.Error())
	}
	gh, err := r.o.Gh(deps.WithEnv(r.o.Env, tokens.GH))
	if err != nil {
		return observation{}, stopf(ResultError, "観測できなかった: %v", err)
	}
	if err := verify(cfg, gh); err != nil {
		return observation{}, classifyGhError(err, "置き場を検査できなかった: ")
	}

	issues, err := github.OpenIssues(gh, cfg.IssueRepo)
	if err != nil {
		return observation{}, classifyGhError(err, "観測できなかった: ")
	}
	prs, err := github.OpenPRs(gh, cfg.CLRepo, cfg.IssueRepo)
	if err != nil {
		return observation{}, classifyGhError(err, "観測できなかった: ")
	}
	snapshot := Classify(cfg, issues, prs, r.o.Now)
	obs := observation{config: cfg, claudeEnv: deps.WithEnv(r.o.Env, tokens.Claude), snapshot: snapshot, instructions: Derive(snapshot)}
	if len(obs.instructions) == 0 {
		// 静止した tick は plugin を読まない
		return obs, nil
	}
	if obs.install, err = plugin.Resolve(r.o.Home, r.o.Cwd); err != nil {
		return observation{}, stopf(ResultError, "指示を導出できなかった: %v", err)
	}
	// 選定母集合が空の start はここで落ちる。残りが 0 件なら静止した tick と同じ扱い (orchestrator を起動しない)
	if obs.instructions, err = WithPlaybooks(obs.instructions, obs.install); err != nil {
		return observation{}, stopf(ResultError, "指示を導出できなかった: %v", err)
	}
	if len(obs.instructions) == 0 {
		return obs, nil
	}
	if err := obs.install.RequirePrincipleIndex(); err != nil {
		// 無いまま起動すると worker は原則を 1 つも受け取らずに黙って走る (system.md §11)
		return observation{}, stopf(ResultError, "指示を導出できなかった: %v", err)
	}
	return obs, nil
}

// classifyGhError は gh の失敗を result へ写す。認証の失敗は config の綴りの失敗とも観測の失敗とも取り違えない。
func classifyGhError(err error, prefix string) error {
	var s *stop
	switch {
	case errors.As(err, &s):
		return err
	case github.IsAuth(err):
		return stopf(ResultAuthError, "gh の認証が通らない: %v", err)
	}
	return stopf(ResultError, "%s%v", prefix, err)
}

// verifyOnce は verifyConfig を config の内容ごとに 1 度だけ通す。通った config の hash を marker に残す。
func (r *run) verifyOnce(cfg config.Config, gh github.Runner) error {
	raw, err := os.ReadFile(cfg.Path)
	if err != nil {
		return stopf(ResultConfigError, "宣言 config を読めない: %s (%v)", cfg.Path, err)
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if marker, err := os.ReadFile(r.project.MarkerFile()); err == nil && strings.TrimSpace(string(marker)) == digest {
		return nil
	}
	if err := verifyConfig(cfg, gh); err != nil {
		return err
	}
	return os.WriteFile(r.project.MarkerFile(), []byte(digest+"\n"), 0o644)
}

// verifyConfig は置き場 repo の実在と、機構が付ける label の実在を loud に検査する (何も書かない)。
// config_error にするのは gh が「repo が見えない」「label が無い」と答えたときだけ。gh を起動できない・認証が通らない・
// 読み切れないといった失敗は綴りを直しても直らないので、そのまま返して classifyGhError に写させる (system.md §8)。
func verifyConfig(cfg config.Config, gh github.Runner) error {
	for _, repo := range cfg.Repos() {
		if err := github.RepoExists(gh, repo); err != nil {
			if github.IsNotFound(err) {
				return stopf(ResultConfigError, "%s: 置き場 repo %s が見えない (綴りか権限を確かめる) — %v", cfg.Path, repo, err)
			}
			return err
		}
	}
	var missing []string
	for _, label := range cfg.MechanismLabels() {
		exists, err := github.LabelExists(gh, cfg.IssueRepo, label.Name)
		if err != nil {
			return err
		}
		if !exists {
			missing = append(missing, label.Name)
		}
	}
	if len(missing) > 0 {
		return stopf(ResultConfigError, "%s: 置き場 %s に label [%s] が無い。`claude-dispatcher setup` で作るか `gh label create <name> -R %s` で作る",
			cfg.Path, cfg.IssueRepo, strings.Join(missing, ", "), cfg.IssueRepo)
	}
	return nil
}

// driveOrchestrator は orchestrator を起動して待ち、決定ファイルの採否を log に写し、決定どおり worker を起動する。
func (r *run) driveOrchestrator(obs observation, instructionFile string) error {
	decisionsFile := r.project.DecisionsFile(r.stem)
	if err := os.MkdirAll(filepath.Dir(decisionsFile), 0o755); err != nil {
		return fmt.Errorf("決定ファイルの置き場を作れない: %w", err)
	}
	prompt, err := contract.Prompt(contract.Params{
		InstructionFile: instructionFile,
		DecisionsFile:   decisionsFile,
		HandoffFile:     r.project.HandoffFilePattern(r.stem),
		IssueRepo:       obs.config.IssueRepo.String(),
		CLRepo:          obs.config.CLRepo.String(),
		ReadyLabel:      obs.config.ReadyLabel,
		TriageLabel:     obs.config.TriageLabel,
		PrincipleIndex:  obs.install.PrincipleIndex(),
	})
	if err != nil {
		return err
	}
	launcher, err := r.o.Launcher(obs.claudeEnv)
	if err != nil {
		return err
	}
	if r.stopRequested() {
		r.halt = HaltedBeforeLaunch
		return stopf(ResultError, "停止要求で orchestrator を起動しなかった")
	}
	orchestratorLog := r.project.OrchestratorLog(r.stem)
	r.orchestratorLog = orchestratorLog
	if r.o.OrchestratorStarted != nil {
		r.o.OrchestratorStarted(time.Now())
	}
	run, err := launcher.RunOrchestrator(prompt, orchestratorLog, OrchestratorTimeout, r.o.StopOrchestrator)
	if err != nil {
		return err
	}
	r.line.launchedKeys = &launchedKeys{Orchestrator: newOrchestratorRecord(run), Spawned: []spawned{}}
	if run.Stopped {
		// timeout と同じく決定ファイルは読まない。付いた wip は機械では剥がさない (system.md §9)
		r.halt = HaltedDuringRun
		return stopf(ResultError, "停止要求で orchestrator を止めた")
	}
	// ここから後に届いた停止要求は無視する: 決定どおりの worker の起動を途中で打ち切ると、付いた wip が worker の無いまま残る
	if run.TimedOut || run.ExitCode != 0 {
		// 途中で死んだ orchestrator の決定ファイルは信用しない (wip を付けた後に書き切れていない可能性)。
		// 付いた wip は機械では剥がさない — stale wip として人が回収する
		return fmt.Errorf("orchestrator が正常終了しなかった (exit %d, timed_out=%t)。決定ファイルは読まない。"+
			"経過は %s。この tick で wip を付けたまま残った issue があれば、人が確かめて剥がす", run.ExitCode, run.TimedOut, orchestratorLog)
	}
	decisions, err := ReadDecisions(decisionsFile, obs.instructions)
	if err != nil {
		return err
	}
	if err := appendLine(r.project.LogFile(), orchestratorLine{
		TS: r.line.TS, Project: r.o.Project, Actor: "orchestrator", InstructionFile: instructionFile, Decisions: decisions.Raw,
	}); err != nil {
		return fmt.Errorf("orchestrator の判断を log に写せない: %w", err)
	}

	// 網羅の欠けは error にするが、書かれた分の起動は先に行う — orchestrator が wip を付けた issue を起動せずに
	// 残すと、次 tick では普通の wip に見えて誰も拾わない
	gap := CoverageGap(decisions, obs.instructions)
	if err := r.spawnWorkers(launcher, decisions.Spawn); err != nil {
		return err
	}
	if gap != "" {
		return errors.New(gap)
	}
	return nil
}

// spawnWorkers は決定どおり worker を起動し、起動できた分から tick 行の spawned に積む。
func (r *run) spawnWorkers(launcher launch.Launcher, spawns []Spawn) error {
	if len(spawns) == 0 {
		return nil
	}
	if err := os.MkdirAll(r.project.WorkersDir(), 0o755); err != nil {
		return fmt.Errorf("worker log の置き場を作れない: %w", err)
	}
	for _, s := range spawns {
		logFile := r.project.WorkerLog(s.Issue, r.stem)
		w, err := launcher.SpawnWorker(s.Prompt, logFile)
		if err != nil {
			return fmt.Errorf("issue %d の worker を起動できない: %w", s.Issue, err)
		}
		r.line.Spawned = append(r.line.Spawned, spawned{Issue: s.Issue, Kind: s.Kind, PID: w.PID, Log: logFile, SessionID: w.SessionID})
	}
	return nil
}

func writeJSON(file string, v any) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	line, err := marshalLine(v)
	if err != nil {
		return err
	}
	return os.WriteFile(file, line, 0o644)
}

// --- 試運転 ---

// DryRun は config の検査 (検査済み marker を読まず毎回全部) → 観測 → 指示の導出までを通し、claude を起動する直前で
// 止めて、指示の種別と件数を stdout に 1 行で出す。state dir には何も書かない (system.md §9)。
func DryRun(o Options) (exit int) {
	r := newRun(o)
	fail := func(err error) int {
		result, msg := resultOf(err)
		fmt.Fprintln(o.Stderr, failureLine(time.Now(), o.Project, "", result, msg))
		return result.Exit
	}
	defer onPanic(o.Stderr, func(err error) { exit = fail(err) })

	if err := config.RequireFile(r.project.ConfigFile()); err != nil {
		return fail(stopf(ResultConfigError, "%s", err.Error()))
	}
	// claude の起動経路を通らない分の代わりに、tick が起動する依存が最終的な PATH で解決できるかを見る
	var unresolved []string
	for _, name := range deps.Invoked {
		if _, err := deps.Lookup(name, o.Env); err != nil {
			unresolved = append(unresolved, err.Error())
		}
	}
	if len(unresolved) > 0 {
		return fail(errors.New(strings.Join(unresolved, "\n")))
	}
	obs, err := r.observe(verifyConfig)
	if err != nil {
		return fail(err)
	}
	line, err := marshalLine(dryRunLine{
		TS: r.line.TS, Project: o.Project, DryRun: true, Result: ResultOK.Name,
		Observed: obs.snapshot.Observed, Candidates: len(obs.snapshot.Issues.Candidates),
		WIP: obs.snapshot.Limits.WIPCount, Instructions: Counts(obs.instructions),
	})
	if err != nil {
		return fail(err)
	}
	if _, err := o.Stdout.Write(line); err != nil {
		return fail(fmt.Errorf("stdout に書けない: %w", err))
	}
	return ResultOK.Exit
}
