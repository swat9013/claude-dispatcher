// Package tick は `claude-dispatcher tick` の 1 回分を回す。
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
	"slices"
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

	Gh                  func(env []string) github.Runner
	Launcher            func(env []string) (launch.Launcher, error)
	OrchestratorTimeout time.Duration
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

type run struct {
	o       Options
	project paths.Project
	stem    string
	rec     record
}

func newRun(o Options) *run {
	now := o.Now.UTC()
	return &run{
		o:       o,
		project: o.Roots.Project(o.Project),
		stem:    now.Format(stemLayout),
		rec:     record{"ts": now.Format(logTimeLayout), "project": o.Project, "cwd": o.Cwd},
	}
}

func (r *run) getenv(key string) string { return lookup(r.o.Env, key) }

// Run は 1 tick を回して exit code を返す。どこで止まっても log.jsonl に 1 行を残す (書ける限り)。
func Run(o Options) (exit int) {
	r := newRun(o)
	defer func() {
		if v := recover(); v != nil {
			// stack trace を先に出し、前置付きの 1 行を最後に置く (cron.log は末尾から読まれる)
			fmt.Fprintf(o.Stderr, "panic: %v\n%s", v, debug.Stack())
			exit = r.finish(&stop{ResultError, fmt.Sprintf("想定外の失敗で止まった: %v", v)})
		}
	}()
	return r.finish(r.tick())
}

// finish は tick 行を書き、失敗なら cron.log の行を stderr に出して exit code を返す。
func (r *run) finish(err error) int {
	result, msg := ResultOK, ""
	if err != nil {
		result, msg = ResultError, err.Error()
		var s *stop
		if errors.As(err, &s) {
			result = s.result
		}
	}
	r.rec["result"] = result.Name
	if msg != "" {
		r.rec["error"] = foldLines(msg)
	}
	loggedTS := ""
	if appendLine(r.project.LogFile(), r.rec) == nil {
		loggedTS, _ = r.rec["ts"].(string)
	}
	if result != ResultOK {
		fmt.Fprintln(r.o.Stderr, cronLogLine(time.Now(), r.o.Project, loggedTS, result, msg))
	}
	return result.Exit
}

func (r *run) tick() error {
	if _, err := os.Stat(r.project.ConfigFile()); os.IsNotExist(err) {
		return stopf(ResultConfigError, "宣言 config が無い: %s", r.project.ConfigFile())
	}
	if info, err := os.Stat(r.project.StateDir); err != nil || !info.IsDir() {
		// log.jsonl を置く先も無い。state dir は setup が作る
		return stopf(ResultError, "state dir が無い: %s (`claude-dispatcher setup %s` が作る)", r.project.StateDir, r.o.Project)
	}
	lock, err := os.OpenFile(r.project.LockFile(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("lock file を開けない (%s): %w", r.project.LockFile(), err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// 前 tick が長引いている間も log は進める (最終行の時刻で cron の死活を見るため)
		return stopf(ResultLocked, "前の tick がまだ走っている (%s)", r.project.LockFile())
	}

	obs, err := r.observe(r.verifyOnce)
	if err != nil {
		return err
	}
	r.rec["observed"] = obs.snapshot.Observed
	r.rec["candidates"] = len(obs.snapshot.Issues.Candidates)
	r.rec["wip"] = obs.snapshot.Limits.WIPCount
	r.rec["instructions"] = Counts(obs.instructions)
	r.rec["instruction_file"] = nil
	if len(obs.instructions) == 0 {
		return nil
	}

	instructionFile := r.project.InstructionFile(r.stem)
	if err := writeJSON(instructionFile, map[string]any{"snapshot": obs.snapshot, "instructions": obs.instructions}); err != nil {
		return fmt.Errorf("指示ファイルを書けない: %w", err)
	}
	r.rec["instruction_file"] = instructionFile
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
	gh := r.o.Gh(withEnv(r.o.Env, tokens.GH))
	if err := verify(cfg, gh); err != nil {
		return observation{}, classifyGhError(err, "")
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

	playbooks := &lazyPlaybooks{home: r.o.Home, cwd: r.o.Cwd}
	instructions, err := Derive(snapshot, playbooks)
	if err != nil {
		return observation{}, stopf(ResultError, "指示を導出できなかった: %v", err)
	}
	obs := observation{config: cfg, claudeEnv: withEnv(r.o.Env, tokens.Claude), snapshot: snapshot, instructions: instructions}
	if len(instructions) > 0 {
		// orchestrator の契約に原則索引の path を埋めるので、指示があれば plugin を必ず解決しておく
		if obs.install, err = playbooks.resolve(); err != nil {
			return observation{}, stopf(ResultError, "指示を導出できなかった: %v", err)
		}
	}
	return obs, nil
}

func classifyGhError(err error, prefix string) error {
	var s *stop
	switch {
	case errors.As(err, &s):
		return err
	case github.IsAuth(err):
		return stopf(ResultAuthError, "gh の認証が通らない: %v", err)
	case config.IsError(err):
		return stopf(ResultConfigError, "%s", err.Error())
	}
	return stopf(ResultError, "%s%v", prefix, err)
}

// lazyPlaybooks は plugin の解決を初めて要るときに 1 度だけ行う (静止した tick は plugin を読まない)。
type lazyPlaybooks struct {
	home, cwd string
	done      bool
	install   plugin.Install
	err       error
}

func (l *lazyPlaybooks) resolve() (plugin.Install, error) {
	if !l.done {
		l.install, l.err = plugin.Resolve(l.home, l.cwd)
		l.done = true
	}
	return l.install, l.err
}

func (l *lazyPlaybooks) Playbook(name string) (string, error) {
	install, err := l.resolve()
	if err != nil {
		return "", err
	}
	return install.Playbook(name), nil
}

func (l *lazyPlaybooks) StartPlaybooks() ([]plugin.StartPlaybook, error) {
	install, err := l.resolve()
	if err != nil {
		return nil, err
	}
	return install.StartPlaybooks()
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
// gh の認証の失敗は config の綴りと取り違えないよう、そのまま返す。
func verifyConfig(cfg config.Config, gh github.Runner) error {
	repos := []string{cfg.IssueRepo}
	if cfg.CLRepo != cfg.IssueRepo {
		repos = append(repos, cfg.CLRepo)
	}
	for _, repo := range repos {
		if err := github.RepoExists(gh, repo); err != nil {
			if github.IsAuth(err) {
				return err
			}
			return stopf(ResultConfigError, "%s: 置き場 repo %s を確認できない — %v", cfg.Path, repo, err)
		}
	}
	labels, err := github.Labels(gh, cfg.IssueRepo)
	if err != nil {
		if github.IsAuth(err) {
			return err
		}
		return stopf(ResultConfigError, "%s: 置き場 %s の label を読めない — %v", cfg.Path, cfg.IssueRepo, err)
	}
	if len(labels) >= github.LabelListLimit {
		return stopf(ResultConfigError, "%s: 置き場 %s の label が %d 件以上あり検査できない", cfg.Path, cfg.IssueRepo, github.LabelListLimit)
	}
	required := []string{config.WIPLabel, config.HumanLabel}
	if cfg.TriageLabel != "" {
		required = append(required, cfg.TriageLabel)
	}
	var missing []string
	for _, name := range required {
		if !slices.Contains(labels, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return stopf(ResultConfigError, "%s: 置き場 %s に label [%s] が無い。`claude-dispatcher setup` で作るか `gh label create <name> -R %s` で作る",
			cfg.Path, cfg.IssueRepo, strings.Join(missing, ", "), cfg.IssueRepo)
	}
	return nil
}

type spawned struct {
	Issue     int    `json:"issue"`
	Kind      string `json:"kind"`
	PID       int    `json:"pid"`
	Log       string `json:"log"`
	SessionID string `json:"session_id"`
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
		IssueRepo:       obs.config.IssueRepo,
		CLRepo:          obs.config.CLRepo,
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
	timeout := r.o.OrchestratorTimeout
	if timeout == 0 {
		timeout = OrchestratorTimeout
	}
	run, err := launcher.RunOrchestrator(prompt, r.project.OrchestratorLog(r.stem), timeout)
	if err != nil {
		return err
	}
	r.rec["orchestrator"] = map[string]any{
		"exit_code": run.ExitCode, "seconds": float64(int(run.Seconds*10)) / 10, "timed_out": run.TimedOut, "session_id": run.SessionID,
	}
	launched := []spawned{}
	r.rec["spawned"] = launched
	if run.TimedOut || run.ExitCode != 0 {
		// 途中で死んだ orchestrator の決定ファイルは信用しない (wip を付けた後に書き切れていない可能性)。
		// 付いた wip は機械では剥がさない — stale wip として人が回収する
		return fmt.Errorf("orchestrator が正常終了しなかった (exit %d, timed_out=%t)。決定ファイルは読まない", run.ExitCode, run.TimedOut)
	}
	decisions, err := ReadDecisions(decisionsFile, obs.instructions)
	if err != nil {
		return err
	}
	orchestratorLine := map[string]any{
		"ts": r.rec["ts"], "project": r.o.Project, "actor": "orchestrator",
		"instruction_file": instructionFile, "decisions": decisions.Raw,
	}
	if err := appendLine(r.project.LogFile(), orchestratorLine); err != nil {
		return fmt.Errorf("orchestrator の判断を log に写せない: %w", err)
	}

	// 網羅の欠けは error にするが、書かれた分の起動は先に行う — orchestrator が wip を付けた issue を起動せずに
	// 残すと、次 tick では普通の wip に見えて誰も拾わない
	gap := CoverageGap(decisions, obs.instructions)
	if len(decisions.Spawn) > 0 {
		if err := os.MkdirAll(filepath.Dir(r.project.WorkerLog(0, r.stem)), 0o755); err != nil {
			return fmt.Errorf("worker log の置き場を作れない: %w", err)
		}
	}
	for _, s := range decisions.Spawn {
		logFile := r.project.WorkerLog(s.Issue, r.stem)
		w, err := launcher.SpawnWorker(s.Prompt, logFile)
		if err != nil {
			return fmt.Errorf("issue %d の worker を起動できない: %w", s.Issue, err)
		}
		launched = append(launched, spawned{Issue: s.Issue, Kind: s.Kind, PID: w.PID, Log: logFile, SessionID: w.SessionID})
		r.rec["spawned"] = launched
	}
	if gap != "" {
		return errors.New(gap)
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
	fail := func(result Result, msg string) int {
		fmt.Fprintln(o.Stderr, cronLogLine(time.Now(), o.Project, "", result, msg))
		return result.Exit
	}
	defer func() {
		if v := recover(); v != nil {
			fmt.Fprintf(o.Stderr, "panic: %v\n%s", v, debug.Stack())
			exit = fail(ResultError, fmt.Sprintf("想定外の失敗で止まった: %v", v))
		}
	}()
	if _, err := os.Stat(r.project.ConfigFile()); os.IsNotExist(err) {
		return fail(ResultConfigError, "宣言 config が無い: "+r.project.ConfigFile())
	}
	// claude の起動経路を通らない分の代わりに、起動に要る依存が最終的な PATH で解決できるかを見る
	var unresolved []string
	for _, name := range deps.Names {
		if deps.LookPath(name, r.getenv("PATH")) == "" {
			unresolved = append(unresolved, name)
		}
	}
	if len(unresolved) > 0 {
		return fail(ResultError, fmt.Sprintf("%s が PATH に無い (PATH=%s)", strings.Join(unresolved, " / "), r.getenv("PATH")))
	}
	obs, err := r.observe(verifyConfig)
	if err != nil {
		var s *stop
		result := ResultError
		if errors.As(err, &s) {
			result = s.result
		}
		return fail(result, err.Error())
	}
	line, err := marshalLine(map[string]any{
		"ts": r.rec["ts"], "project": o.Project, "dry_run": true, "result": ResultOK.Name,
		"observed": obs.snapshot.Observed, "candidates": len(obs.snapshot.Issues.Candidates),
		"wip": obs.snapshot.Limits.WIPCount, "instructions": Counts(obs.instructions),
	})
	if err != nil {
		return fail(ResultError, err.Error())
	}
	o.Stdout.Write(line)
	return ResultOK.Exit
}

// --- env ---

func lookup(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return env[i][len(prefix):]
		}
	}
	return ""
}

func withEnv(env []string, extra map[string]string) []string {
	out := slices.Clone(env)
	for key, value := range extra {
		out = append(out, key+"="+value)
	}
	return out
}
